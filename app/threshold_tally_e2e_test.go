package app_test

// TestThresholdTallyLifecycle is an end-to-end integration test of the Step 1
// threshold tally pipeline. It drives the chain from a seeded TALLYING round
// through FINALIZED using the real PrepareProposal → FinalizeBlock → Commit
// cycle, verifying that Lagrange combination correctly reconstructs vote totals.
//
// Setup: two validators (the genesis proposer + one phantom), threshold=2, n=2.
// Both validators must submit partial decryptions before the tally combiner fires.
//
// Pipeline under test:
//
//	SeedTallyingRoundThreshold
//	  → NextBlockWithPrepareProposal  (partial decrypt injector fires for proposer)
//	  → NextBlockWithPrepareProposal  (tally combiner fires, Lagrange path)
//	  → FINALIZED with correct TallyResults

import (
	"crypto/rand"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/crypto/shamir"
	"github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/types"
)

func TestThresholdTallyLifecycle(t *testing.T) {
	// Use SetupTestAppWithPallasKey so eaSkDir is set and the partial decrypt +
	// tally injectors are active.
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	// -----------------------------------------------------------------------
	// Crypto setup: (t=2, n=2) Shamir split of a fresh ea_sk.
	// -----------------------------------------------------------------------

	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	shares, _, err := shamir.Split(eaSk.Scalar, 2, 2)
	require.NoError(t, err)

	// Phantom validator — no real node, we will pre-seed its D_i directly.
	_, v2Pk := elgamal.KeyGen(rand.Reader)
	v2Addr := sdk.ValAddress([]byte("phantom-validator-2-------")).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: v2Addr, PallasPk: v2Pk.Point.ToAffineCompressed()},
	}
	vks := [][]byte{
		G.Mul(shares[0].Value).ToAffineCompressed(),
		G.Mul(shares[1].Value).ToAffineCompressed(),
	}

	// 2 proposals × 2 options = 4 accumulators.
	proposals := []*types.Proposal{
		{Id: 1, Title: "Prop 1", Options: []*types.VoteOption{
			{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"},
		}},
		{Id: 2, Title: "Prop 2", Options: []*types.VoteOption{
			{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"},
		}},
	}

	// Expected vote totals per accumulator (small values: BSGS finds them
	// immediately since all < m = ceil(sqrt(bsgsDefaultBound))).
	wantValues := map[uint64]uint64{
		uint64(1)<<32 | 0: 10, // proposal 1, decision 0
		uint64(1)<<32 | 1: 20, // proposal 1, decision 1
		uint64(2)<<32 | 0: 5,  // proposal 2, decision 0
		uint64(2)<<32 | 1: 15, // proposal 2, decision 1
	}

	// -----------------------------------------------------------------------
	// Step 1: Seed TALLYING round.
	// -----------------------------------------------------------------------

	roundID := make([]byte, 32)
	roundID[0] = 0xE0

	ta.SeedTallyingRoundThreshold(roundID, 2, proposals, validators, vks)

	// -----------------------------------------------------------------------
	// Step 2: Populate tally accumulators and pre-seed validator 2's D_i.
	//
	// We write both in a single uncommitted context then commit via NextBlock.
	// -----------------------------------------------------------------------

	ctx := ta.NewUncachedContext(false, cmtproto.Header{
		Height: ta.Height,
		Time:   ta.Time,
	})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	// Encrypt each value and accumulate; simultaneously compute D_i for v2.
	type accumInfo struct {
		proposalID uint32
		decision   uint32
		ct         *elgamal.Ciphertext
	}
	var accums []accumInfo

	for _, prop := range proposals {
		for _, opt := range prop.Options {
			accKey := uint64(prop.Id)<<32 | uint64(opt.Index)
			value := wantValues[accKey]

			ct, err := elgamal.Encrypt(eaPk, value, rand.Reader)
			require.NoError(t, err)
			ctBytes, err := elgamal.MarshalCiphertext(ct)
			require.NoError(t, err)
			require.NoError(t, ta.VoteKeeper().AddToTally(kvStore, roundID, prop.Id, opt.Index, ctBytes))

			accums = append(accums, accumInfo{prop.Id, opt.Index, ct})
		}
	}

	// Pre-seed validator 2's partial decryptions: D_{2,acc} = share_2 * C1_acc.
	var v2Entries []*types.PartialDecryptionEntry
	for _, acc := range accums {
		Di2 := acc.ct.C1.Mul(shares[1].Value)
		v2Entries = append(v2Entries, &types.PartialDecryptionEntry{
			ProposalId:     acc.proposalID,
			VoteDecision:   acc.decision,
			PartialDecrypt: Di2.ToAffineCompressed(),
		})
	}
	require.NoError(t, ta.VoteKeeper().SetPartialDecryptions(kvStore, roundID, 2, v2Entries))

	// Commit these writes.
	ta.NextBlock()

	// -----------------------------------------------------------------------
	// Step 3: Write proposer's share to disk.
	// -----------------------------------------------------------------------

	shareBytes, err := elgamal.MarshalSecretKey(&elgamal.SecretKey{Scalar: shares[0].Value})
	require.NoError(t, err)
	ta.WriteShareForRound(roundID, shareBytes)

	// -----------------------------------------------------------------------
	// Step 4: First PrepareProposal block.
	//
	// The partial decrypt injector detects:
	//   - TALLYING round with Threshold=2
	//   - Proposer is at validator_index=1 and has not yet submitted
	//   - share.<round_id> file exists
	//   - 4 non-empty accumulators
	//
	// It injects MsgSubmitPartialDecryption for the proposer. After FinalizeBlock
	// commits it, CountPartialDecryptionValidators == 2 (proposer + phantom v2).
	//
	// The tally combiner also runs this block but sees count=1 at the START
	// of PrepareProposal (the partial decrypt tx hasn't landed yet), so it skips.
	// -----------------------------------------------------------------------

	ta.NextBlockWithPrepareProposal()

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height, Time: ta.Time})
	kvStore = ta.VoteKeeper().OpenKVStore(ctx)

	count, err := ta.VoteKeeper().CountPartialDecryptionValidators(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, 2, count,
		"both validators should have submitted partial decryptions after block 1")

	// Round must still be TALLYING — tally combiner has not fired yet.
	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status,
		"round should still be TALLYING after partial decryption block")

	// -----------------------------------------------------------------------
	// Step 5: Second PrepareProposal block.
	//
	// The partial decrypt injector sees that proposer already submitted → skips.
	// The tally combiner sees count=2 >= threshold=2 → calls
	// decryptRoundTalliesThreshold, Lagrange-combines the stored D_i values,
	// runs BSGS, injects MsgSubmitTally.
	//
	// After FinalizeBlock commits it, the round is FINALIZED.
	// -----------------------------------------------------------------------

	ta.NextBlockWithPrepareProposal()

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height, Time: ta.Time})
	kvStore = ta.VoteKeeper().OpenKVStore(ctx)

	round, err = ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after tally block")

	// -----------------------------------------------------------------------
	// Step 6: Verify TallyResults match the encrypted values.
	// -----------------------------------------------------------------------

	for _, prop := range proposals {
		for _, opt := range prop.Options {
			accKey := uint64(prop.Id)<<32 | uint64(opt.Index)
			want := wantValues[accKey]

			result, err := ta.VoteKeeper().GetTallyResult(kvStore, roundID, prop.Id, opt.Index)
			require.NoError(t, err)
			require.NotNil(t, result,
				"TallyResult missing for (proposal=%d, decision=%d)", prop.Id, opt.Index)
			require.Equal(t, want, result.TotalValue,
				"wrong TotalValue for (proposal=%d, decision=%d)", prop.Id, opt.Index)
		}
	}
}

