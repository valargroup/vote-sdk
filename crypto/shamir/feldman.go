package shamir

import (
	"fmt"

	"github.com/mikelodder7/curvey"
)

// FeldmanCommit computes Feldman polynomial commitments from the coefficient
// vector returned by Split. Each commitment is C_j = a_j * G for j = 0..t-1.
//
// The resulting commitments are public and safe to broadcast. Validators use
// them to verify their share without learning the secret or any other share:
//
//	share_i * G == sum(C_j * i^j)   for j = 0..t-1
//
// The first commitment C_0 = a_0 * G = secret * G is the public key
// corresponding to the shared secret.
func FeldmanCommit(G curvey.Point, coeffs []curvey.Scalar) ([]curvey.Point, error) {
	if G == nil {
		return nil, fmt.Errorf("shamir: FeldmanCommit: generator G must not be nil")
	}
	if !G.IsOnCurve() {
		return nil, fmt.Errorf("shamir: FeldmanCommit: generator G is not on the curve")
	}
	if G.IsIdentity() {
		return nil, fmt.Errorf("shamir: FeldmanCommit: generator G must not be the identity point")
	}
	if len(coeffs) == 0 {
		return nil, fmt.Errorf("shamir: FeldmanCommit: coefficients must not be empty")
	}
	commitments := make([]curvey.Point, len(coeffs))
	for j, a := range coeffs {
		if a == nil {
			return nil, fmt.Errorf("shamir: FeldmanCommit: coefficient %d is nil", j)
		}
		commitments[j] = G.Mul(a)
	}
	return commitments, nil
}

// VerifyFeldmanShare checks that a Shamir share is consistent with public
// Feldman commitments. Returns true if and only if:
//
//	share * G == EvalCommitmentPolynomial(commitments, index)
//
// A malicious dealer who sends a share inconsistent with the published
// commitments will be detected by this check.
func VerifyFeldmanShare(G curvey.Point, commitments []curvey.Point, index int, share curvey.Scalar) (bool, error) {
	if G == nil {
		return false, fmt.Errorf("shamir: VerifyFeldmanShare: generator G must not be nil")
	}
	if !G.IsOnCurve() {
		return false, fmt.Errorf("shamir: VerifyFeldmanShare: generator G is not on the curve")
	}
	if G.IsIdentity() {
		return false, fmt.Errorf("shamir: VerifyFeldmanShare: generator G must not be the identity point")
	}
	if share == nil {
		return false, fmt.Errorf("shamir: VerifyFeldmanShare: share must not be nil")
	}
	if index <= 0 {
		return false, fmt.Errorf("shamir: VerifyFeldmanShare: index must be > 0, got %d", index)
	}

	expected, err := EvalCommitmentPolynomial(commitments, index)
	if err != nil {
		return false, err
	}

	actual := G.Mul(share)
	return actual.Equal(expected), nil
}

// CombineCommitments performs the point-wise sum of n Feldman commitment
// vectors, producing the combined commitment vector for a Joint-Feldman DKG.
//
// In Joint-Feldman DKG each contributor i publishes commitments
// C_{i,j} = a_{i,j} * G for j = 0..t-1. The combined commitment vector is:
//
//	C_j = sum_i(C_{i,j})   for j = 0..t-1
//
// C_0 is the combined public key: ea_pk = (sum of all contributors' secret
// shares) * G. The result can be used with VerifyFeldmanShare and
// EvalCommitmentPolynomial exactly like a single-dealer commitment vector,
// because each validator's combined share s_i = sum_k(s_{k,i}) satisfies:
//
//	s_i * G == EvalCommitmentPolynomial(combined, i)
//
// All contribution vectors must have the same length (the threshold t).
// At least one contribution is required.
func CombineCommitments(contributions [][]curvey.Point) ([]curvey.Point, error) {
	if len(contributions) == 0 {
		return nil, fmt.Errorf("shamir: CombineCommitments: contributions must not be empty")
	}

	t := len(contributions[0])
	if t == 0 {
		return nil, fmt.Errorf("shamir: CombineCommitments: commitment vectors must not be empty")
	}

	for i, vec := range contributions {
		if len(vec) != t {
			return nil, fmt.Errorf("shamir: CombineCommitments: contribution %d has length %d, expected %d", i, len(vec), t)
		}
		for j, pt := range vec {
			if pt == nil {
				return nil, fmt.Errorf("shamir: CombineCommitments: contribution %d commitment %d is nil", i, j)
			}
			if !pt.IsOnCurve() {
				return nil, fmt.Errorf("shamir: CombineCommitments: contribution %d commitment %d is not on the curve", i, j)
			}
		}
	}

	combined := make([]curvey.Point, t)
	for j := 0; j < t; j++ {
		sum := contributions[0][j]
		for i := 1; i < len(contributions); i++ {
			sum = sum.Add(contributions[i][j])
		}
		combined[j] = sum
	}

	return combined, nil
}

// EvalCommitmentPolynomial evaluates the Feldman commitment polynomial at a
// given Shamir index using Horner's method in the group:
//
//	VK = C_0 + index*C_1 + index^2*C_2 + ... + index^{t-1}*C_{t-1}
//
// The result is the verification key VK_i for the validator at that index.
// This is used during partial decryption DLEQ verification to derive VK_i
// on the fly from the stored Feldman commitments.
func EvalCommitmentPolynomial(commitments []curvey.Point, index int) (curvey.Point, error) {
	if len(commitments) == 0 {
		return nil, fmt.Errorf("shamir: EvalCommitmentPolynomial: commitments must not be empty")
	}
	if index <= 0 {
		return nil, fmt.Errorf("shamir: EvalCommitmentPolynomial: index must be > 0, got %d", index)
	}
	for j, c := range commitments {
		if c == nil {
			return nil, fmt.Errorf("shamir: EvalCommitmentPolynomial: commitment %d is nil", j)
		}
		if !c.IsOnCurve() {
			return nil, fmt.Errorf("shamir: EvalCommitmentPolynomial: commitment %d is not on the curve", j)
		}
	}

	xScalar := intToScalar(index)

	// Horner's method in the group: rewrite
	//   C_0 + x*C_1 + x^2*C_2 + ... + x^{d}*C_d
	// as C_0 + x*(C_1 + x*(C_2 + ... + x*C_d)...), evaluated inside-out.
	result := commitments[len(commitments)-1]
	for i := len(commitments) - 2; i >= 0; i-- {
		result = result.Mul(xScalar).Add(commitments[i])
	}
	return result, nil
}
