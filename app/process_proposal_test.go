package app_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	abci "github.com/cometbft/cometbft/abci/types"

	voteapi "github.com/z-cale/zally/api"
	"github.com/z-cale/zally/crypto/ecies"
	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Table-driven unit tests for ProcessProposalHandler
// ---------------------------------------------------------------------------

// TestProcessProposalAckValidation exercises the injected MsgAckExecutiveAuthorityKey
// validation path in ProcessProposal with various good and bad inputs.
func TestProcessProposalAckValidation(t *testing.T) {
	app, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := app.ValidatorOperAddr()

	// ECIES-encrypt ea_sk to the validator's Pallas public key.
	G := elgamal.PallasGenerator()
	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}

	// Track the current round ID (set by each test's setup func).
	var currentRoundID []byte

	// Helper to build a valid ack tx targeting the current round.
	validAckTx := func() []byte {
		h := sha256.New()
		h.Write([]byte("ack"))
		h.Write(eaPkBytes)
		h.Write([]byte(valAddr))
		sig := h.Sum(nil)

		msg := &types.MsgAckExecutiveAuthorityKey{
			Creator:      valAddr,
			AckSignature: sig,
			VoteRoundId:  currentRoundID,
		}
		txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagAckExecutiveAuthorityKey)
		require.NoError(t, err)
		return txBytes
	}

	tests := []struct {
		name     string
		setup    func()                   // mutate state before this case
		txs      func() [][]byte          // txs for the ProcessProposal request
		wantAccept bool
	}{
		{
			name: "valid ack tx in DEALT state",
			setup: func() {
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)
			},
			txs: func() [][]byte {
				return [][]byte{validAckTx()}
			},
			wantAccept: true,
		},
		{
			name: "ack tx with no matching PENDING round → reject",
			setup: func() {
				// SeedConfirmedCeremony is a no-op; no PENDING round exists.
				app.SeedConfirmedCeremony(eaPkBytes)
				currentRoundID = bytes.Repeat([]byte{0xFF}, 32) // non-existent
			},
			txs: func() [][]byte {
				return [][]byte{validAckTx()}
			},
			wantAccept: false,
		},
		{
			name: "ack tx from unregistered validator → reject",
			setup: func() {
				// Seed DEALT with a different validator address.
				fakeValidators := []*types.ValidatorPallasKey{
					{ValidatorAddress: "cosmosvaloper1fake", PallasPk: pallasPk.Point.ToAffineCompressed()},
				}
				fakePayloads := []*types.DealerPayload{
					{
						ValidatorAddress: "cosmosvaloper1fake",
						EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
						Ciphertext:       env.Ciphertext,
					},
				}
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, fakePayloads, fakeValidators)
			},
			txs: func() [][]byte {
				return [][]byte{validAckTx()}
			},
			wantAccept: false,
		},
		{
			name: "duplicate ack from same validator → reject",
			setup: func() {
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)
				// Run PrepareProposal → FinalizeBlock to process the first ack.
				app.NextBlockWithPrepareProposal()
			},
			txs: func() [][]byte {
				// Second ack from same validator.
				return [][]byte{validAckTx()}
			},
			wantAccept: false,
		},
		{
			name: "malformed ack tx (corrupted protobuf) → reject",
			setup: func() {
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)
			},
			txs: func() [][]byte {
				return [][]byte{{voteapi.TagAckExecutiveAuthorityKey, 0xFF, 0xFF, 0xFF}}
			},
			wantAccept: false,
		},
		{
			name: "short tx (only tag byte) skipped → accept",
			setup: func() {
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)
			},
			txs: func() [][]byte {
				return [][]byte{{voteapi.TagAckExecutiveAuthorityKey}}
			},
			wantAccept: true,
		},
		{
			name: "non-custom tx bytes pass through → accept",
			setup: func() {
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)
			},
			txs: func() [][]byte {
				return [][]byte{bytes.Repeat([]byte{0xAA}, 100)}
			},
			wantAccept: true,
		},
		{
			name: "valid ack mixed with non-custom tx → accept",
			setup: func() {
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)
			},
			txs: func() [][]byte {
				return [][]byte{
					validAckTx(),
					bytes.Repeat([]byte{0xBB}, 50),
				}
			},
			wantAccept: true,
		},
		{
			name: "empty tx list → accept",
			setup: func() {
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)
			},
			txs: func() [][]byte {
				return nil
			},
			wantAccept: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()

			resp := app.CallProcessProposal(tc.txs())
			if tc.wantAccept {
				require.Equal(t, abci.ResponseProcessProposal_ACCEPT, resp.Status,
					"expected ACCEPT for case: %s", tc.name)
			} else {
				require.Equal(t, abci.ResponseProcessProposal_REJECT, resp.Status,
					"expected REJECT for case: %s", tc.name)
			}
		})
	}
}

