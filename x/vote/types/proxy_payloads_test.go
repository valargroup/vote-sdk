package types_test

import (
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// payloadCreator is the coordinator that creates the payload fixtures.
var payloadCreator = svtest.TestAccAddr(0x71)

// requireRegisteredCode fails unless err is want, its message contains
// contains, and want's registered code surfaces in codespace vote
// (contracts-v1 §0, §6).
func requireRegisteredCode(t *testing.T, err error, want *errorsmod.Error, code uint32, contains string) {
	t.Helper()
	require.ErrorIs(t, err, want)
	require.ErrorContains(t, err, contains)
	codespace, gotCode, _ := errorsmod.ABCIInfo(err, false)
	require.Equal(t, types.ModuleName, codespace)
	require.Equal(t, code, gotCode)
}

// validParamsPayload returns a params payload listing two verifiers at
// threshold 2.
func validParamsPayload() *types.MsgSetProxyDelegationParams {
	params := validProxyParams(2)
	return &types.MsgSetProxyDelegationParams{
		Creator:           payloadCreator,
		Verifiers:         params.Verifiers,
		VerifierThreshold: params.VerifierThreshold,
		MaxDelegates:      params.MaxDelegates,
	}
}

// validStatusPayload returns a payload that suspends delegates 3 and 4 for
// impersonation.
func validStatusPayload() *types.MsgSetDelegateStatus {
	return &types.MsgSetDelegateStatus{
		Creator:         payloadCreator,
		DelegateIndexes: []uint32{3, 4},
		Action:          types.DelegateStatusActionSuspend,
		Reason:          types.DelegateStatusReasonImpersonation,
	}
}

// TestMsgSetProxyDelegationParamsValidateBasic checks the params payload's
// creator and that it applies the params rules with their §6 codes: 66 for a
// verifier key that fails the key rule, 68 otherwise.
// TestValidateProxyDelegationParams covers each params rule.
func TestMsgSetProxyDelegationParamsValidateBasic(t *testing.T) {
	require.NoError(t, validParamsPayload().ValidateBasic())
	require.NoError(t, (&types.MsgSetProxyDelegationParams{Creator: payloadCreator}).ValidateBasic(), "no verifiers closes the set")

	for _, c := range []struct {
		name     string
		mutate   func(*types.MsgSetProxyDelegationParams)
		want     *errorsmod.Error
		code     uint32
		contains string
	}{
		{"empty creator", func(m *types.MsgSetProxyDelegationParams) { m.Creator = "" }, types.ErrInvalidProxyParams, 68, "creator"},
		{"pubkey fails the key rule", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers[1].Pubkey = identityKey }, types.ErrInvalidDelegateKey, 66, "verifiers[1].pubkey"},
		{"max_delegates above 2000", func(m *types.MsgSetProxyDelegationParams) { m.MaxDelegates = 2001 }, types.ErrInvalidProxyParams, 68, "max_delegates must be at most 2000"},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := validParamsPayload()
			c.mutate(msg)
			requireRegisteredCode(t, msg.ValidateBasic(), c.want, c.code, c.contains)
		})
	}
}

// TestMsgSetProxyDelegationPauseValidateBasic checks that the pause payload
// needs only a valid creator, and that a bad one is code 9.
func TestMsgSetProxyDelegationPauseValidateBasic(t *testing.T) {
	for _, paused := range []bool{true, false} {
		require.NoError(t, (&types.MsgSetProxyDelegationPause{Creator: payloadCreator, Paused: paused}).ValidateBasic())
	}
	requireInvalidField(t, (&types.MsgSetProxyDelegationPause{Creator: "not-an-address"}).ValidateBasic(), "creator")
}

// TestMsgSetDelegateStatusValidateBasic checks the status payload's §2.5
// rules, every failure code 9 (§6).
func TestMsgSetDelegateStatusValidateBasic(t *testing.T) {
	for _, action := range []uint32{1, 2, 3} {
		msg := validStatusPayload()
		msg.Action, msg.Reason = action, types.DelegateStatusReasonOther
		require.NoError(t, msg.ValidateBasic(), "action %d", action)
	}
	full := validStatusPayload()
	full.DelegateIndexes = nil
	for i := range types.MaxDelegateStatusIndexes {
		full.DelegateIndexes = append(full.DelegateIndexes, uint32(i+1))
	}
	require.NoError(t, full.ValidateBasic(), "64 delegates")

	for _, c := range []struct {
		name   string
		mutate func(*types.MsgSetDelegateStatus)
		want   string
	}{
		{"empty creator", func(m *types.MsgSetDelegateStatus) { m.Creator = "" }, "creator"},
		{"no delegates", func(m *types.MsgSetDelegateStatus) { m.DelegateIndexes = nil }, "delegate_indexes count must be between 1 and 64, got 0"},
		{"65 delegates", func(m *types.MsgSetDelegateStatus) {
			for i := uint32(5); len(m.DelegateIndexes) < types.MaxDelegateStatusIndexes+1; i++ {
				m.DelegateIndexes = append(m.DelegateIndexes, i)
			}
		}, "got 65"},
		{"index zero", func(m *types.MsgSetDelegateStatus) { m.DelegateIndexes[0] = 0 }, "delegate_indexes[0] must be at least 1"},
		{"repeated index", func(m *types.MsgSetDelegateStatus) { m.DelegateIndexes[1] = 3 }, "delegate_indexes[1] repeats 3"},
		{"action 4", func(m *types.MsgSetDelegateStatus) { m.Action = 4 }, "action must be 1, 2 or 3, got 4"},
		{"reason 5", func(m *types.MsgSetDelegateStatus) { m.Reason = 5 }, "reason must be 1, 2, 3, 4 or 99, got 5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := validStatusPayload()
			c.mutate(msg)
			requireInvalidField(t, msg.ValidateBasic(), c.want)
		})
	}
}
