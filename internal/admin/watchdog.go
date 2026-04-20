package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cosmossdk.io/log"

	"github.com/valargroup/vote-sdk/sentry"
)

// checkKey uniquely identifies a probe so the watchdog can track alert
// state across ticks (fire once, recover once).
type checkKey struct {
	url   string
	check string
}

// RunWatchdog periodically probes every vote server and PIR endpoint
// listed in voting-config.json and fires Sentry alerts on failure.
// It blocks until ctx is cancelled.
func RunWatchdog(ctx context.Context, adm *Admin, interval time.Duration, logger log.Logger) {
	logger.Info("fleet health watchdog started", "interval", interval.String())

	client := &http.Client{Timeout: 10 * time.Second}

	// alerted tracks which checks have an outstanding alert so we fire
	// once per failure episode and log recovery when the endpoint comes back.
	alerted := map[checkKey]bool{}

	tick := time.NewTicker(interval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("fleet health watchdog stopped")
			return
		case <-tick.C:
			cfg, err := adm.RefreshConfig()
			if err != nil {
				logger.Error("watchdog: failed to refresh voting-config", "error", err)
				continue
			}
			checkFleet(ctx, client, cfg, alerted, logger)
		}
	}
}

func checkFleet(ctx context.Context, client *http.Client, cfg *VotingConfig, alerted map[checkKey]bool, logger log.Logger) {
	for _, vs := range cfg.VoteServers {
		if ctx.Err() != nil {
			return
		}
		checkVoteServer(client, vs, alerted, logger)
	}

	var roots []pirRootResult
	for _, pir := range cfg.PIRServers {
		if ctx.Err() != nil {
			return
		}
		if r, ok := checkPIRServer(client, pir, cfg.SnapshotHeight, alerted, logger); ok {
			roots = append(roots, r)
		}
	}
	if len(roots) > 1 {
		checkPIRRootConsistency(roots, alerted, logger)
	}
}

// checkVoteServer probes a vote chain server's rounds and helper status endpoints.
func checkVoteServer(client *http.Client, entry ServiceEntry, alerted map[checkKey]bool, logger log.Logger) {
	base := strings.TrimRight(entry.URL, "/")

	probeHTTP200(client, base+"/shielded-vote/v1/rounds", "vote_rounds", entry, alerted, logger)
	probeHTTP200(client, base+"/shielded-vote/v1/status", "vote_helper", entry, alerted, logger)
}

// pirRootResponse is the subset of the PIR /root JSON we need.
type pirRootResponse struct {
	Root29 string  `json:"root29"`
	Root25 string  `json:"root25"`
	Height *uint64 `json:"height"`
}

// pirRootResult pairs a fetched root response with the endpoint it came from.
type pirRootResult struct {
	entry ServiceEntry
	root  pirRootResponse
}

// checkPIRServer probes a PIR server's readiness and snapshot height.
// Returns the parsed root response and true if the server is healthy and
// serving at the expected height (usable for cross-replica consistency checks).
func checkPIRServer(client *http.Client, entry ServiceEntry, canonicalHeight *uint64, alerted map[checkKey]bool, logger log.Logger) (pirRootResult, bool) {
	base := strings.TrimRight(entry.URL, "/")

	probeHTTP200(client, base+"/ready", "pir_ready", entry, alerted, logger)

	if canonicalHeight == nil {
		return pirRootResult{}, false
	}

	key := checkKey{url: entry.URL, check: "pir_height_mismatch"}
	rootURL := base + "/root"
	resp, err := client.Get(rootURL)
	if err != nil {
		handleFailure(key, entry, fmt.Errorf("GET %s: %w", rootURL, err), map[string]string{
			"expected_height": fmt.Sprintf("%d", *canonicalHeight),
		}, alerted, logger)
		return pirRootResult{}, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// /root returns 503 when not serving; the pir_ready check already
		// covers that, so don't double-alert.
		return pirRootResult{}, false
	}

	var root pirRootResponse
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		handleFailure(key, entry, fmt.Errorf("decode %s: %w", rootURL, err), map[string]string{
			"expected_height": fmt.Sprintf("%d", *canonicalHeight),
		}, alerted, logger)
		return pirRootResult{}, false
	}

	if root.Height == nil {
		handleFailure(key, entry, fmt.Errorf("PIR %s has no height in /root", entry.Label), map[string]string{
			"expected_height": fmt.Sprintf("%d", *canonicalHeight),
			"served_height":   "unknown",
		}, alerted, logger)
		return pirRootResult{}, false
	}

	if *root.Height != *canonicalHeight {
		handleFailure(key, entry, fmt.Errorf(
			"PIR %s height mismatch: serving %d, expected %d",
			entry.Label, *root.Height, *canonicalHeight,
		), map[string]string{
			"expected_height": fmt.Sprintf("%d", *canonicalHeight),
			"served_height":   fmt.Sprintf("%d", *root.Height),
		}, alerted, logger)
		return pirRootResult{}, false
	}

	handleRecovery(key, entry, alerted, logger)
	return pirRootResult{entry: entry, root: root}, true
}

