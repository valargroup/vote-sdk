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

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
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

	// Vote-manager set.
	require.NoError(t, k.SetVoteManagers(kvStore, &types.VoteManagerSet{Addresses: []string{"sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"}}))

	// Vote rounds.
	round := &types.VoteRound{
		VoteRoundId:       roundID,
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x11}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x22}, 32),
		VoteEndTime:       2_000_000,
		NullifierImtRoot:  bytes.Repeat([]byte{0x33}, 32),
		NcRoot:            bytes.Repeat([]byte{0x44}, 32),
		Creator:           "sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3",
		Status:            types.SessionStatus_SESSION_STATUS_ACTIVE,
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

	// Commitment leaves (scoped to roundID).
	leaf0 := bytes.Repeat([]byte{0x01}, 32)
	leaf1 := bytes.Repeat([]byte{0x02}, 32)
	leaf2 := bytes.Repeat([]byte{0x03}, 32)
	idx0, err := k.AppendCommitment(kvStore, roundID, leaf0)
	require.NoError(t, err)
	require.Equal(t, uint64(0), idx0)
	idx1, err := k.AppendCommitment(kvStore, roundID, leaf1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), idx1)
	idx2, err := k.AppendCommitment(kvStore, roundID, leaf2)
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

	// Commitment roots (scoped to roundID).
	root10 := bytes.Repeat([]byte{0xDD}, 32)
	require.NoError(t, k.SetCommitmentRootAtHeight(kvStore, roundID, 10, root10))
	root20 := bytes.Repeat([]byte{0xEE}, 32)
	require.NoError(t, k.SetCommitmentRootAtHeight(kvStore, roundID, 20, root20))

	// Block leaf indices (scoped to roundID).
	require.NoError(t, k.SetBlockLeafIndex(kvStore, roundID, 10, 0, 2))
	require.NoError(t, k.SetBlockLeafIndex(kvStore, roundID, 20, 2, 1))

	// Partial decryptions (scoped to roundID).
	pdEntry1 := &types.PartialDecryptionEntry{
		ProposalId:     1,
		VoteDecision:   0,
		PartialDecrypt: bytes.Repeat([]byte{0xF1}, 32),
	}
	pdEntry2 := &types.PartialDecryptionEntry{
		ProposalId:     1,
		VoteDecision:   1,
		PartialDecrypt: bytes.Repeat([]byte{0xF2}, 32),
	}
	require.NoError(t, k.SetPartialDecryptions(kvStore, roundID, 1, []*types.PartialDecryptionEntry{pdEntry1, pdEntry2}))
	pdEntry3 := &types.PartialDecryptionEntry{
		ProposalId:     1,
		VoteDecision:   0,
		PartialDecrypt: bytes.Repeat([]byte{0xF3}, 32),
	}
	require.NoError(t, k.SetPartialDecryptions(kvStore, roundID, 2, []*types.PartialDecryptionEntry{pdEntry3}))

	// --- Export ---
	gs, err := k.ExportGenesis(kvStore)
	require.NoError(t, err)

	// Verify export contents.
	require.Equal(t, []string{"sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"}, gs.VoteManagerAddresses)
	require.Len(t, gs.Rounds, 2)
	require.Len(t, gs.Nullifiers, 3)
	require.Len(t, gs.TallyResults, 1)
	require.Len(t, gs.PallasKeys, 1)
	require.Len(t, gs.TallyAccumulators, 1)
	require.Len(t, gs.ShareCounts, 1)
	require.Len(t, gs.PartialDecryptions, 3)

	// Verify per-round tree export (only roundID has tree data).
	require.Len(t, gs.RoundTrees, 1)
	rt := gs.RoundTrees[0]
	require.Equal(t, roundID, rt.VoteRoundId)
	require.NotNil(t, rt.TreeState)
	require.Equal(t, uint64(3), rt.TreeState.NextIndex)
	require.Len(t, rt.CommitmentLeaves, 3)
	require.Len(t, rt.CommitmentRoots, 2)
	require.Len(t, rt.BlockLeafIndices, 2)

	// Verify leaf ordering.
	require.Equal(t, uint64(0), rt.CommitmentLeaves[0].Index)
	require.Equal(t, uint64(1), rt.CommitmentLeaves[1].Index)
	require.Equal(t, uint64(2), rt.CommitmentLeaves[2].Index)
	require.Equal(t, leaf0, rt.CommitmentLeaves[0].Value)
	require.Equal(t, leaf1, rt.CommitmentLeaves[1].Value)
	require.Equal(t, leaf2, rt.CommitmentLeaves[2].Value)

	// Verify share count value.
	require.Equal(t, uint64(2), gs.ShareCounts[0].Count)

	// Verify tally result.
	require.Equal(t, uint64(100), gs.TallyResults[0].TotalValue)

	// Verify commitment roots (in per-round export).
	require.Equal(t, uint64(10), rt.CommitmentRoots[0].Height)
	require.Equal(t, root10, rt.CommitmentRoots[0].Root)
	require.Equal(t, uint64(20), rt.CommitmentRoots[1].Height)
	require.Equal(t, root20, rt.CommitmentRoots[1].Root)

	// Verify block leaf indices (in per-round export).
	require.Equal(t, uint64(10), rt.BlockLeafIndices[0].Height)
	require.Equal(t, uint64(0), rt.BlockLeafIndices[0].StartIndex)
	require.Equal(t, uint64(2), rt.BlockLeafIndices[0].Count)
	require.Equal(t, uint64(20), rt.BlockLeafIndices[1].Height)
	require.Equal(t, uint64(2), rt.BlockLeafIndices[1].StartIndex)
	require.Equal(t, uint64(1), rt.BlockLeafIndices[1].Count)

	// Verify partial decryptions export.
	require.Equal(t, roundID, gs.PartialDecryptions[0].RoundId)
	require.Equal(t, uint32(1), gs.PartialDecryptions[0].ValidatorIndex)
	require.Equal(t, bytes.Repeat([]byte{0xF1}, 32), gs.PartialDecryptions[0].PartialDecrypt)
	require.Equal(t, uint32(2), gs.PartialDecryptions[2].ValidatorIndex)
	require.Equal(t, bytes.Repeat([]byte{0xF3}, 32), gs.PartialDecryptions[2].PartialDecrypt)

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

	// Verify per-round tree state.
	ts, err := k2.GetCommitmentTreeState(kvStore2, roundID)
	require.NoError(t, err)
	require.Equal(t, uint64(3), ts.NextIndex)
	// Height must be zero after genesis import so ensureRoundTreeLoaded takes
	// the first-boot replay path (shard blobs are not carried in genesis).
	require.Equal(t, uint64(0), ts.Height, "Height must be 0 after genesis import to force tree rebuild from leaves")

	// Verify commitment leaves (per-round).
	bz, err := kvStore2.Get(types.CommitmentLeafKey(roundID, 0))
	require.NoError(t, err)
	require.Equal(t, leaf0, bz)
	bz, err = kvStore2.Get(types.CommitmentLeafKey(roundID, 2))
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

	// Verify Pallas key reverse-lookup index is populated after genesis import.
	owner, err := k2.GetPallasKeyOwner(kvStore2, bytes.Repeat([]byte{0xCC}, 32))
	require.NoError(t, err)
	require.Equal(t, "svvaloper1abc", owner)

	// Verify vote-manager set.
	vms, err := k2.GetVoteManagers(kvStore2)
	require.NoError(t, err)
	require.Equal(t, []string{"sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"}, vms.Addresses)

	// Verify commitment roots (per-round).
	rootVal, err := k2.GetCommitmentRootAtHeight(kvStore2, roundID, 10)
	require.NoError(t, err)
	require.Equal(t, root10, rootVal)
	rootVal, err = k2.GetCommitmentRootAtHeight(kvStore2, roundID, 20)
	require.NoError(t, err)
	require.Equal(t, root20, rootVal)

	// Verify block leaf indices (per-round).
	startIdx, cnt, found, err := k2.GetBlockLeafIndex(kvStore2, roundID, 10)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(0), startIdx)
	require.Equal(t, uint64(2), cnt)
	startIdx, cnt, found, err = k2.GetBlockLeafIndex(kvStore2, roundID, 20)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint64(2), startIdx)
	require.Equal(t, uint64(1), cnt)
	// Negative check: no mapping at height 30.
	_, _, found, err = k2.GetBlockLeafIndex(kvStore2, roundID, 30)
	require.NoError(t, err)
	require.False(t, found)

	// Verify partial decryptions.
	pd, err := k2.GetPartialDecryption(kvStore2, roundID, 1, 1, 0)
	require.NoError(t, err)
	require.NotNil(t, pd)
	require.Equal(t, bytes.Repeat([]byte{0xF1}, 32), pd.PartialDecrypt)
	pd, err = k2.GetPartialDecryption(kvStore2, roundID, 1, 1, 1)
	require.NoError(t, err)
	require.NotNil(t, pd)
	require.Equal(t, bytes.Repeat([]byte{0xF2}, 32), pd.PartialDecrypt)
	pd, err = k2.GetPartialDecryption(kvStore2, roundID, 2, 1, 0)
	require.NoError(t, err)
	require.NotNil(t, pd)
	require.Equal(t, bytes.Repeat([]byte{0xF3}, 32), pd.PartialDecrypt)
	// Negative check: non-existent partial decryption.
	pd, err = k2.GetPartialDecryption(kvStore2, roundID, 3, 1, 0)
	require.NoError(t, err)
	require.Nil(t, pd)
}

