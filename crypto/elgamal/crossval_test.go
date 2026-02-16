package elgamal

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

// TestCrossValidationVectors prints compressed-point hex for known Pallas scalars
// using the standard Pallas generator (-1, 2). These vectors should match
// Rust's pasta_curves output.
//
// Run:  go test -run TestCrossValidationVectors -v ./crypto/elgamal/
func TestCrossValidationVectors(t *testing.T) {
	G := PallasGenerator()

	scalars := []int{1, 2, 7, 42, 1000}

	for _, v := range scalars {
		s := new(curvey.ScalarPallas).New(v)
		P := G.Mul(s)
		bs := P.ToAffineCompressed()
		require.Len(t, bs, 32)
		fmt.Printf("VECTOR scalar=%d hex=%s\n", v, hex.EncodeToString(bs))
	}

	// Identity point
	id := new(curvey.PointPallas).Identity()
	idBytes := id.ToAffineCompressed()
	fmt.Printf("VECTOR scalar=identity hex=%s\n", hex.EncodeToString(idBytes))

	// ElGamal round-trip: encrypt then decrypt value=7
	sk, pk := KeyGen(rand.Reader)
	pkBytes := pk.Point.ToAffineCompressed()
	fmt.Printf("VECTOR pk hex=%s\n", hex.EncodeToString(pkBytes))

	ct, err := Encrypt(pk, 7, rand.Reader)
	require.NoError(t, err)
	ctBytes, err := MarshalCiphertext(ct)
	require.NoError(t, err)
	fmt.Printf("VECTOR encrypt_7 ct_hex=%s\n", hex.EncodeToString(ctBytes))

	// Decrypt and verify
	decPoint := DecryptToPoint(sk, ct)
	expected := G.Mul(new(curvey.ScalarPallas).New(7))
	require.True(t, decPoint.Equal(expected), "decrypt(encrypt(7)) should equal 7*G")
	fmt.Println("VECTOR decrypt_verify=OK")
}
