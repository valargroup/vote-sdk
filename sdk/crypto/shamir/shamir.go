// Package shamir implements (t, n) Shamir secret sharing over the Pallas
// scalar field (Fq). It provides polynomial-based secret splitting, Lagrange
// interpolation for reconstruction, and the building blocks reused by Feldman
// commitments and threshold ElGamal decryption.
package shamir

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/mikelodder7/curvey"
)

// Share is a single evaluation of the secret-sharing polynomial f at a
// non-zero point: Value = f(Index). Indices are 1-based (1..n); index 0
// is reserved for the secret itself and must never appear as a share index.
type Share struct {
	Index int           // evaluation point (1..n)
	Value curvey.Scalar // f(Index) in Pallas Fq
}

// Split generates a (t, n) Shamir secret sharing of secret over Pallas Fq.
//
// It constructs a random degree-(t-1) polynomial f(x) with f(0) = secret,
// then evaluates f at points 1..n to produce n shares. Any t shares suffice
// to reconstruct the secret; fewer than t reveal nothing.
//
// Returns the shares and the full coefficient vector [a_0=secret, a_1, ..., a_{t-1}].
// The caller needs the coefficients to compute Feldman commitments via FeldmanCommit.
// Coefficients contain secret material and should be zeroized after use.
func Split(secret curvey.Scalar, t, n int) ([]Share, []curvey.Scalar, error) {
	if secret == nil {
		return nil, nil, fmt.Errorf("shamir: Split: secret must not be nil")
	}
	if t < 2 {
		return nil, nil, fmt.Errorf("shamir: Split: threshold t must be >= 2, got %d", t)
	}
	if n < t {
		return nil, nil, fmt.Errorf("shamir: Split: n must be >= t, got n=%d t=%d", n, t)
	}

	// Build polynomial coefficients: f(x) = secret + a_1*x + ... + a_{t-1}*x^{t-1}
	coeffs := make([]curvey.Scalar, t)
	coeffs[0] = secret
	for i := 1; i < t; i++ {
		coeffs[i] = new(curvey.ScalarPallas).Random(rand.Reader)
	}

	shares := make([]Share, n)
	for i := 0; i < n; i++ {
		idx := i + 1 // 1-based
		shares[i] = Share{
			Index: idx,
			Value: evalPolynomial(coeffs, idx),
		}
	}

	return shares, coeffs, nil
}

// LagrangeCoefficients computes the Lagrange basis scalars for the given
// evaluation indices at a target point. Each lambda_j satisfies:
//
//	lambda_j = product_{m != j} (target - x_m) / (x_j - x_m)
//
// With target=0 this gives reconstruction coefficients: secret = sum(lambda_j * share_j).
// The same function is reused for interpolation in the exponent (CombinePartials).
func LagrangeCoefficients(indices []int, target int) ([]curvey.Scalar, error) {
	if len(indices) == 0 {
		return nil, fmt.Errorf("shamir: LagrangeCoefficients: indices must not be empty")
	}

	// Check for duplicates and non-positive indices.
	seen := make(map[int]struct{}, len(indices))
	for _, idx := range indices {
		if idx <= 0 {
			return nil, fmt.Errorf("shamir: LagrangeCoefficients: index must be > 0, got %d", idx)
		}
		if _, dup := seen[idx]; dup {
			return nil, fmt.Errorf("shamir: LagrangeCoefficients: duplicate index %d", idx)
		}
		seen[idx] = struct{}{}
	}

	tScalar := intToScalar(target)
	lambdas := make([]curvey.Scalar, len(indices))

	for j, xj := range indices {
		xjScalar := intToScalar(xj)
		num := new(curvey.ScalarPallas).New(1) // multiplicative identity
		den := new(curvey.ScalarPallas).New(1)

		for m, xm := range indices {
			if m == j {
				continue
			}
			xmScalar := intToScalar(xm)

			num = num.Mul(tScalar.Sub(xmScalar))
			den = den.Mul(xjScalar.Sub(xmScalar))
		}

		denInv, err := den.Invert()
		if err != nil {
			return nil, fmt.Errorf("shamir: LagrangeCoefficients: failed to invert denominator for index %d: %w", xj, err)
		}
		lambdas[j] = num.Mul(denInv)
	}

	return lambdas, nil
}

// Reconstruct recovers the secret from at least t shares using Lagrange
// interpolation at point 0.
//
// This function is intended for tests to verify split correctness. Production
// threshold decryption uses CombinePartials (interpolation in the exponent)
// instead of reconstructing the scalar secret.
func Reconstruct(shares []Share, t int) (curvey.Scalar, error) {
	if len(shares) < t {
		return nil, fmt.Errorf("shamir: Reconstruct: need at least %d shares, got %d", t, len(shares))
	}

	indices := make([]int, len(shares))
	for i, s := range shares {
		indices[i] = s.Index
	}

	lambdas, err := LagrangeCoefficients(indices, 0)
	if err != nil {
		return nil, fmt.Errorf("shamir: Reconstruct: %w", err)
	}

	result := new(curvey.ScalarPallas).Zero()
	for i, s := range shares {
		result = result.Add(lambdas[i].Mul(s.Value))
	}
	return result, nil
}

// evalPolynomial evaluates f(x) = coeffs[0] + coeffs[1]*x + ... + coeffs[d]*x^d
// at the given integer point using Horner's method.
func evalPolynomial(coeffs []curvey.Scalar, x int) curvey.Scalar {
	xScalar := intToScalar(x)

	// Horner's method rewrites a_0 + a_1*x + ... + a_d*x^d as
	// a_0 + x*(a_1 + x*(a_2 + ... + x*a_d)...), evaluating inside-out
	// in d multiplications + d additions with no separate exponentiation.
	// Naive approach would require O(d^2) multiplications.
	result := coeffs[len(coeffs)-1]
	for i := len(coeffs) - 2; i >= 0; i-- {
		result = result.Mul(xScalar).Add(coeffs[i])
	}
	return result
}

// intToScalar converts an integer to a Pallas scalar via big.Int.
func intToScalar(v int) curvey.Scalar {
	bi := new(big.Int).SetInt64(int64(v))
	s, err := new(curvey.ScalarPallas).SetBigInt(bi)
	if err != nil {
		panic(fmt.Sprintf("shamir: intToScalar: failed to convert %d to Pallas scalar: %v", v, err))
	}
	return s
}
