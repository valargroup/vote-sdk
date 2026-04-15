package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"cosmossdk.io/log"
	"golang.org/x/sync/errgroup"
)

// Admin manages the server directory lifecycle.
type Admin struct {
	Store          *Store
	AdminAddress   string
	StaleThreshold int
	PIRServers     []ServiceEntry
	CheckBonding   BondingChecker
	GetVoteManager VoteManagerGetter
	Logger         log.Logger
}

// New creates a new Admin from the given configuration.
func New(cfg Config, checkBonding BondingChecker, getVoteManager VoteManagerGetter, homeDir string, logger log.Logger) (*Admin, error) {
	logger = logger.With("module", "admin")

	if cfg.Disable {
		logger.Info("admin server disabled")
		return nil, nil
	}

	dbPath := cfg.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(homeDir, "admin.db")
	}

	store, err := NewStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("create admin store: %w", err)
	}

	var pirServers []ServiceEntry
	if cfg.PIRServers != "" {
		if err := json.Unmarshal([]byte(cfg.PIRServers), &pirServers); err != nil {
			logger.Error("invalid pir_servers config, using empty list", "error", err)
		}
	}

	return &Admin{
		Store:          store,
		AdminAddress:   cfg.AdminAddress,
		StaleThreshold: cfg.StaleThreshold,
		PIRServers:     pirServers,
		CheckBonding:   checkBonding,
		GetVoteManager: getVoteManager,
		Logger:         logger,
	}, nil
}

// Start launches the background monitor goroutines. It blocks until the
// context is cancelled.
func (a *Admin) Start(ctx context.Context, cfg Config) error {
	a.Logger.Info("starting admin server")

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		RunHealthProber(ctx, a.Store, time.Duration(cfg.ProbeInterval)*time.Second, a.Logger)
		return nil
	})
	g.Go(func() error {
		RunStaleEvictor(ctx, a.Store, time.Duration(cfg.EvictInterval)*time.Second, cfg.StaleThreshold, a.Logger)
		return nil
	})

	return g.Wait()
}

// Close shuts down the admin server and releases resources.
func (a *Admin) Close() error {
	return a.Store.Close()
}
