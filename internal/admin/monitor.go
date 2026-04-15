package admin

import (
	"context"
	"net/http"
	"time"

	"cosmossdk.io/log"
)

const probeTimeoutMs = 5000

// RunHealthProber periodically probes each approved server and removes
// unreachable ones. This replaces the Vercel health-check-servers cron.
func RunHealthProber(ctx context.Context, store *Store, interval time.Duration, logger log.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeAll(store, logger)
		}
	}
}

func probeAll(store *Store, logger log.Logger) {
	servers, err := store.ListApprovedServers()
	if err != nil {
		logger.Error("health probe: list servers", "error", err)
		return
	}
	if len(servers) == 0 {
		return
	}

	type result struct {
		entry   ServiceEntry
		healthy bool
	}
	results := make(chan result, len(servers))

	for _, s := range servers {
		go func(entry ServiceEntry) {
			healthy := probeServer(entry.URL)
			results <- result{entry: entry, healthy: healthy}
		}(s)
	}

	var unhealthy []ServiceEntry
	for range servers {
		r := <-results
		if !r.healthy {
			unhealthy = append(unhealthy, r.entry)
		}
	}

	if len(unhealthy) == 0 {
		logger.Info("health probe: all servers healthy", "count", len(servers))
		return
	}

	for _, u := range unhealthy {
		url, _ := store.RemoveApprovedServer(u.OperatorAddress)
		if url != "" {
			_ = store.RemovePulse(url)
		}
		logger.Info("health probe: removed unhealthy server", "url", u.URL, "operator", u.OperatorAddress)
	}
}

func probeServer(url string) bool {
	client := &http.Client{Timeout: time.Duration(probeTimeoutMs) * time.Millisecond}
	resp, err := client.Get(url + "/shielded-vote/v1/status")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// RunStaleEvictor periodically removes pulse entries older than the threshold.
// This replaces the Vercel evict-stale-servers cron.
func RunStaleEvictor(ctx context.Context, store *Store, interval time.Duration, staleThreshold int, logger log.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stale, err := store.EvictStalePulses(staleThreshold)
			if err != nil {
				logger.Error("stale evictor: evict pulses", "error", err)
				continue
			}
			if len(stale) > 0 {
				logger.Info("stale evictor: evicted stale servers", "count", len(stale), "urls", stale)
			}
		}
	}
}
