package app_test

import (
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"testing"
	"unsafe"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"github.com/z-cale/zally/crypto/votetree"
	"github.com/z-cale/zally/testutil"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

type treeSnapshot struct {
	Height    int64
	NextIndex uint64
	RootHex   string
}

func stressBatchSize() int {
	v := os.Getenv("TREE_STRESS_N")
	if v == "" {
		return 400
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 400
	}
	return n
}

func setupStressRound(t *testing.T) (*testutil.TestApp, []byte) {
	t.Helper()
	app := testutil.SetupTestApp(t)
	roundID := app.SeedVotingSession(testutil.ValidCreateVotingSessionAt(app.Time))
	return app, roundID
}

func queryTreeState(t *testing.T, app *testutil.TestApp) *types.CommitmentTreeState {
	t.Helper()
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	state, err := app.VoteKeeper().GetCommitmentTreeState(kvStore)
	require.NoError(t, err)
	return state
}

func queryRootAtHeight(t *testing.T, app *testutil.TestApp, height uint64) []byte {
	t.Helper()
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	root, err := app.VoteKeeper().GetCommitmentRootAtHeight(kvStore, height)
	require.NoError(t, err)
	return root
}

func assertTreeConsistency(t *testing.T, app *testutil.TestApp, expectedLeaves uint64) {
	t.Helper()
	state := queryTreeState(t, app)
	require.Equal(t, expectedLeaves, state.NextIndex)
	if expectedLeaves > 0 {
		require.NotEmpty(t, state.Root)
		require.Greater(t, state.Height, uint64(0))
	}
}

func assertNoPartialAppendOnFailure(t *testing.T, app *testutil.TestApp, before uint64) {
	t.Helper()
	state := queryTreeState(t, app)
	require.Equal(t, before, state.NextIndex)
}

func encodeVoteMessages(msgs []types.VoteMessage) [][]byte {
	out := make([][]byte, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, testutil.MustEncodeVoteTx(msg))
	}
	return out
}

func countSuccess(results []*abci.ExecTxResult) int {
	ok := 0
	for _, r := range results {
		if r.Code == 0 {
			ok++
		}
	}
	return ok
}

func loadAllLeaves(t *testing.T, app *testutil.TestApp, nextIndex uint64) [][]byte {
	t.Helper()
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)

	leaves := make([][]byte, nextIndex)
	for i := uint64(0); i < nextIndex; i++ {
		leaf, err := kvStore.Get(types.CommitmentLeafKey(i))
		require.NoError(t, err)
		require.NotEmpty(t, leaf, "missing leaf at index %d", i)
		leaves[i] = leaf
	}
	return leaves
}

func forceResetTreeHandle(t *testing.T, k *votekeeper.Keeper) {
	t.Helper()
	rv := reflect.ValueOf(k).Elem().FieldByName("treeHandle")
	require.True(t, rv.IsValid(), "keeper.treeHandle field missing")
	ptr := unsafe.Pointer(rv.UnsafeAddr())
	reflect.NewAt(rv.Type(), ptr).Elem().Set(reflect.Zero(rv.Type()))
}

