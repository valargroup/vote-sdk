package votetree

import (
	"bytes"
	"testing"
)

func TestSingleLeafRootDeterministic(t *testing.T) {
	leaf := make([]byte, LeafBytes)
	leaf[0] = 42
	first, err := SingleLeafRoot(leaf)
	if err != nil {
		t.Fatalf("SingleLeafRoot: %v", err)
	}
	second, err := SingleLeafRoot(leaf)
	if err != nil {
		t.Fatalf("SingleLeafRoot repeat: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("single-leaf root is not deterministic: %x != %x", first, second)
	}
	if len(first) != LeafBytes {
		t.Fatalf("root length = %d, want %d", len(first), LeafBytes)
	}

	if _, err := SingleLeafRoot(leaf[:LeafBytes-1]); err == nil {
		t.Fatal("expected wrong-length leaf rejection")
	}
}
