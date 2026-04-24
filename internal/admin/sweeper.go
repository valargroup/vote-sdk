package admin

import (
	"context"
	"time"

	"cosmossdk.io/log"
)

// RunPendingSweeper periodically deletes expired pending_registrations rows.
// It blocks until ctx is cancelled.
func RunPendingSweeper(ctx context.Context, a *Admin, interval time.Duration, logger log.Logger) {
	if a == nil || a.Store() == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := a.Store().EvictExpiredPending()
			if err != nil {
				logger.Error("pending sweeper: evict", "error", err)
				continue
			}
			if n > 0 {
				logger.Info("pending sweeper: evicted rows", "count", n)
			}
		}
	}
}
