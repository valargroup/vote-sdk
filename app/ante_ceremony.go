package app

import (
	"fmt"

	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	votekeeper "github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// CeremonyValidatorDecorator rejects ceremony messages from non-validators.
// It inspects each message in the transaction; if any is a ceremony type
// that requires validator authorization, the first signer must be a bonded
// validator. MsgCreateValidatorWithPallasKey is exempt because the sender
// is becoming a validator. MsgSetVoteManager is also exempt because its
// handler already implements its own authorization check (vote manager OR
// validator).
type CeremonyValidatorDecorator struct {
	voteKeeper *votekeeper.Keeper
}

func NewCeremonyValidatorDecorator(k *votekeeper.Keeper) CeremonyValidatorDecorator {
	return CeremonyValidatorDecorator{voteKeeper: k}
}

func (d CeremonyValidatorDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	for _, msg := range tx.GetMsgs() {
		creator, needsValidator := ceremonyValidatorRequired(msg)
		if !needsValidator {
			continue
		}

		// Convert the account address to a valoper address. The creator
		// field stores a valoper bech32, but at this point signature
		// verification has already confirmed the signer controls the
		// underlying account. Convert to valoper for the staking query.
		valAddr, err := sdk.ValAddressFromBech32(creator)
		if err != nil {
			// creator might be an acc address — convert via raw bytes.
			accAddr, accErr := sdk.AccAddressFromBech32(creator)
			if accErr != nil {
				return ctx, fmt.Errorf("ceremony message has invalid creator address %q: %w", creator, err)
			}
			valAddr = sdk.ValAddress(accAddr)
		}

		if !d.voteKeeper.IsValidator(ctx, valAddr.String()) {
			return ctx, fmt.Errorf("ceremony message rejected: sender %s is not a bonded validator", creator)
		}
	}

	return next(ctx, tx, simulate)
}

// ceremonyValidatorRequired returns the creator address and true if the
// message is a ceremony type that requires the sender to be a validator.
func ceremonyValidatorRequired(msg sdk.Msg) (string, bool) {
	switch m := msg.(type) {
	case *types.MsgRegisterPallasKey:
		return m.Creator, true
	case *types.MsgDealExecutiveAuthorityKey:
		return m.Creator, true
	default:
		return "", false
	}
}

// CeremonyFeeExemptDecorator grants an infinite gas meter to all standard
// Cosmos transactions. This chain is fee-free — no DeductFeeDecorator is
// present and MinGasPrices is 0. The infinite gas meter prevents "out of
// gas" failures for any message type (bank sends, slashing unjails, etc.)
// without requiring callers to estimate gas.
type CeremonyFeeExemptDecorator struct{}

func NewCeremonyFeeExemptDecorator() CeremonyFeeExemptDecorator {
	return CeremonyFeeExemptDecorator{}
}

func (d CeremonyFeeExemptDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	return next(ctx, tx, simulate)
}
