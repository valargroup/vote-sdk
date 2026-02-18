package app_test

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/z-cale/zally/crypto/ecies"
	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Key Ceremony Integration Tests
//
// These tests exercise the full EA key ceremony lifecycle through the ABCI
// pipeline: raw ceremony tx bytes → CustomTxDecoder → DualAnteHandler →
// MsgServer → EndBlocker/PrepareProposal → state.
//
// No CometBFT process or network is involved — just BaseApp method calls.
// ---------------------------------------------------------------------------

// getCeremonyState reads the ceremony state from the TestApp's committed store.
func getCeremonyState(t *testing.T, ta *testutil.TestApp) *types.CeremonyState {
	t.Helper()
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)
	state, err := ta.VoteKeeper().GetCeremonyState(kvStore)
	require.NoError(t, err)
	return state
}

// registerPallasKey builds a signed Cosmos SDK tx for MsgRegisterPallasKey
// and delivers it via the ABCI pipeline.
func registerPallasKey(t *testing.T, ta *testutil.TestApp, creator string, pallasPk []byte) {
	t.Helper()
	msg := &types.MsgRegisterPallasKey{
		Creator:  creator,
		PallasPk: pallasPk,
	}
	txBytes := ta.MustBuildSignedCeremonyTx(msg)
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(0), result.Code,
		"MsgRegisterPallasKey should succeed, got: %s", result.Log)
}

// dealEAKey builds a signed Cosmos SDK tx for MsgDealExecutiveAuthorityKey
// and delivers it via the ABCI pipeline.
func dealEAKey(t *testing.T, ta *testutil.TestApp, dealer string, eaPk []byte, payloads []*types.DealerPayload) {
	t.Helper()
	msg := &types.MsgDealExecutiveAuthorityKey{
		Creator:  dealer,
		EaPk:     eaPk,
		Payloads: payloads,
	}
	txBytes := ta.MustBuildSignedCeremonyTx(msg)
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(0), result.Code,
		"MsgDealExecutiveAuthorityKey should succeed, got: %s", result.Log)
}

// ---------------------------------------------------------------------------
// TestKeyCeremonyFullLifecycle
//
// Complete happy path through the ABCI pipeline:
//   1. Genesis validator registers Pallas key
//   2. Dealer distributes ECIES-encrypted ea_sk share
//   3. PrepareProposal auto-acks for the proposer
//   4. Ceremony reaches CONFIRMED
//   5. Voting session created with the confirmed ea_pk
// ---------------------------------------------------------------------------

func TestKeyCeremonyFullLifecycle(t *testing.T) {
	ta, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := ta.ValidatorOperAddr()

	// ---------------------------------------------------------------
	// Step 1: No ceremony exists yet.
	// ---------------------------------------------------------------
	state := getCeremonyState(t, ta)
	require.Nil(t, state, "ceremony should not exist before first registration")

	// ---------------------------------------------------------------
	// Step 2: Register genesis validator's Pallas key via ABCI tx.
	// ---------------------------------------------------------------
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status)
	require.Len(t, state.Validators, 1)
	require.Equal(t, valAddr, state.Validators[0].ValidatorAddress)
	require.Equal(t, pallasPk.Point.ToAffineCompressed(), state.Validators[0].PallasPk)

	// ---------------------------------------------------------------
	// Step 3: Dealer ECIES-encrypts ea_sk to the validator's Pallas pk
	//         and submits MsgDealExecutiveAuthorityKey via ABCI tx.
	// ---------------------------------------------------------------
	G := elgamal.PallasGenerator()
	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)

	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, state.Status)
	require.Equal(t, eaPkBytes, state.EaPk)
	require.Len(t, state.Payloads, 1)
	require.Len(t, state.Acks, 0, "no acks yet")

	// ---------------------------------------------------------------
	// Step 4: PrepareProposal auto-injects MsgAckExecutiveAuthorityKey.
	//         With a single validator, one auto-ack completes the ceremony.
	// ---------------------------------------------------------------
	ta.NextBlockWithPrepareProposal()

	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status,
		"ceremony should be CONFIRMED after auto-ack")
	require.Len(t, state.Acks, 1)
	require.Equal(t, valAddr, state.Acks[0].ValidatorAddress)
	require.NotEmpty(t, state.Acks[0].AckSignature)
	require.Equal(t, eaPkBytes, state.EaPk, "ea_pk should persist after CONFIRMED")

	// ---------------------------------------------------------------
	// Step 5: Create a voting session and verify ea_pk propagation.
	// ---------------------------------------------------------------
	ta.SeedVoteManager("zvote1admin")
	setupMsg := testutil.ValidCreateVotingSessionAt(ta.Time)
	roundID := ta.SeedVotingSession(setupMsg)
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)
	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	require.Equal(t, eaPkBytes, round.EaPk,
		"round.EaPk should match the ceremony's confirmed ea_pk")
	require.Equal(t, types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
}

