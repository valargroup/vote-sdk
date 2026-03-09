package types

import (
	"hash"

	"golang.org/x/crypto/blake2b"
)

// CastVoteSighashDomain is the domain string for the canonical cast-vote
// sighash. Must match the e2e-tests encoding.
const CastVoteSighashDomain = "SVOTE_CAST_VOTE_SIGHASH_V0"

// AckSigDomain is the domain prefix for the ceremony ack signature hash:
// SHA256(AckSigDomain || ea_pk || validator_address).
const AckSigDomain = "ack"

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
	var pidBuf [32]byte
	pidBuf[0] = byte(msg.ProposalId)
	pidBuf[1] = byte(msg.ProposalId >> 8)
	pidBuf[2] = byte(msg.ProposalId >> 16)
	pidBuf[3] = byte(msg.ProposalId >> 24)
	h.Write(pidBuf[:])
	// vote_comm_tree_anchor_height: 8 bytes LE, zero-padded to 32 bytes.
	var ahBuf [32]byte
	ahBuf[0] = byte(msg.VoteCommTreeAnchorHeight)
	ahBuf[1] = byte(msg.VoteCommTreeAnchorHeight >> 8)
	ahBuf[2] = byte(msg.VoteCommTreeAnchorHeight >> 16)
	ahBuf[3] = byte(msg.VoteCommTreeAnchorHeight >> 24)
	ahBuf[4] = byte(msg.VoteCommTreeAnchorHeight >> 32)
	ahBuf[5] = byte(msg.VoteCommTreeAnchorHeight >> 40)
	ahBuf[6] = byte(msg.VoteCommTreeAnchorHeight >> 48)
	ahBuf[7] = byte(msg.VoteCommTreeAnchorHeight >> 56)
	h.Write(ahBuf[:])
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