func TestTreeStress_HighVolume(t *testing.T) {
	app, roundID := setupStressRound(t)
	n := stressBatchSize()

	// Block 1: high-volume delegation append.
	delegations := testutil.ValidDelegationN(roundID, n, 10)
	delegationMsgs := make([]types.VoteMessage, 0, len(delegations))
	for _, m := range delegations {
		delegationMsgs = append(delegationMsgs, m)
	}
	results := app.DeliverVoteTxs(encodeVoteMessages(delegationMsgs))
	require.Len(t, results, n)
	require.Equal(t, n, countSuccess(results))
	assertTreeConsistency(t, app, uint64(n))

	// Block 2: high-volume cast-vote append (2 leaves per success).
	anchor := uint64(app.Height)
	casts := testutil.ValidCastVoteN(roundID, anchor, n, 10000)
	castMsgs := make([]types.VoteMessage, 0, len(casts))
	for _, m := range casts {
		castMsgs = append(castMsgs, m)
	}
	results = app.DeliverVoteTxs(encodeVoteMessages(castMsgs))
	require.Len(t, results, n)
	require.Equal(t, n, countSuccess(results))
	assertTreeConsistency(t, app, uint64(3*n))

	// Block 3: another mixed growth pass.
	anchor = uint64(app.Height)
	delegations2 := testutil.ValidDelegationN(roundID, n/2, 20000)
	casts2 := testutil.ValidCastVoteN(roundID, anchor, n/2, 30000)

	var mixed []types.VoteMessage
	for _, m := range delegations2 {
		mixed = append(mixed, m)
	}
	for _, m := range casts2 {
		mixed = append(mixed, m)
	}
	results = app.DeliverVoteTxs(encodeVoteMessages(mixed))
	require.Len(t, results, n)
	require.Equal(t, n, countSuccess(results))
	assertTreeConsistency(t, app, uint64(3*n+3*(n/2)))
}

func runOrderingScenario(t *testing.T, seed uint64) ([]treeSnapshot, [][]uint32) {
	t.Helper()
	app, roundID := setupStressRound(t)

	var snapshots []treeSnapshot
	var blockCodes [][]uint32

	record := func(results []*abci.ExecTxResult) {
		state := queryTreeState(t, app)
		codes := make([]uint32, 0, len(results))
		for _, r := range results {
			codes = append(codes, r.Code)
		}
		blockCodes = append(blockCodes, codes)
		snapshots = append(snapshots, treeSnapshot{
			Height:    app.Height,
			NextIndex: state.NextIndex,
			RootHex:   hex.EncodeToString(state.Root),
		})
	}

	// Block 1: pure delegation batch.
	var block1 []types.VoteMessage
	for _, m := range testutil.ValidDelegationN(roundID, 40, seed+1) {
		block1 = append(block1, m)
	}
	record(app.DeliverVoteTxs(encodeVoteMessages(block1)))

	// Block 2: mixed txs with deliberate gov-nullifier conflict.
	anchor1 := uint64(app.Height)
	conflict1 := testutil.BuildConflictingNullifierSet(roundID, anchor1, seed+100)

	var block2 []types.VoteMessage
	block2 = append(block2, conflict1.GovWinner, conflict1.GovLoser, conflict1.GovFresh)
	for _, m := range testutil.ValidCastVoteN(roundID, anchor1, 20, seed+200) {
		block2 = append(block2, m)
	}
	for _, m := range testutil.ValidDelegationN(roundID, 12, seed+300) {
		block2 = append(block2, m)
	}
	block2 = testutil.ShuffleWithSeed(block2, seed+5000)
	record(app.DeliverVoteTxs(encodeVoteMessages(block2)))

	// Block 3: mixed txs with deliberate VAN-nullifier conflict.
	anchor2 := uint64(app.Height)
	conflict2 := testutil.BuildConflictingNullifierSet(roundID, anchor2, seed+101)

	var block3 []types.VoteMessage
	block3 = append(block3, conflict2.VanWinner, conflict2.VanLoser, conflict2.VanFresh)
	for _, m := range testutil.ValidCastVoteN(roundID, anchor2, 18, seed+400) {
		block3 = append(block3, m)
	}
	for _, m := range testutil.ValidDelegationN(roundID, 10, seed+500) {
		block3 = append(block3, m)
	}
	block3 = testutil.ShuffleWithSeed(block3, seed+9000)
	record(app.DeliverVoteTxs(encodeVoteMessages(block3)))

	return snapshots, blockCodes
}

func TestTreeStress_OrderingDeterminism(t *testing.T) {
	traceA, codesA := runOrderingScenario(t, 424242)
	traceB, codesB := runOrderingScenario(t, 424242)

	require.Equal(t, codesA, codesB, "tx success/failure pattern must be deterministic")
	require.Equal(t, traceA, traceB, "root/next_index trace must be deterministic")
}

