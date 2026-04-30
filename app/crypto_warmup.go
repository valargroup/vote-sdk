package app

import (
	"context"
	"sync"
	"time"

	"cosmossdk.io/log"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
)

const (
	// cryptoWarmupNotStarted is the zero-value readiness state before the start
	// command launches warm-up.
	cryptoWarmupNotStarted = "not_started"

	// cryptoWarmupWarming means this process is currently initializing the
	// verifier caches.
	cryptoWarmupWarming = "warming"

	// cryptoWarmupReady means the verifier caches are initialized and write
	// endpoints can safely broadcast vote transactions.
	cryptoWarmupReady = "ready"

	// cryptoWarmupFailed means warm-up failed and write endpoints should remain
	// unavailable until the process restarts.
	cryptoWarmupFailed = "failed"
)

// cryptoWarmupState tracks whether the process has initialized the expensive
// Halo2 verifier caches. It is read by HTTP readiness checks while the warm-up
// goroutine is running.
type cryptoWarmupState struct {
	mu          sync.RWMutex
	status      string
	startedAt   time.Time
	completedAt time.Time
	err         string
}

// StartCryptoWarmup initializes the real Halo2 verifier caches and records the
// readiness state. It blocks until warm-up completes or ctx is cancelled, so it
// should be launched from the start command's errgroup.
func (app *SvoteApp) StartCryptoWarmup(ctx context.Context, logger log.Logger) error {
	if !app.markCryptoWarmupStarted() {
		return nil
	}

	logger = logger.With("module", "crypto-warmup")
	start := time.Now()
	logger.Info("starting Halo2 verifier cache warm-up")

	if err := ctx.Err(); err != nil {
		app.markCryptoWarmupFailed(ctx.Err())
		logger.Error("Halo2 verifier cache warm-up cancelled", "duration_ms", time.Since(start).Milliseconds(), "error", ctx.Err())
		return nil
	}

	if err := halo2.WarmVerifierCaches(); err != nil {
		app.markCryptoWarmupFailed(err)
		logger.Error("Halo2 verifier cache warm-up failed", "duration_ms", time.Since(start).Milliseconds(), "error", err)
		return nil
	}

	app.markCryptoWarmupReady()
	logger.Info("Halo2 verifier cache warm-up completed", "duration_ms", time.Since(start).Milliseconds())
	return nil
}

// CryptoWarmupStatus returns the current crypto readiness snapshot for HTTP
// handlers and operator status endpoints.
func (app *SvoteApp) CryptoWarmupStatus() voteapi.CryptoReadinessStatus {
	app.cryptoWarmup.mu.RLock()
	defer app.cryptoWarmup.mu.RUnlock()

	status := app.cryptoWarmup.status
	if status == "" {
		status = cryptoWarmupNotStarted
	}

	var durationMS int64
	if !app.cryptoWarmup.startedAt.IsZero() {
		end := app.cryptoWarmup.completedAt
		if end.IsZero() {
			end = time.Now()
		}
		durationMS = end.Sub(app.cryptoWarmup.startedAt).Milliseconds()
	}

	var startedAt *time.Time
	if !app.cryptoWarmup.startedAt.IsZero() {
		t := app.cryptoWarmup.startedAt
		startedAt = &t
	}
	var completedAt *time.Time
	if !app.cryptoWarmup.completedAt.IsZero() {
		t := app.cryptoWarmup.completedAt
		completedAt = &t
	}

	return voteapi.CryptoReadinessStatus{
		Status:      status,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		DurationMS:  durationMS,
		Error:       app.cryptoWarmup.err,
	}
}

// markCryptoWarmupStarted transitions the process into warming exactly once.
// It returns false if warm-up is already running or has completed successfully.
func (app *SvoteApp) markCryptoWarmupStarted() bool {
	app.cryptoWarmup.mu.Lock()
	defer app.cryptoWarmup.mu.Unlock()

	if app.cryptoWarmup.status == cryptoWarmupWarming || app.cryptoWarmup.status == cryptoWarmupReady {
		return false
	}

	app.cryptoWarmup.status = cryptoWarmupWarming
	app.cryptoWarmup.startedAt = time.Now()
	app.cryptoWarmup.completedAt = time.Time{}
	app.cryptoWarmup.err = ""
	return true
}

// markCryptoWarmupReady records successful verifier cache initialization.
func (app *SvoteApp) markCryptoWarmupReady() {
	app.cryptoWarmup.mu.Lock()
	defer app.cryptoWarmup.mu.Unlock()

	app.cryptoWarmup.status = cryptoWarmupReady
	app.cryptoWarmup.completedAt = time.Now()
	app.cryptoWarmup.err = ""
}

// markCryptoWarmupFailed records a terminal warm-up failure. The readiness gate
// fails closed after this point so operators see 503s instead of cold-path
// broadcast timeouts.
func (app *SvoteApp) markCryptoWarmupFailed(err error) {
	app.cryptoWarmup.mu.Lock()
	defer app.cryptoWarmup.mu.Unlock()

	app.cryptoWarmup.status = cryptoWarmupFailed
	app.cryptoWarmup.completedAt = time.Now()
	if err != nil {
		app.cryptoWarmup.err = err.Error()
	}
}
