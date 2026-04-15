package app_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	abci "github.com/cometbft/cometbft/abci/types"

	"github.com/mikelodder7/curvey"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/crypto/ecies"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/testutil"
	votekeeper "github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestABCILatencies measures wall-clock latencies for each ABCI phase across
// the full voting lifecycle. Run with:
//
//	go test -count=1 -run TestABCILatencies -v -timeout 10m ./app/...
//
// The test drives a single-validator chain through:
//
//	DKG → ack → voting (N delegations + N cast votes + N reveals) → EndBlock tally transition
//	→ partial decrypt → tally decryption (BSGS) → finalized
//
// and prints per-phase timings.
func TestABCILatencies(t *testing.T) {
	const numVotes = 50

	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	proposerAddr := app.ValidatorOperAddr()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed(), ShamirIndex: 1},
	}
	voteEndTime := app.Time.Add(120 * time.Second)

	roundID := make([]byte, 32)
	roundID[0] = 0xBE

	// Seed a REGISTERING round.
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round := &types.VoteRound{
		VoteRoundId:        roundID,
		Status:             types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: validators,
		VoteEndTime:        uint64(voteEndTime.Unix()),
		Proposals:          testutil.SampleProposals(),
		NullifierImtRoot:   bytes.Repeat([]byte{0x01}, 32),
		NcRoot:             bytes.Repeat([]byte{0x02}, 32),
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// -----------------------------------------------------------------------
	// Phase 1: DKG contribution via PrepareProposal
	// -----------------------------------------------------------------------
	start := time.Now()
	app.NextBlockWithPrepareProposal()
	t.Logf("DKG contribute (PrepareProposal + FinalizeBlock + Commit): %s", time.Since(start))

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)

	// -----------------------------------------------------------------------
	// Phase 2: Auto-ack via PrepareProposal → ACTIVE
	// -----------------------------------------------------------------------
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	t.Logf("Ceremony ack (PrepareProposal + FinalizeBlock + Commit):   %s", time.Since(start))

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)

	eaPk, err := elgamal.UnmarshalPublicKey(round.EaPk)
	require.NoError(t, err)

	// -----------------------------------------------------------------------
	// Phase 3: Voting — batch delegations
	// -----------------------------------------------------------------------
	delegations := testutil.ValidDelegationN(roundID, numVotes, 1000)
	delegationTxs := make([][]byte, len(delegations))
	for i, d := range delegations {
		delegationTxs[i] = testutil.MustEncodeVoteTx(d)
	}

	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          app.Height,
		Time:            app.Time,
		Txs:             delegationTxs,
		ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	finalizeTime := time.Since(start)

	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "delegation %d failed: %s", i, r.Log)
	}

	start = time.Now()
	_, err = app.Commit()
	require.NoError(t, err)
	commitTime := time.Since(start)

	t.Logf("Deliver %d delegations:  FinalizeBlock=%s  Commit=%s", numVotes, finalizeTime, commitTime)

	anchorHeight := uint64(app.Height)

	// -----------------------------------------------------------------------
	// Phase 4: Cast votes
	// -----------------------------------------------------------------------
	castVotes := testutil.ValidCastVoteN(roundID, anchorHeight, numVotes, 5000)
	castTxs := make([][]byte, len(castVotes))
	for i, cv := range castVotes {
		castTxs[i] = testutil.MustEncodeVoteTx(cv)
	}

	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          app.Height,
		Time:            app.Time,
		Txs:             castTxs,
		ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	finalizeTime = time.Since(start)

	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "cast vote %d failed: %s", i, r.Log)
	}

	start = time.Now()
	_, err = app.Commit()
	require.NoError(t, err)
	commitTime = time.Since(start)

	t.Logf("Deliver %d cast votes:   FinalizeBlock=%s  Commit=%s", numVotes, finalizeTime, commitTime)

	// -----------------------------------------------------------------------
	// Phase 5: Reveal shares (real ElGamal encryptions)
	// -----------------------------------------------------------------------
	revealTxs := make([][]byte, numVotes)
	for i := range numVotes {
		value := uint64(1 + i%100)
		ct, err := elgamal.Encrypt(eaPk, value, rand.Reader)
		require.NoError(t, err)
		encShare, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)

		msg := testutil.ValidRevealShareReal(roundID, anchorHeight, byte(0x50+i),
			1, 1, encShare)
		revealTxs[i] = testutil.MustEncodeVoteTx(msg)
	}

	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          app.Height,
		Time:            app.Time,
		Txs:             revealTxs,
		ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	finalizeTime = time.Since(start)

	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "reveal %d failed: %s", i, r.Log)
	}

	start = time.Now()
	_, err = app.Commit()
	require.NoError(t, err)
	commitTime = time.Since(start)

	t.Logf("Deliver %d reveals:      FinalizeBlock=%s  Commit=%s", numVotes, finalizeTime, commitTime)

	// -----------------------------------------------------------------------
	// Phase 6: EndBlocker transitions round to TALLYING
	// -----------------------------------------------------------------------
	start = time.Now()
	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))
	t.Logf("EndBlocker ACTIVE→TALLYING transition:                     %s", time.Since(start))

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// -----------------------------------------------------------------------
	// Phase 7: Partial decrypt injection via PrepareProposal
	// -----------------------------------------------------------------------
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	pdTime := time.Since(start)
	t.Logf("Partial decrypt (PrepareProposal + FinalizeBlock + Commit): %s", pdTime)

	// -----------------------------------------------------------------------
	// Phase 8: Tally decryption (Lagrange + BSGS) via PrepareProposal
	// -----------------------------------------------------------------------
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	tallyTime := time.Since(start)
	t.Logf("Tally decryption (PrepareProposal + FinalizeBlock + Commit): %s", tallyTime)

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)

	// -----------------------------------------------------------------------
	// Summary
	// -----------------------------------------------------------------------
	t.Log("")
	t.Log("=== Latency Summary ===")
	t.Logf("  Partial decrypt block:  %s", pdTime)
	t.Logf("  Tally decryption block: %s", tallyTime)
	t.Log("")
	t.Logf("  timeout_propose budget: 2.5s")
	if tallyTime > 2500*time.Millisecond {
		t.Logf("  ⚠ Tally block (%s) EXCEEDS timeout_propose (2.5s)", tallyTime)
	} else {
		t.Logf("  ✓ Tally block fits within timeout_propose budget")
	}
}

