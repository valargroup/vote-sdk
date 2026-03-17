package shamir

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
)

// TestPartialDecryptEndToEnd verifies that threshold partial decryption
// produces the same result as full-key decryption via elgamal.DecryptToPoint.
func TestPartialDecryptEndToEnd(t *testing.T) {
	sk, pk := elgamal.KeyGen(rand.Reader)
	G := elgamal.PallasGenerator()

	for _, tc := range []struct {
		name  string
		t, n  int
		value uint64
	}{
		{"2-of-3 v=0", 2, 3, 0},
		{"2-of-3 v=42", 2, 3, 42},
		{"3-of-5 v=1000", 3, 5, 1000},
		{"5-of-5 v=1", 5, 5, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := elgamal.Encrypt(pk, tc.value, rand.Reader)
			require.NoError(t, err)

			shares, _, err := Split(sk.Scalar, tc.t, tc.n)
			require.NoError(t, err)

			// Compute all partial decryptions, use exactly t.
			partials := make([]PartialDecryption, tc.t)
			for i := 0; i < tc.t; i++ {
				di, err := PartialDecrypt(shares[i].Value, ct.C1)
				require.NoError(t, err)
				partials[i] = PartialDecryption{Index: shares[i].Index, Di: di}
			}

			combined, err := CombinePartials(partials, tc.t)
			require.NoError(t, err)

			// ct.C2 - combined should equal v*G
			got := ct.C2.Sub(combined)
			want := elgamal.DecryptToPoint(sk, ct)

			// Compare compressed representations.
			require.Equal(t, want.ToAffineCompressed(), got.ToAffineCompressed())

			// Also verify against v*G directly for v > 0.
			if tc.value > 0 {
				vScalar := new(curvey.ScalarPallas).New(int(tc.value))
				vG := G.Mul(vScalar)
				require.Equal(t, vG.ToAffineCompressed(), got.ToAffineCompressed())
			} else {
				require.True(t, got.IsIdentity())
			}
		})
	}
}

// TestCombinePartials_SingleShare verifies that CombinePartials works with a
// single partial decryption (t=1, n=1). The Lagrange coefficient is 1 (empty
// product), so the result is just D_1 = share * C1 = sk * C1.
func TestCombinePartials_SingleShare(t *testing.T) {
	sk, pk := elgamal.KeyGen(rand.Reader)

	ct, err := elgamal.Encrypt(pk, 42, rand.Reader)
	require.NoError(t, err)

	shares, _, err := Split(sk.Scalar, 1, 1)
	require.NoError(t, err)
	require.Len(t, shares, 1)

	Di, err := PartialDecrypt(shares[0].Value, ct.C1)
	require.NoError(t, err)

	combined, err := CombinePartials([]PartialDecryption{{Index: 1, Di: Di}}, 1)
	require.NoError(t, err)

	// CombinePartials returns sk*C1. Verify: C2 - combined == v*G.
	vG := ct.C2.Sub(combined)
	expectedVG := elgamal.DecryptToPoint(sk, ct)
	require.True(t, vG.Equal(expectedVG),
		"C2 - CombinePartials must equal DecryptToPoint result")
}

// TestPartialDecryptAnySubset verifies that any t-sized subset of n partial
// decryptions produces the same combined result.
func TestPartialDecryptAnySubset(t *testing.T) {
	sk, pk := elgamal.KeyGen(rand.Reader)
	threshold := 3
	n := 7
	value := uint64(777)

	ct, err := elgamal.Encrypt(pk, value, rand.Reader)
	require.NoError(t, err)

	shares, _, err := Split(sk.Scalar, threshold, n)
	require.NoError(t, err)

	// Compute all n partial decryptions.
	allPartials := make([]PartialDecryption, n)
	for i := 0; i < n; i++ {
		di, err := PartialDecrypt(shares[i].Value, ct.C1)
		require.NoError(t, err)
		allPartials[i] = PartialDecryption{Index: shares[i].Index, Di: di}
	}

	want := elgamal.DecryptToPoint(sk, ct).ToAffineCompressed()

	subsets := [][]int{
		{0, 1, 2},
		{0, 3, 6},
		{2, 4, 5},
		{1, 5, 6},
		{4, 5, 6},
	}

	for _, subset := range subsets {
		picked := make([]PartialDecryption, len(subset))
		for i, idx := range subset {
			picked[i] = allPartials[idx]
		}

		combined, err := CombinePartials(picked, threshold)
		require.NoError(t, err)

		got := ct.C2.Sub(combined).ToAffineCompressed()
		require.Equal(t, want, got, "subset %v should produce correct decryption", subset)
	}
}

