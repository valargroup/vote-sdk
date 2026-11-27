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
	"google.golang.org/protobuf/proto"

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

// routingRoundID is the round of most routing tests.
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

// newRoutingFixture stores an ACTIVE proxy round roundID under election key
// eaSK, with proposals 1..n, proposal i having optionCounts[i-1] options.
func newRoutingFixture(t *testing.T, roundID []byte, eaSK *elgamal.SecretKey, optionCounts ...int) *routingFixture {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	eaPK := &elgamal.PublicKey{Point: elgamal.PallasGenerator().Mul(eaSK.Scalar)}
	round := proxytest.Round(roundID, optionCounts...)
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

// newDefaultRoutingFixture is newRoutingFixture for routingRoundID, with
// proposals of 2, 3 and 4 options.
func newDefaultRoutingFixture(t *testing.T) *routingFixture {
	eaSK, _ := proxytest.ElectionKey("routing/ea_sk")
	return newRoutingFixture(t, routingRoundID, eaSK, 2, 3, 4)
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
	f.poolShare(d, v, f.nextRandomness())
}

// poolShare reveals a pool share of v for delegate d with randomness r.
func (f *routingFixture) poolShare(d uint32, v uint64, r curvey.Scalar) {
	f.addTally(types.PoolProposalID, d, f.encrypt(v, r))
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

// TestRouteProxyPoolsCountsOnlyDelegatesWithBoth checks that a pool without
// a ballot counts nowhere and a ballot without a pool creates no bucket:
// every tally stays byte for byte, while pools_routed is still set and the
// event still counts the ballots.
func TestRouteProxyPoolsCountsOnlyDelegatesWithBoth(t *testing.T) {
	t.Run("pools without ballots", func(t *testing.T) {
		f := newDefaultRoutingFixture(t)
		f.vote(1, 1, 8)
		f.delegate(2, 70)
		f.delegate(2, 5)
		f.delegate(3, 1)
		before := f.tallies()
		require.NoError(t, f.route())
		require.Equal(t, before, f.tallies())
		require.True(t, f.round.ProxyDelegation.PoolsRouted)
		f.requireRoutedEvent("0", "0")
	})
	t.Run("ballots without pools", func(t *testing.T) {
		f := newDefaultRoutingFixture(t)
		f.vote(1, 1, 8)
		f.ballot(3, 1, 2, 3)
		f.ballot(4, 0, 0, 0)
		f.delegate(5, 9)
		before := f.tallies()
		require.NoError(t, f.route())
		require.Equal(t, before, f.tallies())
		require.True(t, f.round.ProxyDelegation.PoolsRouted)
		f.requireRoutedEvent("2", "0")
	})
	t.Run("no ballots and no pools", func(t *testing.T) {
		f := newDefaultRoutingFixture(t)
		before := f.tallies()
		require.NoError(t, f.route())
		require.Equal(t, before, f.tallies())
		f.requireRoutedEvent("0", "0")
	})
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

// TestRouteProxyPoolsDefeatsIdentityGriefing routes the griefing pools of the
// routing vectors (contracts-v1 §5.6), each accepted by AddToTally.
// Delegates 1 and 2 share options and their pools' randomness cancels, so
// the naive sum of each of their buckets has an identity C1; delegates 3
// and 4 do the same with two encryptions of 0, whose naive sum is the
// identity ciphertext. After routing no bucket has an identity C1 or C2,
// each decrypts to its pools' total, and a partial-decryption DLEQ over its
// C1 verifies, where one over the naive sum's does not.
func TestRouteProxyPoolsDefeatsIdentityGriefing(t *testing.T) {
	f := newDefaultRoutingFixture(t)
	r1, r2, r := proxytest.Scalar("griefing/r1"), proxytest.Scalar("griefing/r2"), proxytest.Scalar("griefing/r")
	f.poolShare(1, 5, r1)
	f.poolShare(1, 3, r2)
	f.poolShare(2, 7, r1.Add(r2).Neg())
	f.poolShare(3, 0, r)
	f.poolShare(4, 0, r.Neg())
	f.ballot(1, 1, 1, 1)
	f.ballot(2, 1, 1, 1)
	f.ballot(3, 0, 2, 2)
	f.ballot(4, 0, 2, 2)
	before := f.tallies()
	naive := func(d1, d2 uint32) *elgamal.Ciphertext {
		return elgamal.HomomorphicAdd(
			f.ciphertext(before, f.tallyKey(types.PoolProposalID, d1)),
			f.ciphertext(before, f.tallyKey(types.PoolProposalID, d2)))
	}
	cancelsC1, cancelsBoth := naive(1, 2), naive(3, 4)
	require.True(t, cancelsC1.C1.IsIdentity())
	require.False(t, cancelsC1.C2.IsIdentity())
	require.True(t, cancelsBoth.C1.IsIdentity() && cancelsBoth.C2.IsIdentity())
	require.False(t, partialDecryptionVerifies(f.eaSK, f.eaPK, cancelsC1.C1))

	require.NoError(t, f.route())
	f.requireRouted(before)
	for _, b := range []routingBucket{{1, 1}, {2, 1}, {3, 1}, {1, 0}, {2, 2}, {3, 2}} {
		ct := f.ciphertext(f.tallies(), f.tallyKey(b.p, b.o))
		require.False(t, ct.C1.IsIdentity(), "bucket %v C1", b)
		require.False(t, ct.C2.IsIdentity(), "bucket %v C2", b)
		require.True(t, partialDecryptionVerifies(f.eaSK, f.eaPK, ct.C1), "bucket %v", b)
	}
}

// TestRouteProxyPoolsMatchesRoutingVectors routes, for each routing vector,
// a stored direct tally and two pools that sum to its combined ciphertext,
// chosen by both delegates' ballots, and checks that the bucket ends as the
// vector's final, byte for byte (contracts-v1 §13).
func TestRouteProxyPoolsMatchesRoutingVectors(t *testing.T) {
	for _, c := range routingVectorCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Inputs
			eaSK, err := elgamal.UnmarshalSecretKey(routingVectorHex(t, in.EaSK))
			require.NoError(t, err)
			optionCounts := slices.Repeat([]int{types.MaxVoteOptions}, int(in.ProposalID))
			f := newRoutingFixture(t, routingVectorHex(t, in.VoteRoundID), eaSK, optionCounts...)
			require.Equal(t, in.EaPK, hex.EncodeToString(f.round.EaPk))
			combined, err := elgamal.UnmarshalCiphertext(routingVectorHex(t, in.Combined))
			require.NoError(t, err)

			direct := f.encrypt(11, proxytest.Scalar("vectors/direct"))
			pool := f.encrypt(13, proxytest.Scalar("vectors/pool"))
			rest := elgamal.HomomorphicAdd(direct, pool)
			f.addTally(in.ProposalID, in.VoteDecision, direct)
			f.addTally(types.PoolProposalID, 1, pool)
			f.addTally(types.PoolProposalID, 2, &elgamal.Ciphertext{C1: combined.C1.Sub(rest.C1), C2: combined.C2.Sub(rest.C2)})
			options := make([]uint32, in.ProposalID)
			options[in.ProposalID-1] = in.VoteDecision
			f.ballot(1, options...)
			f.ballot(2, options...)

			require.NoError(t, f.route())
			final, err := f.keeper.GetTally(f.kv(), f.round.VoteRoundId, in.ProposalID, in.VoteDecision)
			require.NoError(t, err)
			require.Equal(t, c.Expected.Final, hex.EncodeToString(final))
		})
	}
}

