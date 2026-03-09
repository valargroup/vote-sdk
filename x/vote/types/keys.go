package types

import "fmt"

const (
	// ModuleName defines the module name.
	ModuleName = "vote"

	// StoreKey defines the primary module store key.
	StoreKey = ModuleName

	// RouterKey defines the module's message routing key.
	RouterKey = ModuleName
)

// DefaultDealTimeout is the ceremony deal/ack phase timeout in seconds (30 minutes).
const DefaultDealTimeout uint64 = 1800

// RoundIDLen is the fixed byte-length of a VoteRoundId (SHA-256 digest).
const RoundIDLen = 32

// MinProposalID is the minimum valid proposal ID (1-indexed).
// Bit 0 of the circuit's proposal_authority bitmask is reserved as a
// sentinel (rejected by the non-zero gate), so valid IDs start at 1.
const MinProposalID = 1

// MaxProposals is the maximum number of proposals per voting round.
// The circuit's proposal_authority bitmask is 16 bits with bit 0 reserved
// as a sentinel (rejected by the non-zero gate), leaving bits 1-15 usable.
const MaxProposals = 15

// MaxVoteOptions is the maximum number of options per proposal (0-indexed).
// Circuit-constrained by the vote decision encoding.
const MaxVoteOptions = 8

// MaxTreePosition is the largest valid commitment tree leaf index.
// The tree uses zero-based uint32 leaf addressing (depth-24 Poseidon Merkle tree),
// so valid positions are 0..2^32-1.
const MaxTreePosition = (1 << 32) - 1

// Session creation field names — used in the HTTP API response, CLI input
// parsing, and structured logging. Single source of truth for the JSON keys
// of hex-encoded fields in MsgCreateVotingSession.
const (
	SessionKeyNcRoot           = "nc_root"
	SessionKeyNullifierImtRoot = "nullifier_imt_root"
	SessionKeyBlockhash        = "snapshot_blockhash"
	SessionKeyProposalsHash    = "proposals_hash"
	SessionKeyVkZkp1           = "vk_zkp1"
	SessionKeyVkZkp2           = "vk_zkp2"
	SessionKeyVkZkp3           = "vk_zkp3"
)

// NullifierType distinguishes the three independent nullifier sets per voting round.
type NullifierType byte

const (
	// NullifierTypeGov identifies governance nullifiers recorded by MsgDelegateVote.
	NullifierTypeGov NullifierType = 0x00
	// NullifierTypeVoteAuthorityNote identifies vote-authority-note nullifiers recorded by MsgCastVote.
	NullifierTypeVoteAuthorityNote NullifierType = 0x01
	// NullifierTypeShare identifies share nullifiers recorded by MsgRevealShare.
	NullifierTypeShare NullifierType = 0x02
)

// KV store key prefixes for the vote module.
var (
	// NullifierPrefix stores spent nullifiers, scoped by type and round:
	//   0x01 || type_byte || round_id (32 bytes) || nullifier_bytes -> []byte{1}
	NullifierPrefix = []byte{0x01}

	// CommitmentLeafPrefix stores append-only commitment tree entries: 0x02 || big-endian uint64 index -> commitment_bytes
	CommitmentLeafPrefix = []byte{0x02}

	// CommitmentRootByHeightPrefix stores commitment tree roots indexed by block height: 0x03 || big-endian uint64 height -> root_bytes
	CommitmentRootByHeightPrefix = []byte{0x03}

	// VoteRoundPrefix stores vote round data: 0x04 || round_id -> VoteRound (protobuf)
	VoteRoundPrefix = []byte{0x04}

	// TallyPrefix stores vote tally accumulators: 0x05 || round_id || big-endian uint32 proposal_id || big-endian uint32 decision -> big-endian uint64 amount
	TallyPrefix = []byte{0x05}

	// TreeStateKey stores the current commitment tree state (next_index, etc.): single key
	TreeStateKey = []byte{0x06}

	// TallyResultPrefix stores finalized tally results: 0x07 || round_id || big-endian uint32 proposal_id || big-endian uint32 decision -> TallyResult (protobuf)
	TallyResultPrefix = []byte{0x07}

	// BlockLeafIndexPrefix maps block heights to the range of commitment leaves
	// appended during that block: 0x08 || big-endian uint64 height -> (start_index uint64 BE, count uint64 BE)
	// Written by EndBlocker when tree root changes. Used by the CommitmentLeaves query.
	BlockLeafIndexPrefix = []byte{0x08}

	// VoteManagerKey stores the singleton vote manager address: single key -> VoteManagerState (protobuf)
	VoteManagerKey = []byte{0x0A}

	// ShareCountPrefix stores share reveal counts per (round, proposal, decision):
	//   0x0B || round_id || big-endian uint32 proposal_id || big-endian uint32 decision -> uint64 BE
	ShareCountPrefix = []byte{0x0B}

	// PallasKeyPrefix stores the global Pallas PK registry (decoupled from ceremony):
	//   0x0C || valoper_address_bytes -> ValidatorPallasKey (protobuf)
	PallasKeyPrefix = []byte{0x0C}

	// CeremonyStateKey stores the singleton ceremony state: single key -> CeremonyState (protobuf)
	CeremonyStateKey = []byte{0x0E}

	// ShardPrefix stores vote commitment tree shards persisted by EndBlocker:
	//   0x0F || uint64 BE shard_index -> shard blob (WorkingSetShardStore format)
	ShardPrefix = []byte{0x0F}

	// ShardCapKey stores the vote commitment tree cap (nodes above shard level):
	//   single key -> cap blob (WorkingSetShardStore format)
	ShardCapKey = []byte{0x10}

	// ShardCheckpointPrefix stores per-block tree checkpoints:
	//   0x11 || uint32 BE checkpoint_id -> checkpoint blob (WorkingSetShardStore format)
	ShardCheckpointPrefix = []byte{0x11}

	// PartialDecryptionPrefix stores per-validator partial decryptions during the
	// TALLYING phase of a threshold-mode voting round:
	//   0x12 || round_id (32 bytes) || uint32 BE validator_index || uint32 BE proposal_id || uint32 BE vote_decision
	//   -> PartialDecryptionEntry (protobuf)
	//
	// Prefix scans:
	//   0x12 || round_id                                   — all partials for a round
	//   0x12 || round_id || uint32 BE validator_index      — all entries from one validator
	PartialDecryptionPrefix = []byte{0x12}
)

