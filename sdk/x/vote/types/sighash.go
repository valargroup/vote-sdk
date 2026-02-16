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
//   - gov_comm: 32 bytes
//   - gov_nullifiers: exactly 4 × 32 bytes (pad with zeros if fewer than 4)
func ComputeDelegationSighash(msg *MsgDelegateVote) []byte {
	h, _ := blake2b.New256(nil)
	h.Write([]byte(DelegationSighashDomain))
	write32(h, msg.VoteRoundId)
	write32(h, msg.Rk)
	write32(h, msg.SignedNoteNullifier)
	write32(h, msg.CmxNew)
	write64(h, msg.EncMemo)
	write32(h, msg.GovComm)
	// Exactly 4 × 32 bytes for gov_nullifiers (pad with zeros if fewer than 4).
	for i := 0; i < 4; i++ {
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
