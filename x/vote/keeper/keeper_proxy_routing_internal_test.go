package keeper

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests force a degenerate routing re-randomizer
// (docs/proxy-delegation/contracts-v1.md §5.6), which a real hash reaches
// with probability about 2^-250.

// scriptedHash stands in for ScalarPallas.Hash: call i records its preimage
// and returns script[i], and calls past the script use ScalarPallas.Hash.
type scriptedHash struct {
	script    []curvey.Scalar
	preimages [][]byte
}

// hash is the hash-to-scalar function the tests pass to rerandomizeRoutedBucket.
func (h *scriptedHash) hash(preimage []byte) curvey.Scalar {
	h.preimages = append(h.preimages, bytes.Clone(preimage))
	if i := len(h.preimages) - 1; i < len(h.script) {
		return h.script[i]
	}
	return new(curvey.ScalarPallas).Hash(preimage)
}

// routingDegenerateFixture is a valid bucket and two buckets whose C1 or C2
// the randomness s cancels.
type routingDegenerateFixture struct {
	eaPK      *elgamal.PublicKey
	roundID   []byte
	s         curvey.Scalar
	plain     *elgamal.Ciphertext // Enc(15, 99)
	cancelsC1 *elgamal.Ciphertext // C1 = -s*G, so adding Enc(0, s) makes C1 the identity
	cancelsC2 *elgamal.Ciphertext // C1 = G and C2 = -s*ea_pk, so adding Enc(0, s) makes only C2 the identity
}

// newRoutingDegenerateFixture builds the fixture under the election key 17.
func newRoutingDegenerateFixture(t *testing.T) routingDegenerateFixture {
	t.Helper()
	eaPK := &elgamal.PublicKey{Point: elgamal.PallasGenerator().Mul(new(curvey.ScalarPallas).New(17))}
	s := new(curvey.ScalarPallas).New(1234567)
	plain, err := elgamal.EncryptWithRandomness(eaPK, 15, new(curvey.ScalarPallas).New(99))
	require.NoError(t, err)
	cancelsC1, err := elgamal.EncryptWithRandomness(eaPK, 15, s.Neg())
	require.NoError(t, err)
	return routingDegenerateFixture{
		eaPK:      eaPK,
		roundID:   bytes.Repeat([]byte{0x0a}, types.RoundIDLen),
		s:         s,
		plain:     plain,
		cancelsC1: cancelsC1,
		cancelsC2: &elgamal.Ciphertext{C1: elgamal.PallasGenerator(), C2: eaPK.Point.Mul(s.Neg())},
	}
}

// TestRerandomizeRoutedBucketDegenerate checks that a zero rho, or one that
// leaves C1 or C2 at the identity, ends in ErrRoutingRerandomizerDegenerate
// after one hash, with no retry.
func TestRerandomizeRoutedBucketDegenerate(t *testing.T) {
	f := newRoutingDegenerateFixture(t)
	for _, tc := range []struct {
		name     string
		combined *elgamal.Ciphertext
		rho      curvey.Scalar
	}{
		{"zero rho", f.plain, new(curvey.ScalarPallas).Zero()},
		{"identity C1", f.cancelsC1, f.s},
		{"identity C2", f.cancelsC2, f.s},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &scriptedHash{script: []curvey.Scalar{tc.rho}}
			final, rho, err := rerandomizeRoutedBucket(h.hash, f.eaPK, f.roundID, 1, 0, tc.combined)
			require.ErrorIs(t, err, ErrRoutingRerandomizerDegenerate)
			require.Nil(t, final)
			require.Nil(t, rho)
			require.Len(t, h.preimages, 1)
		})
	}
}

// TestRerandomizeRoutedBucketRejectsMalformedInputs checks that malformed
// inputs fail with their own errors, not ErrRoutingRerandomizerDegenerate.
func TestRerandomizeRoutedBucketRejectsMalformedInputs(t *testing.T) {
	f := newRoutingDegenerateFixture(t)

	_, _, err := RerandomizeRoutedBucket(f.eaPK, f.roundID[:31], 1, 0, f.plain)
	require.ErrorIs(t, err, types.ErrInvalidRoundIDLen)

	_, _, err = RerandomizeRoutedBucket(f.eaPK, f.roundID, 1, 0, nil)
	require.ErrorContains(t, err, "ciphertext must not be nil")

	identityKey := &elgamal.PublicKey{Point: new(curvey.PointPallas).Identity()}
	_, _, err = RerandomizeRoutedBucket(identityKey, f.roundID, 1, 0, f.plain)
	require.ErrorContains(t, err, "public key must not be the identity point")
}

