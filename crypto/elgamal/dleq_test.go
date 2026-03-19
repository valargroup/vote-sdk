package elgamal

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/shamir"
)

// ---------------------------------------------------------------------------
// Aggregate DLEQ — round-trip
// ---------------------------------------------------------------------------

func TestDLEQRoundTrip(t *testing.T) {
	values := []uint64{0, 1, 42, 1 << 24, 1 << 28}

	for _, v := range values {
		sk, pk := KeyGen(rand.Reader)
		ct, err := Encrypt(pk, v, rand.Reader)
		require.NoError(t, err)

		proof, err := GenerateDLEQProof(sk, ct, v)
		require.NoError(t, err)
		require.Len(t, proof, DLEQProofSize)

		err = VerifyDLEQProof(proof, pk, ct, v)
		require.NoError(t, err, "DLEQ verification failed for value %d", v)
	}
}

// ---------------------------------------------------------------------------
// Aggregate DLEQ — verification must reject
// ---------------------------------------------------------------------------

func TestDLEQVerifyRejects(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 42, rand.Reader)
	require.NoError(t, err)

	proof, err := GenerateDLEQProof(sk, ct, 42)
	require.NoError(t, err)

	_, pk2 := KeyGen(rand.Reader)
	ct2, err := Encrypt(pk, 42, rand.Reader)
	require.NoError(t, err)

	skFake, _ := KeyGen(rand.Reader)
	fakeProof, err := GenerateDLEQProof(skFake, ct, 42)
	require.NoError(t, err)

	tampered := make([]byte, DLEQProofSize)
	copy(tampered, proof)
	tampered[0] ^= 0x01

	tests := []struct {
		name        string
		proof       []byte
		pk          *PublicKey
		ct          *Ciphertext
		value       uint64
		errContains string
	}{
		{
			name:        "wrong_value",
			proof:       proof,
			pk:          pk,
			ct:          ct,
			value:       43,
			errContains: "verification failed",
		},
		{
			name:        "wrong_public_key",
			proof:       proof,
			pk:          pk2,
			ct:          ct,
			value:       42,
			errContains: "verification failed",
		},
		{
			name:        "wrong_ciphertext",
			proof:       proof,
			pk:          pk,
			ct:          ct2,
			value:       42,
			errContains: "verification failed",
		},
		{
			name:        "wrong_secret_key",
			proof:       fakeProof,
			pk:          pk,
			ct:          ct,
			value:       42,
			errContains: "verification failed",
		},
		{
			name:        "tampered_proof",
			proof:       tampered,
			pk:          pk,
			ct:          ct,
			value:       42,
			errContains: "",
		},
		{
			name:        "all_zero_proof",
			proof:       make([]byte, DLEQProofSize),
			pk:          pk,
			ct:          ct,
			value:       42,
			errContains: "",
		},
		{
			name:        "identity_public_key",
			proof:       proof,
			pk:          &PublicKey{Point: new(curvey.PointPallas).Identity()},
			ct:          ct,
			value:       42,
			errContains: "verification failed",
		},
		{
			name:        "identity_C1",
			proof:       proof,
			pk:          pk,
			ct:          &Ciphertext{C1: new(curvey.PointPallas).Identity(), C2: ct.C2},
			value:       42,
			errContains: "verification failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyDLEQProof(tc.proof, tc.pk, tc.ct, tc.value)
			require.Error(t, err)
			if tc.errContains != "" {
				require.Contains(t, err.Error(), tc.errContains)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Aggregate DLEQ — verify input validation
// ---------------------------------------------------------------------------

func TestDLEQVerifyInputValidation(t *testing.T) {
	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	sk, _ := KeyGen(rand.Reader)
	proof, err := GenerateDLEQProof(sk, &Ciphertext{C1: ct.C1, C2: ct.C2}, 10)
	require.NoError(t, err)
	_ = proof

	validProof := make([]byte, DLEQProofSize)
	copy(validProof, proof)

	tests := []struct {
		name        string
		proof       []byte
		pk          *PublicKey
		ct          *Ciphertext
		errContains string
	}{
		{
			name:        "nil_proof",
			proof:       nil,
			pk:          pk,
			ct:          ct,
			errContains: "expected",
		},
		{
			name:        "empty_proof",
			proof:       []byte{},
			pk:          pk,
			ct:          ct,
			errContains: "expected",
		},
		{
			name:        "truncated_proof",
			proof:       make([]byte, DLEQProofSize-1),
			pk:          pk,
			ct:          ct,
			errContains: "expected",
		},
		{
			name:        "oversized_proof",
			proof:       make([]byte, DLEQProofSize+1),
			pk:          pk,
			ct:          ct,
			errContains: "expected",
		},
		{
			name:        "nil_public_key",
			proof:       validProof,
			pk:          nil,
			ct:          ct,
			errContains: "public key must not be nil",
		},
		{
			name:        "nil_public_key_point",
			proof:       validProof,
			pk:          &PublicKey{Point: nil},
			ct:          ct,
			errContains: "public key must not be nil",
		},
		{
			name:        "nil_ciphertext",
			proof:       validProof,
			pk:          pk,
			ct:          nil,
			errContains: "ciphertext must not be nil",
		},
		{
			name:        "nil_C1",
			proof:       validProof,
			pk:          pk,
			ct:          &Ciphertext{C1: nil, C2: ct.C2},
			errContains: "ciphertext must not be nil",
		},
		{
			name:        "nil_C2",
			proof:       validProof,
			pk:          pk,
			ct:          &Ciphertext{C1: ct.C1, C2: nil},
			errContains: "ciphertext must not be nil",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyDLEQProof(tc.proof, tc.pk, tc.ct, 10)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// ---------------------------------------------------------------------------
// Aggregate DLEQ — generate input validation
// ---------------------------------------------------------------------------

func TestDLEQGenerateInputValidation(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)
	_ = sk

	tests := []struct {
		name        string
		sk          *SecretKey
		ct          *Ciphertext
		errContains string
	}{
		{
			name:        "nil_secret_key",
			sk:          nil,
			ct:          ct,
			errContains: "secret key must not be nil",
		},
		{
			name:        "nil_secret_key_scalar",
			sk:          &SecretKey{Scalar: nil},
			ct:          ct,
			errContains: "secret key must not be nil",
		},
		{
			name:        "nil_ciphertext",
			sk:          sk,
			ct:          nil,
			errContains: "ciphertext must not be nil",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := GenerateDLEQProof(tc.sk, tc.ct, 10)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// ---------------------------------------------------------------------------
// Aggregate DLEQ — homomorphic accumulator
// ---------------------------------------------------------------------------

func TestDLEQHomomorphicAccumulator(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)

	values := []uint64{100, 200, 300, 400}
	var totalValue uint64

	var acc *Ciphertext
	for _, v := range values {
		ct, err := Encrypt(pk, v, rand.Reader)
		require.NoError(t, err)
		if acc == nil {
			acc = ct
		} else {
			acc = HomomorphicAdd(acc, ct)
		}
		totalValue += v
	}

	vG := DecryptToPoint(sk, acc)
	G := PallasGenerator()
	expectedVG := G.Mul(scalarFromUint64(totalValue))
	require.True(t, vG.Equal(expectedVG), "decrypted point should match totalValue*G")

	proof, err := GenerateDLEQProof(sk, acc, totalValue)
	require.NoError(t, err)

	err = VerifyDLEQProof(proof, pk, acc, totalValue)
	require.NoError(t, err, "DLEQ proof should verify for accumulated ciphertext")

	err = VerifyDLEQProof(proof, pk, acc, totalValue+1)
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Partial-decryption DLEQ — round-trip
// ---------------------------------------------------------------------------

func TestPartialDecryptDLEQRoundTrip(t *testing.T) {
	G := PallasGenerator()
	share := new(curvey.ScalarPallas).Random(rand.Reader)
	VKi := G.Mul(share)

	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 42, rand.Reader)
	require.NoError(t, err)

	proof, err := GeneratePartialDecryptDLEQ(share, ct.C1)
	require.NoError(t, err)
	require.Len(t, proof, DLEQProofSize)

	Di := ct.C1.Mul(share)
	err = VerifyPartialDecryptDLEQ(proof, VKi, ct.C1, Di)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Partial-decryption DLEQ — verification must reject
// ---------------------------------------------------------------------------

func TestPartialDecryptDLEQVerifyRejects(t *testing.T) {
	G := PallasGenerator()
	share := new(curvey.ScalarPallas).Random(rand.Reader)
	VKi := G.Mul(share)

	_, pk := KeyGen(rand.Reader)
	ct1, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)
	ct2, err := Encrypt(pk, 20, rand.Reader)
	require.NoError(t, err)

	proof, err := GeneratePartialDecryptDLEQ(share, ct1.C1)
	require.NoError(t, err)
	Di := ct1.C1.Mul(share)

	fakeShare := new(curvey.ScalarPallas).Random(rand.Reader)
	fakeVK := G.Mul(fakeShare)
	fakeProof, err := GeneratePartialDecryptDLEQ(fakeShare, ct1.C1)
	require.NoError(t, err)
	fakeDi := ct1.C1.Mul(fakeShare)

	tampered := make([]byte, DLEQProofSize)
	copy(tampered, proof)
	tampered[0] ^= 0x01

	tests := []struct {
		name  string
		proof []byte
		VKi   curvey.Point
		C1    curvey.Point
		Di    curvey.Point
	}{
		{
			name:  "wrong_share_proof_against_real_VK",
			proof: fakeProof,
			VKi:   VKi,
			C1:    ct1.C1,
			Di:    fakeDi,
		},
		{
			name:  "wrong_VK",
			proof: proof,
			VKi:   fakeVK,
			C1:    ct1.C1,
			Di:    Di,
		},
		{
			name:  "wrong_C1",
			proof: proof,
			VKi:   VKi,
			C1:    ct2.C1,
			Di:    Di,
		},
		{
			name:  "tampered_proof",
			proof: tampered,
			VKi:   VKi,
			C1:    ct1.C1,
			Di:    Di,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyPartialDecryptDLEQ(tc.proof, tc.VKi, tc.C1, tc.Di)
			require.Error(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// Partial-decryption DLEQ — verify input validation
// ---------------------------------------------------------------------------

func TestPartialDecryptDLEQVerifyInputValidation(t *testing.T) {
	G := PallasGenerator()
	share := new(curvey.ScalarPallas).Random(rand.Reader)
	VKi := G.Mul(share)

	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	proof, err := GeneratePartialDecryptDLEQ(share, ct.C1)
	require.NoError(t, err)
	Di := ct.C1.Mul(share)

	tests := []struct {
		name        string
		proof       []byte
		VKi         curvey.Point
		C1          curvey.Point
		Di          curvey.Point
		errContains string
	}{
		{
			name:        "nil_proof",
			proof:       nil,
			VKi:         VKi,
			C1:          ct.C1,
			Di:          Di,
			errContains: "expected",
		},
		{
			name:        "truncated_proof",
			proof:       []byte{0x01},
			VKi:         VKi,
			C1:          ct.C1,
			Di:          Di,
			errContains: "expected",
		},
		{
			name:        "nil_VKi",
			proof:       proof,
			VKi:         nil,
			C1:          ct.C1,
			Di:          Di,
			errContains: "VK_i must not be nil",
		},
		{
			name:        "nil_C1",
			proof:       proof,
			VKi:         VKi,
			C1:          nil,
			Di:          Di,
			errContains: "C1 must not be nil",
		},
		{
			name:        "nil_Di",
			proof:       proof,
			VKi:         VKi,
			C1:          ct.C1,
			Di:          nil,
			errContains: "D_i must not be nil",
		},
		{
			name:        "identity_C1",
			proof:       proof,
			VKi:         VKi,
			C1:          new(curvey.PointPallas).Identity(),
			Di:          Di,
			errContains: "C1 must be a valid non-identity point",
		},
		{
			name:        "identity_VKi",
			proof:       proof,
			VKi:         new(curvey.PointPallas).Identity(),
			C1:          ct.C1,
			Di:          Di,
			errContains: "VK_i must be a valid non-identity point",
		},
		{
			name:        "identity_Di",
			proof:       proof,
			VKi:         VKi,
			C1:          ct.C1,
			Di:          new(curvey.PointPallas).Identity(),
			errContains: "verification failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyPartialDecryptDLEQ(tc.proof, tc.VKi, tc.C1, tc.Di)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// ---------------------------------------------------------------------------
// Partial-decryption DLEQ — generate input validation
// ---------------------------------------------------------------------------

func TestPartialDecryptDLEQGenerateInputValidation(t *testing.T) {
	_, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 10, rand.Reader)
	require.NoError(t, err)

	share := new(curvey.ScalarPallas).Random(rand.Reader)

	tests := []struct {
		name        string
		share       curvey.Scalar
		C1          curvey.Point
		errContains string
	}{
		{
			name:        "nil_share",
			share:       nil,
			C1:          ct.C1,
			errContains: "share must not be nil or zero",
		},
		{
			name:        "zero_share",
			share:       new(curvey.ScalarPallas).Zero(),
			C1:          ct.C1,
			errContains: "share must not be nil or zero",
		},
		{
			name:        "nil_C1",
			share:       share,
			C1:          nil,
			errContains: "C1 must not be nil",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := GeneratePartialDecryptDLEQ(tc.share, tc.C1)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// ---------------------------------------------------------------------------
// Cross-protocol domain separation
// ---------------------------------------------------------------------------

func TestDLEQDomainSeparation(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	ct, err := Encrypt(pk, 0, rand.Reader)
	require.NoError(t, err)

	// For value=0: D = C2 - 0*G = C2 = sk*C1, and pk = sk*G.
	// So VK_i = pk, C1 = ct.C1, D_i = C2 — identical points in both statements.

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "aggregate_proof_rejected_by_partial_verifier",
			run: func(t *testing.T) {
				aggProof, err := GenerateDLEQProof(sk, ct, 0)
				require.NoError(t, err)

				err = VerifyPartialDecryptDLEQ(aggProof, pk.Point, ct.C1, ct.C2)
				require.Error(t, err, "aggregate proof must not pass partial-decrypt verification")
			},
		},
		{
			name: "partial_proof_rejected_by_aggregate_verifier",
			run: func(t *testing.T) {
				pdProof, err := GeneratePartialDecryptDLEQ(sk.Scalar, ct.C1)
				require.NoError(t, err)

				err = VerifyDLEQProof(pdProof, pk, ct, 0)
				require.Error(t, err, "partial-decrypt proof must not pass aggregate verification")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: threshold partial decryption + aggregate decryption
// ---------------------------------------------------------------------------

func TestThresholdDLEQEndToEnd(t *testing.T) {
	// Simulates the full voting pipeline:
	//  1. KeyGen + Shamir split
	//  2. Encrypt ballots and homomorphically accumulate
	//  3. Each validator produces D_i + partial DLEQ proof
	//  4. Verifier checks each partial proof against on-chain VK_i
	//  5. Lagrange-combine partials → recover ea_sk * C1_agg
	//  6. Derive v*G = C2_agg - ea_sk*C1_agg, verify against expected total
	//  7. Aggregate DLEQ proof (when full ea_sk is available)

	const (
		n         = 5
		threshold = 3
	)
	voteValues := []uint64{100, 200, 150, 50}
	var expectedTotal uint64
	for _, v := range voteValues {
		expectedTotal += v
	}

	G := PallasGenerator()

	// --- 1. KeyGen + Shamir split ---
	eaSk, eaPk := KeyGen(rand.Reader)

	shares, _, err := shamir.Split(eaSk.Scalar, threshold, n)
	require.NoError(t, err)
	require.Len(t, shares, n)

	VKs := make([]curvey.Point, n)
	for i, s := range shares {
		VKs[i] = G.Mul(s.Value)
	}

	// --- 2. Encrypt ballots and homomorphically accumulate ---
	var accCt *Ciphertext
	for _, v := range voteValues {
		ct, err := Encrypt(eaPk, v, rand.Reader)
		require.NoError(t, err)
		if accCt == nil {
			accCt = ct
		} else {
			accCt = HomomorphicAdd(accCt, ct)
		}
	}

	C1agg := accCt.C1
	C2agg := accCt.C2

	// --- 3 & 4. Each validator: partial decrypt + DLEQ proof + verification ---
	type validatorPartial struct {
		index int
		Di    curvey.Point
		proof []byte
	}
	partials := make([]validatorPartial, n)
	for i, s := range shares {
		Di := C1agg.Mul(s.Value)

		proof, err := GeneratePartialDecryptDLEQ(s.Value, C1agg)
		require.NoError(t, err, "validator %d: proof generation failed", i)
		require.Len(t, proof, DLEQProofSize)

		err = VerifyPartialDecryptDLEQ(proof, VKs[i], C1agg, Di)
		require.NoError(t, err, "validator %d: partial DLEQ verification failed", i)

		partials[i] = validatorPartial{
			index: s.Index,
			Di:    Di,
			proof: proof,
		}
	}

	// --- 4b. Negative: bogus D_i with valid proof for a different share must fail ---
	t.Run("bogus_Di_rejected", func(t *testing.T) {
		fakeShare := new(curvey.ScalarPallas).Random(rand.Reader)
		fakeDi := C1agg.Mul(fakeShare)
		fakeProof, err := GeneratePartialDecryptDLEQ(fakeShare, C1agg)
		require.NoError(t, err)

		err = VerifyPartialDecryptDLEQ(fakeProof, VKs[0], C1agg, fakeDi)
		require.Error(t, err, "bogus partial must be rejected against real VK_i")
	})

	// --- 5. Lagrange-combine a threshold subset → ea_sk * C1_agg ---
	shamirPartials := make([]shamir.PartialDecryption, threshold)
	for i := 0; i < threshold; i++ {
		shamirPartials[i] = shamir.PartialDecryption{
			Index: partials[i].index,
			Di:    partials[i].Di,
		}
	}
	skC1, err := shamir.CombinePartials(shamirPartials, threshold)
	require.NoError(t, err)

	// --- 6. Derive v*G and verify against expected total ---
	vG := C2agg.Sub(skC1)
	expectedVG := G.Mul(scalarFromUint64(expectedTotal))
	require.True(t, vG.Equal(expectedVG),
		"C2_agg - ea_sk*C1_agg must equal expectedTotal*G (got mismatch for total=%d)", expectedTotal)

	// --- 7. Aggregate decryption proof using full ea_sk as a "share" ---
	// When the full secret key is available, GeneratePartialDecryptDLEQ can
	// prove correct decryption directly: D = ea_sk * C1_agg, with
	// log_G(ea_pk) == log_{C1_agg}(D).
	aggProof, err := GeneratePartialDecryptDLEQ(eaSk.Scalar, C1agg)
	require.NoError(t, err)
	require.Len(t, aggProof, DLEQProofSize)

	D := C1agg.Mul(eaSk.Scalar)
	err = VerifyPartialDecryptDLEQ(aggProof, eaPk.Point, C1agg, D)
	require.NoError(t, err, "aggregate DLEQ proof must verify with full ea_sk")

	// --- 7b. Aggregate DLEQ with v bound into the proof ---
	// GenerateDLEQProof binds the plaintext total into the Fiat-Shamir
	// challenge (D = C2 - v*G), so a wrong v is rejected by the verifier.
	aggProof2, err := GenerateDLEQProof(eaSk, accCt, expectedTotal)
	require.NoError(t, err)
	require.Len(t, aggProof2, DLEQProofSize)

	err = VerifyDLEQProof(aggProof2, eaPk, accCt, expectedTotal)
	require.NoError(t, err, "aggregate DLEQ proof must verify with correct total")

	err = VerifyDLEQProof(aggProof2, eaPk, accCt, expectedTotal+1)
	require.Error(t, err, "aggregate DLEQ proof must reject wrong total")

	// D must yield the correct plaintext: C2_agg - D == expectedTotal * G.
	vGFromD := C2agg.Sub(D)
	require.True(t, vGFromD.Equal(expectedVG),
		"C2_agg - ea_sk*C1_agg via full key must equal expectedTotal*G")

	// Proof against a wrong ea_pk must fail.
	_, wrongPk := KeyGen(rand.Reader)
	err = VerifyPartialDecryptDLEQ(aggProof, wrongPk.Point, C1agg, D)
	require.Error(t, err, "aggregate DLEQ proof must fail against wrong pk")

	// --- 8. Verify with different threshold subsets ---
	shamirPartialsAlt := make([]shamir.PartialDecryption, threshold)
	for i := 0; i < threshold; i++ {
		p := partials[n-threshold+i]
		shamirPartialsAlt[i] = shamir.PartialDecryption{
			Index: p.index,
			Di:    p.Di,
		}
	}
	skC1Alt, err := shamir.CombinePartials(shamirPartialsAlt, threshold)
	require.NoError(t, err)

	vGAlt := C2agg.Sub(skC1Alt)
	require.True(t, vGAlt.Equal(expectedVG),
		"alternate subset must produce the same v*G")
}