// TestRouteProxyPoolsHaltsOnCorruptState checks that routing refuses state
// the chain never writes with ErrRoutingCorruptState, which EndBlock returns
// to halt the chain, before writing anything: no tally changes, the round is
// not marked and no event is emitted. A ballot without a pool is never
// decoded, so a corrupt one does not halt.
func TestRouteProxyPoolsHaltsOnCorruptState(t *testing.T) {
	// set writes value at key, failing the test on a store error.
	set := func(f *routingFixture, key, value []byte) {
		require.NoError(f.t, f.kv().Set(key, value))
	}
	// ballotKey returns 0x1B || round || u32be(d).
	ballotKey := func(f *routingFixture, d uint32) []byte {
		key, err := types.DelegateBallotKey(f.round.VoteRoundId, d)
		require.NoError(f.t, err)
		return key
	}
	// storeRecord stores record under delegate d's ballot key.
	storeRecord := func(f *routingFixture, d uint32, record *types.DelegateBallotRecord) {
		bz, err := proto.MarshalOptions{Deterministic: true}.Marshal(record)
		require.NoError(f.t, err)
		set(f, ballotKey(f, d), bz)
	}
	notAPoint := bytes.Repeat([]byte{0xff}, elgamal.CiphertextSize)
	for _, tc := range []struct {
		name    string
		corrupt func(f *routingFixture)
	}{
		{"a ballot that does not decode", func(f *routingFixture) { set(f, ballotKey(f, 1), []byte{0xff}) }},
		{"a ballot with too few options", func(f *routingFixture) { f.ballot(1, 0, 1) }},
		{"a ballot with too many options", func(f *routingFixture) { f.ballot(1, 0, 1, 2, 3) }},
		{"a ballot with an option out of range", func(f *routingFixture) { f.ballot(1, 0, 3, 2) }},
		{"a ballot naming another delegate", func(f *routingFixture) {
			storeRecord(f, 1, &types.DelegateBallotRecord{VoteRoundId: f.round.VoteRoundId, DelegateIndex: 2, Options: []uint32{0, 1, 2}})
		}},
		{"a ballot naming another round", func(f *routingFixture) {
			storeRecord(f, 1, &types.DelegateBallotRecord{VoteRoundId: ballotRoundA, DelegateIndex: 1, Options: []uint32{0, 1, 2}})
		}},
		{"a malformed ballot key", func(f *routingFixture) {
			bz, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.DelegateBallotRecord{
				VoteRoundId: f.round.VoteRoundId, DelegateIndex: 1, Options: []uint32{0, 1, 2},
			})
			require.NoError(f.t, err)
			set(f, append(ballotKey(f, 1), 0), bz)
		}},
		{"a pool that does not decode", func(f *routingFixture) { set(f, f.tallyKey(types.PoolProposalID, 1), notAPoint) }},
		{"a direct tally that does not decode", func(f *routingFixture) { set(f, f.tallyKey(1, 0), notAPoint) }},
		{"a missing ea_pk", func(f *routingFixture) { f.round.EaPk = nil }},
		{"an identity ea_pk", func(f *routingFixture) { f.round.EaPk = make([]byte, 32) }},
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

	t.Run("a corrupt ballot without a pool", func(t *testing.T) {
		f := newDefaultRoutingFixture(t)
		f.delegate(1, 10)
		f.ballot(1, 0, 1, 2)
		set(f, ballotKey(f, 2), []byte{0xff})
		before := f.tallies()
		require.NoError(t, f.route())
		f.requireRouted(before)
		f.requireRoutedEvent("2", "1")
	})
}
