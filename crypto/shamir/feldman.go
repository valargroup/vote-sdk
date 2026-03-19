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
