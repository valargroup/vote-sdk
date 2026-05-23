package shamir

import (
	"fmt"

	"github.com/mikelodder7/curvey"
)

// Reconstruct recovers the secret from at least t shares using Lagrange
// interpolation at point 0. It is test-only. Production threshold decryption
// uses CombinePartials instead of reconstructing the scalar secret.
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