// NullifierKey returns the store key for a nullifier scoped by type and round.
// Format: 0x01 || type_byte || round_id (32 B) || nullifier_bytes
func NullifierKey(nfType NullifierType, roundID, nullifier []byte) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(NullifierPrefix)+1+RoundIDLen+len(nullifier))
	key = append(key, NullifierPrefix...)
	key = append(key, byte(nfType))
	key = append(key, roundID...)
	key = append(key, nullifier...)
	return key, nil
}

// NullifierPrefixForRound returns the KV prefix for all nullifiers of a given
// type within a specific round. Useful for prefix iteration (e.g., genesis export).
// Format: 0x01 || type_byte || round_id (32 B)
func NullifierPrefixForRound(nfType NullifierType, roundID []byte) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(NullifierPrefix)+1+RoundIDLen)
	key = append(key, NullifierPrefix...)
	key = append(key, byte(nfType))
	key = append(key, roundID...)
	return key, nil
}

// CommitmentLeafKey returns the store key for a commitment tree leaf at a given index.
func CommitmentLeafKey(index uint64) []byte {
	key := make([]byte, len(CommitmentLeafPrefix)+8)
	copy(key, CommitmentLeafPrefix)
	putUint64BE(key[len(CommitmentLeafPrefix):], index)
	return key
}

// CommitmentRootKey returns the store key for a commitment tree root at a given height.
func CommitmentRootKey(height uint64) []byte {
	key := make([]byte, len(CommitmentRootByHeightPrefix)+8)
	copy(key, CommitmentRootByHeightPrefix)
	putUint64BE(key[len(CommitmentRootByHeightPrefix):], height)
	return key
}

// VoteRoundKey returns the store key for a vote round.
func VoteRoundKey(roundID []byte) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(VoteRoundPrefix)+RoundIDLen)
	key = append(key, VoteRoundPrefix...)
	key = append(key, roundID...)
	return key, nil
}

// TallyKey returns the store key for a tally accumulator entry.
func TallyKey(roundID []byte, proposalID uint32, decision uint32) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(TallyPrefix)+RoundIDLen+4+4)
	key = append(key, TallyPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	key = appendUint32BE(key, decision)
	return key, nil
}

// TallyPrefixForProposal returns the KV prefix for all tally entries
// of a given (round_id, proposal_id) pair. Used for prefix iteration
// to collect all vote decisions for a proposal.
func TallyPrefixForProposal(roundID []byte, proposalID uint32) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(TallyPrefix)+RoundIDLen+4)
	key = append(key, TallyPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	return key, nil
}

// PrefixEndBytes returns the exclusive end key for prefix iteration.
// It increments the last byte of the prefix, handling overflow by
// truncating trailing 0xFF bytes.
func PrefixEndBytes(prefix []byte) []byte {
	if len(prefix) == 0 {
		return nil
	}
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			return end[:i+1]
		}
	}
	return nil // overflow: prefix is all 0xFF
}

