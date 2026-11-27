package types

import (
	"bytes"
	"strings"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// TestWriteExact32RejectsOtherLengths checks that the strict writer accepts
// only 32 bytes and writes nothing when it rejects.
func TestWriteExact32RejectsOtherLengths(t *testing.T) {
	empty := blake2b.Sum256(nil)
	for _, n := range []int{0, 1, 31, 33, 64} {
		h, _ := blake2b.New256(nil)
		require.ErrorIs(t, writeExact32(h, "field", make([]byte, n)), ErrInvalidField, "length %d", n)
		require.Equal(t, empty[:], h.Sum(nil), "length %d wrote to the hash", n)
	}

	h, _ := blake2b.New256(nil)
	value := bytes.Repeat([]byte{7}, 32)
	require.NoError(t, writeExact32(h, "field", value))
	want := blake2b.Sum256(value)
	require.Equal(t, want[:], h.Sum(nil))
}

// TestWriteLP8AndU8 pins the length-prefix and single-byte encodings.
func TestWriteLP8AndU8(t *testing.T) {
	for _, n := range []int{0, 1, 255} {
		value := bytes.Repeat([]byte{'a'}, n)
		h, _ := blake2b.New256(nil)
		require.NoError(t, writeLP8(h, "field", value))
		want := blake2b.Sum256(append([]byte{byte(n)}, value...))
		require.Equal(t, want[:], h.Sum(nil), "length %d", n)
	}

	h, _ := blake2b.New256(nil)
	require.ErrorIs(t, writeLP8(h, "field", make([]byte, 256)), ErrInvalidField)

	h.Reset()
	writeU8(h, 0x7f)
	want := blake2b.Sum256([]byte{0x7f})
	require.Equal(t, want[:], h.Sum(nil))
}

// TestProxyDigestsRejectMalformedFields checks that every contracts-v1 §4
// digest rejects a byte field of the wrong length, or a chain id longer than
// 255 bytes, with ErrInvalidField's registered code.
func TestProxyDigestsRejectMalformedFields(t *testing.T) {
	ok32 := bytes.Repeat([]byte{1}, 32)
	short := ok32[:31]
	longChainID := strings.Repeat("c", 256)
	dcWith := func(commitment []byte) []ProxyDCSighashFields {
		return []ProxyDCSighashFields{{
			RVpk: ok32, VanNullifier: ok32, VoteAuthorityNoteNew: ok32,
			ProxyDelegationCommitment: commitment, DCSlotNullifier: ok32,
		}}
	}
	badDK := DelegateRegistrationFields{DK: short, Provider: DelegateProviderX, Handle: "alice", ProofID: "17"}
	longHandle := DelegateRegistrationFields{DK: ok32, Provider: DelegateProviderX, Handle: strings.Repeat("a", 256), ProofID: "17"}

	cases := map[string]func() ([]byte, error){
		"d1 chain_id": func() ([]byte, error) {
			return ComputeProxyDelegationBatchSighash(longChainID, ProxyBatchSighashFields{VoteRoundID: ok32, DCs: dcWith(ok32)})
		},
		"d1 vote_round_id": func() ([]byte, error) {
			return ComputeProxyDelegationBatchSighash("c", ProxyBatchSighashFields{VoteRoundID: short, DCs: dcWith(ok32)})
		},
		"d1 registration van_cmx": func() ([]byte, error) {
			return ComputeProxyDelegationBatchSighash("c", ProxyBatchSighashFields{VoteRoundID: ok32, HasRegistration: true, DCs: dcWith(ok32)})
		},
		"d1 dc field": func() ([]byte, error) {
			return ComputeProxyDelegationBatchSighash("c", ProxyBatchSighashFields{VoteRoundID: ok32, DCs: dcWith(short)})
		},
		"register dk":     func() ([]byte, error) { return ComputeDelegateRegisterDigest("c", badDK) },
		"register handle": func() ([]byte, error) { return ComputeDelegateRegisterDigest("c", longHandle) },
		"attest dk":       func() ([]byte, error) { return ComputeDelegateAttestDigest("c", badDK, 1, 2) },
		"retire dk":       func() ([]byte, error) { return ComputeDelegateRetireDigest("c", 1, short) },
		"ballot vote_round_id": func() ([]byte, error) {
			return ComputeDelegateBallotDigest("c", short, 1, []uint32{0})
		},
		"ballot chain_id": func() ([]byte, error) {
			return ComputeDelegateBallotDigest(longChainID, ok32, 1, []uint32{0})
		},
	}
	for name, compute := range cases {
		t.Run(name, func(t *testing.T) {
			digest, err := compute()
			require.Nil(t, digest)
			require.ErrorIs(t, err, ErrInvalidField)
			codespace, code, _ := errorsmod.ABCIInfo(err, false)
			require.Equal(t, ModuleName, codespace)
			require.Equal(t, ErrInvalidField.ABCICode(), code)
		})
	}
}

// TestProxyBatchDigestIgnoresExpiryOnly checks that batch_digest is D1 at
// expiry 0, so re-signing with a new expiry keeps the batch identity, and
// that the registration flag, not a stray van_cmx, decides what D1 binds.
func TestProxyBatchDigestIgnoresExpiryOnly(t *testing.T) {
	fill := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	base := ProxyBatchSighashFields{
		VoteRoundID:              fill(1),
		VoteCommTreeAnchorHeight: 10,
		ExpiryHeight:             500,
		DCs: []ProxyDCSighashFields{{
			RVpk: fill(2), VanNullifier: fill(3), VoteAuthorityNoteNew: fill(4),
			ProxyDelegationCommitment: fill(5), DCSlotNullifier: fill(6),
		}},
	}
	compute := func(f ProxyBatchSighashFields) (d1, batchDigest []byte) {
		d1, err := ComputeProxyDelegationBatchSighash("zvote-1", f)
		require.NoError(t, err)
		batchDigest, err = ComputeProxyDelegationBatchDigest("zvote-1", f)
		require.NoError(t, err)
		return d1, batchDigest
	}
	baseD1, baseDigest := compute(base)

	resigned := base
	resigned.ExpiryHeight = 1000
	d1, batchDigest := compute(resigned)
	require.NotEqual(t, baseD1, d1, "expiry must change D1")
	require.Equal(t, baseDigest, batchDigest, "expiry must not change batch_digest")

	atZero := base
	atZero.ExpiryHeight = 0
	d1, _ = compute(atZero)
	require.Equal(t, baseDigest, d1, "batch_digest is D1 at expiry 0")

	stray := base
	stray.RegistrationVanCmx = fill(7)
	d1, _ = compute(stray)
	require.Equal(t, baseD1, d1, "van_cmx without a registration must not be bound")

	registered := stray
	registered.HasRegistration = true
	d1, _ = compute(registered)
	require.NotEqual(t, baseD1, d1, "a registration must change D1")
}
