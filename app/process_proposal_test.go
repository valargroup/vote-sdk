package app_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/app"
	voteapi "github.com/valargroup/vote-sdk/api"
	votekeeper "github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/crypto/ecies"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// Table-driven unit tests for ProcessProposalHandler
// ---------------------------------------------------------------------------

// TestProcessProposalDealValidation exercises the injected MsgDealExecutiveAuthorityKey
// validation path in ProcessProposal with various good and bad inputs.
func TestProcessProposalDealValidation(t *testing.T) {
	app := testutil.SetupTestApp(t)
	valAddr := app.ValidatorOperAddr()

	_, eaPk := elgamal.KeyGen(rand.Reader)
	eaPkBytes := eaPk.Point.ToAffineCompressed()

	_, ephPk := elgamal.KeyGen(rand.Reader)
	ephPkBytes := ephPk.Point.ToAffineCompressed()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr},
	}

	var currentRoundID []byte

	buildDealTx := func(creator string, roundID []byte, ceremonyValidators []*types.ValidatorPallasKey) []byte {
		payloads := make([]*types.DealerPayload, len(ceremonyValidators))
		for i, v := range ceremonyValidators {
			payloads[i] = &types.DealerPayload{
				ValidatorAddress: v.ValidatorAddress,
				EphemeralPk:      ephPkBytes,
				Ciphertext:       bytes.Repeat([]byte{0x01}, 48),
			}
		}
		threshold, err := votekeeper.ThresholdForN(len(ceremonyValidators))
		require.NoError(t, err)
		msg := &types.MsgDealExecutiveAuthorityKey{
			Creator:     creator,
			VoteRoundId: roundID,
			EaPk:        eaPkBytes,
			Payloads:    payloads,
			Threshold:   uint32(threshold),
		}
		txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagDealExecutiveAuthorityKey)
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
			name: "valid deal tx in REGISTERING state",
			setup: func() {
				currentRoundID = app.SeedRegisteringCeremony(validators)
			},
			txs: func() [][]byte {
				return [][]byte{buildDealTx(valAddr, currentRoundID, validators)}
			},
			wantAccept: true,
		},
		{
			name: "deal tx for non-existent round → reject",
			setup: func() {
				currentRoundID = bytes.Repeat([]byte{0xFF}, 32)
			},
			txs: func() [][]byte {
				return [][]byte{buildDealTx(valAddr, currentRoundID, validators)}
			},
			wantAccept: false,
		},
		{
			name: "deal tx when round is not REGISTERING (DEALT) → reject",
			setup: func() {
				payload := []*types.DealerPayload{
					{ValidatorAddress: valAddr, EphemeralPk: ephPkBytes, Ciphertext: bytes.Repeat([]byte{0x01}, 48)},
				}
				currentRoundID = app.SeedDealtCeremony(eaPkBytes, eaPkBytes, payload, validators)
			},
			txs: func() [][]byte {
				return [][]byte{buildDealTx(valAddr, currentRoundID, validators)}
			},
			wantAccept: false,
		},
		{
			name: "creator is not a ceremony validator → reject",
			setup: func() {
				currentRoundID = app.SeedRegisteringCeremony(validators)
			},
			txs: func() [][]byte {
				return [][]byte{buildDealTx("cosmosvaloper1notincermony", currentRoundID, validators)}
			},
			wantAccept: false,
		},
		{
			name: "creator does not match block proposer → reject",
			setup: func() {
				// Seed a round whose only ceremony validator is NOT the block proposer.
				// Creator passes the ceremony-validator check but fails the proposer check.
				other := []*types.ValidatorPallasKey{{ValidatorAddress: "cosmosvaloper1other"}}
				currentRoundID = app.SeedRegisteringCeremony(other)
			},
			txs: func() [][]byte {
				other := []*types.ValidatorPallasKey{{ValidatorAddress: "cosmosvaloper1other"}}
				return [][]byte{buildDealTx("cosmosvaloper1other", currentRoundID, other)}
			},
			wantAccept: false,
		},
		{
			name: "payload count mismatch → reject",
			setup: func() {
				currentRoundID = app.SeedRegisteringCeremony(validators)
			},
			txs: func() [][]byte {
				msg := &types.MsgDealExecutiveAuthorityKey{
					Creator:     valAddr,
					VoteRoundId: currentRoundID,
					EaPk:        eaPkBytes,
					Payloads:    nil, // 0 payloads for a 1-validator round
				}
				txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagDealExecutiveAuthorityKey)
				require.NoError(t, err)
				return [][]byte{txBytes}
			},
			wantAccept: false,
		},
		{
			name: "malformed deal tx (corrupted protobuf) → reject",
			setup: func() {
				currentRoundID = app.SeedRegisteringCeremony(validators)
			},
			txs: func() [][]byte {
				return [][]byte{{voteapi.TagDealExecutiveAuthorityKey, 0xFF, 0xFF, 0xFF}}
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
		h.Write([]byte(types.AckSigDomain))
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
				// Manually add an ack entry so the round has already been acked by this validator.
				ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.Height})
				kvStore := app.VoteKeeper().OpenKVStore(ctx)
				round, _ := app.VoteKeeper().GetVoteRound(kvStore, currentRoundID)
				round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
					ValidatorAddress: valAddr,
					AckSignature:     bytes.Repeat([]byte{0x01}, 32),
				})
				require.NoError(t, app.VoteKeeper().SetVoteRound(kvStore, round))
				app.NextBlock()
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
		Creator:           "sv1admin",
		SnapshotHeight:    800,
		SnapshotBlockhash: bytes.Repeat([]byte{0x8A}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x8B}, 32),
		VoteEndTime:       uint64(voteEndTime.Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x08}, 32),
		NcRoot:            bytes.Repeat([]byte{0x09}, 32),
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
// Table-driven unit test for validateInjectedDKGContribution
// ---------------------------------------------------------------------------

