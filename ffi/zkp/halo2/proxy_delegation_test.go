//go:build halo2

package halo2

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/ffi/votetree"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// proxyDelegationFixture is a real ZKP #4 proof, its public inputs and the
// VAN it spends.
type proxyDelegationFixture struct {
	proof    []byte
	inputs   zkp.ProxyDelegationInputs
	spentVan []byte
}

// mustReadProxyDelegationFixture parses ffi/zkp/testdata/<name>, in the layout
// that build_proxy_delegation_fixtures (circuits/tests/generate_fixtures.rs)
// documents.
func mustReadProxyDelegationFixture(t *testing.T, name string) proxyDelegationFixture {
	t.Helper()
	reader := bytes.NewReader(mustReadFixture(t, name))
	read := func(n int) []byte {
		value := make([]byte, n)
		_, err := io.ReadFull(reader, value)
		require.NoError(t, err, name)
		return value
	}

	require.Equal(t, []byte("SVZKP4D1"), read(8))
	proofLen := binary.LittleEndian.Uint32(read(4))
	require.LessOrEqual(t, int(proofLen), types.MaxProofSize)
	f := proxyDelegationFixture{proof: read(int(proofLen))}
	f.inputs.VanNullifier = read(32)
	f.inputs.RVpk = read(32)
	f.inputs.VoteAuthorityNoteNew = read(32)
	f.inputs.ProxyDelegationCommitment = read(32)
	f.inputs.VoteCommTreeRoot = read(32)
	f.inputs.AnchorHeight = binary.LittleEndian.Uint64(read(8))
	f.inputs.VoteRoundId = read(32)
	f.inputs.EaPk = read(32)
	f.inputs.DcSlotNullifier = read(32)
	f.spentVan = read(32)
	require.Zero(t, reader.Len(), "unexpected trailing fixture bytes")
	return f
}

// requireSingleLeafRoot checks that f is proven against the chain's
// SingleLeafRoot of the VAN it spends.
func requireSingleLeafRoot(t *testing.T, f proxyDelegationFixture) {
	t.Helper()
	root, err := votetree.SingleLeafRoot(f.spentVan)
	require.NoError(t, err)
	require.Equal(t, root, f.inputs.VoteCommTreeRoot)
}

// TestVerifyProxyDelegationProofFixtures verifies the real ZKP #4 fixtures and
// checks that their roots and anchor heights follow the 0x09 anchor rule
// (contracts-v1 §5.1, step 8).
func TestVerifyProxyDelegationProofFixtures(t *testing.T) {
	require.False(t, IsMock, "this test requires the real Halo2 verifier")
	anchored := mustReadProxyDelegationFixture(t, "proxy_delegation_anchored.bin")
	registration := mustReadProxyDelegationFixture(t, "proxy_delegation_registration.bin")
	singleShare := mustReadProxyDelegationFixture(t, "proxy_delegation_single_share.bin")

	for _, tc := range []struct {
		name string
		f    proxyDelegationFixture
	}{
		{"anchored", anchored},
		{"registration", registration},
		{"single_share", singleShare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, VerifyProxyDelegationProof(tc.f.proof, tc.f.inputs))
			// The chain reaches the FFI through zkp.Verifier.
			require.NoError(t, NewVerifier().VerifyProxyDelegation(tc.f.proof, tc.f.inputs))
		})
	}

	// A batch's first DC proves against the tree root at a nonzero anchor.
	require.NotZero(t, anchored.inputs.AnchorHeight)
	// A DC after a registration proves at anchor 0 against the single-leaf
	// root of the registration's VAN.
	require.Zero(t, registration.inputs.AnchorHeight)
	requireSingleLeafRoot(t, registration)
	// A batch's later DC spends the previous DC's successor VAN, against its
	// single-leaf root, at the batch's anchor height.
	require.Equal(t, anchored.inputs.VoteAuthorityNoteNew, singleShare.spentVan)
	require.Equal(t, anchored.inputs.AnchorHeight, singleShare.inputs.AnchorHeight)
	requireSingleLeafRoot(t, singleShare)
}

// TestVerifyProxyDelegationProofRejectsMutations checks that a ZKP #4 proof
// fails once any public input or the proof changes, or when it is paired with
// another DC's public inputs.
func TestVerifyProxyDelegationProofRejectsMutations(t *testing.T) {
	require.False(t, IsMock, "this test requires the real Halo2 verifier")
	f := mustReadProxyDelegationFixture(t, "proxy_delegation_anchored.bin")
	other := mustReadProxyDelegationFixture(t, "proxy_delegation_single_share.bin")

	// Flipping the low bit keeps a hash output canonical; flipping a
	// compressed point's sign bit negates the point.
	flipLowBit := func(b []byte) []byte { c := bytes.Clone(b); c[0] ^= 1; return c }
	negate := func(point []byte) []byte { c := bytes.Clone(point); c[31] ^= 0x80; return c }
	for _, tc := range []struct {
		name   string
		mutate func(*zkp.ProxyDelegationInputs)
	}{
		{"van_nullifier", func(in *zkp.ProxyDelegationInputs) { in.VanNullifier = flipLowBit(in.VanNullifier) }},
		{"r_vpk", func(in *zkp.ProxyDelegationInputs) { in.RVpk = negate(in.RVpk) }},
		{"vote_authority_note_new", func(in *zkp.ProxyDelegationInputs) {
			in.VoteAuthorityNoteNew = flipLowBit(in.VoteAuthorityNoteNew)
		}},
		{"delegation_commitment", func(in *zkp.ProxyDelegationInputs) {
			in.ProxyDelegationCommitment = flipLowBit(in.ProxyDelegationCommitment)
		}},
		{"vote_comm_tree_root", func(in *zkp.ProxyDelegationInputs) { in.VoteCommTreeRoot = flipLowBit(in.VoteCommTreeRoot) }},
		{"anchor_height", func(in *zkp.ProxyDelegationInputs) { in.AnchorHeight++ }},
		{"voting_round_id", func(in *zkp.ProxyDelegationInputs) { in.VoteRoundId = flipLowBit(in.VoteRoundId) }},
		{"ea_pk", func(in *zkp.ProxyDelegationInputs) { in.EaPk = negate(in.EaPk) }},
		{"dc_slot_nullifier", func(in *zkp.ProxyDelegationInputs) { in.DcSlotNullifier = flipLowBit(in.DcSlotNullifier) }},
		{"another_dc", func(in *zkp.ProxyDelegationInputs) { *in = other.inputs }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := f.inputs
			tc.mutate(&inputs)
			require.ErrorContains(t, VerifyProxyDelegationProof(f.proof, inputs), "proof verification failed")
		})
	}

	t.Run("through_verifier", func(t *testing.T) {
		inputs := f.inputs
		inputs.DcSlotNullifier = other.inputs.DcSlotNullifier
		require.ErrorContains(t, NewVerifier().VerifyProxyDelegation(f.proof, inputs), "proof verification failed")
	})
	t.Run("proof_byte", func(t *testing.T) {
		proof := bytes.Clone(f.proof)
		proof[len(proof)/2] ^= 1
		require.ErrorContains(t, VerifyProxyDelegationProof(proof, f.inputs), "proof verification failed")
	})
	t.Run("trailing_bytes", func(t *testing.T) {
		proof := append(bytes.Clone(f.proof), "junk"...)
		require.ErrorContains(t, VerifyProxyDelegationProof(proof, f.inputs), "4 trailing unread bytes")
	})
}