func TestTreeStress_NullifierRace(t *testing.T) {
	app, roundID := setupStressRound(t)

	// Prime one root height for cast-vote anchors.
	prime := testutil.ValidDelegationN(roundID, 1, 7000)
	result := app.DeliverVoteTx(testutil.MustEncodeVoteTx(prime[0]))
	require.Equal(t, uint32(0), result.Code, result.Log)
	beforeGov := queryTreeState(t, app).NextIndex
	anchor := uint64(app.Height)

	conflict := testutil.BuildConflictingNullifierSet(roundID, anchor, 9000)

	// Same-block gov nullifier conflict.
	govBlock := []types.VoteMessage{conflict.GovWinner, conflict.GovLoser, conflict.GovFresh}
	govResults := app.DeliverVoteTxs(encodeVoteMessages(govBlock))
	require.Equal(t, uint32(0), govResults[0].Code)
	require.NotEqual(t, uint32(0), govResults[1].Code)
	require.Equal(t, uint32(0), govResults[2].Code)
	afterGov := queryTreeState(t, app).NextIndex
	require.Equal(t, beforeGov+2, afterGov, "only successful delegation txs append one leaf each")

	// Same-block VAN nullifier conflict.
	beforeVan := afterGov
	anchor = uint64(app.Height)
	conflict = testutil.BuildConflictingNullifierSet(roundID, anchor, 10000)
	vanBlock := []types.VoteMessage{conflict.VanWinner, conflict.VanLoser, conflict.VanFresh}
	vanResults := app.DeliverVoteTxs(encodeVoteMessages(vanBlock))
	require.Equal(t, uint32(0), vanResults[0].Code)
	require.NotEqual(t, uint32(0), vanResults[1].Code)
	require.Equal(t, uint32(0), vanResults[2].Code)
	afterVan := queryTreeState(t, app).NextIndex
	require.Equal(t, beforeVan+4, afterVan, "only successful cast-vote txs append two leaves each")
}

func TestTreeStress_ColdStartRebuild(t *testing.T) {
	app, roundID := setupStressRound(t)

	// Grow the tree across multiple blocks.
	for block := 0; block < 4; block++ {
		var msgs []types.VoteMessage
		for _, m := range testutil.ValidDelegationN(roundID, 15, uint64(20000+block*100)) {
			msgs = append(msgs, m)
		}
		results := app.DeliverVoteTxs(encodeVoteMessages(msgs))
		require.Equal(t, len(msgs), countSuccess(results))
	}

	state := queryTreeState(t, app)
	require.Greater(t, state.NextIndex, uint64(0))
	require.NotEmpty(t, state.Root)

	// Full replay validation from KV leaves (cold rebuild equivalence).
	leaves := loadAllLeaves(t, app, state.NextIndex)
	require.NoError(t, votetree.VerifyRootFromLeaves(leaves, state.Root))

	// Simulate restart by resetting keeper.treeHandle to nil, then recompute.
	forceResetTreeHandle(t, app.VoteKeeper())
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	recomputed, err := app.VoteKeeper().ComputeTreeRoot(kvStore, state.NextIndex, uint64(app.Height))
	require.NoError(t, err)
	require.Equal(t, state.Root, recomputed)

	// Verify post-restart delta append still works.
	more := testutil.ValidDelegationN(roundID, 3, 90000)
	var msgs []types.VoteMessage
	for _, m := range more {
		msgs = append(msgs, m)
	}
	results := app.DeliverVoteTxs(encodeVoteMessages(msgs))
	require.Equal(t, len(msgs), countSuccess(results))
	newState := queryTreeState(t, app)
	require.Equal(t, state.NextIndex+3, newState.NextIndex)
	require.NotEqual(t, hex.EncodeToString(state.Root), hex.EncodeToString(newState.Root))
}