// routingHookFixture is a proxy round in a fresh store under the
// degenerate fixture's election key, with two proposals of three options, a direct vote
// for option 1 of proposal 1, and pools and ballots for delegates 1 to 3 that
// route into options 0 and 2 of proposal 1 and options 0 and 1 of proposal 2.
type routingHookFixture struct {
	ctx    sdk.Context
	keeper *Keeper
	round  *types.VoteRound
}

// newRoutingHookFixture stores the fixture's round, tallies and ballots.
func newRoutingHookFixture(t *testing.T) routingHookFixture {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient_test"))
	f := routingHookFixture{
		ctx:    testCtx.Ctx.WithEventManager(sdk.NewEventManager()),
		keeper: NewKeeper(runtime.NewKVStoreService(key), "sv1authority", log.NewNopLogger(), nil, nil),
	}
	base := newRoutingDegenerateFixture(t)
	f.round = proxytest.Round(base.roundID, 3, 3)
	f.round.EaPk = base.eaPK.Point.ToAffineCompressed()
	kv := f.keeper.OpenKVStore(f.ctx)
	require.NoError(t, f.keeper.SetVoteRound(kv, f.round))
	add := func(proposalID, decision uint32, value uint64) {
		ct, err := elgamal.EncryptWithRandomness(base.eaPK, value, new(curvey.ScalarPallas).New(int(100*proposalID+decision+1)))
		require.NoError(t, err)
		bz, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		require.NoError(t, f.keeper.AddToTally(kv, base.roundID, proposalID, decision, bz))
	}
	add(1, 1, 4)
	for d, options := range map[uint32][]uint32{1: {2, 0}, 2: {0, 0}, 3: {2, 1}} {
		add(types.PoolProposalID, d, uint64(10*d))
		require.NoError(t, f.keeper.SetDelegateBallot(kv, &types.DelegateBallotRecord{VoteRoundId: base.roundID, DelegateIndex: d, Options: options}))
	}
	return f
}

// tallies returns every tally of the fixture's round by raw key.
func (f routingHookFixture) tallies(t *testing.T) map[string][]byte {
	return proxytest.Values(t, f.keeper.OpenKVStore(f.ctx), append(slices.Clone(types.TallyPrefix), f.round.VoteRoundId...))
}

// TestRouteProxyPoolsRerandomizesInBucketOrder checks that routing derives
// each routed bucket's rho from the contract's preimage, once per bucket
// that gained a pool, ascending by proposal and then option (contracts-v1
// §5.6).
func TestRouteProxyPoolsRerandomizesInBucketOrder(t *testing.T) {
	f := newRoutingHookFixture(t)
	h := &scriptedHash{}
	require.NoError(t, f.keeper.routeProxyPools(f.ctx, f.round, h.hash))
	var buckets [][2]uint32
	for _, preimage := range h.preimages {
		head := append([]byte(routingRerandomizerDomain), f.round.VoteRoundId...)
		require.True(t, bytes.HasPrefix(preimage, head), "the contract's preimage")
		require.Len(t, preimage, len(head)+8+elgamal.CiphertextSize)
		buckets = append(buckets, [2]uint32{
			binary.BigEndian.Uint32(preimage[len(head):]),
			binary.BigEndian.Uint32(preimage[len(head)+4:]),
		})
	}
	require.Equal(t, [][2]uint32{{1, 0}, {1, 2}, {2, 0}, {2, 1}}, buckets)
}

// TestRouteProxyPoolsHaltsWhenRerandomizerDegenerate checks that a bucket
// whose rho is zero ends routing with ErrRoutingRerandomizerDegenerate, which
// EndBlock returns to halt the chain, with no retry and nothing written, even
// for buckets computed before it: no tally changes, the round is not marked
// and no event is emitted.
func TestRouteProxyPoolsHaltsWhenRerandomizerDegenerate(t *testing.T) {
	zero := new(curvey.ScalarPallas).Zero()
	for name, firstFailure := range map[string]int{"the first bucket": 0, "the last bucket": 3} {
		t.Run(name, func(t *testing.T) {
			f := newRoutingHookFixture(t)
			before := f.tallies(t)
			calls := 0
			hash := func(preimage []byte) curvey.Scalar {
				calls++
				if calls > firstFailure {
					return zero
				}
				return new(curvey.ScalarPallas).Hash(preimage)
			}
			err := f.keeper.routeProxyPools(f.ctx, f.round, hash)
			require.ErrorIs(t, err, ErrRoutingRerandomizerDegenerate)
			require.Equal(t, firstFailure+1, calls)
			require.Equal(t, before, f.tallies(t))
			require.False(t, f.round.ProxyDelegation.PoolsRouted)
			require.Empty(t, f.ctx.EventManager().Events())
		})
	}
}
