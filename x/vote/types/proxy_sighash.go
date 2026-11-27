package types

import (
	"hash"

	errorsmod "cosmossdk.io/errors"
	"golang.org/x/crypto/blake2b"
)

// Proxy delegation digest domains (docs/proxy-delegation/contracts-v1.md §4).
// Domains are hashed raw, without a length prefix, so every SVOTE_ domain
// must stay prefix-free (§4.3).
const (
	ProxyDelegationBatchSighashDomain = "SVOTE_PROXY_DELEGATION_BATCH_SIGHASH_V1"
	ProxyDelegateRegisterDomain       = "SVOTE_PROXY_DELEGATE_REGISTER_V1"
	ProxyDelegateAttestDomain         = "SVOTE_PROXY_DELEGATE_ATTEST_V1"
	ProxyDelegateRetireDomain         = "SVOTE_PROXY_DELEGATE_RETIRE_V1"
	ProxyDelegateBallotDomain         = "SVOTE_PROXY_DELEGATE_BALLOT_V1"
)

// ProxyDCSighashFields are the public fields of one 0x09 DC action that D1
// binds. Its proof and signature are not bound.
type ProxyDCSighashFields struct {
	RVpk                      []byte
	VanNullifier              []byte
	VoteAuthorityNoteNew      []byte
	ProxyDelegationCommitment []byte
	DCSlotNullifier           []byte
}

// ProxyBatchSighashFields are the fields of a 0x09 batch that D1 binds.
type ProxyBatchSighashFields struct {
	VoteRoundID              []byte
	VoteCommTreeAnchorHeight uint64
	ExpiryHeight             uint64
	// HasRegistration says whether the batch carries a ZKP1 registration.
	// RegistrationVanCmx is bound only when it does.
	HasRegistration    bool
	RegistrationVanCmx []byte
	DCs                []ProxyDCSighashFields
}

// DelegateRegistrationFields are the fields that REGISTER and ATTEST both
// bind.
type DelegateRegistrationFields struct {
	DK       []byte
	Provider uint32
	Handle   string
	ProofID  string
}

// SighashFields returns the fields of msg that D1 binds (contracts-v1 §4.1).
func (msg *MsgProxyDelegationBatch) SighashFields() ProxyBatchSighashFields {
	f := ProxyBatchSighashFields{
		VoteRoundID:              msg.GetVoteRoundId(),
		VoteCommTreeAnchorHeight: msg.GetVoteCommTreeAnchorHeight(),
		ExpiryHeight:             msg.GetExpiryHeight(),
		HasRegistration:          msg.GetRegistration() != nil,
		RegistrationVanCmx:       msg.GetRegistration().GetVanCmx(),
		DCs:                      make([]ProxyDCSighashFields, len(msg.GetDcs())),
	}
	for i, dc := range msg.GetDcs() {
		f.DCs[i] = ProxyDCSighashFields{
			RVpk:                      dc.GetRVpk(),
			VanNullifier:              dc.GetVanNullifier(),
			VoteAuthorityNoteNew:      dc.GetVoteAuthorityNoteNew(),
			ProxyDelegationCommitment: dc.GetProxyDelegationCommitment(),
			DCSlotNullifier:           dc.GetDcSlotNullifier(),
		}
	}
	return f
}

// ComputeProxyDelegationBatchSighash returns D1, which every DC's
// vote_auth_sig signs (contracts-v1 §4.1):
//
//	BLAKE2b-256(domain || lp8(chain_id) || vote_round_id
//	  || u64(anchor_height) || u64(expiry_height)
//	  || u8(has_registration) || [registration van_cmx]
//	  || u32(n) || for each DC i: u32(i) || r_vpk || van_nullifier
//	     || vote_authority_note_new || DC || dc_slot_nullifier)
//
// Integers are little-endian in zeroed 32-byte slots. Every byte field must
// be exactly 32 bytes; any other length is ErrInvalidField.
func ComputeProxyDelegationBatchSighash(chainID string, f ProxyBatchSighashFields) ([]byte, error) {
	h, err := newProxyDigest(ProxyDelegationBatchSighashDomain, chainID)
	if err != nil {
		return nil, err
	}
	if err := writeExact32(h, "vote_round_id", f.VoteRoundID); err != nil {
		return nil, err
	}
	writeU64As32(h, f.VoteCommTreeAnchorHeight)
	writeU64As32(h, f.ExpiryHeight)
	if f.HasRegistration {
		writeU8(h, 1)
		if err := writeExact32(h, "registration.van_cmx", f.RegistrationVanCmx); err != nil {
			return nil, err
		}
	} else {
		writeU8(h, 0)
	}
	writeU32As32(h, uint32(len(f.DCs)))
	for i, dc := range f.DCs {
		writeU32As32(h, uint32(i))
		for _, field := range [...]struct {
			name  string
			value []byte
		}{
			{"r_vpk", dc.RVpk},
			{"van_nullifier", dc.VanNullifier},
			{"vote_authority_note_new", dc.VoteAuthorityNoteNew},
			{"proxy_delegation_commitment", dc.ProxyDelegationCommitment},
			{"dc_slot_nullifier", dc.DCSlotNullifier},
		} {
			if err := writeExact32(h, field.name, field.value); err != nil {
				return nil, errorsmod.Wrapf(err, "dcs[%d]", i)
			}
		}
	}
	return h.Sum(nil), nil
}

