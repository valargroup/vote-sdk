package types

import (
	"encoding/binary"
	"hash"

	"golang.org/x/crypto/blake2b"
)

// CastVoteSighashDomain is the domain string for the canonical cast-vote
// sighash. Must match the e2e-tests encoding.
const CastVoteSighashDomain = "SVOTE_CAST_VOTE_SIGHASH_V0"

// CastVoteBatchSighashDomain separates atomic batch authorization from the
// legacy single-vote signature scheme.
const CastVoteBatchSighashDomain = "SVOTE_CAST_VOTE_BATCH_SIGHASH_V1"

// DelegateAndCastVoteBatchSighashDomain separates authorization for a batch
// whose initial VAN is created by the delegation in the same transaction.
const DelegateAndCastVoteBatchSighashDomain = "SVOTE_DELEGATE_AND_CAST_VOTE_BATCH_SIGHASH_V1"

// AckDigestDomain is the domain prefix for the ceremony ack commitment digest:
// SHA256(AckDigestDomain || ea_pk || validator_address).
//
// This is NOT a cryptographic signature. Authentication of the ack message
// relies on ValidateProposerIsCreator (which verifies the message creator is
// the current block proposer). The digest merely binds the acknowledgement to
// a specific (ea_pk, validator_address) pair as a commitment.
const AckDigestDomain = "ack"

// ComputeCastVoteSighash returns the 32-byte Blake2b-256 hash of the
// canonical signable payload for MsgCastVote. The chain computes this
// on-chain and uses it as the message for RedPallas signature verification.
//
// Canonical encoding (domain || fixed-order fields):
//   - domain: CastVoteSighashDomain (no trailing null)
//   - vote_round_id: 32 bytes (pad with zeros if shorter)
//   - r_vpk: 32 bytes (compressed Pallas point)
//   - van_nullifier: 32 bytes
//   - vote_authority_note_new: 32 bytes
//   - vote_commitment: 32 bytes
//   - proposal_id: 4 bytes LE, padded to 32 bytes
//   - vote_comm_tree_anchor_height: 8 bytes LE, padded to 32 bytes
func ComputeCastVoteSighash(msg *MsgCastVote) []byte {
	if msg == nil {
		return nil
	}
	h, _ := blake2b.New256(nil)
	h.Write([]byte(CastVoteSighashDomain))
	write32(h, msg.VoteRoundId)
	write32(h, msg.RVpk)
	write32(h, msg.VanNullifier)
	write32(h, msg.VoteAuthorityNoteNew)
	write32(h, msg.VoteCommitment)
	// proposal_id: 4 bytes LE, zero-padded to 32 bytes.
	writeU32As32(h, msg.ProposalId)
	// vote_comm_tree_anchor_height: 8 bytes LE, zero-padded to 32 bytes.
	writeU64As32(h, msg.VoteCommTreeAnchorHeight)
	return h.Sum(nil)
}

// ComputeCastVoteBatchSighash returns the batch-wide Blake2b-256 digest that
// every action in MsgCastVoteBatch must sign. It binds the common round and
// anchor plus the ordered, effecting public fields of every action. Proof and
// signature bytes are deliberately excluded; valid proofs independently bind
// these public inputs.
//
// Canonical encoding (domain || fixed-width fields):
//   - domain: CastVoteBatchSighashDomain (no trailing null)
//   - vote_round_id: 32 bytes
//   - vote_comm_tree_anchor_height: 8 bytes LE, padded to 32 bytes
//   - action count: 4 bytes LE, padded to 32 bytes
//   - for each action in order:
//   - action index: 4 bytes LE, padded to 32 bytes
//   - r_vpk, van_nullifier, vote_authority_note_new, vote_commitment: 32 bytes each
//   - proposal_id: 4 bytes LE, padded to 32 bytes
func ComputeCastVoteBatchSighash(msg *MsgCastVoteBatch) []byte {
	if msg == nil || len(msg.Votes) == 0 || msg.Votes[0] == nil {
		return nil
	}

	h, _ := blake2b.New256(nil)
	h.Write([]byte(CastVoteBatchSighashDomain))
	write32(h, msg.Votes[0].VoteRoundId)
	writeU64As32(h, msg.Votes[0].VoteCommTreeAnchorHeight)
	writeU32As32(h, uint32(len(msg.Votes)))
	for i, vote := range msg.Votes {
		writeU32As32(h, uint32(i))
		if vote == nil {
			for range 5 {
				write32(h, nil)
			}
			continue
		}
		write32(h, vote.RVpk)
		write32(h, vote.VanNullifier)
		write32(h, vote.VoteAuthorityNoteNew)
		write32(h, vote.VoteCommitment)
		writeU32As32(h, vote.ProposalId)
	}
	return h.Sum(nil)
}

// ComputeDelegateAndCastVoteBatchSighash binds every cast authorization to the
// delegation-created VAN and the complete ordered set of cast effects. The
// delegation proof and signature are excluded because they have their own
// canonical authorization and proof verification.
func ComputeDelegateAndCastVoteBatchSighash(msg *MsgDelegateAndCastVoteBatch) []byte {
	if msg == nil || msg.Delegation == nil || msg.Batch == nil || len(msg.Batch.Votes) == 0 {
		return nil
	}
	h, _ := blake2b.New256(nil)
	h.Write([]byte(DelegateAndCastVoteBatchSighashDomain))
	write32(h, msg.Delegation.VoteRoundId)
	write32(h, msg.Delegation.VanCmx)
	writeU32As32(h, uint32(len(msg.Batch.Votes)))
	for i, vote := range msg.Batch.Votes {
		writeU32As32(h, uint32(i))
		if vote == nil {
			for range 5 {
				write32(h, nil)
			}
			continue
		}
		write32(h, vote.RVpk)
		write32(h, vote.VanNullifier)
		write32(h, vote.VoteAuthorityNoteNew)
		write32(h, vote.VoteCommitment)
		writeU32As32(h, vote.ProposalId)
	}
	return h.Sum(nil)
}

func write32(h hash.Hash, b []byte) {
	var buf [32]byte
	if len(b) >= 32 {
		copy(buf[:], b[:32])
	} else if len(b) > 0 {
		copy(buf[:], b)
	}
	h.Write(buf[:])
}

func writeU32As32(h hash.Hash, value uint32) {
	var buf [32]byte
	binary.LittleEndian.PutUint32(buf[:4], value)
	h.Write(buf[:])
}

func writeU64As32(h hash.Hash, value uint64) {
	var buf [32]byte
	binary.LittleEndian.PutUint64(buf[:8], value)
	h.Write(buf[:])
}
