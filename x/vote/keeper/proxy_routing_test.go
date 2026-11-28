package keeper_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/mikelodder7/curvey"
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
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover routing at the tally transition
// (docs/proxy-delegation/contracts-v1.md §5.6) through RouteProxyPools,
// which EndBlock runs once the gate is open. Every bucket is checked against
// a plaintext model and against the routing identity on ciphertexts.

// routingRoundID is the round of the routing tests.
var routingRoundID = bytes.Repeat([]byte{0xE1}, 32)

// routingBucket names option o of proposal p.
type routingBucket struct{ p, o uint32 }

// routingFixture is an ACTIVE proxy round in a fresh store, with the
// plaintext of every direct vote and pool share it reveals and every ballot
// it stores.
type routingFixture struct {
	t       *testing.T
	ctx     sdk.Context
	keeper  *keeper.Keeper
	eaSK    *elgamal.SecretKey
	eaPK    *elgamal.PublicKey
	round   *types.VoteRound
	direct  map[routingBucket]uint64
	pools   map[uint32]uint64
	ballots map[uint32][]uint32
	shares  int
}

// newDefaultRoutingFixture stores an ACTIVE proxy round routingRoundID
// with proposals of 2, 3 and 4 options.
func newDefaultRoutingFixture(t *testing.T) *routingFixture {
	t.Helper()
	eaSK, _ := proxytest.ElectionKey("routing/ea_sk")
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	eaPK := &elgamal.PublicKey{Point: elgamal.PallasGenerator().Mul(eaSK.Scalar)}
	round := proxytest.Round(routingRoundID, 2, 3, 4)
	round.VoteEndTime = proxyTestTime + 10*proxyTestDay
	round.EaPk = eaPK.Point.ToAffineCompressed()
	f := &routingFixture{
		t:       t,
		ctx:     testCtx.Ctx,
		keeper:  keeper.NewKeeper(runtime.NewKVStoreService(key), svtest.TestAuthority, log.NewNopLogger(), nil, nil),
		eaSK:    eaSK,
		eaPK:    eaPK,
		round:   round,
		direct:  map[routingBucket]uint64{},
		pools:   map[uint32]uint64{},
		ballots: map[uint32][]uint32{},
	}
	require.NoError(t, f.keeper.SetVoteRound(f.kv(), f.round))
	return f
}

// kv opens the fixture's vote store.
func (f *routingFixture) kv() store.KVStore {
	return f.keeper.OpenKVStore(f.ctx)
}

// encrypt encrypts v under the election key with randomness r.
func (f *routingFixture) encrypt(v uint64, r curvey.Scalar) *elgamal.Ciphertext {
	f.t.Helper()
	ct, err := elgamal.EncryptWithRandomness(f.eaPK, v, r)
	require.NoError(f.t, err)
	return ct
}

// nextRandomness returns fresh randomness from a counter, so every run
// builds the same ciphertexts.
func (f *routingFixture) nextRandomness() curvey.Scalar {
	f.shares++
	return proxytest.Scalar(fmt.Sprintf("share/%d", f.shares))
}

// addTally adds ct to the tally at (proposalID, decision) as a reveal does.
func (f *routingFixture) addTally(proposalID, decision uint32, ct *elgamal.Ciphertext) {
	f.t.Helper()
	bz, err := elgamal.MarshalCiphertext(ct)
	require.NoError(f.t, err)
	require.NoError(f.t, f.keeper.AddToTally(f.kv(), f.round.VoteRoundId, proposalID, decision, bz))
}

// vote reveals a direct share of v for option o of proposal p.
func (f *routingFixture) vote(p, o uint32, v uint64) {
	f.addTally(p, o, f.encrypt(v, f.nextRandomness()))
	f.direct[routingBucket{p, o}] += v
}

// delegate reveals a pool share of v for delegate d.
func (f *routingFixture) delegate(d uint32, v uint64) {
	f.addTally(types.PoolProposalID, d, f.encrypt(v, f.nextRandomness()))
	f.pools[d] += v
}

// ballot stores delegate d's ballot as the ballot op does.
func (f *routingFixture) ballot(d uint32, options ...uint32) {
	f.t.Helper()
	require.NoError(f.t, f.keeper.SetDelegateBallot(f.kv(), &types.DelegateBallotRecord{
		VoteRoundId:   f.round.VoteRoundId,
		DelegateIndex: d,
		Options:       options,
		Height:        100,
	}))
	f.ballots[d] = options
}

// route runs RouteProxyPools on the fixture's round under a fresh event
// manager and, on success, stores the round, as EndBlock does next.
func (f *routingFixture) route() error {
	f.ctx = f.ctx.WithEventManager(sdk.NewEventManager())
	if err := f.keeper.RouteProxyPools(f.ctx, f.round); err != nil {
		return err
	}
	return f.keeper.SetVoteRound(f.kv(), f.round)
}

