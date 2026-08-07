// Package pallas provides encoding checks for Pallas field elements.
package pallas

const encodedFieldElementSize = 32

// baseFieldModulus is the Pallas base field modulus in big-endian byte order:
// p = 0x40000000000000000000000000000000224698fc094cf91b992d30ed00000001.
var baseFieldModulus = [encodedFieldElementSize]byte{
	0x40, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x22, 0x46, 0x98, 0xfc, 0x09, 0x4c, 0xf9, 0x1b,
	0x99, 0x2d, 0x30, 0xed, 0x00, 0x00, 0x00, 0x01,
}

// IsCanonicalBaseFieldElement reports whether value is the canonical 32-byte
// little-endian encoding of a Pallas base field element.
func IsCanonicalBaseFieldElement(value []byte) bool {
	if len(value) != encodedFieldElementSize {
		return false
	}

	for i := encodedFieldElementSize - 1; i >= 0; i-- {
		modulusIndex := encodedFieldElementSize - 1 - i
		if value[i] < baseFieldModulus[modulusIndex] {
			return true
		}
		if value[i] > baseFieldModulus[modulusIndex] {
			return false
		}
	}

	return false
}