// getUint64BE reads a uint64 from big-endian bytes.
func getUint64BE(b []byte) uint64 {
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

// putUint64BE writes a uint64 in big-endian byte order.
func putUint64BE(b []byte, v uint64) {
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
}

// appendUint32BE appends a uint32 in big-endian byte order.
func appendUint32BE(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// putUint32BE writes a uint32 in big-endian byte order.
func putUint32BE(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

// getUint32BE reads a uint32 from big-endian bytes.
func getUint32BE(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// ErrInvalidRoundIDLen is returned when a roundID is not exactly RoundIDLen bytes.
var ErrInvalidRoundIDLen = fmt.Errorf("vote/types: roundID must be exactly %d bytes", RoundIDLen)

// ValidateRoundID returns an error if id is not exactly RoundIDLen bytes.
func ValidateRoundID(id []byte) error {
	if len(id) != RoundIDLen {
		return ErrInvalidRoundIDLen
	}
	return nil
}

// BlockLeafIndexKey returns the store key for a block-to-leaf-index mapping.
// Format: 0x08 || big-endian uint64 height
func BlockLeafIndexKey(height uint64) []byte {
	key := make([]byte, len(BlockLeafIndexPrefix)+8)
	copy(key, BlockLeafIndexPrefix)
	putUint64BE(key[len(BlockLeafIndexPrefix):], height)
	return key
}

// TallyResultKey returns the store key for a finalized tally result.
// Format: 0x07 || round_id (32 B) || big-endian uint32 proposal_id || big-endian uint32 decision
func TallyResultKey(roundID []byte, proposalID uint32, decision uint32) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(TallyResultPrefix)+RoundIDLen+4+4)
	key = append(key, TallyResultPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	key = appendUint32BE(key, decision)
	return key, nil
}

// ShareCountKey returns the store key for a share count entry.
// Format: 0x0B || round_id (32 B) || big-endian uint32 proposal_id || big-endian uint32 decision
func ShareCountKey(roundID []byte, proposalID uint32, decision uint32) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(ShareCountPrefix)+RoundIDLen+4+4)
	key = append(key, ShareCountPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	key = appendUint32BE(key, decision)
	return key, nil
}

// PallasKeyKey returns the store key for a validator's Pallas PK in the global registry.
// Format: 0x0C || valoper_address_bytes
func PallasKeyKey(valoperAddr string) []byte {
	return append(PallasKeyPrefix, []byte(valoperAddr)...)
}

// ShardKey returns the store key for a vote commitment tree shard.
// Format: 0x0F || uint64 BE shard_index
func ShardKey(index uint64) []byte {
	key := make([]byte, len(ShardPrefix)+8)
	copy(key, ShardPrefix)
	putUint64BE(key[len(ShardPrefix):], index)
	return key
}

// ShardIndexFromKey extracts the shard index from a ShardKey.
func ShardIndexFromKey(key []byte) uint64 {
	return getUint64BE(key[len(ShardPrefix):])
}

// ShardCheckpointKey returns the store key for a vote commitment tree checkpoint.
// Format: 0x11 || uint32 BE checkpoint_id
func ShardCheckpointKey(id uint32) []byte {
	key := make([]byte, len(ShardCheckpointPrefix)+4)
	copy(key, ShardCheckpointPrefix)
	putUint32BE(key[len(ShardCheckpointPrefix):], id)
	return key
}

// ShardCheckpointIDFromKey extracts the checkpoint ID from a ShardCheckpointKey.
func ShardCheckpointIDFromKey(key []byte) uint32 {
	return getUint32BE(key[len(ShardCheckpointPrefix):])
}

// PartialDecryptionKey returns the store key for one partial decryption entry.
// Format: 0x12 || round_id (32 B) || uint32 BE validator_index || uint32 BE proposal_id || uint32 BE vote_decision
func PartialDecryptionKey(roundID []byte, validatorIndex, proposalID, decision uint32) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(PartialDecryptionPrefix)+RoundIDLen+4+4+4)
	key = append(key, PartialDecryptionPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, validatorIndex)
	key = appendUint32BE(key, proposalID)
	key = appendUint32BE(key, decision)
	return key, nil
}

// PartialDecryptionPrefixForRound returns the KV prefix for all partial decryptions
// stored for a given round. Used to iterate all validators' entries for a round.
// Format: 0x12 || round_id (32 B)
func PartialDecryptionPrefixForRound(roundID []byte) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(PartialDecryptionPrefix)+RoundIDLen)
	key = append(key, PartialDecryptionPrefix...)
	key = append(key, roundID...)
	return key, nil
}

// PartialDecryptionPrefixForValidator returns the KV prefix for all partial decryptions
// from a specific validator within a round. Used to check whether a validator has
// already submitted and to retrieve all their entries.
// Format: 0x12 || round_id (32 B) || uint32 BE validator_index
func PartialDecryptionPrefixForValidator(roundID []byte, validatorIndex uint32) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(PartialDecryptionPrefix)+RoundIDLen+4)
	key = append(key, PartialDecryptionPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, validatorIndex)
	return key, nil
}

// TallyResultPrefixForRound returns the KV prefix for all tally results
// of a given round. Used for prefix iteration to collect all finalized results.
// Format: 0x07 || round_id (32 B)
func TallyResultPrefixForRound(roundID []byte) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	key := make([]byte, 0, len(TallyResultPrefix)+RoundIDLen)
	key = append(key, TallyResultPrefix...)
	key = append(key, roundID...)
	return key, nil
}
