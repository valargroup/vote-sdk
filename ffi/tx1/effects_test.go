package tx1

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func testEffects() []byte {
	effects := make([]byte, EffectsLen)
	effects[0] = EffectsVersion
	return effects
}

func setTestAction(effects []byte, index int, rk, nf, cmx []byte) {
	start := 1 + index*ActionEffectsLen
	copy(effects[start+nullifierOffset:start+nullifierOffset+fieldLen], nf)
	copy(effects[start+rkOffset:start+rkOffset+fieldLen], rk)
	copy(effects[start+cmxOffset:start+cmxOffset+fieldLen], cmx)
}

func TestValidateDelegationBinding(t *testing.T) {
	rk := bytes.Repeat([]byte{1}, fieldLen)
	nf := bytes.Repeat([]byte{2}, fieldLen)
	cmx := bytes.Repeat([]byte{3}, fieldLen)

	effects := testEffects()
	setTestAction(effects, 0, rk, nf, cmx)
	require.NoError(t, ValidateDelegationBinding(effects, rk, nf, cmx))

	effects = testEffects()
	require.ErrorContains(t, ValidateDelegationBinding(effects, rk, nf, cmx), "must match")
}

func TestValidateDelegationBindingRejectsMismatchedFields(t *testing.T) {
	rk := bytes.Repeat([]byte{1}, fieldLen)
	nf := bytes.Repeat([]byte{2}, fieldLen)
	cmx := bytes.Repeat([]byte{3}, fieldLen)

	effects := testEffects()
	setTestAction(effects, 0, rk, nf, cmx)

	require.ErrorContains(t, ValidateDelegationBinding(effects, bytes.Repeat([]byte{4}, fieldLen), nf, cmx), "must match")
	require.ErrorContains(t, ValidateDelegationBinding(effects, rk, bytes.Repeat([]byte{4}, fieldLen), cmx), "must match")
	require.ErrorContains(t, ValidateDelegationBinding(effects, rk, nf, bytes.Repeat([]byte{4}, fieldLen)), "must match")
}

func TestValidateEffectsFraming(t *testing.T) {
	effects := testEffects()
	require.NoError(t, ValidateEffectsFraming(effects))
	require.ErrorContains(t, ValidateEffectsFraming(effects[:len(effects)-1]), "must be 821 bytes")

	effects[0]++
	require.ErrorContains(t, ValidateEffectsFraming(effects), "unsupported tx1 effects version")
}
