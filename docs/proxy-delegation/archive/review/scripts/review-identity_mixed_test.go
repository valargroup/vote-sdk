package reviewidentity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"testing"

	"filippo.io/edwards25519"
	cmted "github.com/cometbft/cometbft/crypto/ed25519"
)

// validateDK mirrors the chain/identity spec rule: canonical and not small-order.
func validateDK(dk []byte) bool {
	p, err := new(edwards25519.Point).SetBytes(dk)
	if err != nil || !bytes.Equal(p.Bytes(), dk) {
		return false
	}
	return new(edwards25519.Point).MultByCofactor(p).Equal(edwards25519.NewIdentityPoint()) != 1
}

// TestMixedOrderKeyDivergence shows a mixed-order key passes the proposed key check,
// and its holder can make signatures ZIP-215 (CometBFT) accepts but cofactorless Go stdlib rejects.
func TestMixedOrderKeyDivergence(t *testing.T) {
	var seed [64]byte
	rand.Read(seed[:])
	a, _ := new(edwards25519.Scalar).SetUniformBytes(seed[:])
	Ap := new(edwards25519.Point).ScalarBaseMult(a)
	tb := make([]byte, 32)
	tb[0] = 0xec
	for i := 1; i < 31; i++ {
		tb[i] = 0xff
	}
	tb[31] = 0x7f // (0,-1), order 2
	T, err := new(edwards25519.Point).SetBytes(tb)
	if err != nil {
		t.Fatal(err)
	}
	A := new(edwards25519.Point).Add(Ap, T)
	pk := A.Bytes()
	if !validateDK(pk) {
		t.Fatal("mixed-order key rejected by validateDK")
	}
	zipOK, stdOK, n := 0, 0, 2000
	for i := 0; i < n; i++ {
		msg := make([]byte, 32)
		rand.Read(msg)
		var rs [64]byte
		rand.Read(rs[:])
		r, _ := new(edwards25519.Scalar).SetUniformBytes(rs[:])
		R := new(edwards25519.Point).ScalarBaseMult(r)
		h := sha512.New()
		h.Write(R.Bytes())
		h.Write(pk)
		h.Write(msg)
		k, _ := new(edwards25519.Scalar).SetUniformBytes(h.Sum(nil))
		S := new(edwards25519.Scalar).MultiplyAdd(k, a, r)
		sig := append(R.Bytes(), S.Bytes()...)
		if cmted.PubKey(pk).VerifySignature(msg, sig) {
			zipOK++
		}
		if ed25519.Verify(ed25519.PublicKey(pk), msg, sig) {
			stdOK++
		}
	}
	t.Logf("mixed-order key passes validateDK; of %d sigs: ZIP-215 accepted %d, Go stdlib accepted %d", n, zipOK, stdOK)
}