func TestTreeStress_AnchorStaleness(t *testing.T) {
	app, roundID := setupStressRound(t)

	// Produce a valid anchor.
	okDeleg := testutil.ValidDelegationN(roundID, 1, 110000)[0]
	res := app.DeliverVoteTx(testutil.MustEncodeVoteTx(okDeleg))
	require.Equal(t, uint32(0), res.Code, res.Log)
	oldAnchor := uint64(app.Height)

	// Move tree forward so oldAnchor is stale-but-still-valid.
	moreDeleg := testutil.ValidDelegationN(roundID, 2, 120000)
	var msgs []types.VoteMessage
	for _, m := range moreDeleg {
		msgs = append(msgs, m)
	}
	results := app.DeliverVoteTxs(encodeVoteMessages(msgs))
	require.Equal(t, len(msgs), countSuccess(results))

	// Valid stale anchor succeeds.
	before := queryTreeState(t, app).NextIndex
	validOld := testutil.ValidCastVoteN(roundID, oldAnchor, 1, 130000)[0]
	res = app.DeliverVoteTx(testutil.MustEncodeVoteTx(validOld))
	require.Equal(t, uint32(0), res.Code, res.Log)
	after := queryTreeState(t, app).NextIndex
	require.Equal(t, before+2, after)

	// Non-existent old anchor fails and must not append.
	before = after
	invalidOld := testutil.ValidCastVoteN(roundID, oldAnchor-1, 1, 130100)[0]
	res = app.DeliverVoteTx(testutil.MustEncodeVoteTx(invalidOld))
	require.NotEqual(t, uint32(0), res.Code)
	assertNoPartialAppendOnFailure(t, app, before)

	// Future anchor fails and must not append.
	before = queryTreeState(t, app).NextIndex
	futureAnchor := uint64(app.Height) + 1000
	invalidFuture := testutil.ValidCastVoteN(roundID, futureAnchor, 1, 130200)[0]
	res = app.DeliverVoteTx(testutil.MustEncodeVoteTx(invalidFuture))
	require.NotEqual(t, uint32(0), res.Code)
	assertNoPartialAppendOnFailure(t, app, before)
}

func TestTreeStress_EmptyBlocks(t *testing.T) {
	app, roundID := setupStressRound(t)

	// Create one root-bearing block.
	deleg := testutil.ValidDelegationN(roundID, 1, 140000)[0]
	res := app.DeliverVoteTx(testutil.MustEncodeVoteTx(deleg))
	require.Equal(t, uint32(0), res.Code, res.Log)
	h1 := uint64(app.Height)
	root1 := queryRootAtHeight(t, app, h1)
	require.NotEmpty(t, root1)

	// Advance empty blocks and verify no new root snapshots.
	app.NextBlock()
	h2 := uint64(app.Height)
	require.Nil(t, queryRootAtHeight(t, app, h2))

	app.NextBlock()
	h3 := uint64(app.Height)
	require.Nil(t, queryRootAtHeight(t, app, h3))

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	_, _, found2, err := app.VoteKeeper().GetBlockLeafIndex(kvStore, h2)
	require.NoError(t, err)
	require.False(t, found2, "empty block should not have BlockLeafIndex entry")
	_, _, found3, err := app.VoteKeeper().GetBlockLeafIndex(kvStore, h3)
	require.NoError(t, err)
	require.False(t, found3, "empty block should not have BlockLeafIndex entry")

	// Previous root remains queryable.
	require.Equal(t, root1, queryRootAtHeight(t, app, h1))
}

func TestTreeStress_Interleaved(t *testing.T) {
	app, roundID := setupStressRound(t)
	cycles := 120
	expectedLeaves := uint64(0)

	for i := 0; i < cycles; i++ {
		deleg := testutil.ValidDelegationN(roundID, 1, uint64(150000+i*10))[0]
		res := app.DeliverVoteTx(testutil.MustEncodeVoteTx(deleg))
		require.Equal(t, uint32(0), res.Code, "delegation cycle %d failed: %s", i, res.Log)
		expectedLeaves++

		anchor := uint64(app.Height)
		cast := testutil.ValidCastVoteN(roundID, anchor, 1, uint64(160000+i*10))[0]
		res = app.DeliverVoteTx(testutil.MustEncodeVoteTx(cast))
		require.Equal(t, uint32(0), res.Code, "cast cycle %d failed: %s", i, res.Log)
		expectedLeaves += 2

		assertTreeConsistency(t, app, expectedLeaves)
	}
}

