package keeper

import (
	"context"

	"github.com/z-cale/zally/x/vote/types"
)

// CreateValidatorWithPallasKeyHandler is the exported handler for
// MsgCreateValidatorWithPallasKey. This is separate from the MsgServer
// interface because the message is hand-written (not protoc-generated)
// and cannot be registered through the standard gRPC ServiceDesc path.
type CreateValidatorWithPallasKeyHandler struct {
	ms msgServer
}

// NewCreateValidatorWithPallasKeyHandler creates a handler backed by the
// given keeper.
func NewCreateValidatorWithPallasKeyHandler(k Keeper) *CreateValidatorWithPallasKeyHandler {
	return &CreateValidatorWithPallasKeyHandler{ms: msgServer{k: k}}
}

// Handle executes the MsgCreateValidatorWithPallasKey logic.
func (h *CreateValidatorWithPallasKeyHandler) Handle(ctx context.Context, msg *types.MsgCreateValidatorWithPallasKey) (*types.MsgCreateValidatorWithPallasKeyResponse, error) {
	return h.ms.CreateValidatorWithPallasKey(ctx, msg)
}
