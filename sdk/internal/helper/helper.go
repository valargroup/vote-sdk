package helper

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"cosmossdk.io/log"
	"github.com/gorilla/mux"
)

// Helper manages the share processing pipeline lifecycle.
type Helper struct {
	Store             *ShareStore
	Processor         *Processor
	APIToken          string
	ExposeQueueStatus bool
	VCHash            VCHashFunc
	Logger            log.Logger
}

// New creates a new Helper from the given configuration.
//
// Parameters:
//   - cfg: helper configuration (from app.toml [helper] section)
//   - tree: accesses the commitment tree (status + merkle paths + leaf reads) from the keeper's KV store
//   - prover: generates ZKP #3 proofs (real FFI or mock)
//   - roundFetcher: queries the chain for round metadata (direct keeper access)
//   - vcHash: computes vote commitment Poseidon hash for ingress validation
//   - homeDir: the chain's home directory (for default DB path)
//   - logger: module logger
func New(cfg Config, tree TreeReader, prover ProofGenerator, roundFetcher RoundInfoFetcher, vcHash VCHashFunc, homeDir string, logger log.Logger) (*Helper, error) {
	logger = logger.With("module", "helper")

	if cfg.Disable {
		logger.Info("helper server disabled")
		return nil, nil
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
		time.Duration(cfg.MinDelay)*time.Second,
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

	if cfg.MaxConcurrentProofs < 1 {
		logger.Info(
			"invalid helper.max_concurrent_proofs, using fallback",
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
		time.Duration(cfg.ProcessInterval)*time.Second,
		cfg.MaxConcurrentProofs,
	)

	return &Helper{
		Store:             store,
		Processor:         processor,
		APIToken:          cfg.APIToken,
		ExposeQueueStatus: cfg.ExposeQueueStatus,
		VCHash:            vcHash,
		Logger:            logger,
	}, nil
}

// RegisterRoutes registers the helper's HTTP routes on the given router.
func (h *Helper) RegisterRoutes(router *mux.Router) {
	RegisterRoutesWithGetters(
		router,
		func() *ShareStore { return h.Store },
		func() string { return h.APIToken },
		func() bool { return h.ExposeQueueStatus },
		func() TreeReader { return h.Processor.tree },
		func() VCHashFunc { return h.VCHash },
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