// tallies returns every tally and share count of the round, pools included,
// by raw key.
func (f *routingFixture) tallies() map[string][]byte {
	f.t.Helper()
	roundID := f.round.VoteRoundId
	return proxytest.Values(f.t, f.kv(), append(slices.Clone(types.TallyPrefix), roundID...), append(slices.Clone(types.ShareCountPrefix), roundID...))
}

// tallyKey returns the round's TallyKey(p, o).
func (f *routingFixture) tallyKey(p, o uint32) []byte {
	f.t.Helper()
	key, err := types.TallyKey(f.round.VoteRoundId, p, o)
	require.NoError(f.t, err)
	return key
}

// ciphertext decodes the value at key of state, or returns nil if absent.
func (f *routingFixture) ciphertext(state map[string][]byte, key []byte) *elgamal.Ciphertext {
	f.t.Helper()
	bz, ok := state[string(key)]
	if !ok {
		return nil
	}
	ct, err := elgamal.UnmarshalCiphertext(bz)
	require.NoError(f.t, err)
	return ct
}

// model returns the plaintext of each bucket after routing: its direct
// votes plus the pool of every delegate with both a pool and a ballot that
// chose it. A bucket the model lacks must stay absent.
func (f *routingFixture) model() map[routingBucket]uint64 {
	want := maps.Clone(f.direct)
	for d, options := range f.ballots {
		v, ok := f.pools[d]
		if !ok {
			continue
		}
		for i, o := range options {
			want[routingBucket{uint32(i + 1), o}] += v
		}
	}
	return want
}

// requireRouted checks the store after a successful route against before,
// the tallies ahead of it. A bucket that a counted pool chose holds
// RerandomizeRoutedBucket of its direct tally, the identity if absent, plus
// every such pool; every other key under the round's tallies and share
// counts, pools included, is unchanged; and every option of every proposal
// decrypts to the model, or is absent where the model has nothing.
func (f *routingFixture) requireRouted(before map[string][]byte) {
	f.t.Helper()
	sums := map[routingBucket]*elgamal.Ciphertext{}
	for d, options := range f.ballots {
		pool := f.ciphertext(before, f.tallyKey(types.PoolProposalID, d))
		if pool == nil {
			continue
		}
		for i, o := range options {
			b := routingBucket{uint32(i + 1), o}
			if sum, ok := sums[b]; ok {
				sums[b] = elgamal.HomomorphicAdd(sum, pool)
			} else {
				sums[b] = pool
			}
		}
	}
	want := maps.Clone(before)
	for b, sum := range sums {
		combined := sum
		if direct := f.ciphertext(before, f.tallyKey(b.p, b.o)); direct != nil {
			combined = elgamal.HomomorphicAdd(direct, sum)
		}
		final, _, err := keeper.RerandomizeRoutedBucket(f.eaPK, f.round.VoteRoundId, b.p, b.o, combined)
		require.NoError(f.t, err)
		want[string(f.tallyKey(b.p, b.o))], err = elgamal.MarshalCiphertext(final)
		require.NoError(f.t, err)
	}
	after := f.tallies()
	require.Equal(f.t, want, after, "routed buckets are re-randomized sums; nothing else changes")

	model := f.model()
	for _, proposal := range f.round.Proposals {
		for _, option := range proposal.Options {
			b := routingBucket{proposal.Id, option.Index}
			ct := f.ciphertext(after, f.tallyKey(b.p, b.o))
			v, ok := model[b]
			if !ok {
				require.Nil(f.t, ct, "bucket %v", b)
				continue
			}
			require.NotNil(f.t, ct, "bucket %v", b)
			require.True(f.t, elgamal.DecryptToPoint(f.eaSK, ct).Equal(elgamal.ValuePoint(v)), "bucket %v decrypts to %d", b, v)
		}
	}
}

// requireRoutedEvent checks the one proxy_pools_routed event of the last
// route.
func (f *routingFixture) requireRoutedEvent(ballots, routedPools string) {
	f.t.Helper()
	require.Equal(f.t, map[string]string{
		types.AttributeKeyRoundID:     hex.EncodeToString(f.round.VoteRoundId),
		types.AttributeKeyBallots:     ballots,
		types.AttributeKeyRoutedPools: routedPools,
	}, eventAttributes(f.t, f.ctx.EventManager().Events(), types.EventTypeProxyPoolsRouted))
}

