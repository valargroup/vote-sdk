// Package tx1 validates delegation transaction effects and computes their
// canonical Ironwood signature digest.
package tx1

import (
	"bytes"
	"fmt"
)

const (
	// EffectsVersion identifies the fixed V6/NU6.3 delegation transaction profile.
	EffectsVersion byte = 1
	// ActionCount is the number of Ironwood actions in the delegation transaction.
	ActionCount = 2
	// ActionEffectsLen is the serialized length of one Ironwood action's effecting data.
	ActionEffectsLen = 820
	// EffectsLen is the serialized length of the versioned delegation effects payload.
	EffectsLen = 1 + ActionCount*ActionEffectsLen

	fieldLen        = 32
	nullifierOffset = 32
	rkOffset        = 64
	cmxOffset       = 96
)

// ValidateEffectsFraming checks the version and fixed length of a delegation
// transaction effects payload.
func ValidateEffectsFraming(effects []byte) error {
	if len(effects) != EffectsLen {
		return fmt.Errorf("tx1 effects must be %d bytes, got %d", EffectsLen, len(effects))
	}
	if effects[0] != EffectsVersion {
		return fmt.Errorf("unsupported tx1 effects version: expected %d, got %d", EffectsVersion, effects[0])
	}
	return nil
}

// ValidateDelegationBinding requires the delegation message's randomized key
// to identify exactly one action whose nullifier and commitment also match.
func ValidateDelegationBinding(effects, rk, signedNoteNullifier, cmxNew []byte) error {
	if err := ValidateEffectsFraming(effects); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value []byte
	}{
		{name: "rk", value: rk},
		{name: "signed_note_nullifier", value: signedNoteNullifier},
		{name: "cmx_new", value: cmxNew},
	}
	for _, field := range fields {
		if len(field.value) != fieldLen {
			return fmt.Errorf("%s must be %d bytes, got %d", field.name, fieldLen, len(field.value))
		}
	}

	matchingAction := -1
	rkMatches := 0
	for index := range ActionCount {
		start := 1 + index*ActionEffectsLen
		if bytes.Equal(effects[start+rkOffset:start+rkOffset+fieldLen], rk) {
			rkMatches++
			matchingAction = index
		}
	}

	if rkMatches != 1 {
		return fmt.Errorf("tx1 effects must contain exactly one action with delegation rk, got %d", rkMatches)
	}

	start := 1 + matchingAction*ActionEffectsLen
	if !bytes.Equal(effects[start+nullifierOffset:start+nullifierOffset+fieldLen], signedNoteNullifier) ||
		!bytes.Equal(effects[start+cmxOffset:start+cmxOffset+fieldLen], cmxNew) {
		return fmt.Errorf("tx1 action with delegation rk must match signed_note_nullifier and cmx_new")
	}
	return nil
}
