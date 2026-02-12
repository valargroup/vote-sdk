package elgamal

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
)

func TestMarshalCiphertext_RoundTrip(t *testing.T) {
	// Generate a keypair and encrypt a known value.
	sk, pk := KeyGen(rand.Reader)
	_ = sk

	ct, err := Encrypt(pk, 42, rand.Reader)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	data, err := MarshalCiphertext(ct)
	if err != nil {
		t.Fatalf("MarshalCiphertext: %v", err)
	}

	if len(data) != CiphertextSize {
		t.Fatalf("expected %d bytes, got %d", CiphertextSize, len(data))
	}

	ct2, err := UnmarshalCiphertext(data)
	if err != nil {
		t.Fatalf("UnmarshalCiphertext: %v", err)
	}

	// Verify C1 and C2 are preserved.
	if !ct.C1.Equal(ct2.C1) {
		t.Error("C1 mismatch after round-trip")
	}
	if !ct.C2.Equal(ct2.C2) {
		t.Error("C2 mismatch after round-trip")
	}
}

func TestIdentityCiphertextBytes_RoundTrip(t *testing.T) {
	data := IdentityCiphertextBytes()
	if len(data) != CiphertextSize {
		t.Fatalf("expected %d bytes, got %d", CiphertextSize, len(data))
	}

	ct, err := UnmarshalCiphertext(data)
	if err != nil {
		t.Fatalf("UnmarshalCiphertext: %v", err)
	}

	// Both points should be identity.
	if !ct.C1.IsIdentity() {
		t.Error("C1 should be identity")
	}
	if !ct.C2.IsIdentity() {
		t.Error("C2 should be identity")
	}
}

func TestUnmarshalCiphertext_WrongLength(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"too short", make([]byte, 32)},
		{"too long", make([]byte, 128)},
		{"off by one short", make([]byte, CiphertextSize-1)},
		{"off by one long", make([]byte, CiphertextSize+1)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UnmarshalCiphertext(tc.data)
			if err == nil {
				t.Error("expected error for wrong-length input")
			}
		})
	}
}

func TestMarshalCiphertext_NilInputs(t *testing.T) {
	_, err := MarshalCiphertext(nil)
	if err == nil {
		t.Error("expected error for nil ciphertext")
	}

	_, err = MarshalCiphertext(&Ciphertext{C1: nil, C2: nil})
	if err == nil {
		t.Error("expected error for nil C1/C2")
	}
}

func TestMarshalCiphertext_HomomorphicAddRoundTrip(t *testing.T) {
	_, pk := KeyGen(rand.Reader)

	ct1, err := Encrypt(pk, 100, rand.Reader)
	if err != nil {
		t.Fatalf("Encrypt ct1: %v", err)
	}
	ct2, err := Encrypt(pk, 200, rand.Reader)
	if err != nil {
		t.Fatalf("Encrypt ct2: %v", err)
	}

	sum := HomomorphicAdd(ct1, ct2)

	// Serialize sum.
	data, err := MarshalCiphertext(sum)
	if err != nil {
		t.Fatalf("MarshalCiphertext: %v", err)
	}

	// Deserialize.
	sum2, err := UnmarshalCiphertext(data)
	if err != nil {
		t.Fatalf("UnmarshalCiphertext: %v", err)
	}

	// Verify the deserialized sum matches the original.
	if !sum.C1.Equal(sum2.C1) {
		t.Error("C1 mismatch after HomomorphicAdd round-trip")
	}
	if !sum.C2.Equal(sum2.C2) {
		t.Error("C2 mismatch after HomomorphicAdd round-trip")
	}
}

func TestIdentityCiphertextBytes_Deterministic(t *testing.T) {
	a := IdentityCiphertextBytes()
	b := IdentityCiphertextBytes()
	if !bytes.Equal(a, b) {
		t.Error("IdentityCiphertextBytes should be deterministic")
	}
}

func TestHomomorphicAdd_WithIdentity(t *testing.T) {
	_, pk := KeyGen(rand.Reader)

	ct, err := Encrypt(pk, 42, rand.Reader)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Deserialize identity.
	identity, err := UnmarshalCiphertext(IdentityCiphertextBytes())
	if err != nil {
		t.Fatalf("UnmarshalCiphertext identity: %v", err)
	}

	// Adding identity should not change the ciphertext.
	sum := HomomorphicAdd(ct, identity)

	if !sum.C1.Equal(ct.C1) {
		t.Error("C1 should be unchanged after adding identity")
	}
	if !sum.C2.Equal(ct.C2) {
		t.Error("C2 should be unchanged after adding identity")
	}
}

func TestMarshalCiphertext_IdentityPoint(t *testing.T) {
	// Test that the identity point marshals and unmarshals correctly.
	id := new(curvey.PointPallas).Identity()
	ct := &Ciphertext{C1: id, C2: id}

	data, err := MarshalCiphertext(ct)
	if err != nil {
		t.Fatalf("MarshalCiphertext identity: %v", err)
	}

	ct2, err := UnmarshalCiphertext(data)
	if err != nil {
		t.Fatalf("UnmarshalCiphertext identity: %v", err)
	}

	if !ct2.C1.IsIdentity() {
		t.Error("deserialized C1 should be identity")
	}
	if !ct2.C2.IsIdentity() {
		t.Error("deserialized C2 should be identity")
	}
}
