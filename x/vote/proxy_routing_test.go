package vote_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	vote "github.com/valargroup/vote-sdk/x/vote"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover the routing hook in EndBlock
// (docs/proxy-delegation/contracts-v1.md §5.6): when it runs with the gate
// open (-tags proxyenabled), its dormancy, determinism and halt.
// keeper's routing tests cover RouteProxyPools itself.

// routingEnd is the routed round's vote_end_time.
const routingEnd uint64 = 1_000_000

// routingRoundID is the routed round.
var routingRoundID = bytes.Repeat([]byte{0xE2}, 32)

// routingEaSK is the routed round's election secret key.
func routingEaSK() *elgamal.SecretKey {
	sk, _ := proxytest.ElectionKey("EndBlock routing/ea_sk")
	return sk
}

// routingEaPK is the routed round's election public key.
func routingEaPK() *elgamal.PublicKey {
	_, pk := proxytest.ElectionKey("EndBlock routing/ea_sk")
	return pk
}

// routingRound returns the routed round: an ACTIVE proxy round ending at
// routingEnd under routingEaPK, with proposals of 2, 3 and 4 options.
func routingRound() *types.VoteRound {
	round := proxytest.Round(routingRoundID, 2, 3, 4)
	round.CreatedAtTime = routingEnd - 86_400
	round.VoteEndTime = routingEnd
	round.EaPk = routingEaPK().Point.ToAffineCompressed()
	return round
}

// seedRoutingState stores round roundID's revealed shares and ballots
// through ctx, in reverse order if reverse, and returns the plaintext of
// every bucket after routing. Delegates 1 to 4 have pools and ballots,
// delegate 5 a pool and no ballot, and delegate 6 a ballot and no pool.
func seedRoutingState(t testing.TB, k *keeper.Keeper, ctx sdk.Context, roundID []byte, reverse bool) map[[2]uint32]uint64 {
	t.Helper()
	kv := k.OpenKVStore(ctx)
	shares := []struct {
		p, o uint32
		v    uint64
	}{
		{1, 0, 40}, {1, 0, 2}, {2, 2, 9}, {3, 3, 5},
		{0, 1, 100}, {0, 1, 1}, {0, 2, 200}, {0, 3, 3000}, {0, 4, 40000}, {0, 5, 7},
	}
	ballots := map[uint32][]uint32{1: {0, 1, 2}, 2: {1, 1, 3}, 3: {0, 2, 2}, 4: {1, 0, 0}, 6: {1, 2, 3}}
	var writes []func()
	for i, s := range shares {
		ct, err := elgamal.EncryptWithRandomness(routingEaPK(), s.v, proxytest.Scalar(fmt.Sprintf("%x/share/%d", roundID, i)))
		require.NoError(t, err)
		bz, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		writes = append(writes, func() {
			require.NoError(t, k.AddToTally(kv, roundID, s.p, s.o, bz))
			require.NoError(t, k.IncrementShareCount(kv, roundID, s.p, s.o))
		})
	}
	for _, d := range slices.Sorted(maps.Keys(ballots)) {
		writes = append(writes, func() {
			require.NoError(t, k.SetDelegateBallot(kv, &types.DelegateBallotRecord{VoteRoundId: roundID, DelegateIndex: d, Options: ballots[d], Height: 1}))
		})
	}
	if reverse {
		slices.Reverse(writes)
	}
	for _, write := range writes {
		write()
	}

	pools := map[uint32]uint64{}
	want := map[[2]uint32]uint64{}
	for _, s := range shares {
		if s.p == types.PoolProposalID {
			pools[s.o] += s.v
		} else {
			want[[2]uint32{s.p, s.o}] += s.v
		}
	}
	for d, options := range ballots {
		if v, ok := pools[d]; ok {
			for i, o := range options {
				want[[2]uint32{uint32(i + 1), o}] += v
			}
		}
	}
	return want
}

// routingChain is one node's vote store, a committing IAVL store, with its
// keeper and module.
type routingChain struct {
	t      testing.TB
	cms    storetypes.CommitMultiStore
	ctx    sdk.Context
	keeper *keeper.Keeper
	module vote.AppModule
}

