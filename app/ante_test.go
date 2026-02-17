package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	sdkmath "cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/z-cale/zally/testutil"
)

// ---------------------------------------------------------------------------
// MsgCreateValidator ante handler blocking tests
// ---------------------------------------------------------------------------

// TestMsgCreateValidatorBlockedPostGenesis verifies that MsgCreateValidator
// is rejected by CheckTx at block heights > 0 (post-genesis).
func TestMsgCreateValidatorBlockedPostGenesis(t *testing.T) {
	app := testutil.SetupTestApp(t)

	// Build a standard Cosmos tx containing MsgCreateValidator.
	// The tx doesn't need to be validly signed — the ante handler's
	// MsgCreateValidator check fires before signature verification.
	msgCreateVal := &stakingtypes.MsgCreateValidator{
		Description:       stakingtypes.Description{Moniker: "test-validator"},
		Commission:        stakingtypes.CommissionRates{Rate: sdkmath.LegacyNewDecWithPrec(1, 1), MaxRate: sdkmath.LegacyNewDecWithPrec(2, 1), MaxChangeRate: sdkmath.LegacyNewDecWithPrec(1, 2)},
		MinSelfDelegation: sdkmath.NewInt(1),
		ValidatorAddress:  "zvotevaloper1deadbeef",
		Pubkey:            nil,
		Value:             sdk.NewCoin(sdk.DefaultBondDenom, sdkmath.NewInt(10_000_000)),
	}

	txConfig := app.TxConfig()
	txBuilder := txConfig.NewTxBuilder()
	err := txBuilder.SetMsgs(msgCreateVal)
	require.NoError(t, err)
	txBuilder.SetGasLimit(200_000)

	txBuilder.SetFeeAmount(sdk.NewCoins())

	// Encode the unsigned tx. The ante handler's MsgCreateValidator check
	// fires before signature verification, so a valid signature is not needed.
	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(t, err)

	// The app is at height > 0, so MsgCreateValidator should be blocked.
	require.Greater(t, app.Height, int64(0), "test app should be past genesis")

	resp := app.CheckTxSync(txBytes)
	require.NotEqual(t, uint32(0), resp.Code,
		"MsgCreateValidator should be rejected post-genesis")
	require.Contains(t, resp.Log, "MsgCreateValidator is disabled")

	// Also verify it's blocked via FinalizeBlock (DeliverTx path).
	result := app.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code,
		"MsgCreateValidator should be rejected in DeliverTx post-genesis")
	require.Contains(t, result.Log, "MsgCreateValidator is disabled")
}

// TestGenesisValidatorCreationSucceeds verifies that the genesis flow
// (which uses standard MsgCreateValidator via gentx) succeeds. This is
// implicitly tested by SetupTestApp — if the genesis MsgCreateValidator
// were blocked, InitChain would panic.
func TestGenesisValidatorCreationSucceeds(t *testing.T) {
	// SetupTestApp calls InitChain with GenesisStateWithValSet, which
	// includes a gentx containing MsgCreateValidator. If our ante handler
	// blocked it at genesis height, this would panic.
	app := testutil.SetupTestApp(t)

	// Verify the genesis validator was actually created.
	valAddr := app.ValidatorOperAddr()
	require.NotEmpty(t, valAddr, "genesis validator should exist")
}
