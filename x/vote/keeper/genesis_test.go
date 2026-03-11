package keeper_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"

	svtest "github.com/valargroup/shielded-vote/testutil"
	"github.com/valargroup/shielded-vote/x/vote/keeper"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

func TestExportImportGenesis(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(t, key, tkey)
	ctx := testCtx.Ctx.WithBlockTime(time.Unix(1_000_000, 0).UTC())
	storeService := runtime.NewKVStoreService(key)
	k := keeper.NewKeeper(storeService, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	kvStore := k.OpenKVStore(ctx)

	roundID := bytes.Repeat([]byte{0xAA}, 32)
	roundID2 := bytes.Repeat([]byte{0xBB}, 32)

	// --- Populate state ---

	// Vote manager.
	require.NoError(t, k.SetVoteManager(kvStore, &types.VoteManagerState{Address: "sv15fjfr6rrs60vu4st6arrd94w5j6z7f6k0mfzpl"}))

	// Vote rounds.
	round := &types.VoteRound{
		VoteRoundId:      roundID,
		SnapshotHeight:   100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x11}, 32),
		ProposalsHash:    bytes.Repeat([]byte{0x22}, 32),
		VoteEndTime:      2_000_000,
		NullifierImtRoot: bytes.Repeat([]byte{0x33}, 32),
		NcRoot:           bytes.Repeat([]byte{0x44}, 32),
		Creator:          "sv15fjfr6rrs60vu4st6arrd94w5j6z7f6k0mfzpl",
		Status:           types.SessionStatus_SESSION_STATUS_ACTIVE,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Prop 1", Options: []*types.VoteOption{
				{Index: 0, Label: "Yes"},
				{Index: 1, Label: "No"},
			}},
		},
	}
	require.NoError(t, k.SetVoteRound(kvStore, round))
	round2 := &types.VoteRound{
		VoteRoundId: roundID2,
		Status:      types.SessionStatus_SESSION_STATUS_FINALIZED,
		VoteEndTime: 1_500_000,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Prop A", Options: []*types.VoteOption{
				{Index: 0, Label: "For"},
				{Index: 1, Label: "Against"},
			}},
		},
	}
	require.NoError(t, k.SetVoteRound(kvStore, round2))

	// Commitment leaves.
	leaf0 := bytes.Repeat([]byte{0x01}, 32)
	leaf1 := bytes.Repeat([]byte{0x02}, 32)
	leaf2 := bytes.Repeat([]byte{0x03}, 32)
	idx0, err := k.AppendCommitment(kvStore, leaf0)
	require.NoError(t, err)
	require.Equal(t, uint64(0), idx0)
	idx1, err := k.AppendCommitment(kvStore, leaf1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), idx1)
	idx2, err := k.AppendCommitment(kvStore, leaf2)
	require.NoError(t, err)
	require.Equal(t, uint64(2), idx2)

	// Nullifiers (various types and rounds).
	nf1 := bytes.Repeat([]byte{0xA1}, 32)
	nf2 := bytes.Repeat([]byte{0xA2}, 32)
	nf3 := bytes.Repeat([]byte{0xA3}, 32)
	require.NoError(t, k.SetNullifier(kvStore, types.NullifierTypeGov, roundID, nf1))
	require.NoError(t, k.SetNullifier(kvStore, types.NullifierTypeVoteAuthorityNote, roundID, nf2))
	require.NoError(t, k.SetNullifier(kvStore, types.NullifierTypeShare, roundID, nf3))

	// Tally accumulators (valid ElGamal ciphertexts).
	ct := validCiphertextBytes(t, 42)
	require.NoError(t, k.AddToTally(kvStore, roundID, 1, 0, ct))

	// Share counts.
	require.NoError(t, k.IncrementShareCount(kvStore, roundID, 1, 0))
	require.NoError(t, k.IncrementShareCount(kvStore, roundID, 1, 0))

	// Tally results.
	require.NoError(t, k.SetTallyResult(kvStore, &types.TallyResult{
		VoteRoundId:  roundID2,
		ProposalId:   1,
		VoteDecision: 0,
		TotalValue:   100,
	}))

	// Pallas keys.
	require.NoError(t, k.SetPallasKey(kvStore, &types.ValidatorPallasKey{
		ValidatorAddress: "svvaloper1abc",
		PallasPk:         bytes.Repeat([]byte{0xCC}, 32),
	}))

	// Commitment roots.
	root10 := bytes.Repeat([]byte{0xDD}, 32)
	require.NoError(t, k.SetCommitmentRootAtHeight(kvStore, 10, root10))
	root20 := bytes.Repeat([]byte{0xEE}, 32)
	require.NoError(t, k.SetCommitmentRootAtHeight(kvStore, 20, root20))

	// --- Export ---
	gs, err := k.ExportGenesis(kvStore)
	require.NoError(t, err)

	// Verify export contents.
	require.NotNil(t, gs.TreeState)
	require.Equal(t, uint64(3), gs.TreeState.NextIndex)
	require.Equal(t, "sv15fjfr6rrs60vu4st6arrd94w5j6z7f6k0mfzpl", gs.VoteManager)
	require.Len(t, gs.Rounds, 2)
	require.Len(t, gs.CommitmentLeaves, 3)
	require.Len(t, gs.Nullifiers, 3)
	require.Len(t, gs.TallyResults, 1)
	require.Len(t, gs.PallasKeys, 1)
	require.Len(t, gs.TallyAccumulators, 1)
	require.Len(t, gs.ShareCounts, 1)
	require.Len(t, gs.CommitmentRoots, 2)

	// Verify leaf ordering.
	require.Equal(t, uint64(0), gs.CommitmentLeaves[0].Index)
	require.Equal(t, uint64(1), gs.CommitmentLeaves[1].Index)
	require.Equal(t, uint64(2), gs.CommitmentLeaves[2].Index)
	require.Equal(t, leaf0, gs.CommitmentLeaves[0].Value)
	require.Equal(t, leaf1, gs.CommitmentLeaves[1].Value)
	require.Equal(t, leaf2, gs.CommitmentLeaves[2].Value)

	// Verify share count value.
	require.Equal(t, uint64(2), gs.ShareCounts[0].Count)

	// Verify tally result.
	require.Equal(t, uint64(100), gs.TallyResults[0].TotalValue)

	// Verify commitment roots.
	require.Equal(t, uint64(10), gs.CommitmentRoots[0].Height)
	require.Equal(t, root10, gs.CommitmentRoots[0].Root)
	require.Equal(t, uint64(20), gs.CommitmentRoots[1].Height)
	require.Equal(t, root20, gs.CommitmentRoots[1].Root)

	// --- Import into a fresh keeper ---
	key2 := storetypes.NewKVStoreKey(types.StoreKey + "2")
	tkey2 := storetypes.NewTransientStoreKey("transient_test2")
	testCtx2 := testutil.DefaultContextWithDB(t, key2, tkey2)
	ctx2 := testCtx2.Ctx.WithBlockTime(time.Unix(1_000_000, 0).UTC())
	storeService2 := runtime.NewKVStoreService(key2)
	k2 := keeper.NewKeeper(storeService2, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	kvStore2 := k2.OpenKVStore(ctx2)

	require.NoError(t, k2.InitGenesis(kvStore2, gs))

	// Verify rounds.
	r1, err := k2.GetVoteRound(kvStore2, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, r1.Status)
	require.Equal(t, uint64(100), r1.SnapshotHeight)

	r2, err := k2.GetVoteRound(kvStore2, roundID2)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, r2.Status)

	// Verify tree state.
	ts, err := k2.GetCommitmentTreeState(kvStore2)
	require.NoError(t, err)
	require.Equal(t, uint64(3), ts.NextIndex)

	// Verify commitment leaves.
	bz, err := kvStore2.Get(types.CommitmentLeafKey(0))
	require.NoError(t, err)
	require.Equal(t, leaf0, bz)
	bz, err = kvStore2.Get(types.CommitmentLeafKey(2))
	require.NoError(t, err)
	require.Equal(t, leaf2, bz)

	// Verify nullifiers.
	has, err := k2.HasNullifier(kvStore2, types.NullifierTypeGov, roundID, nf1)
	require.NoError(t, err)
	require.True(t, has)
	has, err = k2.HasNullifier(kvStore2, types.NullifierTypeVoteAuthorityNote, roundID, nf2)
	require.NoError(t, err)
	require.True(t, has)
	has, err = k2.HasNullifier(kvStore2, types.NullifierTypeShare, roundID, nf3)
	require.NoError(t, err)
	require.True(t, has)
	// Negative check: non-existent nullifier.
	has, err = k2.HasNullifier(kvStore2, types.NullifierTypeGov, roundID, bytes.Repeat([]byte{0xFF}, 32))
	require.NoError(t, err)
	require.False(t, has)

	// Verify tally accumulator.
	accBytes, err := k2.GetTally(kvStore2, roundID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, ct, accBytes)

	// Verify share count.
	count, err := k2.GetShareCount(kvStore2, roundID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, uint64(2), count)

	// Verify tally result.
	tr, err := k2.GetTallyResult(kvStore2, roundID2, 1, 0)
	require.NoError(t, err)
	require.NotNil(t, tr)
	require.Equal(t, uint64(100), tr.TotalValue)

	// Verify Pallas keys.
	vpk, err := k2.GetPallasKey(kvStore2, "svvaloper1abc")
	require.NoError(t, err)
	require.NotNil(t, vpk)
	require.Equal(t, bytes.Repeat([]byte{0xCC}, 32), vpk.PallasPk)

	// Verify vote manager.
	vm, err := k2.GetVoteManager(kvStore2)
	require.NoError(t, err)
	require.Equal(t, "sv15fjfr6rrs60vu4st6arrd94w5j6z7f6k0mfzpl", vm.Address)

	// Verify commitment roots.
	rootVal, err := k2.GetCommitmentRootAtHeight(kvStore2, 10)
	require.NoError(t, err)
	require.Equal(t, root10, rootVal)
	rootVal, err = k2.GetCommitmentRootAtHeight(kvStore2, 20)
	require.NoError(t, err)
	require.Equal(t, root20, rootVal)
}