// checkPIRRootConsistency compares root hashes across all healthy PIR
// replicas. If any two disagree on root29 or root25 at the same height,
// it fires a pir_root_mismatch alert.
func checkPIRRootConsistency(roots []pirRootResult, alerted map[checkKey]bool, logger log.Logger) {
	// Use the first replica as the reference.
	ref := roots[0]
	for _, other := range roots[1:] {
		if ref.root.Root29 == other.root.Root29 && ref.root.Root25 == other.root.Root25 {
			continue
		}
		// Alert on the divergent replica.
		key := checkKey{url: other.entry.URL, check: "pir_root_mismatch"}
		handleFailure(key, other.entry, fmt.Errorf(
			"PIR root hash mismatch: %s (root29=%s) vs %s (root29=%s)",
			ref.entry.Label, ref.root.Root29,
			other.entry.Label, other.root.Root29,
		), map[string]string{
			"reference_label":  ref.entry.Label,
			"reference_root29": ref.root.Root29,
			"reference_root25": ref.root.Root25,
			"other_root29":     other.root.Root29,
			"other_root25":     other.root.Root25,
		}, alerted, logger)
		return
	}

	// All roots match — clear any previous mismatch alerts.
	for _, r := range roots {
		key := checkKey{url: r.entry.URL, check: "pir_root_mismatch"}
		handleRecovery(key, r.entry, alerted, logger)
	}
}

// probeHTTP200 issues a GET and treats anything other than 200 as failure.
func probeHTTP200(client *http.Client, url, checkName string, entry ServiceEntry, alerted map[checkKey]bool, logger log.Logger) {
	key := checkKey{url: entry.URL, check: checkName}
	resp, err := client.Get(url)
	if err != nil {
		handleFailure(key, entry, fmt.Errorf("GET %s: %w", url, err), nil, alerted, logger)
		return
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		handleFailure(key, entry, fmt.Errorf("GET %s returned HTTP %d", url, resp.StatusCode), nil, alerted, logger)
		return
	}

	handleRecovery(key, entry, alerted, logger)
}

func handleFailure(key checkKey, entry ServiceEntry, err error, extraTags map[string]string, alerted map[checkKey]bool, logger log.Logger) {
	logger.Error("fleet health check failed",
		"check", key.check,
		"label", entry.Label,
		"url", entry.URL,
		"error", err,
	)
	if alerted[key] {
		return
	}
	alerted[key] = true

	tags := map[string]string{
		"alert":          "fleet_health",
		"check":          key.check,
		"endpoint_url":   entry.URL,
		"endpoint_label": entry.Label,
	}
	for k, v := range extraTags {
		tags[k] = v
	}
	sentry.CaptureErr(err, tags)
}

func handleRecovery(key checkKey, entry ServiceEntry, alerted map[checkKey]bool, logger log.Logger) {
	if !alerted[key] {
		return
	}
	delete(alerted, key)
	// Log only — the sentry wrapper only has CaptureErr (Error level), and
	// sending an Error-level recovery event would re-trigger the same Slack
	// alert rule. Clearing the alerted state is what matters: the next
	// failure episode will fire a fresh Sentry event.
	logger.Info("fleet health check recovered",
		"check", key.check,
		"label", entry.Label,
		"url", entry.URL,
	)
}
