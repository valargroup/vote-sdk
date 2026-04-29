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

	row.State = VoteServerHealthUp
	row.LastSuccessAt = row.LastCheckedAt
	row.Error = ""
	if height, ok := latestBlockHeight(resp.Body); ok {
		row.Height = &height
	}
	return row
}

func latestBlockHeight(r io.Reader) (int64, bool) {
	dec := json.NewDecoder(r)
	dec.UseNumber()

	if !readJSONObjectStart(dec) {
		return 0, false
	}
	for dec.More() {
		key, ok := readJSONObjectKey(dec)
		if !ok {
			return 0, false
		}
		if key != "block" {
			if err := skipJSONValue(dec); err != nil {
				return 0, false
			}
			continue
		}
		return latestBlockHeightFromBlock(dec)
	}
	return 0, false
}

func latestBlockHeightFromBlock(dec *json.Decoder) (int64, bool) {
	if !readJSONObjectStart(dec) {
		return 0, false
	}
	for dec.More() {
		key, ok := readJSONObjectKey(dec)
		if !ok {
			return 0, false
		}
		if key != "header" {
			if err := skipJSONValue(dec); err != nil {
				return 0, false
			}
			continue
		}
		return latestBlockHeightFromHeader(dec)
	}
	return 0, false
}

func latestBlockHeightFromHeader(dec *json.Decoder) (int64, bool) {
	if !readJSONObjectStart(dec) {
		return 0, false
	}
	for dec.More() {
		key, ok := readJSONObjectKey(dec)
		if !ok {
			return 0, false
		}
		if key != "height" {
			if err := skipJSONValue(dec); err != nil {
				return 0, false
			}
			continue
		}
		height, ok := readPositiveInt64(dec)
		return height, ok
	}
	return 0, false
}

func readJSONObjectStart(dec *json.Decoder) bool {
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	delim, ok := tok.(json.Delim)
	return ok && delim == '{'
}

func readJSONObjectKey(dec *json.Decoder) (string, bool) {
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	key, ok := tok.(string)
	return key, ok
}

func readPositiveInt64(dec *json.Decoder) (int64, bool) {
	tok, err := dec.Token()
	if err != nil {
		return 0, false
	}

	var raw string
	switch v := tok.(type) {
	case string:
		raw = v
	case json.Number:
		raw = v.String()
	default:
		return 0, false
	}

	height, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || height <= 0 {
		return 0, false
	}
	return height, true
}

func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}

	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		for dec.More() {
			if _, ok := readJSONObjectKey(dec); !ok {
				return fmt.Errorf("invalid object key")
			}
			if err := skipJSONValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := skipJSONValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	default:
		return nil
	}
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
