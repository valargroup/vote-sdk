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
// §2.5 rules and their §6 codes: 66 for a verifier key that fails the key
// rule, 68 for every other failure.
func TestMsgSetProxyDelegationParamsValidateBasic(t *testing.T) {
	sixteen := validParamsPayload()
	sixteen.Verifiers, sixteen.VerifierThreshold = validProxyParams(16).Verifiers, 16
	for name, msg := range map[string]*types.MsgSetProxyDelegationParams{
		"two verifiers, 2-of-2":       validParamsPayload(),
		"sixteen verifiers":           sixteen,
		"no verifiers closes the set": {Creator: payloadCreator},
		"largest max_delegates":       {Creator: payloadCreator, Verifiers: validProxyParams(1).Verifiers, VerifierThreshold: 1, MaxDelegates: types.MaxDelegates},
	} {
		require.NoError(t, msg.ValidateBasic(), name)
	}

	var nilPayload *types.MsgSetProxyDelegationParams
	requireRegisteredCode(t, nilPayload.ValidateBasic(), types.ErrInvalidProxyParams, 68, "cannot be nil")

	for _, c := range []struct {
		name     string
		mutate   func(*types.MsgSetProxyDelegationParams)
		want     *errorsmod.Error
		code     uint32
		contains string
	}{
		{"empty creator", func(m *types.MsgSetProxyDelegationParams) { m.Creator = "" }, types.ErrInvalidProxyParams, 68, "creator"},
		{"creator not bech32", func(m *types.MsgSetProxyDelegationParams) { m.Creator = "not-an-address" }, types.ErrInvalidProxyParams, 68, "creator"},
		{"seventeen verifiers", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers = validProxyParams(17).Verifiers }, types.ErrInvalidProxyParams, 68, "at most 16 verifiers"},
		{"nil verifier", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers[1] = nil }, types.ErrInvalidProxyParams, 68, "cannot be nil"},
		{"repeated id", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers[1].VerifierId = 1 }, types.ErrInvalidProxyParams, 68, "repeats verifier_id 1"},
		{"short pubkey", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers[1].Pubkey = m.Verifiers[1].Pubkey[:31] }, types.ErrInvalidProxyParams, 68, "must be 32 bytes"},
		{"repeated pubkey", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers[1].Pubkey = m.Verifiers[0].Pubkey }, types.ErrInvalidProxyParams, 68, "repeats a pubkey"},
		{"pubkey fails the key rule", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers[1].Pubkey = identityKey }, types.ErrInvalidDelegateKey, 66, "verifiers[1].pubkey"},
		{"threshold 0 with verifiers", func(m *types.MsgSetProxyDelegationParams) { m.VerifierThreshold = 0 }, types.ErrInvalidProxyParams, 68, "verifier_threshold must be 1..2"},
		{"threshold above the verifier count", func(m *types.MsgSetProxyDelegationParams) { m.VerifierThreshold = 3 }, types.ErrInvalidProxyParams, 68, "got 3"},
		{"threshold without verifiers", func(m *types.MsgSetProxyDelegationParams) { m.Verifiers, m.VerifierThreshold = nil, 1 }, types.ErrInvalidProxyParams, 68, "must be 0 without verifiers"},
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
	var nilPayload *types.MsgSetProxyDelegationPause
	requireInvalidField(t, nilPayload.ValidateBasic(), "cannot be nil")
	requireInvalidField(t, (&types.MsgSetProxyDelegationPause{Paused: true}).ValidateBasic(), "creator")
	requireInvalidField(t, (&types.MsgSetProxyDelegationPause{Creator: "not-an-address"}).ValidateBasic(), "creator")
}

// TestMsgSetDelegateStatusValidateBasic checks the status payload's §2.5
// rules, every failure code 9 (§6).
func TestMsgSetDelegateStatusValidateBasic(t *testing.T) {
	for _, action := range []uint32{1, 2, 3} {
		for _, reason := range []uint32{1, 2, 3, 4, 99} {
			msg := validStatusPayload()
			msg.Action, msg.Reason = action, reason
			require.NoError(t, msg.ValidateBasic(), "action %d reason %d", action, reason)
		}
	}
	full := validStatusPayload()
	full.DelegateIndexes = nil
	for i := range types.MaxDelegateStatusIndexes {
		full.DelegateIndexes = append(full.DelegateIndexes, uint32(i+1))
	}
	require.NoError(t, full.ValidateBasic(), "64 delegates")

	var nilPayload *types.MsgSetDelegateStatus
	requireInvalidField(t, nilPayload.ValidateBasic(), "cannot be nil")

	for _, c := range []struct {
		name   string
		mutate func(*types.MsgSetDelegateStatus)
		want   string
	}{
		{"empty creator", func(m *types.MsgSetDelegateStatus) { m.Creator = "" }, "creator"},
		{"creator not bech32", func(m *types.MsgSetDelegateStatus) { m.Creator = "not-an-address" }, "creator"},
		{"no delegates", func(m *types.MsgSetDelegateStatus) { m.DelegateIndexes = nil }, "delegate_indexes count must be between 1 and 64, got 0"},
		{"65 delegates", func(m *types.MsgSetDelegateStatus) {
			for i := uint32(5); len(m.DelegateIndexes) < types.MaxDelegateStatusIndexes+1; i++ {
				m.DelegateIndexes = append(m.DelegateIndexes, i)
			}
		}, "got 65"},
		{"index zero", func(m *types.MsgSetDelegateStatus) { m.DelegateIndexes[0] = 0 }, "delegate_indexes[0] must be at least 1"},
		{"repeated index", func(m *types.MsgSetDelegateStatus) { m.DelegateIndexes[1] = 3 }, "delegate_indexes[1] repeats 3"},
		{"action 0", func(m *types.MsgSetDelegateStatus) { m.Action = 0 }, "action must be 1, 2 or 3, got 0"},
		{"action 4", func(m *types.MsgSetDelegateStatus) { m.Action = 4 }, "got 4"},
		{"reason 0", func(m *types.MsgSetDelegateStatus) { m.Reason = 0 }, "reason must be 1, 2, 3, 4 or 99, got 0"},
		{"reason 5", func(m *types.MsgSetDelegateStatus) { m.Reason = 5 }, "got 5"},
		{"reason 100", func(m *types.MsgSetDelegateStatus) { m.Reason = 100 }, "got 100"},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := validStatusPayload()
			c.mutate(msg)
			requireInvalidField(t, msg.ValidateBasic(), c.want)
		})
	}
}
