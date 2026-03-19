//go:build halo2

package halo2

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/ffi/zkp"
)

// TestSignBitFlip_RejectedAfterFix verifies that the ciphertext sign-malleability
// vulnerability is closed. After the fix, the ZKP #3 circuit includes y-coordinates
// in the share commitment Poseidon hash, so flipping the sign bit changes the
// y-coordinate and the proof no longer verifies.
//
// Run with:
//
//	go test -tags halo2 -v -run TestSignBitFlip_RejectedAfterFix ./ffi/zkp/halo2/ -timeout 120s
func TestSignBitFlip_RejectedAfterFix(t *testing.T) {
	require.False(t, IsMock, "this test requires the real Halo2 verifier (build with -tags halo2)")

	// ── Step 1: Parse the fixture (same layout as prove_test.go). ──
	fixture, err := os.ReadFile("../testdata/share_reveal_inputs.bin")
	require.NoError(t, err, "fixture file missing — run: make fixtures")
	require.Len(t, fixture, 1488, "unexpected fixture size — run: make fixtures")

	merklePath := fixture[0:772]

	var shareComms [16][32]byte
	for i := 0; i < 16; i++ {
		copy(shareComms[i][:], fixture[772+i*32:772+(i+1)*32])
	}

	var primaryBlind [32]byte
	copy(primaryBlind[:], fixture[1284:1316])

	var encC1 [32]byte
	copy(encC1[:], fixture[1316:1348])

	var encC2 [32]byte
	copy(encC2[:], fixture[1348:1380])

	shareIndex := binary.LittleEndian.Uint32(fixture[1380:1384])
	proposalID := binary.LittleEndian.Uint32(fixture[1384:1388])
	voteDecision := binary.LittleEndian.Uint32(fixture[1388:1392])

	var roundID [32]byte
	copy(roundID[:], fixture[1392:1424])

	encShare := make([]byte, 64)
	copy(encShare, fixture[1424:1488])

	t.Logf("Original EncShare: %s", hex.EncodeToString(encShare))

	// ── Step 2: Generate a real Halo2 proof via Rust FFI. ──
	t.Log("Generating real share reveal proof via Rust FFI (~2s)...")
	proof, nullifier, treeRoot, err := GenerateShareRevealProof(
		merklePath, shareComms, primaryBlind,
		encC1, encC2,
		shareIndex, proposalID, voteDecision, roundID,
	)
	require.NoError(t, err)
	require.NotEmpty(t, proof)
	t.Logf("Proof: %d bytes, nullifier: %s, treeRoot: %s",
		len(proof), hex.EncodeToString(nullifier[:8]), hex.EncodeToString(treeRoot[:8]))

	// ── Step 3: Verify with original EncShare — must PASS. ──
	originalInputs := zkp.VoteShareInputs{
		ShareNullifier:   nullifier[:],
		EncShare:         encShare,
		ProposalId:       proposalID,
		VoteDecision:     voteDecision,
		VoteRoundId:      roundID[:],
		VoteCommTreeRoot: treeRoot[:],
	}

	err = VerifyShareRevealProof(proof, originalInputs)
	require.NoError(t, err, "real proof must verify against original EncShare")
	t.Log("[+] Step 3: Real Halo2 proof VERIFIED against original EncShare")

	// ── Step 4: Flip sign bits on EncShare. ──
	encFlipped := make([]byte, 64)
	copy(encFlipped, encShare)
	encFlipped[31] ^= 0x80
	encFlipped[63] ^= 0x80

	require.False(t, bytes.Equal(encShare, encFlipped),
		"flipped encoding must differ from original")
	t.Logf("Flipped  EncShare: %s", hex.EncodeToString(encFlipped))

	_, err = elgamal.UnmarshalCiphertext(encFlipped)
	require.NoError(t, err, "flipped EncShare must be valid Pallas points (both y-values are on-curve)")
	t.Log("[+] Step 4: Flipped EncShare is valid (both points on Pallas curve)")

	// ── Step 5: Verify SAME proof with flipped EncShare — must FAIL. ──
	// After the fix, the verifier decompresses to get y-coordinates. Flipping
	// the sign bit negates y, producing different public inputs. The proof was
	// generated for the original y-coordinates and must not verify.
	flippedInputs := zkp.VoteShareInputs{
		ShareNullifier:   nullifier[:],
		EncShare:         encFlipped,
		ProposalId:       proposalID,
		VoteDecision:     voteDecision,
		VoteRoundId:      roundID[:],
		VoteCommTreeRoot: treeRoot[:],
	}

	err = VerifyShareRevealProof(proof, flippedInputs)
	require.Error(t, err,
		"FIX VERIFIED: proof must NOT verify against sign-bit-flipped EncShare")
	t.Log("[+] Step 5: Proof correctly REJECTED for FLIPPED EncShare — vulnerability is closed")

	// ── Step 6: Confirm that the flipped points are negations of the originals. ──
	ctOriginal, err := elgamal.UnmarshalCiphertext(encShare)
	require.NoError(t, err)
	ctFlipped, err := elgamal.UnmarshalCiphertext(encFlipped)
	require.NoError(t, err)

	c1Sum := ctOriginal.C1.Add(ctFlipped.C1)
	require.True(t, c1Sum.IsIdentity(),
		"original.C1 + flipped.C1 must be identity (proving negation)")
	c2Sum := ctOriginal.C2.Add(ctFlipped.C2)
	require.True(t, c2Sum.IsIdentity(),
		"original.C2 + flipped.C2 must be identity (proving negation)")
	t.Log("[+] Step 6: Confirmed flipped points are negations of originals")

	t.Log("")
	t.Log("=================================================================")
	t.Log("  FIX VERIFIED: Sign-Bit Flip Now Correctly Rejected")
	t.Log("=================================================================")
	t.Log("")
	t.Log("  The verifier now decompresses ciphertext points to extract full")
	t.Log("  (x, y) coordinates. The circuit binds y-coordinates via the")
	t.Log("  5-input Poseidon share commitment hash, so flipping a sign bit")
	t.Log("  changes the y-coordinate and invalidates the proof chain:")
	t.Log("  share_comm → shares_hash → vote_commitment → Merkle root.")
}
