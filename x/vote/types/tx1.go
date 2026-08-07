package types

import (
	"fmt"

	"github.com/valargroup/vote-sdk/ffi/tx1"
)

// validateDelegationTX1Effects checks the fixed effects framing and binds the
// signed Ironwood action to the delegation message fields.
func validateDelegationTX1Effects(msg *MsgDelegateVote) error {
	if err := tx1.ValidateDelegationBinding(
		msg.Tx1Effects,
		msg.Rk,
		msg.SignedNoteNullifier,
		msg.CmxNew,
	); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidField, err)
	}
	return nil
}