// TestRouteProxyPoolsConservesWeight routes direct votes and multi-share
// pools of known plaintexts. Delegates share options on some proposals and
// differ on others; delegate 5 has a pool and no ballot, and delegate 6 a
// ballot and no pool. Each counted pool lands once on every proposal, in
// its delegate's option, so per-proposal totals differ only by direct
// votes; every bucket matches the model and the routing identity; pools and
// share counts stay as they were.
func TestRouteProxyPoolsConservesWeight(t *testing.T) {
	f := newDefaultRoutingFixture(t)
	f.vote(1, 0, 100)
	f.vote(1, 0, 23)
	f.vote(1, 1, 7)
	f.vote(2, 2, 50)
	f.vote(3, 3, 9)
	f.delegate(1, 5)
	f.delegate(1, 6)
	f.delegate(2, 20)
	f.delegate(3, 300)
	f.delegate(4, 1000)
	f.delegate(4, 3000)
	f.delegate(5, 50000)
	f.delegate(7, 600000)
	f.ballot(1, 0, 1, 2)
	f.ballot(2, 0, 1, 3)
	f.ballot(3, 1, 2, 2)
	f.ballot(4, 1, 0, 0)
	f.ballot(6, 0, 0, 1)
	f.ballot(7, 1, 2, 3)
	require.Equal(t, map[routingBucket]uint64{
		{1, 0}: 123 + 11 + 20,
		{1, 1}: 7 + 300 + 4000 + 600000,
		{2, 0}: 4000,
		{2, 1}: 11 + 20,
		{2, 2}: 50 + 300 + 600000,
		{3, 0}: 4000,
		{3, 2}: 11 + 300,
		{3, 3}: 9 + 20 + 600000,
	}, f.model(), "the model by hand: option 1 of proposal 3 has only delegate 6's ballot, without a pool")
	before := f.tallies()

	require.NoError(t, f.route())
	f.requireRouted(before)
	f.requireRoutedEvent("6", "5")
	stored, err := f.keeper.GetVoteRound(f.kv(), routingRoundID)
	require.NoError(t, err)
	require.True(t, stored.ProxyDelegation.PoolsRouted)
}

// TestRouteProxyPoolsRunsOnce checks that pools_routed makes routing run
// once: routing again, with the same round or the one read back from the
// store, changes nothing and emits nothing.
func TestRouteProxyPoolsRunsOnce(t *testing.T) {
	f := newDefaultRoutingFixture(t)
	f.vote(1, 0, 3)
	f.delegate(1, 10)
	f.ballot(1, 0, 1, 2)
	before := f.tallies()
	require.NoError(t, f.route())
	f.requireRouted(before)
	routed := f.tallies()
	stored, err := f.keeper.GetVoteRound(f.kv(), routingRoundID)
	require.NoError(t, err)
	require.True(t, stored.ProxyDelegation.PoolsRouted)

	for _, round := range []*types.VoteRound{f.round, stored} {
		ctx := f.ctx.WithEventManager(sdk.NewEventManager())
		require.NoError(t, f.keeper.RouteProxyPools(ctx, round))
		require.Equal(t, routed, f.tallies())
		require.Empty(t, ctx.EventManager().Events())
	}
}

// TestRouteProxyPoolsSkipsRoundsWithoutTheFlag checks that a round without
// field 31, or with it unset, is left alone even with pools and ballots
// stored under it: no tally changes, the round is not marked and no event
// is emitted.
func TestRouteProxyPoolsSkipsRoundsWithoutTheFlag(t *testing.T) {
	for name, state := range map[string]*types.ProxyDelegationRoundState{
		"without field 31": nil,
		"not enabled":      {NextDelegateIndexAtCreation: 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDefaultRoutingFixture(t)
			f.round.ProxyDelegation = state
			f.vote(1, 0, 3)
			f.delegate(1, 10)
			f.ballot(1, 0, 1, 2)
			before := f.tallies()
			require.NoError(t, f.route())
			require.Equal(t, before, f.tallies())
			require.False(t, f.round.GetProxyDelegation().GetPoolsRouted())
			require.Empty(t, f.ctx.EventManager().Events())
		})
	}
}

// TestRouteProxyPoolsHaltsOnCorruptState checks that routing refuses state
// the chain never writes with ErrRoutingCorruptState, which EndBlock returns
// to halt the chain, before writing anything: no tally changes, the round is
// not marked and no event is emitted.
func TestRouteProxyPoolsHaltsOnCorruptState(t *testing.T) {
	// set writes value at key, failing the test on a store error.
	set := func(f *routingFixture, key, value []byte) {
		require.NoError(f.t, f.kv().Set(key, value))
	}
	notAPoint := bytes.Repeat([]byte{0xff}, elgamal.CiphertextSize)
	for _, tc := range []struct {
		name    string
		corrupt func(f *routingFixture)
	}{
		{"a ballot that does not decode", func(f *routingFixture) {
			key, err := types.DelegateBallotKey(f.round.VoteRoundId, 1)
			require.NoError(f.t, err)
			set(f, key, []byte{0xff})
		}},
		{"a pool that does not decode", func(f *routingFixture) { set(f, f.tallyKey(types.PoolProposalID, 1), notAPoint) }},
		{"a missing ea_pk", func(f *routingFixture) { f.round.EaPk = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDefaultRoutingFixture(t)
			f.vote(1, 0, 3)
			f.delegate(1, 10)
			f.ballot(1, 0, 1, 2)
			tc.corrupt(f)
			before := f.tallies()
			require.ErrorIs(t, f.route(), keeper.ErrRoutingCorruptState)
			require.Equal(t, before, f.tallies())
			require.False(t, f.round.ProxyDelegation.PoolsRouted)
			require.Empty(t, f.ctx.EventManager().Events())
		})
	}
}