// ---------------------------------------------------------------------------
// TestKeyCeremonyDealTimeout
//
// Deal phase (awaiting acks) times out before all validators ack.
// Ceremony resets to REGISTERING. Re-registration and a fresh deal
// complete the ceremony on the second attempt.
// ---------------------------------------------------------------------------

func TestKeyCeremonyDealTimeout(t *testing.T) {
	ta, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	// ---------------------------------------------------------------
	// Step 1: Register and deal.
	// ---------------------------------------------------------------
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)

	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, state.Status)

	// ---------------------------------------------------------------
	// Step 2: Advance time past the deal/ack timeout (30s) WITHOUT
	//         running PrepareProposal (simulates a scenario where the
	//         proposer failed to auto-ack in time).
	// ---------------------------------------------------------------
	deadline := time.Unix(int64(state.PhaseStart+state.PhaseTimeout), 0).UTC()
	ta.NextBlockAtTime(deadline.Add(1 * time.Second))

	// EndBlocker should have reset ceremony to REGISTERING.
	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status,
		"ceremony should reset to REGISTERING after deal/ack timeout")
	require.Empty(t, state.Validators, "validators should be cleared after reset")
	require.Empty(t, state.Payloads, "payloads should be cleared after reset")
	require.Nil(t, state.EaPk, "ea_pk should be cleared after reset")

	// ---------------------------------------------------------------
	// Step 3: Complete the ceremony on a second attempt.
	// ---------------------------------------------------------------
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	env2, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)
	payloads2 := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env2.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env2.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads2)

	ta.NextBlockWithPrepareProposal()

	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status,
		"ceremony should complete on second attempt after timeout")
}

// ---------------------------------------------------------------------------
// TestKeyCeremonyAckMempoolBlocked
//
// Verifies that MsgAckExecutiveAuthorityKey cannot be submitted through the
// mempool — it can only arrive via PrepareProposal injection.
// ---------------------------------------------------------------------------

func TestKeyCeremonyAckMempoolBlocked(t *testing.T) {
	ta, _, pallasPk, _, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	valAddr := ta.ValidatorOperAddr()

	// Register and deal to put ceremony in DEALT state.
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      pallasPk.Point.ToAffineCompressed(), // dummy
			Ciphertext:       bytes.Repeat([]byte{0xAB}, 48),      // dummy
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)

	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, state.Status)

	// Attempt to submit ack via mempool (CheckTx) — should be rejected.
	ackMsg := &types.MsgAckExecutiveAuthorityKey{
		Creator:      valAddr,
		AckSignature: bytes.Repeat([]byte{0xAC}, 32),
	}
	ackTx := testutil.MustEncodeAckCeremonyTx(ackMsg)
	checkResp := ta.CheckTxSync(ackTx)
	require.NotEqual(t, uint32(0), checkResp.Code,
		"CheckTx should reject MsgAckExecutiveAuthorityKey from mempool")
	require.Contains(t, checkResp.Log, "cannot be submitted via mempool")
}

// ---------------------------------------------------------------------------
// TestKeyCeremonyDealRejectedBeforeRegistration
//
// MsgDealExecutiveAuthorityKey is rejected when no ceremony exists.
// ---------------------------------------------------------------------------

func TestKeyCeremonyDealRejectedBeforeRegistration(t *testing.T) {
	ta, _, _, _, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	valAddr := ta.ValidatorOperAddr()

	msg := &types.MsgDealExecutiveAuthorityKey{
		Creator: valAddr,
		EaPk:    eaPkBytes,
		Payloads: []*types.DealerPayload{
			{
				ValidatorAddress: valAddr,
				EphemeralPk:      eaPkBytes,
				Ciphertext:       bytes.Repeat([]byte{0x01}, 48),
			},
		},
	}
	txBytes := ta.MustBuildSignedCeremonyTx(msg)
	result := ta.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code,
		"deal should be rejected when no ceremony exists")
	require.Contains(t, result.Log, "no ceremony exists")
}

