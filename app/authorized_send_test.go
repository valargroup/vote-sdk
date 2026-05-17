package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/valargroup/vote-sdk/testutil"
	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
)

func TestAuthorizedSendTransfersFromVoteFundingModule(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	voteManager := ta.ValidatorAccAddr()
	ta.SeedVoteManagers(voteManager)

	recipient := testutil.TestAccAddr(0x7a)
	recipientAddr, err := sdk.AccAddressFromBech32(recipient)
	require.NoError(t, err)
	moduleAddr := authtypes.NewModuleAddress(votetypes.VoteFundingModuleName)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	moduleBefore := ta.SvoteApp.BankKeeper.GetBalance(ctx, moduleAddr, sdk.DefaultBondDenom)
	recipientBefore := ta.SvoteApp.BankKeeper.GetBalance(ctx, recipientAddr, sdk.DefaultBondDenom)

	txBytes := ta.MustBuildSignedCoordinatorActionTx(voteManager, &votetypes.MsgAuthorizedSend{
		Creator:   voteManager,
		ToAddress: recipient,
		Amount:    "123",
	})
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(0), result.Code, result.Log)

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	moduleAfter := ta.SvoteApp.BankKeeper.GetBalance(ctx, moduleAddr, sdk.DefaultBondDenom)
	recipientAfter := ta.SvoteApp.BankKeeper.GetBalance(ctx, recipientAddr, sdk.DefaultBondDenom)

	require.Equal(t, moduleBefore.Amount.Sub(sdkmath.NewInt(123)), moduleAfter.Amount)
	require.Equal(t, recipientBefore.Amount.Add(sdkmath.NewInt(123)), recipientAfter.Amount)
}
