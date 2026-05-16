package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ensureAccountExists creates a zero-balance auth account for addr when the
// address is not already present on chain.
func (k *Keeper) ensureAccountExists(ctx context.Context, addr sdk.AccAddress) error {
	if k.accountKeeper == nil {
		return fmt.Errorf("%w: account keeper is not configured", types.ErrInvalidField)
	}
	if k.accountKeeper.GetAccount(ctx, addr) == nil {
		k.accountKeeper.SetAccount(ctx, k.accountKeeper.NewAccountWithAddress(ctx, addr))
	}
	return nil
}
