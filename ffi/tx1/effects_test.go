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
	setTestAction(effects, 1, rk, nf, cmx)
	require.NoError(t, ValidateDelegationBinding(effects, rk, nf, cmx))

	setTestAction(effects, 0, rk, nf, cmx)
	require.ErrorContains(t, ValidateDelegationBinding(effects, rk, nf, cmx), "got 2")

	effects = testEffects()
	require.ErrorContains(t, ValidateDelegationBinding(effects, rk, nf, cmx), "got 0")
}

func TestValidateDelegationBindingRejectsDuplicateRkWithDistinctFields(t *testing.T) {
	rk := bytes.Repeat([]byte{1}, fieldLen)
	nf0 := bytes.Repeat([]byte{2}, fieldLen)
	cmx0 := bytes.Repeat([]byte{3}, fieldLen)
	nf1 := bytes.Repeat([]byte{4}, fieldLen)
	cmx1 := bytes.Repeat([]byte{5}, fieldLen)

	effects := testEffects()
	setTestAction(effects, 0, rk, nf0, cmx0)
	setTestAction(effects, 1, rk, nf1, cmx1)

	require.ErrorContains(t, ValidateDelegationBinding(effects, rk, nf0, cmx0), "got 2")
	require.ErrorContains(t, ValidateDelegationBinding(effects, rk, nf1, cmx1), "got 2")
}

func TestValidateEffectsFraming(t *testing.T) {
	effects := testEffects()
	require.NoError(t, ValidateEffectsFraming(effects))
	require.ErrorContains(t, ValidateEffectsFraming(effects[:len(effects)-1]), "must be 1641 bytes")

	effects[0]++
	require.ErrorContains(t, ValidateEffectsFraming(effects), "unsupported tx1 effects version")
}
