package pallas_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/pallas"
)

func TestIsCanonicalBaseFieldElement(t *testing.T) {
	modulus := []byte{
		0x01, 0x00, 0x00, 0x00, 0xed, 0x30, 0x2d, 0x99,
		0x1b, 0xf9, 0x4c, 0x09, 0xfc, 0x98, 0x46, 0x22,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
	}
	modulusMinusOne := append([]byte(nil), modulus...)
	modulusMinusOne[0]--

	tests := []struct {
		name  string
		value []byte
		want  bool
	}{
		{name: "zero", value: make([]byte, 32), want: true},
		{name: "modulus minus one", value: modulusMinusOne, want: true},
		{name: "modulus", value: modulus, want: false},
		{name: "above modulus", value: bytes.Repeat([]byte{0xff}, 32), want: false},
		{name: "short", value: make([]byte, 31), want: false},
		{name: "long", value: make([]byte, 33), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, pallas.IsCanonicalBaseFieldElement(tc.value))
		})
	}
}
