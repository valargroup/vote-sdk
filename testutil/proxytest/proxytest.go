// Package proxytest holds the fixtures the proxy delegation tests share:
// deterministic keys and scalars, proxy rounds, signed 0x0B ops and store
// dumps. It imports neither the keeper nor the app, so the keeper's internal
// tests can use it too.
package proxytest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// SkipUnlessEnabled skips t unless the proxy delegation gate is open, which
// only the proxyenabled build tag does.
func SkipUnlessEnabled(t testing.TB) {
	t.Helper()
	if !types.ProxyDelegationEnabled {
		t.Skip("covers the open gate; run with -tags proxyenabled")
	}
}

// Key derives a deterministic Ed25519 key from label.
func Key(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("vote-sdk proxy test/" + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

// Pub returns priv's 32-byte public key.
func Pub(priv ed25519.PrivateKey) []byte {
	return priv.Public().(ed25519.PublicKey)
}

// Verifier is verifier id's signing key.
func Verifier(id uint32) ed25519.PrivateKey {
	return Key(fmt.Sprintf("verifier/%d", id))
}

// Registration returns the registration fields of dk for the X account
// handle, with a fixed proof post, and no signatures.
func Registration(dk ed25519.PrivateKey, handle string) *types.DelegateRegister {
	return &types.DelegateRegister{
		Dk:       Pub(dk),
		Provider: types.DelegateProviderX,
		Handle:   handle,
		ProofId:  "1790000000000000000",
	}
}

// Scalar derives a fixed Pallas scalar from label.
func Scalar(label string) curvey.Scalar {
	return new(curvey.ScalarPallas).Hash([]byte("vote-sdk proxy test/" + label))
}

// ElectionKey returns the election key pair whose secret is Scalar(label).
func ElectionKey(label string) (*elgamal.SecretKey, *elgamal.PublicKey) {
	sk := Scalar(label)
	return &elgamal.SecretKey{Scalar: sk}, &elgamal.PublicKey{Point: elgamal.PallasGenerator().Mul(sk)}
}

// Proposals returns proposals 1..n, proposal i with optionCounts[i-1]
// options.
func Proposals(optionCounts ...int) []*types.Proposal {
	proposals := make([]*types.Proposal, len(optionCounts))
	for i, n := range optionCounts {
		proposal := &types.Proposal{Id: uint32(i + 1), Title: fmt.Sprintf("Proposal %d", i+1)}
		for o := 0; o < n; o++ {
			proposal.Options = append(proposal.Options, &types.VoteOption{Index: uint32(o), Label: fmt.Sprintf("Option %d", o)})
		}
		proposals[i] = proposal
	}
	return proposals
}

// Round returns an ACTIVE proxy round roundID, created with next_index 1,
// with Proposals(optionCounts...). Callers set its key and times.
func Round(roundID []byte, optionCounts ...int) *types.VoteRound {
	return &types.VoteRound{
		VoteRoundId:     roundID,
		Status:          types.SessionStatus_SESSION_STATUS_ACTIVE,
		Proposals:       Proposals(optionCounts...),
		ProxyDelegation: &types.ProxyDelegationRoundState{Enabled: true, NextDelegateIndexAtCreation: 1},
	}
}

// Attestation signs verifier id's attestation of fields on chainID, valid on
// [notBefore, expiresAt).
func Attestation(chainID string, id uint32, fields *types.DelegateRegister, notBefore, expiresAt uint64) *types.DelegateAttestation {
	digest, err := types.ComputeDelegateAttestDigest(chainID, fields, notBefore, expiresAt)
	if err != nil {
		panic(err)
	}
	return &types.DelegateAttestation{
		VerifierId: id,
		NotBefore:  notBefore,
		ExpiresAt:  expiresAt,
		Sig:        ed25519.Sign(Verifier(id), digest),
	}
}

// RegisterOp builds the register op for fields on chainID, with dk_sig by dk
// and one Attestation per verifier id over [notBefore, expiresAt).
func RegisterOp(chainID string, fields *types.DelegateRegister, dk ed25519.PrivateKey, notBefore, expiresAt uint64, verifierIDs ...uint32) *types.MsgDelegateOp {
	digest, err := types.ComputeDelegateRegisterDigest(chainID, fields)
	if err != nil {
		panic(err)
	}
	op := &types.DelegateRegister{
		Dk:       fields.Dk,
		Provider: fields.Provider,
		Handle:   fields.Handle,
		ProofId:  fields.ProofId,
		DkSig:    ed25519.Sign(dk, digest),
	}
	for _, id := range verifierIDs {
		op.Attestations = append(op.Attestations, Attestation(chainID, id, fields, notBefore, expiresAt))
	}
	return &types.MsgDelegateOp{Register: op}
}

// RetireOp builds the op retiring entry index on chainID, signed by its key
// dk.
func RetireOp(chainID string, index uint32, dk ed25519.PrivateKey) *types.MsgDelegateOp {
	digest, err := types.ComputeDelegateRetireDigest(chainID, index, Pub(dk))
	if err != nil {
		panic(err)
	}
	return &types.MsgDelegateOp{Retire: &types.DelegateRetire{DelegateIndex: index, DkSig: ed25519.Sign(dk, digest)}}
}

// BallotSig signs delegate d's ballot of options in roundID on chainID with
// dk.
func BallotSig(chainID string, roundID []byte, d uint32, dk ed25519.PrivateKey, options ...uint32) []byte {
	digest, err := types.ComputeDelegateBallotDigest(chainID, roundID, d, options)
	if err != nil {
		panic(err)
	}
	return ed25519.Sign(dk, digest)
}

// BallotOp builds delegate d's ballot of options in roundID on chainID,
// signed by dk.
func BallotOp(chainID string, roundID []byte, d uint32, dk ed25519.PrivateKey, options ...uint32) *types.MsgDelegateOp {
	return &types.MsgDelegateOp{Ballot: &types.DelegateBallot{
		VoteRoundId:   roundID,
		DelegateIndex: d,
		Options:       options,
		DkSig:         BallotSig(chainID, roundID, d, dk, options...),
	}}
}

// Values returns every key and value in kv under the given prefixes, by raw
// key.
func Values(t testing.TB, kv store.KVStore, prefixes ...[]byte) map[string][]byte {
	t.Helper()
	values := map[string][]byte{}
	for _, prefix := range prefixes {
		iter, err := kv.Iterator(prefix, types.PrefixEndBytes(prefix))
		require.NoError(t, err)
		for ; iter.Valid(); iter.Next() {
			values[string(iter.Key())] = bytes.Clone(iter.Value())
		}
		require.NoError(t, iter.Close())
	}
	return values
}

// RequireCode fails unless err is want, whose registered code is code, and
// that code surfaces through ABCIInfo in codespace vote (contracts-v1 §0,
// §6).
func RequireCode(t testing.TB, err error, want *errorsmod.Error, code uint32) {
	t.Helper()
	require.ErrorIs(t, err, want)
	require.Equal(t, code, want.ABCICode(), "registered code of %s", want)
	codespace, gotCode, _ := errorsmod.ABCIInfo(err, false)
	require.Equal(t, types.ModuleName, codespace, "codespace of %v", err)
	require.Equal(t, code, gotCode, "code of %v", err)
}