// TestThresholdTallyLifecycle_WaitsForThreshold checks the waiting behaviour:
// the tally combiner must NOT inject MsgSubmitTally until exactly t validators
// have submitted. Here we have t=2 but only validator 2's partial is pre-seeded;
// the proposer has not yet submitted. The tally combiner must skip.
func TestThresholdTallyLifecycle_WaitsForThreshold(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	shares, _, err := shamir.Split(eaSk.Scalar, 2, 2)
	require.NoError(t, err)

	_, v2Pk := elgamal.KeyGen(rand.Reader)
	v2Addr := sdk.ValAddress([]byte("phantom-validator-2-------")).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: v2Addr, PallasPk: v2Pk.Point.ToAffineCompressed()},
	}
	vks := [][]byte{
		G.Mul(shares[0].Value).ToAffineCompressed(),
		G.Mul(shares[1].Value).ToAffineCompressed(),
	}

	proposals := []*types.Proposal{
		{Id: 1, Title: "P1", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}}},
	}

	roundID := make([]byte, 32)
	roundID[0] = 0xE1

	// VoteEndTime far in the future so EndBlocker doesn't change status.
	ta.SeedTallyingRoundThreshold(roundID, 2, proposals, validators, vks)

	// Store one accumulator.
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height, Time: ta.Time})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	ct, err := elgamal.Encrypt(eaPk, 7, rand.Reader)
	require.NoError(t, err)
	ctBytes, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)
	require.NoError(t, ta.VoteKeeper().AddToTally(kvStore, roundID, 1, 0, ctBytes))
	ta.NextBlock()

	// Do NOT write a share file for the proposer → partial decrypt injector skips.
	// Do NOT pre-seed validator 2's partials → count = 0.

	// PrepareProposal: neither injector fires (count=0 < threshold=2, no share file).
	ta.NextBlockWithPrepareProposal()

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height, Time: time.Time{}})
	kvStore = ta.VoteKeeper().OpenKVStore(ctx)

	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status,
		"round must remain TALLYING when threshold is not reached")

	count, err := ta.VoteKeeper().CountPartialDecryptionValidators(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, 0, count, "no partial decryptions should have been submitted")
}
