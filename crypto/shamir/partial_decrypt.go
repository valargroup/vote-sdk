package shamir

import (
	"fmt"

	"github.com/mikelodder7/curvey"
)

// PartialDecryption is a single validator's contribution to threshold
// ElGamal decryption: D_i = share_i * C1, tagged with the validator's
// Shamir evaluation index.
type PartialDecryption struct {
	Index int         // validator index (matches Share.Index, 1-based)
	Di    curvey.Point // share_i * C1
}

// PartialDecrypt computes a single partial decryption D_i = share * C1.
//
// In threshold ElGamal, each validator holds a Shamir share of the secret
// key. Rather than reconstructing the secret, each validator multiplies
// the ciphertext's C1 component by their share. The results are later
// combined via Lagrange interpolation in the exponent (CombinePartials)
// to recover sk * C1 without any party learning sk.
func PartialDecrypt(share curvey.Scalar, C1 curvey.Point) (curvey.Point, error) {
	if share == nil {
		return nil, fmt.Errorf("shamir: PartialDecrypt: share must not be nil")
	}
	if C1 == nil {
		return nil, fmt.Errorf("shamir: PartialDecrypt: C1 must not be nil")
	}
	if !C1.IsOnCurve() {
		return nil, fmt.Errorf("shamir: PartialDecrypt: C1 is not on the Pallas curve")
	}
	return C1.Mul(share), nil
}

// CombinePartials performs Lagrange interpolation in the exponent to
// recover sk * C1 from at least t partial decryptions.
//
// Given partials D_i = share_i * C1, this computes:
//
//	sum(lambda_i * D_i) = sum(lambda_i * share_i) * C1 = sk * C1
//
// where lambda_i are the Lagrange coefficients evaluated at 0.
// The result can be subtracted from C2 to obtain the plaintext point:
//
//	C2 - CombinePartials(...) = v * G
func CombinePartials(partials []PartialDecryption, t int) (curvey.Point, error) {
	if len(partials) < t {
		return nil, fmt.Errorf("shamir: CombinePartials: need at least %d partials, got %d", t, len(partials))
	}

	indices := make([]int, len(partials))
	for i, p := range partials {
		if p.Di == nil {
			return nil, fmt.Errorf("shamir: CombinePartials: partial at position %d has nil Di", i)
		}
		indices[i] = p.Index
	}

	lambdas, err := LagrangeCoefficients(indices, 0)
	if err != nil {
		return nil, fmt.Errorf("shamir: CombinePartials: %w", err)
	}

	result := new(curvey.PointPallas).Identity()
	for i, p := range partials {
		result = result.Add(p.Di.Mul(lambdas[i]))
	}
	return result, nil
}