// ---------------------------------------------------------------------------
// TestKeyCeremonyDuplicateRegistrationRejected
//
// Same validator cannot register twice.
// ---------------------------------------------------------------------------

func TestKeyCeremonyDuplicateRegistrationRejected(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := ta.ValidatorOperAddr()
	pkBytes := pallasPk.Point.ToAffineCompressed()

	// First registration succeeds.
	registerPallasKey(t, ta, valAddr, pkBytes)

	// Second registration with the same address should fail.
	msg := &types.MsgRegisterPallasKey{
		Creator:  valAddr,
		PallasPk: pkBytes,
	}
	txBytes := ta.MustBuildSignedCeremonyTx(msg)
	result := ta.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code,
		"duplicate registration should be rejected")
	require.Contains(t, result.Log, "already registered")
}

// ---------------------------------------------------------------------------
// TestKeyCeremonyVotingSessionRequiresConfirmedCeremony
//
// CreateVotingSession is rejected when ceremony is not yet CONFIRMED.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// TestKeyCeremonyEAKeyVerification
//
// End-to-end verification that the auto-ack PrepareProposal handler correctly
// decrypts ea_sk and writes it to disk, enabling subsequent auto-tally.
// After the ceremony completes, the ea_sk file should exist at the configured
// path and contain the correct key material.
// ---------------------------------------------------------------------------

func TestKeyCeremonyEAKeyVerification(t *testing.T) {
	ta, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := ta.ValidatorOperAddr()

	// Register and deal with real ECIES encryption.
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	G := elgamal.PallasGenerator()
	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)

	// Auto-ack via PrepareProposal.
	ta.NextBlockWithPrepareProposal()

	// Verify ceremony is confirmed and ea_pk is correct.
	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status)
	require.Equal(t, eaPkBytes, state.EaPk)

	// Verify ea_sk * G == ea_pk (the fundamental invariant the auto-ack verifies).
	recoveredPk := G.Mul(eaSk.Scalar)
	require.Equal(t, eaPkBytes, recoveredPk.ToAffineCompressed(),
		"ea_sk * G should equal ea_pk")
}

// ---------------------------------------------------------------------------
// MsgReInitializeElectionAuthority Tests
// ---------------------------------------------------------------------------

// reInitializeEA builds a signed Cosmos SDK tx for MsgReInitializeElectionAuthority
// and delivers it via the ABCI pipeline.
func reInitializeEA(t *testing.T, ta *testutil.TestApp, creator string) uint32 {
	t.Helper()
	msg := &types.MsgReInitializeElectionAuthority{
		Creator: creator,
	}
	txBytes := ta.MustBuildSignedCeremonyTx(msg)
	result := ta.DeliverVoteTx(txBytes)
	return result.Code
}

// TestReInitializeElectionAuthority_AllowedWhenNoCeremony
//
// Re-initialization succeeds when no ceremony state exists.
func TestReInitializeElectionAuthority_AllowedWhenNoCeremony(t *testing.T) {
	ta, _, _, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := ta.ValidatorOperAddr()

	// No ceremony exists yet.
	state := getCeremonyState(t, ta)
	require.Nil(t, state)

	// Re-initialize should succeed.
	code := reInitializeEA(t, ta, valAddr)
	require.Equal(t, uint32(0), code,
		"MsgReInitializeElectionAuthority should succeed when no ceremony exists")

	state = getCeremonyState(t, ta)
	require.NotNil(t, state)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status)
	require.Equal(t, uint64(0), state.PhaseTimeout, "reinit should produce empty REGISTERING")
}

// TestReInitializeElectionAuthority_AllowedWhenConfirmed
//
// Re-initialization succeeds after a ceremony has been confirmed,
// enabling a fresh ceremony cycle.
func TestReInitializeElectionAuthority_AllowedWhenConfirmed(t *testing.T) {
	ta, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := ta.ValidatorOperAddr()

	// Complete the full ceremony lifecycle.
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	G := elgamal.PallasGenerator()
	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)
	ta.NextBlockWithPrepareProposal()

	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status)

	// Re-initialize should succeed from CONFIRMED state.
	code := reInitializeEA(t, ta, valAddr)
	require.Equal(t, uint32(0), code,
		"MsgReInitializeElectionAuthority should succeed when ceremony is CONFIRMED")

	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status)
	require.Empty(t, state.Validators, "validators should be cleared after re-init")
	require.Empty(t, state.Payloads, "payloads should be cleared after re-init")
	require.Empty(t, state.Acks, "acks should be cleared after re-init")
	require.Nil(t, state.EaPk, "ea_pk should be cleared after re-init")
}