func TestTreeStress_RecheckTxChurn(t *testing.T) {
	app, roundID := setupStressRound(t)

	// Prime one valid anchor for cast-vote conflict fixtures.
	prime := testutil.ValidDelegationN(roundID, 1, 170000)[0]
	primeRes := app.DeliverVoteTx(testutil.MustEncodeVoteTx(prime))
	require.Equal(t, uint32(0), primeRes.Code)
	anchor := uint64(app.Height)

	conflict := testutil.BuildConflictingNullifierSet(roundID, anchor, 171000)

	// CheckTx (new) accepts both before any commit.
	checkWinner := app.CheckTxSync(testutil.MustEncodeVoteTx(conflict.GovWinner))
	require.Equal(t, uint32(0), checkWinner.Code, checkWinner.Log)
	checkLoser := app.CheckTxSync(testutil.MustEncodeVoteTx(conflict.GovLoser))
	require.Equal(t, uint32(0), checkLoser.Code, checkLoser.Log)

	before := queryTreeState(t, app).NextIndex

	// Commit the winner, then ensure recheck rejects loser.
	deliverWinner := app.DeliverVoteTx(testutil.MustEncodeVoteTx(conflict.GovWinner))
	require.Equal(t, uint32(0), deliverWinner.Code, deliverWinner.Log)
	recheckLoser := app.RecheckTxSync(testutil.MustEncodeVoteTx(conflict.GovLoser))
	require.NotEqual(t, uint32(0), recheckLoser.Code)
	require.Contains(t, recheckLoser.Log, "nullifier already spent")

	// Delivering loser now must fail and must not append.
	deliverLoser := app.DeliverVoteTx(testutil.MustEncodeVoteTx(conflict.GovLoser))
	require.NotEqual(t, uint32(0), deliverLoser.Code)
	require.Equal(t, before+1, queryTreeState(t, app).NextIndex)

	// Repeat churn check on VAN nullifiers.
	anchor = queryTreeState(t, app).Height
	conflict = testutil.BuildConflictingNullifierSet(roundID, anchor, 172000)
	checkVanWinner := app.CheckTxSync(testutil.MustEncodeVoteTx(conflict.VanWinner))
	require.Equal(t, uint32(0), checkVanWinner.Code, checkVanWinner.Log)
	checkVanLoser := app.CheckTxSync(testutil.MustEncodeVoteTx(conflict.VanLoser))
	require.Equal(t, uint32(0), checkVanLoser.Code, checkVanLoser.Log)

	beforeVan := queryTreeState(t, app).NextIndex
	deliverVanWinner := app.DeliverVoteTx(testutil.MustEncodeVoteTx(conflict.VanWinner))
	require.Equal(t, uint32(0), deliverVanWinner.Code, deliverVanWinner.Log)
	recheckVanLoser := app.RecheckTxSync(testutil.MustEncodeVoteTx(conflict.VanLoser))
	require.NotEqual(t, uint32(0), recheckVanLoser.Code)
	require.Contains(t, recheckVanLoser.Log, "nullifier already spent")

	deliverVanLoser := app.DeliverVoteTx(testutil.MustEncodeVoteTx(conflict.VanLoser))
	require.NotEqual(t, uint32(0), deliverVanLoser.Code)
	require.Equal(t, beforeVan+2, queryTreeState(t, app).NextIndex)
}

func TestTreeStress_ReproducibilityAcrossRuns(t *testing.T) {
	traceA, _ := runOrderingScenario(t, 987654321)
	traceB, _ := runOrderingScenario(t, 987654321)
	require.Equal(t, traceA, traceB, "same seed/config should produce identical root/index trace")

	if len(traceA) == 0 {
		t.Fatal("trace must not be empty")
	}
	last := traceA[len(traceA)-1]
	require.NotEmpty(t, last.RootHex, fmt.Sprintf("empty root at height %d", last.Height))
}