// TestPartialDecryptValidation checks error handling for invalid inputs.
func TestPartialDecryptValidation(t *testing.T) {
	G := elgamal.PallasGenerator()
	share := new(curvey.ScalarPallas).Random(rand.Reader)

	_, err := PartialDecrypt(nil, G)
	require.Error(t, err)
	require.Contains(t, err.Error(), "share must not be nil")

	_, err = PartialDecrypt(share, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "C1 must not be nil")
}

// TestOneWrongPartialCorruptsTally simulates a 3-of-5 threshold tally where
// validator 3 submits a bogus partial decryption (using a random scalar instead
// of their real share). Without DLEQ proof verification, CombinePartials
// succeeds but produces a wrong tally — the recovered plaintext point does not
// match the expected v*G.
//
// This demonstrates why per-validator DLEQ proofs (Step 2 in the TSS roadmap)
// are essential: without them, a single malicious validator can silently
// sabotage the tally.
func TestOneWrongPartialCorruptsTally(t *testing.T) {
	const (
		n         = 5
		threshold = 3
		yesVotes  = uint64(42)
		noVotes   = uint64(17)
	)

	sk, pk := elgamal.KeyGen(rand.Reader)
	G := elgamal.PallasGenerator()

	// Simulate two accumulators: "yes" and "no" tallies.
	yesAcc, err := elgamal.Encrypt(pk, yesVotes, rand.Reader)
	require.NoError(t, err)
	noAcc, err := elgamal.Encrypt(pk, noVotes, rand.Reader)
	require.NoError(t, err)

	// Split ea_sk into 5 shares with threshold 3.
	shares, _, err := Split(sk.Scalar, threshold, n)
	require.NoError(t, err)

	// --- Correct tally (all 5 honest) ---
	honestYes := make([]PartialDecryption, threshold)
	honestNo := make([]PartialDecryption, threshold)
	for i := 0; i < threshold; i++ {
		di, err := PartialDecrypt(shares[i].Value, yesAcc.C1)
		require.NoError(t, err)
		honestYes[i] = PartialDecryption{Index: shares[i].Index, Di: di}

		di, err = PartialDecrypt(shares[i].Value, noAcc.C1)
		require.NoError(t, err)
		honestNo[i] = PartialDecryption{Index: shares[i].Index, Di: di}
	}

	combinedYes, err := CombinePartials(honestYes, threshold)
	require.NoError(t, err)
	combinedNo, err := CombinePartials(honestNo, threshold)
	require.NoError(t, err)

	correctYesPoint := yesAcc.C2.Sub(combinedYes)
	correctNoPoint := noAcc.C2.Sub(combinedNo)

	wantYes := elgamal.ValuePoint(yesVotes)
	wantNo := elgamal.ValuePoint(noVotes)
	require.Equal(t, wantYes.ToAffineCompressed(), correctYesPoint.ToAffineCompressed(),
		"honest tally should recover correct yes votes")
	require.Equal(t, wantNo.ToAffineCompressed(), correctNoPoint.ToAffineCompressed(),
		"honest tally should recover correct no votes")

	// --- Corrupted tally: validator 3 (index 2) submits a bogus partial ---
	// Use first 3 validators (indices 0,1,2) but corrupt validator 3's share.
	corruptedYes := make([]PartialDecryption, threshold)
	corruptedNo := make([]PartialDecryption, threshold)
	for i := 0; i < threshold; i++ {
		share := shares[i].Value
		if i == 2 {
			// Validator 3 uses a random scalar instead of their real share.
			share = new(curvey.ScalarPallas).Random(rand.Reader)
		}

		di, err := PartialDecrypt(share, yesAcc.C1)
		require.NoError(t, err)
		corruptedYes[i] = PartialDecryption{Index: shares[i].Index, Di: di}

		di, err = PartialDecrypt(share, noAcc.C1)
		require.NoError(t, err)
		corruptedNo[i] = PartialDecryption{Index: shares[i].Index, Di: di}
	}

	// CombinePartials still succeeds — it has no way to detect the lie.
	badCombinedYes, err := CombinePartials(corruptedYes, threshold)
	require.NoError(t, err)
	badCombinedNo, err := CombinePartials(corruptedNo, threshold)
	require.NoError(t, err)

	badYesPoint := yesAcc.C2.Sub(badCombinedYes)
	badNoPoint := noAcc.C2.Sub(badCombinedNo)

	// The recovered points do NOT match the expected tally.
	require.NotEqual(t, wantYes.ToAffineCompressed(), badYesPoint.ToAffineCompressed(),
		"corrupted partial should produce wrong yes tally")
	require.NotEqual(t, wantNo.ToAffineCompressed(), badNoPoint.ToAffineCompressed(),
		"corrupted partial should produce wrong no tally")

	// The bad tally also doesn't match the honest tally.
	require.NotEqual(t, correctYesPoint.ToAffineCompressed(), badYesPoint.ToAffineCompressed(),
		"corrupted yes tally should differ from honest tally")
	require.NotEqual(t, correctNoPoint.ToAffineCompressed(), badNoPoint.ToAffineCompressed(),
		"corrupted no tally should differ from honest tally")

	// --- DLEQ catches the cheater ---
	// Verification keys VK_i = share_i * G are published during the ceremony.
	// A DLEQ proof for the honest partial passes; the corrupt one fails.
	for i := 0; i < threshold; i++ {
		VKi := G.Mul(shares[i].Value) // on-chain verification key

		// Honest partial: proof verifies.
		proof, err := elgamal.GeneratePartialDecryptDLEQ(shares[i].Value, yesAcc.C1)
		require.NoError(t, err)
		honestDi, err := PartialDecrypt(shares[i].Value, yesAcc.C1)
		require.NoError(t, err)
		err = elgamal.VerifyPartialDecryptDLEQ(proof, VKi, yesAcc.C1, honestDi)
		require.NoError(t, err, "honest validator %d DLEQ should pass", i+1)
	}

	// Corrupt validator 3 cannot produce a valid DLEQ proof against VK_3.
	fakeShare := new(curvey.ScalarPallas).Random(rand.Reader)
	fakeDi, err := PartialDecrypt(fakeShare, yesAcc.C1)
	require.NoError(t, err)
	fakeProof, err := elgamal.GeneratePartialDecryptDLEQ(fakeShare, yesAcc.C1)
	require.NoError(t, err)

	VK3 := G.Mul(shares[2].Value) // real on-chain VK for validator 3
	err = elgamal.VerifyPartialDecryptDLEQ(fakeProof, VK3, yesAcc.C1, fakeDi)
	require.Error(t, err, "DLEQ must reject partial decryption from wrong share")
	require.Contains(t, err.Error(), "proof verification failed")
}