// TestInitGenesisClearsTreeHeight verifies that InitGenesis forces Height = 0
// when NextIndex > 0. This prevents ensureRoundTreeLoaded from taking the
// restart branch (which expects shard/cap/checkpoint blobs that genesis does
// not carry), avoiding silent root corruption after chain migration.
func TestInitGenesisClearsTreeHeight(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(t, key, tkey)
	ctx := testCtx.Ctx.WithBlockTime(time.Unix(1_000_000, 0).UTC())
	storeService := runtime.NewKVStoreService(key)
	k := keeper.NewKeeper(storeService, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	kvStore := k.OpenKVStore(ctx)

	roundID := bytes.Repeat([]byte{0xDD}, 32)
	leaf := bytes.Repeat([]byte{0x01}, 32)

	gs := &types.GenesisState{
		MinCeremonyValidators: 1,
		Rounds: []*types.VoteRound{{
			VoteRoundId: roundID,
			Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
		}},
		RoundTrees: []*types.GenesisRoundTree{{
			VoteRoundId: roundID,
			TreeState: &types.CommitmentTreeState{
				NextIndex: 5,
				Height:    999, // simulates exported state with Height > 0
				Root:      bytes.Repeat([]byte{0xAA}, 32),
			},
			CommitmentLeaves: []*types.CommitmentLeaf{
				{Index: 0, Value: leaf},
			},
		}},
	}

	require.NoError(t, k.InitGenesis(kvStore, gs))

	ts, err := k.GetCommitmentTreeState(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, uint64(5), ts.NextIndex, "NextIndex must be preserved")
	require.Equal(t, uint64(0), ts.Height, "Height must be zeroed to force first-boot replay")
	require.Equal(t, bytes.Repeat([]byte{0xAA}, 32), ts.Root, "Root must be preserved for reference")
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
	require.Empty(t, gs.Rounds)
	require.Empty(t, gs.RoundTrees)
	require.Empty(t, gs.Nullifiers)
	require.Empty(t, gs.TallyResults)
	require.Empty(t, gs.PallasKeys)
	require.Empty(t, gs.TallyAccumulators)
	require.Empty(t, gs.ShareCounts)
	require.Empty(t, gs.PartialDecryptions)
	require.Empty(t, gs.VoteManagerAddresses)
}

// TestInitGenesisPopulatesPallasKeyReverseIndex verifies that InitGenesis
// populates the PK -> validator reverse-lookup index so that duplicate PK
// registrations are rejected after chain import (H-2 fix).
func TestInitGenesisPopulatesPallasKeyReverseIndex(t *testing.T) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(t, key, tkey)
	ctx := testCtx.Ctx.WithBlockTime(time.Unix(1_000_000, 0).UTC())
	storeService := runtime.NewKVStoreService(key)
	k := keeper.NewKeeper(storeService, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	kvStore := k.OpenKVStore(ctx)

	pk := bytes.Repeat([]byte{0xDD}, 32)
	gs := &types.GenesisState{
		MinCeremonyValidators: 1,
		PallasKeys: []*types.ValidatorPallasKey{
			{ValidatorAddress: "svvaloper1first", PallasPk: pk},
		},
	}

	require.NoError(t, k.InitGenesis(kvStore, gs))

	// Forward lookup works.
	vpk, err := k.GetPallasKey(kvStore, "svvaloper1first")
	require.NoError(t, err)
	require.NotNil(t, vpk)
	require.Equal(t, pk, vpk.PallasPk)

	// Reverse lookup works.
	owner, err := k.GetPallasKeyOwner(kvStore, pk)
	require.NoError(t, err)
	require.Equal(t, "svvaloper1first", owner)

	// A second validator trying to register the same PK must be rejected.
	err = k.RegisterPallasKeyCore(kvStore, "svvaloper1second", pk)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pallas key already registered by another validator")
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

	// Ensure clean state — any roundID should return default empty state.
	dummyRound := bytes.Repeat([]byte{0xFF}, 32)
	ts, err := k.GetCommitmentTreeState(kvStore, dummyRound)
	require.NoError(t, err)
	require.Equal(t, uint64(0), ts.NextIndex)
}
