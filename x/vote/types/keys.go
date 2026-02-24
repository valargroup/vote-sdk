package types

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
)

// NullifierKey returns the store key for a nullifier scoped by type and round.
// Format: 0x01 || type_byte || round_id || nullifier_bytes
func NullifierKey(nfType NullifierType, roundID, nullifier []byte) []byte {
	key := make([]byte, 0, len(NullifierPrefix)+1+len(roundID)+len(nullifier))
	key = append(key, NullifierPrefix...)
	key = append(key, byte(nfType))
	key = append(key, roundID...)
	key = append(key, nullifier...)
	return key
}

// NullifierPrefixForRound returns the KV prefix for all nullifiers of a given
// type within a specific round. Useful for prefix iteration (e.g., genesis export).
// Format: 0x01 || type_byte || round_id
func NullifierPrefixForRound(nfType NullifierType, roundID []byte) []byte {
	key := make([]byte, 0, len(NullifierPrefix)+1+len(roundID))
	key = append(key, NullifierPrefix...)
	key = append(key, byte(nfType))
	key = append(key, roundID...)
	return key
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
func VoteRoundKey(roundID []byte) []byte {
	return append(VoteRoundPrefix, roundID...)
}

// TallyKey returns the store key for a tally accumulator entry.
func TallyKey(roundID []byte, proposalID uint32, decision uint32) []byte {
	key := make([]byte, 0, len(TallyPrefix)+len(roundID)+4+4)
	key = append(key, TallyPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	key = appendUint32BE(key, decision)
	return key
}

// TallyPrefixForProposal returns the KV prefix for all tally entries
// of a given (round_id, proposal_id) pair. Used for prefix iteration
// to collect all vote decisions for a proposal.
func TallyPrefixForProposal(roundID []byte, proposalID uint32) []byte {
	key := make([]byte, 0, len(TallyPrefix)+len(roundID)+4)
	key = append(key, TallyPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	return key
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

// BlockLeafIndexKey returns the store key for a block-to-leaf-index mapping.
// Format: 0x08 || big-endian uint64 height
func BlockLeafIndexKey(height uint64) []byte {
	key := make([]byte, len(BlockLeafIndexPrefix)+8)
	copy(key, BlockLeafIndexPrefix)
	putUint64BE(key[len(BlockLeafIndexPrefix):], height)
	return key
}

// TallyResultKey returns the store key for a finalized tally result.
// Format: 0x07 || round_id || big-endian uint32 proposal_id || big-endian uint32 decision
func TallyResultKey(roundID []byte, proposalID uint32, decision uint32) []byte {
	key := make([]byte, 0, len(TallyResultPrefix)+len(roundID)+4+4)
	key = append(key, TallyResultPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	key = appendUint32BE(key, decision)
	return key
}

// ShareCountKey returns the store key for a share count entry.
// Format: 0x0B || round_id || big-endian uint32 proposal_id || big-endian uint32 decision
func ShareCountKey(roundID []byte, proposalID uint32, decision uint32) []byte {
	key := make([]byte, 0, len(ShareCountPrefix)+len(roundID)+4+4)
	key = append(key, ShareCountPrefix...)
	key = append(key, roundID...)
	key = appendUint32BE(key, proposalID)
	key = appendUint32BE(key, decision)
	return key
}

// PallasKeyKey returns the store key for a validator's Pallas PK in the global registry.
// Format: 0x0C || valoper_address_bytes
func PallasKeyKey(valoperAddr string) []byte {
	return append(PallasKeyPrefix, []byte(valoperAddr)...)
}

// TallyResultPrefixForRound returns the KV prefix for all tally results
// of a given round. Used for prefix iteration to collect all finalized results.
// Format: 0x07 || round_id
func TallyResultPrefixForRound(roundID []byte) []byte {
	key := make([]byte, 0, len(TallyResultPrefix)+len(roundID))
	key = append(key, TallyResultPrefix...)
	key = append(key, roundID...)
	return key
}