// TestABCILatencies_MultiValidator measures latencies with n=3, t=2 threshold
// (the more realistic production scenario). Run with:
//
//	go test -count=1 -run TestABCILatencies_MultiValidator -v -timeout 10m ./app/...
func TestABCILatencies_MultiValidator(t *testing.T) {
	const numVotes = 50

	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	G := elgamal.PallasGenerator()
	proposerAddr := app.ValidatorOperAddr()

	phantom1Sk, phantom1Pk := elgamal.KeyGen(rand.Reader)
	_, phantom2Pk := elgamal.KeyGen(rand.Reader)

	phantom1Addr := sdk.ValAddress(bytes.Repeat([]byte{0xD1}, 20)).String()
	phantom2Addr := sdk.ValAddress(bytes.Repeat([]byte{0xD2}, 20)).String()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed(), ShamirIndex: 1},
		{ValidatorAddress: phantom1Addr, PallasPk: phantom1Pk.Point.ToAffineCompressed(), ShamirIndex: 2},
		{ValidatorAddress: phantom2Addr, PallasPk: phantom2Pk.Point.ToAffineCompressed(), ShamirIndex: 3},
	}
	voteEndTime := app.Time.Add(120 * time.Second)

	roundID := make([]byte, 32)
	roundID[0] = 0xBF

	n := 3
	tVal := 2

	// Generate phantom DKG contributions.
	phantom1Secret := new(curvey.ScalarPallas).Random(rand.Reader)
	phantom1Shares, phantom1Coeffs, err := shamir.Split(phantom1Secret, tVal, n)
	require.NoError(t, err)
	phantom1CommitPts, err := shamir.FeldmanCommit(G, phantom1Coeffs)
	require.NoError(t, err)

	phantom2Secret := new(curvey.ScalarPallas).Random(rand.Reader)
	phantom2Shares, phantom2Coeffs, err := shamir.Split(phantom2Secret, tVal, n)
	require.NoError(t, err)
	phantom2CommitPts, err := shamir.FeldmanCommit(G, phantom2Coeffs)
	require.NoError(t, err)
	_ = phantom2Coeffs

	phantom1Commitments := make([][]byte, tVal)
	for j, pt := range phantom1CommitPts {
		phantom1Commitments[j] = pt.ToAffineCompressed()
	}
	phantom1Payloads := make([]*types.DealerPayload, 0, 2)
	env, err := ecies.Encrypt(G, pallasPk.Point, phantom1Shares[0].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom1Payloads = append(phantom1Payloads, &types.DealerPayload{
		ValidatorAddress: proposerAddr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})
	env, err = ecies.Encrypt(G, phantom2Pk.Point, phantom1Shares[2].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom1Payloads = append(phantom1Payloads, &types.DealerPayload{
		ValidatorAddress: phantom2Addr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})

	phantom2Commitments := make([][]byte, tVal)
	for j, pt := range phantom2CommitPts {
		phantom2Commitments[j] = pt.ToAffineCompressed()
	}
	phantom2Payloads := make([]*types.DealerPayload, 0, 2)
	env, err = ecies.Encrypt(G, pallasPk.Point, phantom2Shares[0].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom2Payloads = append(phantom2Payloads, &types.DealerPayload{
		ValidatorAddress: proposerAddr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})
	env, err = ecies.Encrypt(G, phantom1Pk.Point, phantom2Shares[1].Value.Bytes(), rand.Reader)
	require.NoError(t, err)
	phantom2Payloads = append(phantom2Payloads, &types.DealerPayload{
		ValidatorAddress: phantom1Addr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	})

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round := &types.VoteRound{
		VoteRoundId:        roundID,
		Status:             types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: validators,
		VoteEndTime:        uint64(voteEndTime.Unix()),
		Proposals:          testutil.SampleProposals(),
		NullifierImtRoot:   bytes.Repeat([]byte{0x01}, 32),
		NcRoot:             bytes.Repeat([]byte{0x02}, 32),
		DkgContributions: []*types.DKGContribution{
			{
				ValidatorAddress:   phantom1Addr,
				FeldmanCommitments: phantom1Commitments,
				Payloads:           phantom1Payloads,
			},
			{
				ValidatorAddress:   phantom2Addr,
				FeldmanCommitments: phantom2Commitments,
				Payloads:           phantom2Payloads,
			},
		},
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// DKG contribution from proposer → DEALT
	start := time.Now()
	app.NextBlockWithPrepareProposal()
	t.Logf("DKG contribute (3-val):  %s", time.Since(start))

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)

	// Auto-ack (proposer via pipeline) → 1/3 acked, stays DEALT.
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	t.Logf("Ceremony ack (proposer): %s", time.Since(start))

	// Seed remaining phantom acks and force CONFIRMED → ACTIVE.
	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Len(t, round.CeremonyAcks, 1, "proposer auto-acked")

	for _, addr := range []string{phantom1Addr, phantom2Addr} {
		round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
			ValidatorAddress: addr,
			AckSignature:     bytes.Repeat([]byte{0xAC}, 64),
			AckHeight:        uint64(app.Height),
		})
	}
	round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
	round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)

	eaPk, err := elgamal.UnmarshalPublicKey(round.EaPk)
	require.NoError(t, err)

	// Voting: delegations, cast votes, reveals
	delegations := testutil.ValidDelegationN(roundID, numVotes, 2000)
	delegationTxs := make([][]byte, len(delegations))
	for i, d := range delegations {
		delegationTxs[i] = testutil.MustEncodeVoteTx(d)
	}

	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          app.Height,
		Time:            app.Time,
		Txs:             delegationTxs,
		ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	finalizeTime := time.Since(start)
	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "delegation %d failed: %s", i, r.Log)
	}
	start = time.Now()
	_, err = app.Commit()
	require.NoError(t, err)
	t.Logf("Deliver %d delegations:  FinalizeBlock=%s  Commit=%s", numVotes, finalizeTime, time.Since(start))

	anchorHeight := uint64(app.Height)

	castVotes := testutil.ValidCastVoteN(roundID, anchorHeight, numVotes, 6000)
	castTxs := make([][]byte, len(castVotes))
	for i, cv := range castVotes {
		castTxs[i] = testutil.MustEncodeVoteTx(cv)
	}

	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          app.Height,
		Time:            app.Time,
		Txs:             castTxs,
		ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	finalizeTime = time.Since(start)
	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "cast vote %d failed: %s", i, r.Log)
	}
	start = time.Now()
	_, err = app.Commit()
	require.NoError(t, err)
	t.Logf("Deliver %d cast votes:   FinalizeBlock=%s  Commit=%s", numVotes, finalizeTime, time.Since(start))

	// Reveal: each voter reveals with a real ElGamal ciphertext
	revealTxs := make([][]byte, numVotes)
	for i := range numVotes {
		value := uint64(1 + i%100)
		ct, err := elgamal.Encrypt(eaPk, value, rand.Reader)
		require.NoError(t, err)
		encShare, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		msg := testutil.ValidRevealShareReal(roundID, anchorHeight, byte(0x50+i), 1, 1, encShare)
		revealTxs[i] = testutil.MustEncodeVoteTx(msg)
	}

	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          app.Height,
		Time:            app.Time,
		Txs:             revealTxs,
		ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	finalizeTime = time.Since(start)
	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "reveal %d failed: %s", i, r.Log)
	}
	start = time.Now()
	_, err = app.Commit()
	require.NoError(t, err)
	t.Logf("Deliver %d reveals:      FinalizeBlock=%s  Commit=%s", numVotes, finalizeTime, time.Since(start))

	// EndBlocker → TALLYING
	start = time.Now()
	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))
	endBlockTime := time.Since(start)
	t.Logf("EndBlocker ACTIVE→TALLYING:  %s", endBlockTime)

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// Compute phantom1's combined Shamir share:
	//   combinedShare = ownPartial(index=2) + sum(decrypt(other_contributions[phantom1]))
	phantom1OwnPartial := shamir.EvalPolynomial(phantom1Coeffs, 2) // ShamirIndex=2
	phantom1CombinedShare := phantom1OwnPartial

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	round, err = app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	for _, contrib := range round.DkgContributions {
		if contrib.ValidatorAddress == phantom1Addr {
			continue
		}
		for _, p := range contrib.Payloads {
			if p.ValidatorAddress != phantom1Addr {
				continue
			}
			ephPk, err := elgamal.UnmarshalPublicKey(p.EphemeralPk)
			require.NoError(t, err)
			shareBytes, err := ecies.Decrypt(phantom1Sk.Scalar, &ecies.Envelope{
				Ephemeral:  ephPk.Point,
				Ciphertext: p.Ciphertext,
			})
			require.NoError(t, err)
			shareScalar, err := new(curvey.ScalarPallas).SetBytes(shareBytes)
			require.NoError(t, err)
			phantom1CombinedShare = phantom1CombinedShare.Add(shareScalar)
		}
	}

	// Seed phantom1's partial decryption using the combined share: D = combined_share * C1
	tallyBytes, err := app.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	tallyCt, err := elgamal.UnmarshalCiphertext(tallyBytes)
	require.NoError(t, err)

	phantom1Di := tallyCt.C1.Mul(phantom1CombinedShare)
	pdEntries := []*types.PartialDecryptionEntry{{
		ProposalId:     1,
		VoteDecision:   1,
		PartialDecrypt: phantom1Di.ToAffineCompressed(),
	}}
	require.NoError(t, app.VoteKeeper().SetPartialDecryptions(kvStore, roundID, 2, pdEntries))
	app.NextBlock()

	// Partial decrypt from proposer
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	pdTime := time.Since(start)
	t.Logf("Partial decrypt (3-val): %s", pdTime)

	// Tally (Lagrange + BSGS)
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	tallyTime := time.Since(start)
	t.Logf("Tally decryption (3-val, t=2): %s", tallyTime)

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)

	// Summary
	t.Log("")
	t.Log("=== Multi-Validator Latency Summary ===")
	t.Logf("  EndBlocker transition:  %s", endBlockTime)
	t.Logf("  Partial decrypt block:  %s", pdTime)
	t.Logf("  Tally decryption block: %s", tallyTime)
	t.Log("")
	t.Logf("  timeout_propose budget: 2.5s")
	if tallyTime > 2500*time.Millisecond {
		t.Logf("  ⚠ Tally block (%s) EXCEEDS timeout_propose (2.5s)", tallyTime)
	} else {
		t.Logf("  ✓ Tally block fits within timeout_propose budget")
	}
}

