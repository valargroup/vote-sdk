package app_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/valargroup/vote-sdk/testutil"
	votetypes "github.com/valargroup/vote-sdk/x/vote/types"
)

// TestUpgradeWithoutProxyKeysReadsRegistryDefaults upgrades a chain whose
// state has no proxy delegation keys, as no v1.6 state has, through a no-op
// handler like the one the activation commit registers for v1.7.0
// (docs/proxy-delegation/contracts-v1.md §9). The absent values then read as
// their defaults: next_index 1 and unpaused params with no verifiers and
// max_delegates 0, so registration stays closed until coordinators set
// params, and nothing is written for them.
func TestUpgradeWithoutProxyKeysReadsRegistryDefaults(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	voteManager := ta.ValidatorAccAddr()
	ta.SeedVoteManagers(voteManager)
	requireNoProxyDelegationKeys(t, ta)

	const upgradeName = "proxy-delegation-defaults-test"
	ta.SvoteApp.UpgradeKeeper.SetUpgradeHandler(upgradeName, func(_ context.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		return vm, nil
	})
	dueHeight := ta.Height + 2
	result := ta.DeliverVoteTx(ta.MustBuildSignedCoordinatorActionTx(voteManager, &votetypes.MsgScheduleUpgrade{
		Creator: voteManager,
		Name:    upgradeName,
		Height:  dueHeight,
	}))
	require.Equal(t, uint32(0), result.Code, result.Log)
	ta.Height++
	ta.Time = ta.Time.Add(5 * time.Second)
	_, err := ta.FinalizeBlock(&abci.RequestFinalizeBlock{Height: ta.Height, Time: ta.Time, ProposerAddress: ta.ProposerAddress})
	require.NoError(t, err)
	_, err = ta.Commit()
	require.NoError(t, err)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height, Time: ta.Time})
	doneHeight, err := ta.SvoteApp.UpgradeKeeper.GetDoneHeight(ctx, upgradeName)
	require.NoError(t, err)
	require.Equal(t, dueHeight, doneHeight)

	k := ta.VoteKeeper()
	kv := k.OpenKVStore(ctx)
	next, err := k.GetProxyNextDelegateIndex(kv)
	require.NoError(t, err)
	require.Equal(t, uint32(1), next)
	params, err := k.GetProxyDelegationParams(kv)
	require.NoError(t, err)
	require.True(t, proto.Equal(&votetypes.ProxyDelegationParams{}, params), "params: %v", params)

	register := &votetypes.MsgDelegateOp{Register: &votetypes.DelegateRegister{
		Dk:           bytes.Repeat([]byte{0x11}, 32),
		Provider:     votetypes.DelegateProviderX,
		Handle:       "alice",
		ProofId:      "1790000000000000000",
		Attestations: []*votetypes.DelegateAttestation{{VerifierId: 1, NotBefore: 1, ExpiresAt: 2, Sig: make([]byte, 64)}},
		DkSig:        make([]byte, 64),
	}}
	require.NoError(t, register.ValidateBasic())
	require.ErrorIs(t, k.ValidateDelegateOp(ctx, register, true), votetypes.ErrRegistrationClosed)

	gs, err := k.ExportGenesis(kv)
	require.NoError(t, err)
	require.Empty(t, gs.ProxyDelegates)
	require.Nil(t, gs.ProxyParams)
	requireNoProxyDelegationKeys(t, ta)
}

// requireNoProxyDelegationKeys fails if the committed vote store holds a key
// under the proxy delegation prefixes 0x1A or 0x1B (contracts-v1 §3.2).
func requireNoProxyDelegationKeys(t *testing.T, ta *testutil.TestApp) {
	t.Helper()
	kv := ta.VoteKeeper().OpenKVStore(ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height}))
	iter, err := kv.Iterator([]byte{0x1A}, []byte{0x1C})
	require.NoError(t, err)
	defer iter.Close()
	if iter.Valid() {
		require.Failf(t, "unexpected proxy delegation key", "key %x", iter.Key())
	}
}
