package types_test

import (
	"bytes"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestValidateDelegateBallotOptions checks the option rule of contracts-v1
// §5.3 ballot step 6 and its code, 58: one option per proposal, each below
// its proposal's option count.
func TestValidateDelegateBallotOptions(t *testing.T) {
	options := func(n int) []*types.VoteOption { return make([]*types.VoteOption, n) }
	round := &types.VoteRound{Proposals: []*types.Proposal{{Id: 1, Options: options(2)}, {Id: 2, Options: options(8)}}}
	require.NoError(t, types.ValidateDelegateBallotOptions(round, []uint32{1, 7}))
	require.NoError(t, types.ValidateDelegateBallotOptions(round, []uint32{0, 0}))

	withNilProposal := &types.VoteRound{Proposals: []*types.Proposal{{Id: 1, Options: options(2)}, nil}}
	for name, c := range map[string]struct {
		round   *types.VoteRound
		options []uint32
	}{
		"too few options":           {round, []uint32{0}},
		"too many options":          {round, []uint32{0, 0, 0}},
		"first option too large":    {round, []uint32{2, 0}},
		"last option too large":     {round, []uint32{0, 8}},
		"largest uint32":            {round, []uint32{0, 4294967295}},
		"a round without proposals": {&types.VoteRound{}, []uint32{0}},
		"a nil round":               {nil, []uint32{0}},
		"a nil proposal":            {withNilProposal, []uint32{0, 0}},
	} {
		t.Run(name, func(t *testing.T) {
			err := types.ValidateDelegateBallotOptions(c.round, c.options)
			require.ErrorIs(t, err, types.ErrInvalidBallot)
			codespace, code, _ := errorsmod.ABCIInfo(err, false)
			require.Equal(t, types.ModuleName, codespace)
			require.Equal(t, uint32(58), code)
		})
	}
}

// TestDelegateBallotPrefixForRound checks that a round's ballot prefix
// starts each of its ballot keys and that a round id must be 32 bytes.
func TestDelegateBallotPrefixForRound(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xAB}, 32)
	prefix, err := types.DelegateBallotPrefixForRound(roundID)
	require.NoError(t, err)
	require.Equal(t, append([]byte{0x1B}, roundID...), prefix)
	key, err := types.DelegateBallotKey(roundID, 17)
	require.NoError(t, err)
	require.Equal(t, append(prefix, 0, 0, 0, 17), key)

	_, err = types.DelegateBallotPrefixForRound(roundID[:31])
	require.ErrorIs(t, err, types.ErrInvalidRoundIDLen)
}

// validProxyRoundGenesis returns validRegistryGenesis with proxy rounds in
// every status and ballots, as live state would export them: an ACTIVE
// round with two ballots, a PENDING round, a TALLYING and a FINALIZED round
// with routed pools and a ballot each, a CEREMONY_FAILED round, and a round
// without the flag. Each round has proposals of 2, 3 and 4 options, numbered
// by position as round creation requires.
func validProxyRoundGenesis() *types.GenesisState {
	gs := validRegistryGenesis()
	round := func(seed byte, status types.SessionStatus, routed bool) *types.VoteRound {
		round := proxytest.Round(bytes.Repeat([]byte{seed}, 32), 2, 3, 4)
		round.VoteEndTime = 2_000_000
		round.Status = status
		round.ProxyDelegation = &types.ProxyDelegationRoundState{Enabled: true, NextDelegateIndexAtCreation: 2, PoolsRouted: routed}
		return round
	}
	plain := round(0xA6, types.SessionStatus_SESSION_STATUS_ACTIVE, false)
	plain.ProxyDelegation = nil
	gs.Rounds = []*types.VoteRound{
		round(0xA1, types.SessionStatus_SESSION_STATUS_ACTIVE, false),
		round(0xA2, types.SessionStatus_SESSION_STATUS_PENDING, false),
		round(0xA3, types.SessionStatus_SESSION_STATUS_TALLYING, true),
		round(0xA4, types.SessionStatus_SESSION_STATUS_FINALIZED, true),
		round(0xA5, types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED, false),
		plain,
	}
	ballot := func(seed byte, d uint32, options ...uint32) *types.DelegateBallotRecord {
		return &types.DelegateBallotRecord{VoteRoundId: bytes.Repeat([]byte{seed}, 32), DelegateIndex: d, Options: options, Height: 7}
	}
	gs.ProxyDelegateBallots = []*types.DelegateBallotRecord{
		ballot(0xA1, 1, 1, 2, 3),
		ballot(0xA1, 3, 0, 0, 0),
		ballot(0xA3, 1, 0, 1, 2),
		ballot(0xA4, 2, 1, 0, 3),
	}
	return gs
}

