package types_test

import (
	"errors"
	"fmt"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestSurfaceRegisteredCode checks that SurfaceRegisteredCode reports the
// code of the first registered error in a chain that hides it, and leaves
// every other error alone (docs/proxy-delegation/contracts-v1.md §0).
func TestSurfaceRegisteredCode(t *testing.T) {
	require.NoError(t, types.SurfaceRegisteredCode(nil))

	inner := errors.New("verifier failed")
	for _, c := range []struct {
		name      string
		err       error
		codespace string
		code      uint32
		is        error
	}{
		{"fmt-wrapped", fmt.Errorf("%w: %x", types.ErrDuplicateNullifier, []byte{1}), types.ModuleName, 2, types.ErrDuplicateNullifier},
		{"nested fmt", fmt.Errorf("votes[1]: %w", fmt.Errorf("%w: vote commitment: %w", types.ErrInvalidProof, inner)), types.ModuleName, 5, inner},
		{"errorsmod over fmt", errorsmod.Wrap(fmt.Errorf("%w: rk", types.ErrInvalidField), "basic validation failed"), types.ModuleName, 9, types.ErrInvalidField},
		{"another codespace", fmt.Errorf("decode: %w", sdkerrors.ErrTxDecode), sdkerrors.RootCodespace, 2, sdkerrors.ErrTxDecode},
		{"the reveal's missing root", &types.CommitmentRootUnavailableError{AnchorHeight: 7}, types.ModuleName, 5, types.ErrInvalidProof},
	} {
		t.Run(c.name, func(t *testing.T) {
			codespace, code, _ := errorsmod.ABCIInfo(c.err, false)
			require.Equal(t, errorsmod.UndefinedCodespace, codespace, "the chain hides its code")
			require.Equal(t, uint32(1), code)

			surfaced := types.SurfaceRegisteredCode(c.err)
			codespace, code, log := errorsmod.ABCIInfo(surfaced, false)
			require.Equal(t, c.codespace, codespace)
			require.Equal(t, c.code, code)
			require.Equal(t, c.err.Error(), log, "the message is unchanged")
			require.Equal(t, c.err.Error(), surfaced.Error())
			require.ErrorIs(t, surfaced, c.is, "the chain is unchanged")
			require.ErrorIs(t, surfaced, c.err)
		})
	}

	// An error that reports a registered code, or holds none, is returned
	// as is.
	for _, err := range []error{
		types.ErrRoundNotFound,
		errorsmod.Wrapf(types.ErrProxyBatchExpired, "height %d", 5),
		errorsmod.Wrap(sdkerrors.ErrTxDecode, "decode"),
		errors.New("kv store failure"),
		fmt.Errorf("invalid block time %d", -1),
	} {
		require.Same(t, err, types.SurfaceRegisteredCode(err), "%v", err)
	}
}
