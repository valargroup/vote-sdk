package roundid

import (
	"bytes"
	"testing"
)

func TestDeriveRoundID_Deterministic(t *testing.T) {
	bh := bytes.Repeat([]byte{0xAA}, 32)
	ph := bytes.Repeat([]byte{0xBB}, 32)
	nfRoot := bytes.Repeat([]byte{0x01}, 32) // canonical Fp
	nc := bytes.Repeat([]byte{0x02}, 32)     // canonical Fp

	r1, err := DeriveRoundID(1000, bh, ph, 2_000_000, nfRoot, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r2, err := DeriveRoundID(1000, bh, ph, 2_000_000, nfRoot, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r1 != r2 {
		t.Fatalf("round_id not deterministic: %x != %x", r1, r2)
	}

	// Should not be all zeros.
	var zero [32]byte
	if r1 == zero {
		t.Fatal("round_id should not be all zeros")
	}
}

func TestDeriveRoundID_DifferentInputs(t *testing.T) {
	bh := bytes.Repeat([]byte{0xAA}, 32)
	ph := bytes.Repeat([]byte{0xBB}, 32)
	nfRoot := bytes.Repeat([]byte{0x01}, 32)
	nc := bytes.Repeat([]byte{0x02}, 32)

	r1, err := DeriveRoundID(1000, bh, ph, 2_000_000, nfRoot, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r2, err := DeriveRoundID(1001, bh, ph, 2_000_000, nfRoot, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r1 == r2 {
		t.Fatal("different snapshot_height should produce different round_id")
	}
}

func TestDeriveRoundID_BadLength(t *testing.T) {
	_, err := DeriveRoundID(1000, []byte{0xAA}, []byte{0xBB}, 2_000_000, []byte{0x01}, []byte{0x02})
	if err == nil {
		t.Fatal("expected error for wrong-length inputs")
	}
}

func TestDeriveRoundID_NonCanonical(t *testing.T) {
	bh := bytes.Repeat([]byte{0xAA}, 32)
	ph := bytes.Repeat([]byte{0xBB}, 32)
	nc := bytes.Repeat([]byte{0x02}, 32)
	// Non-canonical: all 0xFF bytes (exceeds Pallas modulus)
	badRoot := bytes.Repeat([]byte{0xFF}, 32)

	_, err := DeriveRoundID(1000, bh, ph, 2_000_000, badRoot, nc)
	if err == nil {
		t.Fatal("expected error for non-canonical nullifier_imt_root")
	}
}