// TestMaliciousDealerWrongShareCorruptsTally simulates a 3-of-5 ceremony where
// the dealer honestly generates ea_sk and the polynomial, but secretly sends a
// wrong share to validator 3. Without Feldman commitments the victim cannot
// detect the substitution at ack time, so the corrupted share silently poisons
// any threshold subset that includes validator 3.
//
// The test shows:
//  1. Subsets that exclude validator 3 still produce the correct tally.
//  2. Any subset that includes validator 3 produces a wrong tally.
//  3. With Feldman commitments, validator 3 detects the bad share at ack time.
func TestMaliciousDealerWrongShareCorruptsTally(t *testing.T) {
	const (
		n         = 5
		threshold = 3
		totalVote = uint64(100)
	)

	G := elgamal.PallasGenerator()

	// --- Dealer ceremony ---
	sk, pk := elgamal.KeyGen(rand.Reader)
	shares, coeffs, err := Split(sk.Scalar, threshold, n)
	require.NoError(t, err)

	// Encrypt a tally accumulator.
	acc, err := elgamal.Encrypt(pk, totalVote, rand.Reader)
	require.NoError(t, err)

	// The dealer replaces validator 3's share with a random scalar.
	// In a real attack the ECIES payload for validator 3 would contain this
	// bogus value instead of the correct f(3).
	badShare := new(curvey.ScalarPallas).Random(rand.Reader)
	poisonedShares := make([]Share, n)
	copy(poisonedShares, shares)
	poisonedShares[2] = Share{Index: shares[2].Index, Value: badShare}

	// --- Without Feldman: validator 3 cannot detect the bad share ---
	// The legacy (Step 1) ack only checks share_i * G == VK_i, but VK_i
	// itself was provided by the dealer. The dealer can simply publish
	// VK_3' = badShare * G and the check passes trivially.
	fakeVK3 := G.Mul(badShare)
	realVK3 := G.Mul(shares[2].Value)
	require.False(t, fakeVK3.Equal(realVK3),
		"dealer's fake VK should differ from the real one")

	// Compute all 5 partial decryptions using the shares each validator
	// actually received (validator 3 got the bad share).
	allPartials := make([]PartialDecryption, n)
	for i := 0; i < n; i++ {
		di, err := PartialDecrypt(poisonedShares[i].Value, acc.C1)
		require.NoError(t, err)
		allPartials[i] = PartialDecryption{Index: poisonedShares[i].Index, Di: di}
	}

	wantPoint := elgamal.ValuePoint(totalVote)

	// --- Subsets WITHOUT validator 3 → correct tally ---
	cleanSubsets := [][]int{
		{0, 1, 3}, // validators 1, 2, 4
		{0, 1, 4}, // validators 1, 2, 5
		{0, 3, 4}, // validators 1, 4, 5
		{1, 3, 4}, // validators 2, 4, 5
	}
	for _, subset := range cleanSubsets {
		picked := make([]PartialDecryption, len(subset))
		for i, idx := range subset {
			picked[i] = allPartials[idx]
		}
		combined, err := CombinePartials(picked, threshold)
		require.NoError(t, err)

		got := acc.C2.Sub(combined)
		require.Equal(t, wantPoint.ToAffineCompressed(), got.ToAffineCompressed(),
			"subset %v (no validator 3) should produce correct tally", subset)
	}

	// --- Subsets WITH validator 3 → wrong tally ---
	poisonedSubsets := [][]int{
		{0, 1, 2}, // validators 1, 2, 3
		{0, 2, 3}, // validators 1, 3, 4
		{1, 2, 4}, // validators 2, 3, 5
		{2, 3, 4}, // validators 3, 4, 5
	}
	for _, subset := range poisonedSubsets {
		picked := make([]PartialDecryption, len(subset))
		for i, idx := range subset {
			picked[i] = allPartials[idx]
		}
		combined, err := CombinePartials(picked, threshold)
		require.NoError(t, err)

		got := acc.C2.Sub(combined)
		require.NotEqual(t, wantPoint.ToAffineCompressed(), got.ToAffineCompressed(),
			"subset %v (includes validator 3) should produce WRONG tally", subset)
	}

	// --- Feldman commitments catch the bad share at ack time ---
	commitments, err := FeldmanCommit(G, coeffs)
	require.NoError(t, err)

	// Honest shares all pass Feldman verification.
	for i := 0; i < n; i++ {
		ok, err := VerifyFeldmanShare(G, commitments, shares[i].Index, shares[i].Value)
		require.NoError(t, err)
		require.True(t, ok, "honest share %d should pass Feldman verification", i+1)
	}

	// The bad share sent to validator 3 fails Feldman verification.
	ok, err := VerifyFeldmanShare(G, commitments, shares[2].Index, badShare)
	require.NoError(t, err)
	require.False(t, ok,
		"Feldman must reject the bad share — validator 3 detects the malicious dealer")
}

// TestCombinePartialsValidation checks error handling for CombinePartials.
func TestCombinePartialsValidation(t *testing.T) {
	G := elgamal.PallasGenerator()
	share := new(curvey.ScalarPallas).Random(rand.Reader)
	di := G.Mul(share)

	// Fewer than t partials.
	_, err := CombinePartials([]PartialDecryption{{Index: 1, Di: di}}, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "need at least 2 partials")

	// Nil Di in one of the partials.
	_, err = CombinePartials([]PartialDecryption{
		{Index: 1, Di: di},
		{Index: 2, Di: nil},
	}, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil Di")

	// Duplicate indices.
	_, err = CombinePartials([]PartialDecryption{
		{Index: 1, Di: di},
		{Index: 1, Di: di},
	}, 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate index")
}
