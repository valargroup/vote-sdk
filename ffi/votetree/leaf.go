package votetree

import "fmt"

// pallasFpModulus is the Pallas base field modulus in big-endian byte order:
// p = 0x40000000000000000000000000000000224698fc094cf91b992d30ed00000001.
var pallasFpModulus = [LeafBytes]byte{
	0x40, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x22, 0x46, 0x98, 0xfc, 0x09, 0x4c, 0xf9, 0x1b,
	0x99, 0x2d, 0x30, 0xed, 0x00, 0x00, 0x00, 0x01,
}

// ValidateLeaf verifies that leaf is a canonical little-endian Pallas Fp
// encoding accepted by the commitment-tree FFI.
func ValidateLeaf(leaf []byte) error {
	if len(leaf) != LeafBytes {
		return fmt.Errorf("must be %d bytes, got %d", LeafBytes, len(leaf))
	}

	// Compare in big-endian order; leaf[31] is the most-significant byte.
	for i := LeafBytes - 1; i >= 0; i-- {
		modulusIndex := LeafBytes - 1 - i
		if leaf[i] < pallasFpModulus[modulusIndex] {
			return nil
		}
		if leaf[i] > pallasFpModulus[modulusIndex] {
			return fmt.Errorf("must be a canonical Pallas field element")
		}
	}

	// Equality with the modulus is non-canonical.
	return fmt.Errorf("must be a canonical Pallas field element")
}