// TestValidateInjectedDKGContribution exercises the exported
// validateInjectedDKGContribution directly with various good and bad inputs.
func TestValidateInjectedDKGContribution(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	valAddr := ta.ValidatorOperAddr()

	validators := []*types.ValidatorPallasKey{{ValidatorAddress: valAddr}}

	var currentRoundID []byte

	buildDKGTx := func(creator string, roundID []byte) []byte {
		msg := &types.MsgContributeDKG{
			Creator:     creator,
			VoteRoundId: roundID,
		}
		txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagContributeDKG)
		require.NoError(t, err)
		return txBytes
	}

	ctxForValidation := func() sdk.Context {
		return ta.NewUncachedContext(false, cmtproto.Header{
			Height:          ta.Height,
			ProposerAddress: ta.ProposerAddress,
		})
	}

	logger := log.NewNopLogger()

	tests := []struct {
		name        string
		setup       func()
		txBytes     func() []byte
		wantErr     bool
		errContains string
	}{
		{
			name: "valid contribution in REGISTERING state",
			setup: func() {
				currentRoundID = ta.SeedRegisteringCeremony(validators)
			},
			txBytes: func() []byte {
				return buildDKGTx(valAddr, currentRoundID)
			},
		},
		{
			name: "non-existent round → error",
			setup: func() {
				currentRoundID = bytes.Repeat([]byte{0xFF}, 32)
			},
			txBytes: func() []byte {
				return buildDKGTx(valAddr, currentRoundID)
			},
			wantErr: true,
		},
		{
			name: "round not PENDING (ACTIVE) → error",
			setup: func() {
				voteEndTime := ta.Time.Add(1 * time.Hour)
				currentRoundID = ta.SeedVotingSession(&types.MsgCreateVotingSession{
					Creator:           "sv1admin",
					SnapshotHeight:    900,
					SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
					ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
					VoteEndTime:       uint64(voteEndTime.Unix()),
					NullifierImtRoot:  bytes.Repeat([]byte{0xCC}, 32),
					NcRoot:            bytes.Repeat([]byte{0xDD}, 32),
					Proposals:         testutil.SampleProposals(),
				})
			},
			txBytes: func() []byte {
				return buildDKGTx(valAddr, currentRoundID)
			},
			wantErr:     true,
			errContains: "round is not PENDING",
		},
		{
			name: "ceremony not REGISTERING (DEALT) → error",
			setup: func() {
				currentRoundID = ta.SeedDealtCeremony(make([]byte, 32), make([]byte, 32), nil, validators)
			},
			txBytes: func() []byte {
				return buildDKGTx(valAddr, currentRoundID)
			},
			wantErr:     true,
			errContains: "ceremony is not REGISTERING",
		},
		{
			name: "creator not a ceremony validator → error",
			setup: func() {
				currentRoundID = ta.SeedRegisteringCeremony(validators)
			},
			txBytes: func() []byte {
				return buildDKGTx("cosmosvaloper1notinceremony", currentRoundID)
			},
			wantErr:     true,
			errContains: "creator is not a ceremony validator",
		},
		{
			name: "creator does not match block proposer → error",
			setup: func() {
				other := []*types.ValidatorPallasKey{{ValidatorAddress: "cosmosvaloper1other"}}
				currentRoundID = ta.SeedRegisteringCeremony(other)
			},
			txBytes: func() []byte {
				return buildDKGTx("cosmosvaloper1other", currentRoundID)
			},
			wantErr: true,
		},
		{
			name:  "malformed tx (corrupted protobuf) → error",
			setup: func() {},
			txBytes: func() []byte {
				return []byte{voteapi.TagContributeDKG, 0xFF, 0xFF, 0xFF}
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			err := app.ValidateInjectedDKGContribution(ctxForValidation(), ta.VoteKeeper(), tc.txBytes(), logger)
			if tc.wantErr {
				require.Error(t, err, "expected error for case: %s", tc.name)
				if tc.errContains != "" {
					require.Contains(t, err.Error(), tc.errContains)
				}
			} else {
				require.NoError(t, err, "expected no error for case: %s", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Functional tests: PrepareProposal → ProcessProposal pipeline
// ---------------------------------------------------------------------------

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

// ===========================================================================
// F-15: Threshold Downgrade — ProcessProposal Rejection Tests
//
// Verifies that ProcessProposal rejects deal txs whose threshold does not
// match ThresholdForN(n). Each sub-test seeds a fresh REGISTERING round with
// n validators and submits a deal with the given (malicious) threshold.
// ===========================================================================

func TestProcessProposalRejectsDealWithBadThreshold(t *testing.T) {
	tests := []struct {
		name      string
		n         int
		threshold uint32
	}{
		{"n=2 t=1 (downgrade)", 2, 1},
		{"n=3 t=1 (downgrade)", 3, 1},
		{"n=5 t=1 (downgrade)", 5, 1},
		{"n=3 t=100 (above n)", 3, 100},
		{"n=3 t=3 (too high)", 3, 3},
		{"n=10 t=1 (downgrade)", 10, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := testutil.SetupTestApp(t)
			valAddr := app.ValidatorOperAddr()

			_, eaPk := elgamal.KeyGen(rand.Reader)
			eaPkBytes := eaPk.Point.ToAffineCompressed()
			_, ephPk := elgamal.KeyGen(rand.Reader)
			ephPkBytes := ephPk.Point.ToAffineCompressed()

			validators := make([]*types.ValidatorPallasKey, tc.n)
			validators[0] = &types.ValidatorPallasKey{
				ValidatorAddress: valAddr,
				PallasPk:         eaPkBytes,
			}
			for i := 1; i < tc.n; i++ {
				_, pk := elgamal.KeyGen(rand.Reader)
				validators[i] = &types.ValidatorPallasKey{
					ValidatorAddress: fmt.Sprintf("sv1validator%dxxxxxxxxxxxxxxxxxxxxxxxxx", i+1),
					PallasPk:         pk.Point.ToAffineCompressed(),
				}
			}

			roundID := app.SeedRegisteringCeremony(validators)

			payloads := make([]*types.DealerPayload, tc.n)
			for i, v := range validators {
				payloads[i] = &types.DealerPayload{
					ValidatorAddress: v.ValidatorAddress,
					EphemeralPk:      ephPkBytes,
					Ciphertext:       bytes.Repeat([]byte{byte(i + 1)}, 48),
				}
			}

			msg := &types.MsgDealExecutiveAuthorityKey{
				Creator:     valAddr,
				VoteRoundId: roundID,
				EaPk:        eaPkBytes,
				Payloads:    payloads,
				Threshold:   tc.threshold,
			}
			txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagDealExecutiveAuthorityKey)
			require.NoError(t, err)

			resp := app.CallProcessProposal([][]byte{txBytes})
			require.Equal(t, abci.ResponseProcessProposal_REJECT, resp.Status,
				"ProcessProposal must reject threshold=%d for n=%d", tc.threshold, tc.n)
		})
	}
}

// ===========================================================================
// F-15: PrepareProposal → ProcessProposal Pipeline Tests
//
// Verifies that an honest PrepareProposal generates the correct threshold
// for various validator counts and that ProcessProposal accepts the result.
// ===========================================================================

func TestPrepareProposalDealAcceptedByProcessProposal(t *testing.T) {
	tests := []struct {
		name              string
		n                 int
		expectedThreshold uint32
	}{
		{"n=2 → t=2", 2, 2},
		{"n=3 → t=2", 3, 2},
		{"n=5 → t=3", 5, 3},
		{"n=7 → t=4", 7, 4},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
			dealerAddr := ta.ValidatorOperAddr()

			validators := make([]*types.ValidatorPallasKey, tc.n)
			validators[0] = &types.ValidatorPallasKey{
				ValidatorAddress: dealerAddr,
				PallasPk:         pallasPk.Point.ToAffineCompressed(),
			}
			for i := 1; i < tc.n; i++ {
				_, pk := elgamal.KeyGen(rand.Reader)
				validators[i] = &types.ValidatorPallasKey{
					ValidatorAddress: fmt.Sprintf("sv1validator%dxxxxxxxxxxxxxxxxxxxxxxxxx", i+1),
					PallasPk:         pk.Point.ToAffineCompressed(),
				}
			}

			ta.SeedRegisteringCeremony(validators)

			ppResp := ta.CallPrepareProposal()
			require.Len(t, ppResp.Txs, 1, "expected one injected deal tx")

			_, protoMsg, err := voteapi.DecodeCeremonyTx(ppResp.Txs[0])
			require.NoError(t, err)
			deal := protoMsg.(*types.MsgDealExecutiveAuthorityKey)
			require.EqualValues(t, tc.expectedThreshold, deal.Threshold,
				"PrepareProposal threshold for n=%d", tc.n)

			procResp := ta.CallProcessProposal(ppResp.Txs)
			require.Equal(t, abci.ResponseProcessProposal_ACCEPT, procResp.Status,
				"ProcessProposal must accept the honest deal for n=%d", tc.n)
		})
	}
}

