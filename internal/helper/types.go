// Package helper implements the share processing pipeline that receives
// encrypted voting shares from wallets, applies random delays for temporal
// unlinkability, generates ZKP #3 proofs, and submits MsgRevealShare to
// the chain.
//
// This package runs inside the zallyd binary, reading commitment tree
// leaves directly from the vote keeper's KV store.
package helper

// Config holds the helper server configuration, read from app.toml [helper].
type Config struct {
	// Disable turns off the helper server entirely.
	Disable bool `mapstructure:"disable"`

	// APIToken protects POST /api/v1/shares when set (checked via X-Helper-Token).
	APIToken string `mapstructure:"api_token"`

	// DBPath is the path to the SQLite database file. Use ":memory:" for testing.
	DBPath string `mapstructure:"db_path"`

	// MeanDelay is the mean of the exponential delay distribution (seconds).
	// Shares are delayed by Exp(1/mean) seconds for temporal unlinkability,
	// capped at the vote end time. Default: 43200 (12 hours).
	MeanDelay int `mapstructure:"mean_delay"`

	// ProcessInterval is how often to check for shares ready to submit (seconds).
	ProcessInterval int `mapstructure:"process_interval"`

	// ChainAPIPort is the port of the chain's REST API (localhost).
	// Used for submitting MsgRevealShare via POST.
	ChainAPIPort int `mapstructure:"chain_api_port"`

	// MaxConcurrentProofs limits concurrent proof generation goroutines.
	MaxConcurrentProofs int `mapstructure:"max_concurrent_proofs"`
}

// DefaultConfig returns the default helper configuration.
func DefaultConfig() Config {
	return Config{
		Disable:             false,
		APIToken:            "",
		DBPath:              "",
		MeanDelay:           43200,
		ProcessInterval:     5,
		ChainAPIPort:        1318,
		MaxConcurrentProofs: 2,
	}
}

// RoundInfoFetcher queries the chain for vote round metadata.
// Returns the vote_end_time (unix seconds) for the given round ID (hex).
type RoundInfoFetcher func(roundID string) (voteEndTime uint64, err error)

// EncryptedShareWire is the wire format for an encrypted ElGamal share component.
type EncryptedShareWire struct {
	C1         string `json:"c1"`          // base64, 32 bytes
	C2         string `json:"c2"`          // base64, 32 bytes
	ShareIndex uint32 `json:"share_index"` // 0..15
}

// SharePayload is the wire format sent by wallets to the helper server.
type SharePayload struct {
	SharesHash   string             `json:"shares_hash"`   // base64, 32 bytes
	ProposalID   uint32             `json:"proposal_id"`   // proposal being voted on
	VoteDecision uint32             `json:"vote_decision"` // 0=support, 1=oppose, 2=skip
	EncShare     EncryptedShareWire `json:"enc_share"`     // the share to relay
	ShareIndex   uint32             `json:"share_index"`   // redundant with enc_share.share_index
	TreePosition uint64             `json:"tree_position"` // VC leaf index
	VoteRoundID  string             `json:"vote_round_id"` // hex, 32 bytes
	ShareComms   []string           `json:"share_comms"`   // base64, 16 × 32-byte Poseidon commitments
	PrimaryBlind string             `json:"primary_blind"` // base64, 32 bytes
}

// ShareState represents the processing state of a queued share.
type ShareState int

const (
	ShareStateReceived  ShareState = 0 // waiting for delay to elapse
	ShareStateWitnessed ShareState = 1 // ready for proof generation
	ShareStateSubmitted ShareState = 2 // submitted to chain
	ShareStateFailed    ShareState = 3 // permanently failed
)

// QueuedShare is a share payload with processing metadata.
type QueuedShare struct {
	Payload  SharePayload
	State    ShareState
	Attempts int
}

// QueueStatus holds per-round queue statistics.
type QueueStatus struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Submitted int `json:"submitted"`
	Failed    int `json:"failed"`
}

// ProofGenerator abstracts ZKP #3 proof generation for testing.
type ProofGenerator interface {
	// GenerateShareRevealProof generates a share reveal proof.
	// merklePath: 772-byte serialized Merkle path (from votetree.TreeHandle.Path)
	// shareComms: 16 × 32-byte per-share Poseidon commitments
	// primaryBlind: 32-byte blind factor for the revealed share
	// encC1X, encC2X: 32-byte x-coordinates of the revealed share
	// Returns proof bytes, nullifier (32 bytes), tree root (32 bytes).
	GenerateShareRevealProof(
		merklePath []byte,
		shareComms [16][32]byte,
		primaryBlind [32]byte,
		encC1X [32]byte,
		encC2X [32]byte,
		shareIndex uint32,
		proposalID, voteDecision uint32,
		roundID [32]byte,
	) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error)
}

// TreeStatus holds lightweight commitment tree statistics.
type TreeStatus struct {
	LeafCount    uint64 `json:"leaf_count"`
	AnchorHeight uint64 `json:"anchor_height"`
}

// TreeReader abstracts commitment tree access from the keeper.
type TreeReader interface {
	// GetTreeStatus returns lightweight tree statistics (leaf count + anchor height).
	GetTreeStatus() (TreeStatus, error)

	// MerklePath returns the 772-byte serialized Poseidon Merkle authentication
	// path for the leaf at position, anchored to the checkpoint at anchorHeight.
	// anchorHeight must correspond to a checkpoint that exists in the persistent
	// tree (i.e. a block height at which Checkpoint was called by EndBlocker).
	MerklePath(position uint64, anchorHeight uint32) ([]byte, error)
}