// TestValidateProxyRoundGenesis checks the import validation of
// contracts-v1 §9 for rounds and ballots: the valid state passes, and every
// broken rule is refused, directly and through ValidateGenesisState.
func TestValidateProxyRoundGenesis(t *testing.T) {
	require.NoError(t, types.ValidateProxyRoundGenesis(validProxyRoundGenesis()))
	require.NoError(t, types.ValidateProxyRoundGenesis(&types.GenesisState{}), "no rounds")
	require.NoError(t, types.ValidateGenesisState(withValidGenesis(validProxyRoundGenesis())))

	// Rounds and ballots are indexed as validProxyRoundGenesis lists them.
	const active, pending, tallying, finalized, failed, plain = 0, 1, 2, 3, 4, 5
	for _, c := range []struct {
		name   string
		mutate func(gs *types.GenesisState)
		want   string
	}{
		{"pools routed in an ACTIVE round", func(gs *types.GenesisState) { gs.Rounds[active].ProxyDelegation.PoolsRouted = true }, "pools_routed is true in a SESSION_STATUS_ACTIVE round"},
		{"pools routed in a PENDING round", func(gs *types.GenesisState) { gs.Rounds[pending].ProxyDelegation.PoolsRouted = true }, "pools_routed is true"},
		{"pools routed in a CEREMONY_FAILED round", func(gs *types.GenesisState) { gs.Rounds[failed].ProxyDelegation.PoolsRouted = true }, "pools_routed is true"},
		{"pools not routed in a TALLYING round", func(gs *types.GenesisState) { gs.Rounds[tallying].ProxyDelegation.PoolsRouted = false }, "pools_routed is false in a SESSION_STATUS_TALLYING round"},
		{"pools not routed in a FINALIZED round", func(gs *types.GenesisState) { gs.Rounds[finalized].ProxyDelegation.PoolsRouted = false }, "pools_routed is false"},
		{"field 31 without enabled", func(gs *types.GenesisState) { gs.Rounds[plain].ProxyDelegation = &types.ProxyDelegationRoundState{} }, "set but not enabled"},
		{"a proposal id out of position", func(gs *types.GenesisState) { gs.Rounds[active].Proposals[0].Id = 7 }, "proposal id mismatch at index 0: expected 1, got 7"},
		{"a nil proposal", func(gs *types.GenesisState) { gs.Rounds[pending].Proposals[1] = nil }, "proposal id mismatch at index 1: expected 2, got 0"},
		{"an option index out of position", func(gs *types.GenesisState) { gs.Rounds[finalized].Proposals[2].Options[3].Index = 2 }, "proposal 2 option index mismatch at position 3: expected 3, got 2"},
		{"a nil ballot", func(gs *types.GenesisState) { gs.ProxyDelegateBallots[1] = nil }, "cannot be nil"},
		{"a ballot in a missing round", func(gs *types.GenesisState) {
			gs.ProxyDelegateBallots[1].VoteRoundId = bytes.Repeat([]byte{0xEE}, 32)
		}, "does not exist"},
		{"a ballot in a round without the flag", func(gs *types.GenesisState) {
			gs.ProxyDelegateBallots[1].VoteRoundId = gs.Rounds[plain].VoteRoundId
		}, "does not have the proxy flag"},
		{"a ballot of a missing delegate", func(gs *types.GenesisState) { gs.ProxyDelegateBallots[1].DelegateIndex = 5 }, "delegate 5 does not exist"},
		{"a ballot of delegate 0", func(gs *types.GenesisState) { gs.ProxyDelegateBallots[1].DelegateIndex = 0 }, "delegate 0 does not exist"},
		{"two ballots for one (round, d)", func(gs *types.GenesisState) {
			gs.ProxyDelegateBallots[1].DelegateIndex = 1
			gs.ProxyDelegateBallots[1].Options = []uint32{1, 2, 3}
		}, "a second ballot of delegate 1"},
		{"an incomplete ballot", func(gs *types.GenesisState) { gs.ProxyDelegateBallots[2].Options = []uint32{0, 1} }, "2 options for 3 proposals"},
		{"an extra option", func(gs *types.GenesisState) { gs.ProxyDelegateBallots[2].Options = []uint32{0, 1, 2, 0} }, "4 options for 3 proposals"},
		{"an option out of range", func(gs *types.GenesisState) { gs.ProxyDelegateBallots[3].Options = []uint32{1, 3, 3} }, "options[1] is 3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			gs := validProxyRoundGenesis()
			c.mutate(gs)
			require.ErrorContains(t, types.ValidateProxyRoundGenesis(gs), c.want)
			require.ErrorContains(t, types.ValidateGenesisState(withValidGenesis(gs)), c.want, "through ValidateGenesisState")
		})
	}
}

// withValidGenesis returns validGenesis with the rounds and the proxy
// delegation fields of gs added.
func withValidGenesis(gs *types.GenesisState) *types.GenesisState {
	full := validGenesis()
	full.Rounds = append(full.Rounds, gs.Rounds...)
	full.ProxyDelegates, full.ProxyParams = gs.ProxyDelegates, gs.ProxyParams
	full.ProxyDelegateBallots = gs.ProxyDelegateBallots
	return full
}

// TestValidateProxyRoundGenesisNumbering checks the numbering rule's error
// and scope: a flagged round with proposal id 7 at position 0 fails with
// ErrInvalidField, as round creation does, and a round without the flag is
// accepted however it is numbered, as before proxy delegation.
func TestValidateProxyRoundGenesisNumbering(t *testing.T) {
	const active, plain = 0, 5
	gs := validProxyRoundGenesis()
	gs.Rounds[active].Proposals[0].Id = 7
	require.ErrorIs(t, types.ValidateProxyRoundGenesis(gs), types.ErrInvalidField)
	require.ErrorIs(t, types.ValidateGenesisState(withValidGenesis(gs)), types.ErrInvalidField)

	gs = validProxyRoundGenesis()
	gs.Rounds[plain].Proposals[0].Id, gs.Rounds[plain].Proposals[1].Id = 7, 7
	gs.Rounds[plain].Proposals[2].Options[0].Index = 9
	gs.Rounds[plain].Proposals = append(gs.Rounds[plain].Proposals, nil)
	require.NoError(t, types.ValidateProxyRoundGenesis(gs))
	require.NoError(t, types.ValidateGenesisState(withValidGenesis(gs)))
}