// TestProcessProposalTallyValidation exercises the injected MsgSubmitTally
// validation path in ProcessProposal.
func TestProcessProposalTallyValidation(t *testing.T) {
	app := testutil.SetupTestApp(t)
	valAddr := app.ValidatorOperAddr()

	// Create a voting session expiring soon.
	voteEndTime := app.Time.Add(10 * time.Second)
	setupMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    800,
		SnapshotBlockhash: bytes.Repeat([]byte{0x8A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x8B}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x08}, 32),
		NcRoot:            bytes.Repeat([]byte{0x09}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	roundID := app.SeedVotingSession(setupMsg)

	// Helper to build a tally tx.
	buildTallyTx := func(rid []byte) []byte {
		msg := &types.MsgSubmitTally{
			VoteRoundId: rid,
			Creator:     valAddr,
			Entries: []*types.TallyEntry{
				{ProposalId: 0, VoteDecision: 0, TotalValue: 0},
			},
		}
		txBytes, err := voteapi.EncodeVoteTx(msg)
		require.NoError(t, err)
		return txBytes
	}

	tests := []struct {
		name       string
		setup      func()
		txs        func() [][]byte
		wantAccept bool
	}{
		{
			name: "tally tx when round is ACTIVE → reject",
			setup: func() {
				// Round stays ACTIVE; no time advancement.
			},
			txs: func() [][]byte {
				return [][]byte{buildTallyTx(roundID)}
			},
			wantAccept: false,
		},
		{
			name: "tally tx when round is TALLYING → accept",
			setup: func() {
				app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))
			},
			txs: func() [][]byte {
				return [][]byte{buildTallyTx(roundID)}
			},
			wantAccept: true,
		},
		{
			name: "tally tx for non-existent round → reject",
			setup: func() {
				// State already advanced; round is TALLYING.
			},
			txs: func() [][]byte {
				fakeRoundID := bytes.Repeat([]byte{0xFF}, 32)
				return [][]byte{buildTallyTx(fakeRoundID)}
			},
			wantAccept: false,
		},
		{
			name: "malformed tally tx → reject",
			setup: func() {},
			txs: func() [][]byte {
				return [][]byte{{voteapi.TagSubmitTally, 0xFF, 0xFF}}
			},
			wantAccept: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()

			resp := app.CallProcessProposal(tc.txs())
			if tc.wantAccept {
				require.Equal(t, abci.ResponseProcessProposal_ACCEPT, resp.Status,
					"expected ACCEPT for case: %s", tc.name)
			} else {
				require.Equal(t, abci.ResponseProcessProposal_REJECT, resp.Status,
					"expected REJECT for case: %s", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Functional tests: PrepareProposal → ProcessProposal pipeline
// ---------------------------------------------------------------------------

// TestPrepareProposalThenProcessProposalAck verifies that the ack tx produced
// by PrepareProposal is accepted by ProcessProposal (simulates a non-proposer
// validator validating the proposer's injected ack).
func TestPrepareProposalThenProcessProposalAck(t *testing.T) {
	app, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := app.ValidatorOperAddr()

	// ECIES-encrypt ea_sk to the validator's Pallas public key.
	G := elgamal.PallasGenerator()
	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	roundID := app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)

	// Step 1: PrepareProposal should inject an ack tx.
	ppResp := app.CallPrepareProposal()
	require.NotEmpty(t, ppResp.Txs, "PrepareProposal should inject at least one tx")

	// Verify the first tx is an ack.
	require.Equal(t, voteapi.TagAckExecutiveAuthorityKey, ppResp.Txs[0][0],
		"first injected tx should be an ack")

	// Step 2: ProcessProposal should accept the block containing these txs.
	procResp := app.CallProcessProposal(ppResp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, procResp.Status,
		"ProcessProposal should accept the block with the injected ack")

	// Step 3: Deliver the block and verify ceremony reaches CONFIRMED.
	app.NextBlockWithPrepareProposal()

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus,
		"ceremony should be CONFIRMED after ack pipeline")
}

// TestPrepareProposalThenProcessProposalTally verifies that the tally tx
// produced by PrepareProposal is accepted by ProcessProposal.
func TestPrepareProposalThenProcessProposalTally(t *testing.T) {
	app, pk, eaSkBytes := testutil.SetupTestAppWithEAKey(t)

	// Create a voting session expiring soon.
	voteEndTime := app.Time.Add(30 * time.Second)
	setupMsg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    900,
		SnapshotBlockhash: bytes.Repeat([]byte{0x9A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x9B}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x0A}, 32),
		NcRoot:            bytes.Repeat([]byte{0x0B}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x11}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x22}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x33}, 64),
		Proposals:         testutil.SampleProposals(),
	}
	roundID := app.SeedVotingSession(setupMsg)
	app.WriteEaSkForRound(roundID, eaSkBytes)

	// Delegate and reveal a share so there is tally data.
	delegation := testutil.ValidDelegation(roundID, 0x10)
	result := app.DeliverVoteTx(testutil.MustEncodeVoteTx(delegation))
	require.Equal(t, uint32(0), result.Code, "delegation should succeed")

	anchorHeight := uint64(app.Height)

	castVote := testutil.ValidCastVote(roundID, anchorHeight, 0x30)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(castVote))
	require.Equal(t, uint32(0), result.Code, "cast vote should succeed")

	revealAnchor := uint64(app.Height)

	ct, err := elgamal.Encrypt(pk, 77, rand.Reader)
	require.NoError(t, err)
	encShare, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)

	revealMsg := testutil.ValidRevealShareReal(roundID, revealAnchor, 0x50, 1, 1, encShare)
	result = app.DeliverVoteTx(testutil.MustEncodeVoteTx(revealMsg))
	require.Equal(t, uint32(0), result.Code, "reveal share should succeed")

	// Advance to TALLYING.
	app.NextBlockAtTime(voteEndTime.Add(1 * time.Second))

	// Step 1: PrepareProposal should inject a tally tx.
	ppResp := app.CallPrepareProposal()
	require.NotEmpty(t, ppResp.Txs, "PrepareProposal should inject at least one tx")

	// Verify the first tx is a tally.
	require.Equal(t, voteapi.TagSubmitTally, ppResp.Txs[0][0],
		"first injected tx should be a tally")

	// Step 2: ProcessProposal should accept the block with the tally tx.
	procResp := app.CallProcessProposal(ppResp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, procResp.Status,
		"ProcessProposal should accept the block with the injected tally")

	// Step 3: Deliver the block and verify the round is FINALIZED.
	app.NextBlockWithPrepareProposal()

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status,
		"round should be FINALIZED after tally pipeline")

	tallyResults, err := app.VoteKeeper().GetAllTallyResults(kvStore, roundID)
	require.NoError(t, err)
	require.Len(t, tallyResults, 1)
	require.Equal(t, uint64(77), tallyResults[0].TotalValue,
		"decrypted tally should match encrypted value of 77")
}

