package votetree

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateLeaf(t *testing.T) {
	modulusLE := []byte{
		0x01, 0x00, 0x00, 0x00, 0xed, 0x30, 0x2d, 0x99,
		0x1b, 0xf9, 0x4c, 0x09, 0xfc, 0x98, 0x46, 0x22,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
	}
	modulusMinusOneLE := append([]byte(nil), modulusLE...)
	modulusMinusOneLE[0]--

	tests := []struct {
		name    string
		leaf    []byte
		wantErr bool
	}{
		{name: "zero", leaf: make([]byte, LeafBytes)},
		{name: "modulus minus one", leaf: modulusMinusOneLE},
		{name: "short", leaf: make([]byte, LeafBytes-1), wantErr: true},
		{name: "long", leaf: make([]byte, LeafBytes+1), wantErr: true},
		{name: "equal to modulus", leaf: modulusLE, wantErr: true},
		{name: "greater than modulus", leaf: append([]byte(nil), modulusLE...), wantErr: true},
	}
	tests[len(tests)-1].leaf[0]++

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLeaf(tc.leaf)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
