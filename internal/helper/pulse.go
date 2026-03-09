package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"cosmossdk.io/log"
)

// SignFn signs an arbitrary string payload using ADR-036 and returns the
// base64-encoded signature and compressed public key.
type SignFn func(payload string) (signature, pubKey string, err error)

// PulseConfig holds the parameters needed for the heartbeat loop.
type PulseConfig struct {
	PulseURL         string // Vercel base URL (e.g. "https://shielded-vote-phi.vercel.app")
	HelperURL        string // Own public URL (e.g. "https://1-2-3-4.sslip.io")
	OperatorAddress  string // Bech32 operator address derived from validator key
	Moniker          string // Node moniker from CometBFT config
	Sign             SignFn
	Logger           log.Logger
}

const pulseInterval = 2 * time.Hour

type heartbeatRequest struct {
	OperatorAddress string `json:"operator_address"`
	URL             string `json:"url"`
	Moniker         string `json:"moniker"`
	Timestamp       int64  `json:"timestamp"`
	Signature       string `json:"signature"`
	PubKey          string `json:"pub_key"`
}

type heartbeatResponse struct {
	Status    string   `json:"status"`
	Phase     string   `json:"phase,omitempty"`
	ExpiresAt int64    `json:"expires_at,omitempty"`
	Evicted   []string `json:"evicted,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// sendSigned builds the signed payload and POSTs it to the given endpoint.
// Returns the parsed response or an error.
func sendSigned(ctx context.Context, client *http.Client, endpoint string, cfg PulseConfig) (*heartbeatResponse, error) {
	ts := time.Now().Unix()
	payloadStr := fmt.Sprintf(
		`{"operator_address":%q,"url":%q,"moniker":%q,"timestamp":%d}`,
		cfg.OperatorAddress, cfg.HelperURL, cfg.Moniker, ts,
	)

	sig, pubKey, err := cfg.Sign(payloadStr)
	if err != nil {
		return nil, fmt.Errorf("sign payload: %w", err)
	}

	reqBody := heartbeatRequest{
		OperatorAddress: cfg.OperatorAddress,
		URL:             cfg.HelperURL,
		Moniker:         cfg.Moniker,
		Timestamp:       ts,
		Signature:       sig,
		PubKey:          pubKey,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, fmt.Errorf("read response body (HTTP %d): %w", resp.StatusCode, err)
	}

	var result heartbeatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return &result, nil
}

// RunPulse registers with register-validator on startup (to ensure
// approved-servers is populated), then sends a heartbeat pulse to
// server-heartbeat every 2 hours until ctx is cancelled.
//
// If the initial registration returns "pending" (e.g. the chain API wasn't
// reachable yet on Vercel's side), registration is retried on each tick
// until it succeeds, then the loop switches to heartbeat-only mode.
func RunPulse(ctx context.Context, cfg PulseConfig) {
	if cfg.PulseURL == "" || cfg.HelperURL == "" {
		cfg.Logger.Info("heartbeat disabled: pulse_url or helper_url not configured")
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	registerEndpoint := cfg.PulseURL + "/api/register-validator"
	heartbeatEndpoint := cfg.PulseURL + "/api/server-heartbeat"

	registered := tryRegister(ctx, client, registerEndpoint, cfg)

	ticker := time.NewTicker(pulseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !registered {
				registered = tryRegister(ctx, client, registerEndpoint, cfg)
			}
			sendHeartbeat(ctx, client, heartbeatEndpoint, cfg)
		}
	}
}

// tryRegister calls register-validator once and returns true if the server
// was successfully registered (i.e. bonded on-chain and added to
// approved-servers).
func tryRegister(ctx context.Context, client *http.Client, endpoint string, cfg PulseConfig) bool {
	result, err := sendSigned(ctx, client, endpoint, cfg)
	if err != nil {
		cfg.Logger.Error("register: request failed", "error", err)
		return false
	}

	switch result.Status {
	case "registered":
		cfg.Logger.Info("register: registered (bonded)")
		return true
	case "pending":
		cfg.Logger.Warn("register: server not yet bonded or approved, will retry",
			"operator_address", cfg.OperatorAddress)
		return false
	default:
		cfg.Logger.Error("register: unexpected response",
			"status", result.Status, "error", result.Error)
		return false
	}
}

func sendHeartbeat(ctx context.Context, client *http.Client, endpoint string, cfg PulseConfig) {
	result, err := sendSigned(ctx, client, endpoint, cfg)
	if err != nil {
		cfg.Logger.Error("heartbeat: request failed", "error", err)
		return
	}

	switch result.Status {
	case "active":
		cfg.Logger.Debug("heartbeat: active")
	case "pending":
		cfg.Logger.Warn("heartbeat: server not in approved-servers — registration may have failed",
			"operator_address", cfg.OperatorAddress)
	default:
		cfg.Logger.Error("heartbeat: unexpected response",
			"status", result.Status, "error", result.Error)
	}
}
