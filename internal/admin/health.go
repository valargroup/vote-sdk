package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/log"
)

const voteServerHealthProbePath = "/cosmos/base/tendermint/v1beta1/blocks/latest"

type latestBlockResponse struct {
	Block struct {
		Header struct {
			Height string `json:"height"`
		} `json:"header"`
	} `json:"block"`
}

// GetVoteServerHealth returns one health row for each current vote_servers
// entry. Rows are keyed by URL because the published config does not require
// operator_address today.
func (a *Admin) GetVoteServerHealth() ([]VoteServerHealth, error) {
	cfg, err := a.GetVotingConfig()
	if err != nil {
		return nil, err
	}

	a.healthMu.RLock()
	defer a.healthMu.RUnlock()

	out := make([]VoteServerHealth, 0, len(cfg.VoteServers))
	for _, server := range cfg.VoteServers {
		row, ok := a.voteServerHealth[server.URL]
		if !ok {
			row = VoteServerHealth{
				URL:   server.URL,
				Label: server.Label,
				State: VoteServerHealthUnknown,
			}
		}
		row.Label = server.Label
		out = append(out, row)
	}
	return out, nil
}

// ProbeVoteServers actively checks each current vote_servers URL and stores
// the latest in-memory result. A failed latest probe marks the row down while
// preserving the last successful ping timestamp.
func (a *Admin) ProbeVoteServers(ctx context.Context) error {
	cfg, err := a.GetVotingConfig()
	if err != nil {
		return err
	}

	a.healthMu.RLock()
	previous := make(map[string]VoteServerHealth, len(a.voteServerHealth))
	for url, row := range a.voteServerHealth {
		previous[url] = row
	}
	a.healthMu.RUnlock()

	results := make(chan VoteServerHealth, len(cfg.VoteServers))
	var wg sync.WaitGroup
	for _, server := range cfg.VoteServers {
		server := server
		prior := previous[server.URL]
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- a.probeVoteServer(ctx, server, prior)
		}()
	}
	wg.Wait()
	close(results)

	next := make(map[string]VoteServerHealth, len(cfg.VoteServers))
	for row := range results {
		next[row.URL] = row
	}

	a.healthMu.Lock()
	a.voteServerHealth = next
	a.healthMu.Unlock()
	return nil
}

func (a *Admin) probeVoteServer(ctx context.Context, server ServiceEntry, previous VoteServerHealth) VoteServerHealth {
	started := time.Now()
	row := VoteServerHealth{
		URL:           server.URL,
		Label:         server.Label,
		State:         VoteServerHealthDown,
		LastCheckedAt: started.Unix(),
		LastSuccessAt: previous.LastSuccessAt,
	}

	probeURL := strings.TrimRight(server.URL, "/") + voteServerHealthProbePath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		row.Error = err.Error()
		return row
	}

	client := a.healthClient
	if client == nil {
		client = &http.Client{Timeout: VoteServerHealthProbeTimeout}
	}

	resp, err := client.Do(req)
	row.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		row.Error = err.Error()
		return row
	}
	defer resp.Body.Close()

	statusCode := resp.StatusCode
	row.StatusCode = &statusCode
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		row.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return row
	}

	var block latestBlockResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&block); err != nil {
		row.Error = fmt.Sprintf("decode latest block: %v", err)
		return row
	}

	height, err := strconv.ParseInt(block.Block.Header.Height, 10, 64)
	if err != nil || height <= 0 {
		row.Error = fmt.Sprintf("invalid latest height %q", block.Block.Header.Height)
		return row
	}

	row.State = VoteServerHealthUp
	row.LastSuccessAt = row.LastCheckedAt
	row.Error = ""
	row.Height = &height
	return row
}

// RunVoteServerHealthPoller probes vote_servers immediately and then on each
// interval until ctx is cancelled.
func RunVoteServerHealthPoller(ctx context.Context, a *Admin, interval time.Duration, logger log.Logger) {
	if a == nil || interval <= 0 {
		return
	}
	logger.Info("vote-server health poller started", "interval", interval.String(), "timeout", VoteServerHealthProbeTimeout.String())

	probe := func() {
		if err := a.ProbeVoteServers(ctx); err != nil && ctx.Err() == nil {
			logger.Error("vote-server health probe failed", "error", err)
		}
	}

	probe()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("vote-server health poller stopped")
			return
		case <-ticker.C:
			probe()
		}
	}
}