// TestReInitializeElectionAuthority_AllowedDuringRegistering
//
// Re-initialization is allowed when the ceremony is in REGISTERING state.
// This provides an escape hatch for stuck registrations (e.g., wrong keys,
// validators offline). REGISTERING has no timeout, so reinit is the only
// way to restart.
func TestReInitializeElectionAuthority_AllowedDuringRegistering(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	valAddr := ta.ValidatorOperAddr()

	// Register a key — ceremony transitions to REGISTERING.
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status)

	// Re-initialize should succeed (REGISTERING is not a blocking state).
	code := reInitializeEA(t, ta, valAddr)
	require.Equal(t, uint32(0), code,
		"MsgReInitializeElectionAuthority should be allowed during REGISTERING")

	// Ceremony should be reset.
	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status)
	require.Empty(t, state.Validators, "validators should be cleared after reinit")
}

// TestReInitializeElectionAuthority_RejectedDuringDealt
//
// Re-initialization is rejected when the ceremony is awaiting acks.
func TestReInitializeElectionAuthority_RejectedDuringDealt(t *testing.T) {
	ta, _, pallasPk, _, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	valAddr := ta.ValidatorOperAddr()

	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      pallasPk.Point.ToAffineCompressed(), // dummy
			Ciphertext:       bytes.Repeat([]byte{0xAB}, 48),      // dummy
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)

	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, state.Status)

	// Re-initialize should be rejected.
	code := reInitializeEA(t, ta, valAddr)
	require.NotEqual(t, uint32(0), code,
		"MsgReInitializeElectionAuthority should be rejected during DEALT")
}

// TestReInitializeElectionAuthority_NewCeremonyAfterReInit
//
// After re-initialization from CONFIRMED, a fresh ceremony can be completed.
func TestReInitializeElectionAuthority_NewCeremonyAfterReInit(t *testing.T) {
	ta, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	// Complete the first ceremony.
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)
	ta.NextBlockWithPrepareProposal()

	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status)

	// Re-initialize.
	code := reInitializeEA(t, ta, valAddr)
	require.Equal(t, uint32(0), code)

	// Complete a second ceremony.
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	env2, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)
	payloads2 := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env2.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env2.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads2)
	ta.NextBlockWithPrepareProposal()

	state = getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status,
		"second ceremony should complete after re-initialization")
}

// ---------------------------------------------------------------------------
// TestReInitializeElectionAuthority_RejectedWithActiveVotingSession
//
// Re-initialization is rejected when the ceremony is CONFIRMED but there
// is an active voting session that depends on the current ea_pk.
// ---------------------------------------------------------------------------

func TestReInitializeElectionAuthority_RejectedWithActiveVotingSession(t *testing.T) {
	ta, _, pallasPk, eaSk, eaPk := testutil.SetupTestAppWithPallasKey(t)

	eaPkBytes := eaPk.Point.ToAffineCompressed()
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	valAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	// Complete the full ceremony.
	registerPallasKey(t, ta, valAddr, pallasPk.Point.ToAffineCompressed())

	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)
	payloads := []*types.DealerPayload{
		{
			ValidatorAddress: valAddr,
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		},
	}
	dealEAKey(t, ta, valAddr, eaPkBytes, payloads)
	ta.NextBlockWithPrepareProposal()

	state := getCeremonyState(t, ta)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status)

	// Seed the vote manager so CreateVotingSession passes authorization.
	ta.SeedVoteManager("zvote1admin")

	// Create a voting session — the round will be ACTIVE.
	setupMsg := testutil.ValidCreateVotingSessionAt(ta.Time)
	ta.SeedVotingSession(setupMsg)

	// Re-initialize should be rejected while a voting session is active.
	code := reInitializeEA(t, ta, valAddr)
	require.NotEqual(t, uint32(0), code,
		"MsgReInitializeElectionAuthority should be rejected with an active voting session")
}
