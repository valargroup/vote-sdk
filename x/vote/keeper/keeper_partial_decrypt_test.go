package keeper_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	svtest "github.com/valargroup/shielded-vote/testutil"
	"github.com/valargroup/shielded-vote/x/vote/keeper"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// newPartialDecryptTestKeeper returns a fresh keeper backed by an in-memory KV store.
func newPartialDecryptTestKeeper(t *testing.T) (*keeper.Keeper, sdk.Context) {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey + "_pd_test")
	tkey := storetypes.NewTransientStoreKey("transient_pd_test")
	testCtx := sdktestutil.DefaultContextWithDB(t, key, tkey)
	ctx := testCtx.Ctx
	svc := runtime.NewKVStoreService(key)
	k := keeper.NewKeeper(svc, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	return k, ctx
}

var pdRoundID = bytes.Repeat([]byte{0x42}, 32)

// ---------------------------------------------------------------------------
// SetPartialDecryptions / GetPartialDecryption
// ---------------------------------------------------------------------------

func TestPartialDecrypt_SetAndGet(t *testing.T) {
	k, ctx := newPartialDecryptTestKeeper(t)
	kvStore := k.OpenKVStore(ctx)

	entries := []*types.PartialDecryptionEntry{
		{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0xAA}, 32)},
		{ProposalId: 1, VoteDecision: 1, PartialDecrypt: bytes.Repeat([]byte{0xBB}, 32)},
		{ProposalId: 2, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0xCC}, 32)},
	}

	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 1, entries))

	for _, want := range entries {
		got, err := k.GetPartialDecryption(kvStore, pdRoundID, 1, want.ProposalId, want.VoteDecision)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, want.PartialDecrypt, got.PartialDecrypt)
	}
}

func TestPartialDecrypt_GetMissing_ReturnsNil(t *testing.T) {
	k, ctx := newPartialDecryptTestKeeper(t)
	kvStore := k.OpenKVStore(ctx)

	got, err := k.GetPartialDecryption(kvStore, pdRoundID, 99, 1, 0)
	require.NoError(t, err)
	require.Nil(t, got)
}

// ---------------------------------------------------------------------------
// HasPartialDecryptionsFromValidator
// ---------------------------------------------------------------------------

func TestPartialDecrypt_HasFromValidator(t *testing.T) {
	k, ctx := newPartialDecryptTestKeeper(t)
	kvStore := k.OpenKVStore(ctx)

	// No submission yet.
	has, err := k.HasPartialDecryptionsFromValidator(kvStore, pdRoundID, 1)
	require.NoError(t, err)
	require.False(t, has, "expected false before any submission")

	entry := []*types.PartialDecryptionEntry{
		{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 32)},
	}
	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 1, entry))

	has, err = k.HasPartialDecryptionsFromValidator(kvStore, pdRoundID, 1)
	require.NoError(t, err)
	require.True(t, has, "expected true after submission")

	// Different validator index: still false.
	has, err = k.HasPartialDecryptionsFromValidator(kvStore, pdRoundID, 2)
	require.NoError(t, err)
	require.False(t, has)
}

// ---------------------------------------------------------------------------
// CountPartialDecryptionValidators
// ---------------------------------------------------------------------------

func TestPartialDecrypt_CountValidators(t *testing.T) {
	k, ctx := newPartialDecryptTestKeeper(t)
	kvStore := k.OpenKVStore(ctx)

	count, err := k.CountPartialDecryptionValidators(kvStore, pdRoundID)
	require.NoError(t, err)
	require.Equal(t, 0, count, "empty round should have 0 validators")

	entries := func(d byte) []*types.PartialDecryptionEntry {
		return []*types.PartialDecryptionEntry{
			{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{d}, 32)},
			{ProposalId: 1, VoteDecision: 1, PartialDecrypt: bytes.Repeat([]byte{d + 1}, 32)},
		}
	}

	// Validator 1 submits.
	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 1, entries(0xAA)))
	count, err = k.CountPartialDecryptionValidators(kvStore, pdRoundID)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	// Validator 2 submits.
	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 2, entries(0xBB)))
	count, err = k.CountPartialDecryptionValidators(kvStore, pdRoundID)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Validator 3 submits.
	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 3, entries(0xCC)))
	count, err = k.CountPartialDecryptionValidators(kvStore, pdRoundID)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	// Re-submitting validator 1 (overwrite) must not change the count.
	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 1, entries(0xDD)))
	count, err = k.CountPartialDecryptionValidators(kvStore, pdRoundID)
	require.NoError(t, err)
	require.Equal(t, 3, count, "re-submission should not inflate the validator count")
}

