package helper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// MsgRevealShareJSON is the JSON payload for submitting MsgRevealShare to the
// chain's REST API. Byte fields are base64-encoded (Go's default for []byte).
type MsgRevealShareJSON struct {
	ShareNullifier           string `json:"share_nullifier"`              // base64, 32 bytes
	EncShare                 string `json:"enc_share"`                    // base64, 64 bytes (C1||C2)
	ProposalID               uint32 `json:"proposal_id"`
	VoteDecision             uint32 `json:"vote_decision"`
	Proof                    string `json:"proof"`                        // base64
	VoteRoundID              string `json:"vote_round_id"`                // base64, 32 bytes
	VoteCommTreeAnchorHeight uint64 `json:"vote_comm_tree_anchor_height"`
}

// BroadcastResult is the chain's response to a transaction broadcast.
type BroadcastResult struct {
	TxHash string `json:"tx_hash"`
	Code   uint32 `json:"code"`
	Log    string `json:"log"`
}

// ChainSubmitter submits MsgRevealShare transactions to the chain's REST API.
type ChainSubmitter struct {
	baseURL    string
	httpClient *http.Client
}

// NewChainSubmitter creates a submitter targeting the given base URL.
func NewChainSubmitter(baseURL string) *ChainSubmitter {
	return &ChainSubmitter{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 180 * time.Second},
	}
}

// SubmitRevealShare POSTs a MsgRevealShare to the chain endpoint.
func (c *ChainSubmitter) SubmitRevealShare(msg *MsgRevealShareJSON) (*BroadcastResult, error) {
	url := fmt.Sprintf("%s/zally/v1/reveal-share", c.baseURL)

	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal msg: %w", err)
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("HTTP error: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chain returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result BroadcastResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &result, nil
}
