package helper

import (
	"context"
	"fmt"
	"path/filepath"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

// Helper manages the share processing pipeline lifecycle.
type Helper struct {
	Store                 *ShareStore
	Processor             *Processor
	APIToken              string
	ExposeQueueStatus     bool
	ExposeQueueSummary    bool
	VCHash                VCHashFunc
	PayloadValidator      SharePayloadValidator
	ChoiceValidator       ShareChoiceValidator
	RoundStatus           RoundStatusChecker
	ShareNullifierChecker ShareNullifierChecker
	Logger                log.Logger
}

// New creates a new Helper from the given configuration.
//
// Parameters:
//   - cfg: helper configuration (from app.toml [helper] section)
//   - tree: accesses the commitment tree (status + merkle paths + leaf reads) from the keeper's KV store
//   - prover: generates ZKP #3 proofs (real FFI or mock)
//   - roundFetcher: queries the chain for round metadata (direct keeper access)
//   - isRoundActive: checks if a round is still ACTIVE
//   - vcHash: computes vote commitment Poseidon hash
//   - payloadValidator: checks caller-controlled share commitment relationships
//   - choiceValidator: checks proposal and vote-decision membership in the round
//   - shareNFHash: computes share nullifier Poseidon hash before proof generation
//   - homeDir: the chain's home directory (for default DB path)
//   - logger: module logger
func New(cfg Config, tree TreeReader, prover ProofGenerator, roundFetcher RoundInfoFetcher, isRoundActive RoundStatusChecker, vcHash VCHashFunc, payloadValidator SharePayloadValidator, choiceValidator ShareChoiceValidator, shareNFHash ShareNullifierHashFunc, shareNF ShareNullifierChecker, homeDir string, logger log.Logger) (*Helper, error) {
	logger = logger.With("module", "helper")

	if cfg.Disable {
		logger.Info("helper server disabled")
		return nil, nil
	}
	for _, dependency := range []struct {
		name        string
		unavailable bool
	}{
		{name: "commitment tree", unavailable: tree == nil},
		{name: "round status checker", unavailable: isRoundActive == nil},
		{name: "vote commitment hash", unavailable: vcHash == nil},
		{name: "share payload validator", unavailable: payloadValidator == nil},
		{name: "share choice validator", unavailable: choiceValidator == nil},
	} {
		if dependency.unavailable {
			return nil, fmt.Errorf("%w: %s", ErrShareValidationUnavailable, dependency.name)
		}
	}

	// Default DB path: $HOME/.svoted/helper.db
	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(homeDir, "helper.db")
	}

	submitURL := fmt.Sprintf("http://localhost:%d", cfg.ChainAPIPort)
	submitter := NewChainSubmitter(submitURL)

	store, err := NewShareStore(
		dbPath,
		roundFetcher,
	)
	if err != nil {
		return nil, fmt.Errorf("create share store: %w", err)
	}
	store.logger = func(msg string, keyvals ...any) {
		logger.Error(msg, keyvals...)
	}
	store.logInfo = func(msg string, keyvals ...any) {
		logger.Info(msg, keyvals...)
	}
	store.captureErr = CaptureErr

	if cfg.MaxConcurrentProofs < 1 {
		logger.Info(
			"invalid helper proof concurrency, using fallback",
			"configured", cfg.MaxConcurrentProofs,
			"fallback", 1,
		)
		cfg.MaxConcurrentProofs = 1
	}

	processor := NewProcessor(
		store,
		tree,
		prover,
		submitter,
		logger,
		cfg.MaxConcurrentProofs,
		isRoundActive,
		WithPreProofShareDeduper(vcHash, shareNFHash, shareNF),
	)

	return &Helper{
		Store:                 store,
		Processor:             processor,
		APIToken:              cfg.APIToken,
		ExposeQueueStatus:     cfg.ExposeQueueStatus,
		ExposeQueueSummary:    cfg.ExposeQueueSummary,
		VCHash:                vcHash,
		PayloadValidator:      payloadValidator,
		ChoiceValidator:       choiceValidator,
		RoundStatus:           isRoundActive,
		ShareNullifierChecker: shareNF,
		Logger:                logger,
	}, nil
}

// RegisterRoutes registers the helper's HTTP routes on the given router.
func (h *Helper) RegisterRoutes(router *mux.Router) {
	RegisterRoutesWithValidationGetters(
		router,
		func() *ShareStore { return h.Store },
		func() string { return h.APIToken },
		func() bool { return h.ExposeQueueStatus },
		func() bool { return h.ExposeQueueSummary },
		func() bool { return true },
		func() TreeReader { return h.Processor.tree },
		func() VCHashFunc { return h.VCHash },
		func() ShareNullifierChecker { return h.ShareNullifierChecker },
		func() RoundStatusChecker { return h.RoundStatus },
		func() SharePayloadValidator { return h.PayloadValidator },
		func() ShareChoiceValidator { return h.ChoiceValidator },
		h.Logger,
	)
}

// Tree returns the tree reader used by the processor.
func (h *Helper) Tree() TreeReader {
	return h.Processor.tree
}

// Start launches the background processor in the given context.
// It blocks until the context is cancelled.
func (h *Helper) Start(ctx context.Context) error {
	h.Logger.Info("starting helper processor")
	return h.Processor.Run(ctx)
}

// Close shuts down the helper and releases resources.
func (h *Helper) Close() error {
	return h.Store.Close()
}