// CountValidators is isolated to a single round — entries from a different
// round must not be visible.
func TestPartialDecrypt_CountValidators_RoundIsolation(t *testing.T) {
	k, ctx := newPartialDecryptTestKeeper(t)
	kvStore := k.OpenKVStore(ctx)

	otherRound := bytes.Repeat([]byte{0xFF}, 32)
	entry := []*types.PartialDecryptionEntry{
		{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 32)},
	}

	require.NoError(t, k.SetPartialDecryptions(kvStore, otherRound, 1, entry))

	count, err := k.CountPartialDecryptionValidators(kvStore, pdRoundID)
	require.NoError(t, err)
	require.Equal(t, 0, count, "entries from other round must not be counted")
}

// ---------------------------------------------------------------------------
// GetPartialDecryptionsForRound
// ---------------------------------------------------------------------------

func TestPartialDecrypt_GetForRound_GroupedByAccumulator(t *testing.T) {
	k, ctx := newPartialDecryptTestKeeper(t)
	kvStore := k.OpenKVStore(ctx)

	// Three validators, two accumulators each.
	d1p1 := bytes.Repeat([]byte{0x11}, 32) // validator 1, proposal 1, decision 0
	d1p2 := bytes.Repeat([]byte{0x12}, 32) // validator 1, proposal 1, decision 1
	d2p1 := bytes.Repeat([]byte{0x21}, 32) // validator 2, proposal 1, decision 0
	d2p2 := bytes.Repeat([]byte{0x22}, 32) // validator 2, proposal 1, decision 1
	d3p1 := bytes.Repeat([]byte{0x31}, 32) // validator 3, proposal 1, decision 0
	d3p2 := bytes.Repeat([]byte{0x32}, 32) // validator 3, proposal 1, decision 1

	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 1, []*types.PartialDecryptionEntry{
		{ProposalId: 1, VoteDecision: 0, PartialDecrypt: d1p1},
		{ProposalId: 1, VoteDecision: 1, PartialDecrypt: d1p2},
	}))
	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 2, []*types.PartialDecryptionEntry{
		{ProposalId: 1, VoteDecision: 0, PartialDecrypt: d2p1},
		{ProposalId: 1, VoteDecision: 1, PartialDecrypt: d2p2},
	}))
	require.NoError(t, k.SetPartialDecryptions(kvStore, pdRoundID, 3, []*types.PartialDecryptionEntry{
		{ProposalId: 1, VoteDecision: 0, PartialDecrypt: d3p1},
		{ProposalId: 1, VoteDecision: 1, PartialDecrypt: d3p2},
	}))

	got, err := k.GetPartialDecryptionsForRound(kvStore, pdRoundID)
	require.NoError(t, err)

	// Two accumulator buckets.
	require.Len(t, got, 2)

	acc0 := keeper.AccumulatorKey(1, 0)
	acc1 := keeper.AccumulatorKey(1, 1)

	require.Len(t, got[acc0], 3, "accumulator (proposal=1,decision=0) should have 3 partials")
	require.Len(t, got[acc1], 3, "accumulator (proposal=1,decision=1) should have 3 partials")

	// Build lookup maps for order-independent comparison.
	byIdx0 := make(map[uint32][]byte)
	for _, pd := range got[acc0] {
		byIdx0[pd.ValidatorIndex] = pd.PartialDecrypt
	}
	require.Equal(t, d1p1, byIdx0[1])
	require.Equal(t, d2p1, byIdx0[2])
	require.Equal(t, d3p1, byIdx0[3])

	byIdx1 := make(map[uint32][]byte)
	for _, pd := range got[acc1] {
		byIdx1[pd.ValidatorIndex] = pd.PartialDecrypt
	}
	require.Equal(t, d1p2, byIdx1[1])
	require.Equal(t, d2p2, byIdx1[2])
	require.Equal(t, d3p2, byIdx1[3])
}

func TestPartialDecrypt_GetForRound_Empty(t *testing.T) {
	k, ctx := newPartialDecryptTestKeeper(t)
	kvStore := k.OpenKVStore(ctx)

	got, err := k.GetPartialDecryptionsForRound(kvStore, pdRoundID)
	require.NoError(t, err)
	require.Empty(t, got)
}

// ---------------------------------------------------------------------------
// AccumulatorKey
// ---------------------------------------------------------------------------

func TestAccumulatorKey_Uniqueness(t *testing.T) {
	// Different (proposalID, decision) pairs must produce different keys.
	require.NotEqual(t, keeper.AccumulatorKey(1, 0), keeper.AccumulatorKey(1, 1))
	require.NotEqual(t, keeper.AccumulatorKey(1, 0), keeper.AccumulatorKey(2, 0))
	// Encoding must not mix up high/low bits.
	require.NotEqual(t, keeper.AccumulatorKey(0, 1), keeper.AccumulatorKey(1, 0))
}
