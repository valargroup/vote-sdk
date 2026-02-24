package app_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/types"
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