// TestABCILatencies_LargeValidatorSet measures tally latency with larger
// validator sets. Seeds rounds directly in ACTIVE state with real Shamir shares
// and ElGamal ciphertexts, bypassing the DKG ceremony (which would require
// n*(n-1) ECIES encryptions just for test setup). This focuses the measurement
// on the Lagrange interpolation + BSGS path at scale.
//
// Run with:
//
//	go test -count=1 -run TestABCILatencies_LargeValidatorSet -v -timeout 10m ./app/...
func TestABCILatencies_LargeValidatorSet(t *testing.T) {
	type scenario struct {
		numValidators int
		numVotes      int
	}
	scenarios := []scenario{
		{numValidators: 10, numVotes: 50},
		{numValidators: 30, numVotes: 50},
		{numValidators: 30, numVotes: 200},
	}

	for _, sc := range scenarios {
		name := fmt.Sprintf("n=%d_votes=%d", sc.numValidators, sc.numVotes)
		t.Run(name, func(t *testing.T) {
			runLargeValidatorBench(t, sc.numValidators, sc.numVotes)
		})
	}
}

func runLargeValidatorBench(t *testing.T, numValidators, numVotes int) {
	t.Helper()
	G := elgamal.PallasGenerator()

	app, eaPk, eaSkBytes := testutil.SetupTestAppWithEAKey(t)
	proposerAddr := app.ValidatorOperAddr()

	threshold := (numValidators + 1) / 2
	if threshold < 2 {
		threshold = 2
	}

	eaSk, err := elgamal.UnmarshalSecretKey(eaSkBytes)
	require.NoError(t, err)

	shares, coeffs, err := shamir.Split(eaSk.Scalar, threshold, numValidators)
	require.NoError(t, err)

	commitPts, err := shamir.FeldmanCommit(G, coeffs)
	require.NoError(t, err)
	feldmanCommitments := make([][]byte, len(commitPts))
	for i, pt := range commitPts {
		feldmanCommitments[i] = pt.ToAffineCompressed()
	}

	validators := make([]*types.ValidatorPallasKey, numValidators)
	_, proposerPallasPk := elgamal.KeyGen(rand.Reader)
	validators[0] = &types.ValidatorPallasKey{
		ValidatorAddress: proposerAddr,
		PallasPk:         proposerPallasPk.Point.ToAffineCompressed(),
		ShamirIndex:      1,
	}
	for i := 1; i < numValidators; i++ {
		addr := sdk.ValAddress(bytes.Repeat([]byte{byte(i)}, 20)).String()
		_, pk := elgamal.KeyGen(rand.Reader)
		validators[i] = &types.ValidatorPallasKey{
			ValidatorAddress: addr,
			PallasPk:         pk.Point.ToAffineCompressed(),
			ShamirIndex:      uint32(i + 1),
		}
	}

	roundID := make([]byte, 32)
	roundID[0] = 0xDD
	roundID[1] = byte(numValidators)

	voteEndTime := app.Time.Add(120 * time.Second)

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round := &types.VoteRound{
		VoteRoundId:        roundID,
		Status:             types.SessionStatus_SESSION_STATUS_ACTIVE,
		EaPk:               eaPk.Point.ToAffineCompressed(),
		Proposals:          testutil.SampleProposals(),
		CeremonyValidators: validators,
		Threshold:          uint32(threshold),
		FeldmanCommitments: feldmanCommitments,
		VoteEndTime:        uint64(voteEndTime.Unix()),
		NullifierImtRoot:   bytes.Repeat([]byte{0x01}, 32),
		NcRoot:             bytes.Repeat([]byte{0x02}, 32),
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// --- Deliver delegations ---
	delegations := testutil.ValidDelegationN(roundID, numVotes, 10000)
	delegationTxs := make([][]byte, len(delegations))
	for i, d := range delegations {
		delegationTxs[i] = testutil.MustEncodeVoteTx(d)
	}
	start := time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err := app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: app.Height, Time: app.Time,
		Txs: delegationTxs, ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "delegation %d failed: %s", i, r.Log)
	}
	_, err = app.Commit()
	require.NoError(t, err)
	t.Logf("Deliver %d delegations: %s", numVotes, time.Since(start))

	anchorHeight := uint64(app.Height)

	// --- Deliver cast votes ---
	castVotes := testutil.ValidCastVoteN(roundID, anchorHeight, numVotes, 20000)
	castTxs := make([][]byte, len(castVotes))
	for i, cv := range castVotes {
		castTxs[i] = testutil.MustEncodeVoteTx(cv)
	}
	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: app.Height, Time: app.Time,
		Txs: castTxs, ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "cast vote %d failed: %s", i, r.Log)
	}
	_, err = app.Commit()
	require.NoError(t, err)
	t.Logf("Deliver %d cast votes:  %s", numVotes, time.Since(start))

	// --- Deliver reveals (real ElGamal encryptions) ---
	revealTxs := make([][]byte, numVotes)
	for i := range numVotes {
		value := uint64(1 + i%100)
		ct, err := elgamal.Encrypt(eaPk, value, rand.Reader)
		require.NoError(t, err)
		encShare, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		msg := testutil.ValidRevealShareReal(roundID, anchorHeight, byte(i), 1, 1, encShare)
		revealTxs[i] = testutil.MustEncodeVoteTx(msg)
	}
	start = time.Now()
	app.Height++
	app.Time = app.Time.Add(5 * time.Second)
	resp, err = app.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: app.Height, Time: app.Time,
		Txs: revealTxs, ProposerAddress: app.ProposerAddress,
	})
	require.NoError(t, err)
	for i, r := range resp.TxResults {
		require.Equal(t, uint32(0), r.Code, "reveal %d failed: %s", i, r.Log)
	}
	_, err = app.Commit()
	require.NoError(t, err)
	t.Logf("Deliver %d reveals:     %s", numVotes, time.Since(start))

	// --- EndBlocker → TALLYING ---
	start = time.Now()
	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))
	endBlockTime := time.Since(start)
	t.Logf("EndBlocker ACTIVE→TALLYING: %s", endBlockTime)

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// --- Seed partial decryptions from phantom validators (indices 2..threshold) ---
	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)

	tallyBytes, err := app.VoteKeeper().GetTally(kvStore, roundID, 1, 1)
	require.NoError(t, err)
	require.NotNil(t, tallyBytes, "tally accumulator should exist")
	tallyCt, err := elgamal.UnmarshalCiphertext(tallyBytes)
	require.NoError(t, err)

	start = time.Now()
	for i := 1; i < threshold; i++ {
		di := tallyCt.C1.Mul(shares[i].Value)
		entries := []*types.PartialDecryptionEntry{{
			ProposalId:     1,
			VoteDecision:   1,
			PartialDecrypt: di.ToAffineCompressed(),
		}}
		require.NoError(t, app.VoteKeeper().SetPartialDecryptions(
			kvStore, roundID, uint32(i+1), entries,
		))
	}
	t.Logf("Seed %d phantom partial decryptions: %s", threshold-1, time.Since(start))
	app.NextBlock()

	// Write proposer's share for the partial decrypt injector.
	app.WriteShareForRound(roundID, shares[0].Value.Bytes())

	// --- Partial decrypt from proposer ---
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	pdTime := time.Since(start)
	t.Logf("Partial decrypt block: %s", pdTime)

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	count, err := app.VoteKeeper().CountPartialDecryptionValidators(kvStore, roundID)
	require.NoError(t, err)
	t.Logf("Partial decryptions stored: %d (threshold=%d)", count, threshold)
	require.GreaterOrEqual(t, count, threshold)

	// --- Tally decryption (Lagrange + BSGS) ---
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	tallyTime := time.Since(start)
	t.Logf("Tally decryption block: %s", tallyTime)

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED")

	ctx = app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore = app.VoteKeeper().OpenKVStore(ctx)
	tallyResults, err := app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	require.NoError(t, err)
	require.NotEmpty(t, tallyResults)

	expectedTotal := uint64(0)
	for i := range numVotes {
		expectedTotal += uint64(1 + i%100)
	}
	require.Equal(t, expectedTotal, tallyResults[0].TotalValue,
		"decrypted tally must match sum of encrypted values")

	t.Log("")
	t.Logf("=== n=%d, t=%d, votes=%d ===", numValidators, threshold, numVotes)
	t.Logf("  EndBlocker transition:  %s", endBlockTime)
	t.Logf("  Partial decrypt block:  %s", pdTime)
	t.Logf("  Tally decryption block: %s", tallyTime)
	t.Log("")
	t.Logf("  timeout_propose budget: 2.5s")
	if tallyTime > 1800*time.Millisecond {
		t.Logf("  ⚠ Tally block (%s) EXCEEDS timeout_propose (1.8s)", tallyTime)
	} else {
		pct := float64(tallyTime.Milliseconds()) / 1800.0 * 100
		t.Logf("  ✓ Tally block uses %.1f%% of budget", pct)
	}
}