// newRoutingChain returns an empty chain.
func newRoutingChain(t testing.TB) *routingChain {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient"))
	k := keeper.NewKeeper(runtime.NewKVStoreService(key), svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	return &routingChain{t: t, cms: testCtx.CMS, ctx: testCtx.Ctx.WithBlockHeight(0), keeper: k, module: vote.NewAppModule(k, nil)}
}

// block runs fn as the next block's state transition at unix time now, as
// FinalizeBlock does: in a cache over the committed store, whose writes are
// flushed in key order and committed, with its events kept, only if fn
// succeeds. It returns fn's error.
func (c *routingChain) block(now uint64, fn func(ctx sdk.Context) error) error {
	c.ctx = c.ctx.WithBlockHeight(c.ctx.BlockHeight() + 1).
		WithBlockTime(time.Unix(int64(now), 0).UTC()).
		WithEventManager(sdk.NewEventManager())
	cacheCtx, write := c.ctx.CacheContext()
	if err := fn(cacheCtx); err != nil {
		return err
	}
	write()
	c.cms.Commit()
	return nil
}

// endBlock runs the next block, at unix time now, as EndBlock alone.
func (c *routingChain) endBlock(now uint64) error {
	return c.block(now, func(ctx sdk.Context) error {
		return c.module.EndBlock(ctx)
	})
}

// kv opens the committed vote store.
func (c *routingChain) kv() store.KVStore {
	return c.keeper.OpenKVStore(c.ctx)
}

// round returns stored round roundID.
func (c *routingChain) round(roundID []byte) *types.VoteRound {
	c.t.Helper()
	round, err := c.keeper.GetVoteRound(c.kv(), roundID)
	require.NoError(c.t, err)
	return round
}

// state returns every key and value under the given prefixes.
func (c *routingChain) state(prefixes ...[]byte) map[string][]byte {
	return proxytest.Values(c.t, c.kv(), prefixes...)
}

// tallies returns every tally and share count of round roundID, pools
// included.
func (c *routingChain) tallies(roundID []byte) map[string][]byte {
	return c.state(append(slices.Clone(types.TallyPrefix), roundID...), append(slices.Clone(types.ShareCountPrefix), roundID...))
}

// requireBuckets checks that every option of every proposal of the routed
// round decrypts to want, or is absent where want has nothing.
func (c *routingChain) requireBuckets(want map[[2]uint32]uint64) {
	c.t.Helper()
	for _, proposal := range routingRound().Proposals {
		for _, option := range proposal.Options {
			b := [2]uint32{proposal.Id, option.Index}
			bz, err := c.keeper.GetTally(c.kv(), routingRoundID, b[0], b[1])
			require.NoError(c.t, err)
			v, ok := want[b]
			if !ok {
				require.Nil(c.t, bz, "bucket %v", b)
				continue
			}
			ct, err := elgamal.UnmarshalCiphertext(bz)
			require.NoError(c.t, err)
			require.True(c.t, elgamal.DecryptToPoint(routingEaSK(), ct).Equal(elgamal.ValuePoint(v)), "bucket %v decrypts to %d", b, v)
		}
	}
}

// seedRoutedRound stores the routed round and its shares and ballots in
// one block before the round ends, and returns the plaintext model.
func (c *routingChain) seedRoutedRound(reverse bool) map[[2]uint32]uint64 {
	c.t.Helper()
	var want map[[2]uint32]uint64
	require.NoError(c.t, c.block(routingEnd-10, func(ctx sdk.Context) error {
		if err := c.keeper.SetVoteRound(c.keeper.OpenKVStore(ctx), routingRound()); err != nil {
			return err
		}
		want = seedRoutingState(c.t, c.keeper, ctx, routingRoundID, reverse)
		return nil
	}))
	return want
}

// eventTypes returns the types of events, in order.
func eventTypes(events sdk.Events) []string {
	var out []string
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

// TestEndBlockRoutesProxyPools checks the hook with the gate open: the
// block that moves a proxy round from ACTIVE to TALLYING routes its pools,
// stores pools_routed with the new status, and emits proxy_pools_routed
// before round_status_change. Earlier blocks route nothing, and later ones
// never route again.
func TestEndBlockRoutesProxyPools(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	c := newRoutingChain(t)
	want := c.seedRoutedRound(false)
	require.Equal(t, map[[2]uint32]uint64{
		{1, 0}: 42 + 101 + 3000, {1, 1}: 200 + 40000,
		{2, 0}: 40000, {2, 1}: 101 + 200, {2, 2}: 9 + 3000,
		{3, 0}: 40000, {3, 2}: 101 + 3000, {3, 3}: 5 + 200,
	}, want, "the model by hand")
	unrouted := c.tallies(routingRoundID)

	require.NoError(t, c.endBlock(routingEnd-1))
	round := c.round(routingRoundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
	require.False(t, round.ProxyDelegation.PoolsRouted)
	require.Equal(t, unrouted, c.tallies(routingRoundID))
	require.Empty(t, c.ctx.EventManager().Events())

	require.NoError(t, c.endBlock(routingEnd))
	round = c.round(routingRoundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)
	require.True(t, round.ProxyDelegation.PoolsRouted)
	c.requireBuckets(want)
	events := c.ctx.EventManager().Events()
	require.Equal(t, []string{types.EventTypeProxyPoolsRouted, types.EventTypeRoundStatusChange}, eventTypes(events))
	require.Equal(t, []sdk.Attribute{
		{Key: types.AttributeKeyRoundID, Value: hex.EncodeToString(routingRoundID)},
		{Key: types.AttributeKeyBallots, Value: "5"},
		{Key: types.AttributeKeyRoutedPools, Value: "4"},
	}, attributes(events[0]))

	routed := c.tallies(routingRoundID)
	require.NoError(t, c.endBlock(routingEnd+1))
	require.Equal(t, routed, c.tallies(routingRoundID))
	require.Empty(t, c.ctx.EventManager().Events())
}

// attributes returns event's attributes as sdk.Attribute values.
func attributes(event sdk.Event) []sdk.Attribute {
	var out []sdk.Attribute
	for _, attribute := range event.Attributes {
		out = append(out, sdk.Attribute{Key: attribute.Key, Value: attribute.Value})
	}
	return out
}

// TestEndBlockDormantDoesNotRoute checks EndBlock as it ships, with the gate
// closed: a proxy round moves to TALLYING as any other round does, with no
// tally changed, pools_routed unset and no proxy_pools_routed event.
func TestEndBlockDormantDoesNotRoute(t *testing.T) {
	proxytest.SkipUnlessDormant(t)
	c := newRoutingChain(t)
	c.seedRoutedRound(false)
	unrouted := c.tallies(routingRoundID)

	require.NoError(t, c.endBlock(routingEnd))
	round := c.round(routingRoundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)
	require.False(t, round.ProxyDelegation.PoolsRouted)
	require.Equal(t, unrouted, c.tallies(routingRoundID))
	require.Equal(t, []string{types.EventTypeRoundStatusChange}, eventTypes(c.ctx.EventManager().Events()))
}

// TestEndBlockRoutingIsDeterministic gives two independent chains the same
// state, written in opposite orders, and runs the transition block on both:
// their app hashes, ordered events and buckets are identical, and the
// buckets differ from the unrouted ones.
func TestEndBlockRoutingIsDeterministic(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	type outcome struct {
		appHash []byte
		events  []byte
		tallies map[string][]byte
	}
	var unrouted map[string][]byte
	run := func(reverse bool) outcome {
		c := newRoutingChain(t)
		c.seedRoutedRound(reverse)
		unrouted = c.tallies(routingRoundID)
		require.NoError(t, c.endBlock(routingEnd))
		events, err := json.Marshal(c.ctx.EventManager().Events())
		require.NoError(t, err)
		return outcome{c.cms.LastCommitID().Hash, events, c.tallies(routingRoundID)}
	}
	forward, reversed := run(false), run(true)
	require.Equal(t, forward, reversed)
	require.NotEqual(t, unrouted, forward.tallies)
}

// TestEndBlockHaltsOnCorruptRouting checks that a routing failure fails
// EndBlock, which halts the chain, instead of being swallowed: the error
// names the round and wraps ErrRoutingCorruptState, and the block is never
// committed, so the round stays ACTIVE with its buckets unrouted.
func TestEndBlockHaltsOnCorruptRouting(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	c := newRoutingChain(t)
	c.seedRoutedRound(false)
	require.NoError(t, c.block(routingEnd-5, func(ctx sdk.Context) error {
		return c.keeper.SetDelegateBallot(c.keeper.OpenKVStore(ctx), &types.DelegateBallotRecord{
			VoteRoundId: routingRoundID, DelegateIndex: 1, Options: []uint32{0, 1},
		})
	}))
	unrouted := c.tallies(routingRoundID)

	err := c.endBlock(routingEnd)
	require.ErrorIs(t, err, keeper.ErrRoutingCorruptState)
	require.ErrorContains(t, err, hex.EncodeToString(routingRoundID))
	round := c.round(routingRoundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
	require.False(t, round.ProxyDelegation.PoolsRouted)
	require.Equal(t, unrouted, c.tallies(routingRoundID))
}

// seedRoutingBenchmark stores, in one block before routingEnd, the routed
// round with types.MaxProposals proposals of types.MaxVoteOptions options, a
// direct vote in every bucket, and delegates 1 to delegates each with a pool
// and a ballot (plan §4.2 Cost).
func (c *routingChain) seedRoutingBenchmark(delegates int) {
	c.t.Helper()
	eaPK := routingEaPK()
	round := routingRound()
	round.Proposals = proxytest.Proposals(slices.Repeat([]int{types.MaxVoteOptions}, types.MaxProposals)...)
	require.NoError(c.t, c.block(routingEnd-10, func(ctx sdk.Context) error {
		kv := c.keeper.OpenKVStore(ctx)
		require.NoError(c.t, c.keeper.SetVoteRound(kv, round))
		one, err := elgamal.EncryptWithRandomness(eaPK, 1, proxytest.Scalar("bench/one"))
		require.NoError(c.t, err)
		ct, err := elgamal.EncryptWithRandomness(eaPK, 1, proxytest.Scalar("bench/first"))
		require.NoError(c.t, err)
		// next adds one to ct and returns its encoding, so each tally is distinct.
		next := func() []byte {
			ct = elgamal.HomomorphicAdd(ct, one)
			bz, err := elgamal.MarshalCiphertext(ct)
			require.NoError(c.t, err)
			return bz
		}
		for p := uint32(1); p <= types.MaxProposals; p++ {
			for o := uint32(0); o < types.MaxVoteOptions; o++ {
				require.NoError(c.t, c.keeper.AddToTally(kv, routingRoundID, p, o, next()))
			}
		}
		for d := uint32(1); d <= uint32(delegates); d++ {
			require.NoError(c.t, c.keeper.AddToTally(kv, routingRoundID, types.PoolProposalID, d, next()))
			options := make([]uint32, types.MaxProposals)
			for i := range options {
				options[i] = (d + uint32(i)) % types.MaxVoteOptions
			}
			require.NoError(c.t, c.keeper.SetDelegateBallot(kv, &types.DelegateBallotRecord{VoteRoundId: routingRoundID, DelegateIndex: d, Options: options}))
		}
		return nil
	}))
}

// transitionCtx returns the context of the block after the last committed
// one, at routingEnd.
func (c *routingChain) transitionCtx() sdk.Context {
	return c.ctx.WithBlockHeight(c.ctx.BlockHeight() + 1).WithBlockTime(time.Unix(int64(routingEnd), 0).UTC())
}

// BenchmarkEndBlockProxyRouting times the transition block of a proxy round
// seeded by seedRoutingBenchmark. Each iteration runs EndBlock with the gate
// open in a cache over the committed IAVL store, as FinalizeBlock does, and
// discards it. The IAVL store is over an in-memory database.
// docs/proxy-delegation/routing-performance.md records its results.
func BenchmarkEndBlockProxyRouting(b *testing.B) {
	requireRoutingGate(b)
	for _, delegates := range []int{1000, 2000} {
		b.Run(fmt.Sprintf("delegates=%d", delegates), func(b *testing.B) {
			c := newRoutingChain(b)
			c.seedRoutingBenchmark(delegates)
			ctx := c.transitionCtx()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cacheCtx, _ := ctx.CacheContext()
				if err := c.module.EndBlock(cacheCtx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// requireRoutingGate fails a routing benchmark built without the
// proxyenabled tag, where EndBlock never routes and the timings mean nothing.
func requireRoutingGate(b *testing.B) {
	if !types.ProxyDelegationEnabled {
		b.Fatal("routing benchmarks need -tags proxyenabled")
	}
}
