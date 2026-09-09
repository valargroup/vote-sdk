package vote_test

import (
	"bytes"
	"testing"
	"time"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	svtest "github.com/valargroup/vote-sdk/testutil"
	vote "github.com/valargroup/vote-sdk/x/vote"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// A new keeper has no cached roots, so recreating it before every EndBlock
// exercises checkpoint reads instead of the unchanged-root fast path.
func TestUnchangedTreeRootsPreserveCommittedState(t *testing.T) {
	type node struct {
		ctx testutil.TestContext
		key *storetypes.KVStoreKey
		k   *keeper.Keeper
	}
	newKeeper := func(n *node) {
		n.k = keeper.NewKeeper(runtime.NewKVStoreService(n.key), svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	}
	nodes := make([]node, 2)
	roundIDs := [][]byte{bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)}
	for i := range nodes {
		n := &nodes[i]
		n.key = storetypes.NewKVStoreKey(types.StoreKey)
		n.ctx = testutil.DefaultContextWithDB(t, n.key, storetypes.NewTransientStoreKey("transient"))
		newKeeper(n)
		for _, roundID := range roundIDs {
			round := svtest.ActiveRoundFixture(roundID)
			round.VoteEndTime = 2_000_000
			require.NoError(t, n.k.SetVoteRound(n.k.OpenKVStore(n.ctx.Ctx), round))
		}
	}

	// Cross a shard boundary, leave several blocks unchanged, then append again.
	// Round two grows independently; the first node also restarts before both
	// an unchanged block and a block with new leaves.
	counts := [][2]int{{15, 1}, {15, 1}, {15, 1}, {16, 1}, {16, 2}, {16, 2}, {17, 2}, {17, 2}}
	for block, count := range counts {
		var hashes [2][]byte
		var events [2]sdk.Events
		for i := range nodes {
			n := &nodes[i]
			if i == 1 || block == 2 || block == 6 {
				newKeeper(n)
			}
			ctx := n.ctx.Ctx.WithBlockHeight(int64(block + 1)).
				WithBlockTime(time.Unix(1_000_000+int64(block), 0)).
				WithEventManager(sdk.NewEventManager())
			kv := n.k.OpenKVStore(ctx)
			for r, roundID := range roundIDs {
				state, err := n.k.GetCommitmentTreeState(kv, roundID)
				require.NoError(t, err)
				for leaf := state.NextIndex; leaf < uint64(count[r]); leaf++ {
					_, err := n.k.AppendCommitment(kv, roundID, fpLE(leaf+1+uint64(r)*100))
					require.NoError(t, err)
				}
			}
			require.NoError(t, vote.NewAppModule(n.k, nil).EndBlock(ctx))
			events[i] = ctx.EventManager().Events()
			hashes[i] = n.ctx.CMS.Commit().Hash
		}
		require.Equal(t, hashes[1], hashes[0], "committed state at block %d", block+1)
		require.Equal(t, events[1], events[0], "events at block %d", block+1)
		if block > 0 && count == counts[block-1] {
			require.Empty(t, events[0], "unchanged trees must not emit a root update")
		}
	}
}