// TestABCILatencies_DKGCeremony measures the DKG ceremony cost with large
// validator sets. For each of n validators, the DKG contribute block performs:
//   - 1 Shamir split into n shares
//   - t Feldman commitments (t scalar multiplications)
//   - n-1 ECIES encryptions (one per other validator)
//
// The ack block performs:
//   - n-1 ECIES decryptions
//   - n-1 Feldman share verifications
//   - 1 combined Feldman verification
//
// These are the two heaviest per-block operations during the ceremony.
// This test measures them by driving the full PrepareProposal pipeline.
//
// Run with:
//
//	go test -count=1 -run TestABCILatencies_DKGCeremony -v -timeout 10m ./app/...
func TestABCILatencies_DKGCeremony(t *testing.T) {
	for _, n := range []int{3, 10, 30} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			runDKGCeremonyBench(t, n)
		})
	}
}

func runDKGCeremonyBench(t *testing.T, numValidators int) {
	t.Helper()
	G := elgamal.PallasGenerator()

	app, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	proposerAddr := app.ValidatorOperAddr()

	tVal, err := votekeeper.ThresholdForN(numValidators)
	require.NoError(t, err)

	// Build validator set: proposer at index 0, phantoms at 1..n-1.
	type phantomVal struct {
		addr string
		sk   *elgamal.SecretKey
		pk   *elgamal.PublicKey
	}
	phantoms := make([]phantomVal, numValidators-1)
	validators := make([]*types.ValidatorPallasKey, numValidators)
	validators[0] = &types.ValidatorPallasKey{
		ValidatorAddress: proposerAddr,
		PallasPk:         pallasPk.Point.ToAffineCompressed(),
		ShamirIndex:      1,
	}
	for i := 0; i < numValidators-1; i++ {
		sk, pk := elgamal.KeyGen(rand.Reader)
		addr := sdk.ValAddress(bytes.Repeat([]byte{byte(i + 1)}, 20)).String()
		phantoms[i] = phantomVal{addr: addr, sk: sk, pk: pk}
		validators[i+1] = &types.ValidatorPallasKey{
			ValidatorAddress: addr,
			PallasPk:         pk.Point.ToAffineCompressed(),
			ShamirIndex:      uint32(i + 2),
		}
	}

	roundID := make([]byte, 32)
	roundID[0] = 0xEE
	roundID[1] = byte(numValidators)

	// --- Generate all phantom DKG contributions (this is test setup, not measured) ---
	setupStart := time.Now()

	phantomContribs := make([]*types.DKGContribution, len(phantoms))
	for pi, pv := range phantoms {
		secret := new(curvey.ScalarPallas).Random(rand.Reader)
		shares, coeffs, err := shamir.Split(secret, tVal, numValidators)
		require.NoError(t, err)

		commitPts, err := shamir.FeldmanCommit(G, coeffs)
		require.NoError(t, err)
		commitBytes := make([][]byte, len(commitPts))
		for j, pt := range commitPts {
			commitBytes[j] = pt.ToAffineCompressed()
		}

		payloads := make([]*types.DealerPayload, 0, numValidators-1)
		for vi, v := range validators {
			if v.ValidatorAddress == pv.addr {
				continue // skip self
			}
			recipientPk, err := elgamal.UnmarshalPublicKey(v.PallasPk)
			require.NoError(t, err)
			env, err := ecies.Encrypt(G, recipientPk.Point, shares[vi].Value.Bytes(), rand.Reader)
			require.NoError(t, err)
			payloads = append(payloads, &types.DealerPayload{
				ValidatorAddress: v.ValidatorAddress,
				EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
				Ciphertext:       env.Ciphertext,
			})
		}

		phantomContribs[pi] = &types.DKGContribution{
			ValidatorAddress:   pv.addr,
			FeldmanCommitments: commitBytes,
			Payloads:           payloads,
		}
	}
	t.Logf("Test setup: generated %d phantom DKG contributions (%d ECIES encryptions): %s",
		len(phantoms), len(phantoms)*(numValidators-1), time.Since(setupStart))

	// Seed REGISTERING round with all phantom contributions pre-loaded.
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round := &types.VoteRound{
		VoteRoundId:        roundID,
		Status:             types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: validators,
		VoteEndTime:        uint64(app.Time.Add(120 * time.Second).Unix()),
		Proposals:          testutil.SampleProposals(),
		NullifierImtRoot:   bytes.Repeat([]byte{0x01}, 32),
		NcRoot:             bytes.Repeat([]byte{0x02}, 32),
		DkgContributions:   phantomContribs,
	}
	require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
	app.NextBlock()

	// --- Measure: DKG contribution from proposer ---
	// This block: Shamir split + (n-1) ECIES encryptions + Feldman commit
	start := time.Now()
	app.NextBlockWithPrepareProposal()
	dkgContribTime := time.Since(start)
	t.Logf("DKG contribute block (1 Shamir split + %d ECIES encryptions): %s",
		numValidators-1, dkgContribTime)

	round = app.MustGetVoteRound(roundID)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus,
		"should be DEALT after all %d contributions", numValidators)
	require.Len(t, round.DkgContributions, numValidators)

	// --- Measure: Ack from proposer ---
	// This block: (n-1) ECIES decryptions + (n-1) Feldman verifications + combined verify
	start = time.Now()
	app.NextBlockWithPrepareProposal()
	ackTime := time.Since(start)
	t.Logf("Ack block (%d ECIES decryptions + %d Feldman verifications): %s",
		numValidators-1, numValidators-1, ackTime)

	round = app.MustGetVoteRound(roundID)
	require.Len(t, round.CeremonyAcks, 1, "proposer should have acked")

	t.Log("")
	t.Logf("=== DKG Ceremony: n=%d, t=%d ===", numValidators, tVal)
	t.Logf("  DKG contribute block: %s", dkgContribTime)
	t.Logf("  Ack block:            %s", ackTime)
	t.Logf("  Total ECIES ops:      %d encrypt + %d decrypt = %d per validator",
		numValidators-1, numValidators-1, 2*(numValidators-1))
	t.Log("")

	worst := dkgContribTime
	if ackTime > worst {
		worst = ackTime
	}
	t.Logf("  timeout_propose budget: 1.8s")
	if worst > 1800*time.Millisecond {
		t.Logf("  ⚠ Worst block (%s) EXCEEDS timeout_propose (1.8s)", worst)
	} else {
		pct := float64(worst.Milliseconds()) / 1800.0 * 100
		t.Logf("  ✓ Worst block uses %.1f%% of budget", pct)
	}
}
