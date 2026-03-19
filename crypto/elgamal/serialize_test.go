package elgamal

import (
	"bytes"
	"crypto/elliptic"
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
)

// requirePallasPoint asserts that p is concretely a *curvey.PointPallas.
// This catches bugs where deserialization silently returns a point on the
// wrong curve (or a generic wrapper) that happens to satisfy curvey.Point.
func requirePallasPoint(t *testing.T, p curvey.Point, label string) {
	t.Helper()
	if _, ok := p.(*curvey.PointPallas); !ok {
		t.Fatalf("%s: expected *curvey.PointPallas, got %T", label, p)
	}
}

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

	// Verify the deserialized points are Pallas points.
	requirePallasPoint(t, ct2.C1, "C1")
	requirePallasPoint(t, ct2.C2, "C2")
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

	requirePallasPoint(t, ct.C1, "C1")
	requirePallasPoint(t, ct.C2, "C2")
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

	requirePallasPoint(t, sum2.C1, "C1")
	requirePallasPoint(t, sum2.C2, "C2")
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

	requirePallasPoint(t, identity.C1, "identity C1")
	requirePallasPoint(t, identity.C2, "identity C2")

	// Adding identity should not change the ciphertext.
	sum := HomomorphicAdd(ct, identity)

	if !sum.C1.Equal(ct.C1) {
		t.Error("C1 should be unchanged after adding identity")
	}
	if !sum.C2.Equal(ct.C2) {
		t.Error("C2 should be unchanged after adding identity")
	}
}

func TestUnmarshalCiphertext_RejectsCompressedP256Point(t *testing.T) {
	// The NIST P-256 generator in compressed form is 33 bytes (0x02/0x03 prefix
	// + 32-byte big-endian X coordinate). Extract the 32-byte X coordinate and
	// place it into a 64-byte buffer as if it were two Pallas compressed points.
	// UnmarshalCiphertext must reject this — it should not silently accept
	// points from a foreign curve.
	//
	// NOTE: rejection here is not structurally guaranteed. The 32-byte P256 X
	// coordinate is reinterpreted as a Pallas compressed point (little-endian X
	// with a sign bit in the MSB). Whether the resulting field element satisfies
	// x³ + 5 ≡ y² (mod p_pallas) is essentially coin-flip odds (~50%). The P256
	// generator happens to fail this check, but a different P256 point could
	// land on the Pallas curve by coincidence. If this test ever breaks due to a
	// change in the test point, pick another P256 point that doesn't collide.
	//
	// Goal of this test is that something not on the curve is rejected.
	curve := elliptic.P256()
	compressed := elliptic.MarshalCompressed(curve, curve.Params().Gx, curve.Params().Gy)
	if len(compressed) != 33 {
		t.Fatalf("expected 33-byte compressed P256 point, got %d", len(compressed))
	}

	xBytes := compressed[1:] // 32-byte big-endian X coordinate

	data := make([]byte, CiphertextSize)
	copy(data[:CompressedPointSize], xBytes)
	copy(data[CompressedPointSize:], xBytes)

	// The deserializer may return an error or panic on foreign curve data.
	// Both are acceptable rejection modes; only silent success is a bug.
	var unmarshalErr error
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				t.Logf("panicked (acceptable): %v", r)
			}
		}()
		_, unmarshalErr = UnmarshalCiphertext(data)
	}()

	if unmarshalErr == nil && !panicked {
		t.Fatal("expected UnmarshalCiphertext to reject a P256 point as Pallas, but it succeeded")
	}
	if unmarshalErr != nil {
		t.Logf("correctly returned error: %v", unmarshalErr)
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

	requirePallasPoint(t, ct2.C1, "C1")
	requirePallasPoint(t, ct2.C2, "C2")
}