// ComputeProxyDelegationBatchDigest returns batch_digest, D1 computed with
// expiry_height = 0 (contracts-v1 §4.1). It identifies the batch, so every
// re-signed attempt of one batch shares it.
func ComputeProxyDelegationBatchDigest(chainID string, f ProxyBatchSighashFields) ([]byte, error) {
	f.ExpiryHeight = 0
	return ComputeProxyDelegationBatchSighash(chainID, f)
}

// ComputeDelegateRegisterDigest returns REGISTER (contracts-v1 §4.2), which
// the delegate key DK signs as dk_sig.
func ComputeDelegateRegisterDigest(chainID string, f DelegateRegistrationFields) ([]byte, error) {
	h, err := newProxyDigest(ProxyDelegateRegisterDomain, chainID)
	if err != nil {
		return nil, err
	}
	if err := f.write(h); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// ComputeDelegateAttestDigest returns ATTEST (contracts-v1 §4.2), which a
// verifier signs: the REGISTER fields plus the attestation's validity window.
func ComputeDelegateAttestDigest(chainID string, f DelegateRegistrationFields, notBefore, expiresAt uint64) ([]byte, error) {
	h, err := newProxyDigest(ProxyDelegateAttestDomain, chainID)
	if err != nil {
		return nil, err
	}
	if err := f.write(h); err != nil {
		return nil, err
	}
	writeU64As32(h, notBefore)
	writeU64As32(h, expiresAt)
	return h.Sum(nil), nil
}

// ComputeDelegateRetireDigest returns RETIRE (contracts-v1 §4.2), which the
// entry's key dk signs as dk_sig.
func ComputeDelegateRetireDigest(chainID string, delegateIndex uint32, dk []byte) ([]byte, error) {
	h, err := newProxyDigest(ProxyDelegateRetireDomain, chainID)
	if err != nil {
		return nil, err
	}
	writeU32As32(h, delegateIndex)
	if err := writeExact32(h, "dk", dk); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// ComputeDelegateBallotDigest returns BALLOT (contracts-v1 §4.2), which the
// entry's key signs as dk_sig. options[i] is the choice on proposal
// i + 1.
func ComputeDelegateBallotDigest(chainID string, voteRoundID []byte, delegateIndex uint32, options []uint32) ([]byte, error) {
	h, err := newProxyDigest(ProxyDelegateBallotDomain, chainID)
	if err != nil {
		return nil, err
	}
	if err := writeExact32(h, "vote_round_id", voteRoundID); err != nil {
		return nil, err
	}
	writeU32As32(h, delegateIndex)
	writeU32As32(h, uint32(len(options)))
	for _, option := range options {
		writeU32As32(h, option)
	}
	return h.Sum(nil), nil
}

// newProxyDigest starts a contracts-v1 §4 digest: unkeyed BLAKE2b-256 over
// the raw domain, then lp8(chain_id).
func newProxyDigest(domain, chainID string) (hash.Hash, error) {
	h, _ := blake2b.New256(nil)
	h.Write([]byte(domain))
	if err := writeLP8(h, "chain_id", []byte(chainID)); err != nil {
		return nil, err
	}
	return h, nil
}

// write appends the fields REGISTER and ATTEST share, in their digest order.
func (f DelegateRegistrationFields) write(h hash.Hash) error {
	if err := writeExact32(h, "dk", f.DK); err != nil {
		return err
	}
	writeU32As32(h, f.Provider)
	if err := writeLP8(h, "handle", []byte(f.Handle)); err != nil {
		return err
	}
	return writeLP8(h, "proof_id", []byte(f.ProofID))
}
