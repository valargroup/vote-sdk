package types

const (
	// ModuleName defines the module name.
	ModuleName = "vote"

	// StoreKey defines the primary module store key.
	StoreKey = ModuleName

	// RouterKey defines the module's message routing key.
	RouterKey = ModuleName
)

// KV store key prefixes for the vote module.
var (
	// NullifierPrefix stores spent nullifiers: 0x01 || nullifier_bytes -> []byte{1}
	// Used for gov nullifiers, VAN nullifiers, and share nullifiers (single namespace).
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
)

// NullifierKey returns the store key for a nullifier.
func NullifierKey(nullifier []byte) []byte {
	return append(NullifierPrefix, nullifier...)
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
