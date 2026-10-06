package protochain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"filippo.io/edwards25519"
	cmted "github.com/cometbft/cometbft/crypto/ed25519"
)

// validateDK enforces canonical, non-small-order Ed25519 delegate keys (ZIP-215 verification accepts small-order keys).
func validateDK(dk []byte) bool {
	if len(dk) != 32 {
		return false
	}
	p, err := new(edwards25519.Point).SetBytes(dk)
	if err != nil {
		return false
	}
	if !bytes.Equal(p.Bytes(), dk) {
		return false
	}
	return new(edwards25519.Point).MultByCofactor(p).Equal(edwards25519.NewIdentityPoint()) != 1
}

func TestDKValidation(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if !validateDK(pub) {
		t.Fatal("valid key rejected")
	}
	id := edwards25519.NewIdentityPoint().Bytes()
	if validateDK(id) {
		t.Fatal("identity accepted")
	}
	// small-order key: order-2 point (0,-1) encoding
	small := make([]byte, 32)
	small[0] = 0xec
	for i := 1; i < 31; i++ {
		small[i] = 0xff
	}
	small[31] = 0x7f
	if validateDK(small) {
		t.Fatal("small-order key accepted")
	}
	msg := make([]byte, 32)
	sig := ed25519.Sign(priv, msg)
	if !cmted.PubKey(pub).VerifySignature(msg, sig) {
		t.Fatal("cometbft ZIP-215 verify failed")
	}
}
