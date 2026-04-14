package app_test

import (
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// Key Ceremony Integration Tests
//
// These tests exercise ceremony-related messages through the ABCI pipeline.
// The full per-round ceremony lifecycle (create round → auto-deal → auto-ack
// → ACTIVE) is covered by E2E tests in e2e-tests/.
//
// No CometBFT process or network is involved — just BaseApp method calls.
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// TestKeyCeremonyDuplicateRegistrationRejected
//
// Same validator cannot register twice.
// ---------------------------------------------------------------------------

func TestKeyCeremonyDuplicateRegistrationRejected(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	accAddr := ta.ValidatorAccAddr()
	pkBytes := pallasPk.Point.ToAffineCompressed()

	// First registration succeeds.
	registerPallasKey(t, ta, accAddr, pkBytes)

	// Second registration with the same address should fail.
	msg := &types.MsgRegisterPallasKey{
		Creator:  accAddr,
		PallasPk: pkBytes,
	}
	txBytes := ta.MustBuildSignedCeremonyTx(msg)
	result := ta.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code,
		"duplicate registration should be rejected")
	require.Contains(t, result.Log, "already registered")
}

// ---------------------------------------------------------------------------
// TestRotatePallasKey_ABCI_HappyPath
//
// Full ABCI pipeline: register key → rotate to new key → verify state.
// ---------------------------------------------------------------------------

func TestRotatePallasKey_ABCI_HappyPath(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	accAddr := ta.ValidatorAccAddr()
	oldPK := pallasPk.Point.ToAffineCompressed()

	// Register the initial key.
	registerPallasKey(t, ta, accAddr, oldPK)

	// Generate a fresh Pallas key and rotate.
	_, newPallasPk := elgamal.KeyGen(rand.Reader)
	newPK := newPallasPk.Point.ToAffineCompressed()

	rotateMsg := &types.MsgRotatePallasKey{
		Creator:     accAddr,
		NewPallasPk: newPK,
	}
	txBytes := ta.MustBuildSignedCeremonyTx(rotateMsg)
	result := ta.DeliverVoteTx(txBytes)
	require.Equal(t, uint32(0), result.Code,
		"MsgRotatePallasKey should succeed, got: %s", result.Log)

	// Verify the key was updated in the on-chain registry.
	valoperAddr := ta.ValidatorOperAddr()
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kv := ta.VoteKeeper().OpenKVStore(ctx)
	vpk, err := ta.VoteKeeper().GetPallasKey(kv, valoperAddr)
	require.NoError(t, err)
	require.NotNil(t, vpk)
	require.Equal(t, newPK, vpk.PallasPk, "forward index should point to new PK")

	// Verify old reverse index is cleaned up.
	oldOwner, err := ta.VoteKeeper().GetPallasKeyOwner(kv, oldPK)
	require.NoError(t, err)
	require.Empty(t, oldOwner, "old PK reverse index should be deleted")

	// Verify new reverse index exists.
	newOwner, err := ta.VoteKeeper().GetPallasKeyOwner(kv, newPK)
	require.NoError(t, err)
	require.Equal(t, valoperAddr, newOwner, "new PK reverse index should point to validator")
}

// ---------------------------------------------------------------------------
// TestRotatePallasKey_ABCI_RejectsWithoutRegistration
//
// Rotation without a prior registration is rejected through the full pipeline.
// ---------------------------------------------------------------------------

func TestRotatePallasKey_ABCI_RejectsWithoutRegistration(t *testing.T) {
	ta, _, _, _, _ := testutil.SetupTestAppWithPallasKey(t)

	accAddr := ta.ValidatorAccAddr()
	_, newPallasPk := elgamal.KeyGen(rand.Reader)

	rotateMsg := &types.MsgRotatePallasKey{
		Creator:     accAddr,
		NewPallasPk: newPallasPk.Point.ToAffineCompressed(),
	}
	txBytes := ta.MustBuildSignedCeremonyTx(rotateMsg)
	result := ta.DeliverVoteTx(txBytes)
	require.NotEqual(t, uint32(0), result.Code,
		"rotate without prior registration should be rejected")
	require.Contains(t, result.Log, "no registered pallas key")
}