func TestExportGenesisEmpty(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(t, key, tkey)
	ctx := testCtx.Ctx.WithBlockTime(time.Unix(1_000_000, 0).UTC())
	_ = ctx
	storeService := runtime.NewKVStoreService(key)
	k := keeper.NewKeeper(storeService, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	kvStore := k.OpenKVStore(ctx)

	gs, err := k.ExportGenesis(kvStore)
	require.NoError(t, err)
	require.NotNil(t, gs)
	require.NotNil(t, gs.TreeState)
	require.Equal(t, uint64(0), gs.TreeState.NextIndex)
	require.Empty(t, gs.Rounds)
	require.Empty(t, gs.CommitmentLeaves)
	require.Empty(t, gs.Nullifiers)
	require.Empty(t, gs.TallyResults)
	require.Empty(t, gs.PallasKeys)
	require.Empty(t, gs.TallyAccumulators)
	require.Empty(t, gs.ShareCounts)
	require.Empty(t, gs.CommitmentRoots)
	require.Empty(t, gs.VoteManager)
}

func TestInitGenesisNil(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(t, key, tkey)
	ctx := testCtx.Ctx
	storeService := runtime.NewKVStoreService(key)
	k := keeper.NewKeeper(storeService, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	kvStore := k.OpenKVStore(ctx)

	require.NoError(t, k.InitGenesis(kvStore, nil))

	// Ensure clean state.
	ts, err := k.GetCommitmentTreeState(kvStore)
	require.NoError(t, err)
	require.Equal(t, uint64(0), ts.NextIndex)
}
