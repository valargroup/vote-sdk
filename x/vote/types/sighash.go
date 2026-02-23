package types

import (
	"hash"

	"golang.org/x/crypto/blake2b"
)

// DelegationSighashDomain is the domain string for the canonical delegation
// sighash. Must match the e2e-tests and generate_fixtures.rs encoding.
const DelegationSighashDomain = "ZALLY_DELEGATION_SIGHASH_V0"

// ComputeDelegationSighash returns the 32-byte Blake2b-256 hash of the
// canonical signable payload for MsgDelegateVote. The payload binds the
// signature to the message content so the chain can verify that the
// client-provided sighash matches.
//
// Canonical encoding (domain || fixed-order fields):
//   - domain: DelegationSighashDomain (no trailing null)
//   - vote_round_id: 32 bytes (pad with zeros if shorter)
//   - rk: 32 bytes
//   - signed_note_nullifier: 32 bytes
//   - cmx_new: 32 bytes
//   - enc_memo: 64 bytes (pad with zeros if shorter)
//   - van_cmx: 32 bytes
//   - gov_nullifiers: exactly 5 × 32 bytes (pad with zeros if fewer than 5)
func ComputeDelegationSighash(msg *MsgDelegateVote) []byte {
	h, _ := blake2b.New256(nil)
	h.Write([]byte(DelegationSighashDomain))
	write32(h, msg.VoteRoundId)
	write32(h, msg.Rk)
	write32(h, msg.SignedNoteNullifier)
	write32(h, msg.CmxNew)
	write64(h, msg.EncMemo)
	write32(h, msg.VanCmx)
	// Exactly 5 × 32 bytes for gov_nullifiers (pad with zeros if fewer than 5).
	for i := 0; i < 5; i++ {
		var slot [32]byte
		if i < len(msg.GovNullifiers) && len(msg.GovNullifiers[i]) > 0 {
			if len(msg.GovNullifiers[i]) >= 32 {
				copy(slot[:], msg.GovNullifiers[i][:32])
			} else {
				copy(slot[:], msg.GovNullifiers[i])
			}
		}
		h.Write(slot[:])
	}
	return h.Sum(nil)
}

// CastVoteSighashDomain is the domain string for the canonical cast-vote
// sighash. Must match the e2e-tests encoding.
const CastVoteSighashDomain = "ZALLY_CAST_VOTE_SIGHASH_V0"

// ComputeCastVoteSighash returns the 32-byte Blake2b-256 hash of the
// canonical signable payload for MsgCastVote. The payload binds the
// signature to the message content so the chain can verify that the
// client-provided sighash matches.
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

func write64(h hash.Hash, b []byte) {
	var buf [64]byte
	if len(b) >= 64 {
		copy(buf[:], b[:64])
	} else if len(b) > 0 {
		copy(buf[:], b)
	}
	h.Write(buf[:])
}