// TestPrepareProposalIdempotentWhenNoInjection verifies that PrepareProposal
// and ProcessProposal pass through mempool txs unchanged when no injection
// conditions are met (ceremony CONFIRMED, no TALLYING rounds).
func TestPrepareProposalIdempotentWhenNoInjection(t *testing.T) {
	app := testutil.SetupTestApp(t)

	// Some dummy "mempool" txs.
	mempoolTxs := [][]byte{
		bytes.Repeat([]byte{0xAA}, 50),
		bytes.Repeat([]byte{0xBB}, 60),
	}

	ppResp := app.CallPrepareProposalWithTxs(mempoolTxs)
	require.Len(t, ppResp.Txs, 2, "no injection should leave tx count unchanged")
	require.Equal(t, mempoolTxs[0], ppResp.Txs[0])
	require.Equal(t, mempoolTxs[1], ppResp.Txs[1])

	// ProcessProposal should accept these non-custom txs.
	procResp := app.CallProcessProposal(ppResp.Txs)
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, procResp.Status)
}

// TestPrepareProposalSkipsWhenAlreadyAcked verifies that PrepareProposal
// does not inject a second ack if the validator has already acked.
func TestPrepareProposalSkipsWhenAlreadyAcked(t *testing.T) {
	app, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := app.ValidatorOperAddr()

	G := elgamal.PallasGenerator()
	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	roundID := app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payloads, validators)

	// First block: auto-ack fires and moves ceremony to CONFIRMED.
	app.NextBlockWithPrepareProposal()

	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
	kvStore := app.VoteKeeper().OpenKVStore(ctx)
	round, err := app.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus)

	// Second block: PrepareProposal should NOT inject anything (already acked / CONFIRMED).
	ppResp := app.CallPrepareProposal()
	for _, tx := range ppResp.Txs {
		if len(tx) > 0 {
			require.NotEqual(t, voteapi.TagAckExecutiveAuthorityKey, tx[0],
				"should not inject a second ack after ceremony is CONFIRMED")
		}
	}
}
