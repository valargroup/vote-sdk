//go:build proxyenabled

package vote_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestRoutedRoundGenesisRoundTrip exports, after the transition block, a
// TALLYING proxy round whose pools are routed and an ACTIVE one whose pools
// are not, through the module's JSON (contracts-v1 §9): the export
// validates, the import rebuilds the same rounds, tallies, share counts,
// ballots and registry, and the imported chain then routes the ACTIVE round
// at its transition exactly as the original does.
func TestRoutedRoundGenesisRoundTrip(t *testing.T) {
	later := routingRound()
	later.VoteRoundId = bytes.Repeat([]byte{0xE3}, 32)
	later.VoteEndTime = routingEnd + 1000
	c := newRoutingChain(t)
	require.NoError(t, c.block(routingEnd-10, func(ctx sdk.Context) error {
		kv := c.keeper.OpenKVStore(ctx)
		require.NoError(t, c.keeper.SetVoteManagers(kv, &types.VoteManagerSet{Addresses: []string{svtest.DefaultVoteManagerAddress}, Threshold: 1}))
		for d := uint32(1); d <= 6; d++ {
			dk := proxytest.Pub(proxytest.Key(fmt.Sprintf("delegate/%d", d)))
			handle := fmt.Sprintf("delegate_%d", d)
			require.NoError(t, c.keeper.SetProxyDelegateEntry(kv, &types.ProxyDelegateEntry{
				Index: d, Dk: dk, Provider: types.DelegateProviderX, Handle: handle, ProofId: "1790000000000000000",
			}))
			require.NoError(t, c.keeper.SetProxyUsedDelegateKey(kv, dk, d))
			require.NoError(t, c.keeper.SetProxyDelegateByHandle(kv, types.DelegateProviderX, handle, d))
		}
		require.NoError(t, c.keeper.SetProxyNextDelegateIndex(kv, 7))
		for _, round := range []*types.VoteRound{routingRound(), later} {
			require.NoError(t, c.keeper.SetVoteRound(kv, round))
			seedRoutingState(t, c.keeper, ctx, round.VoteRoundId, false)
		}
		return nil
	}))
	require.NoError(t, c.endBlock(routingEnd))

	exported := c.module.ExportGenesis(c.ctx, nil)
	require.NoError(t, c.module.ValidateGenesis(nil, nil, exported))
	var gs types.GenesisState
	require.NoError(t, json.Unmarshal(exported, &gs))
	require.Len(t, gs.Rounds, 2)
	for _, round := range gs.Rounds {
		tallying := bytes.Equal(round.VoteRoundId, routingRoundID)
		require.Equal(t, tallying, round.Status == types.SessionStatus_SESSION_STATUS_TALLYING, "round %x", round.VoteRoundId)
		require.Equal(t, tallying, round.ProxyDelegation.PoolsRouted, "round %x", round.VoteRoundId)
	}

	prefixes := [][]byte{types.VoteRoundPrefix, types.TallyPrefix, types.ShareCountPrefix, {0x1A}, types.DelegateBallotPrefix}
	imported := newRoutingChain(t)
	require.NoError(t, imported.block(routingEnd, func(ctx sdk.Context) error {
		imported.module.InitGenesis(ctx, nil, exported)
		return nil
	}))
	require.Equal(t, c.state(prefixes...), imported.state(prefixes...))
	require.JSONEq(t, string(exported), string(imported.module.ExportGenesis(imported.ctx, nil)))

	for _, chain := range []*routingChain{c, imported} {
		require.NoError(t, chain.endBlock(later.VoteEndTime))
		require.True(t, chain.round(later.VoteRoundId).ProxyDelegation.PoolsRouted)
	}
	require.Equal(t, c.state(prefixes...), imported.state(prefixes...))
}
