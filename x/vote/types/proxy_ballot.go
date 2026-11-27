package types

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
)

// ValidateDelegateBallotOptions checks a delegate ballot's options against its
// round (docs/proxy-delegation/contracts-v1.md §5.3 ballot step 6): one option
// per proposal, and options[i] below the option count of proposal i + 1. A
// failure is ErrInvalidBallot.
func ValidateDelegateBallotOptions(round *VoteRound, options []uint32) error {
	proposals := round.GetProposals()
	if len(options) != len(proposals) {
		return errorsmod.Wrapf(ErrInvalidBallot, "%d options for %d proposals", len(options), len(proposals))
	}
	for i, option := range options {
		if n := len(proposals[i].GetOptions()); uint64(option) >= uint64(n) {
			return errorsmod.Wrapf(ErrInvalidBallot, "options[%d] is %d, but proposal %d has %d options", i, option, i+1, n)
		}
	}
	return nil
}

// ValidateProxyRoundGenesis checks the per-round proxy delegation state of a
// genesis state against contracts-v1 §9: field 31, when set, is enabled and
// has pools_routed exactly when the round is TALLYING or FINALIZED; a flagged
// round numbers proposals from 1 and options from 0, as routing assumes
// (ErrInvalidField, as at creation); and every ballot (field 20) names a
// flagged round and an existing entry, once per (round, d), with valid options.
func ValidateProxyRoundGenesis(gs *GenesisState) error {
	rounds := make(map[string]*VoteRound, len(gs.GetRounds()))
	for i, round := range gs.GetRounds() {
		rounds[string(round.GetVoteRoundId())] = round
		state := round.GetProxyDelegation()
		if state == nil {
			continue
		}
		if !state.Enabled {
			return fmt.Errorf("rounds[%d].proxy_delegation is set but not enabled", i)
		}
		for p, proposal := range round.GetProposals() {
			if proposal.GetId() != uint32(p+1) {
				return fmt.Errorf("rounds[%d]: %w: proposal id mismatch at index %d: expected %d, got %d",
					i, ErrInvalidField, p, p+1, proposal.GetId())
			}
			for o, option := range proposal.GetOptions() {
				if option.GetIndex() != uint32(o) {
					return fmt.Errorf("rounds[%d]: %w: proposal %d option index mismatch at position %d: expected %d, got %d",
						i, ErrInvalidField, p, o, o, option.GetIndex())
				}
			}
		}
		routed := round.Status == SessionStatus_SESSION_STATUS_TALLYING || round.Status == SessionStatus_SESSION_STATUS_FINALIZED
		if state.PoolsRouted != routed {
			return fmt.Errorf("rounds[%d].proxy_delegation.pools_routed is %t in a %s round", i, state.PoolsRouted, round.Status)
		}
	}

	entries := make(map[uint32]struct{}, len(gs.GetProxyDelegates()))
	for _, entry := range gs.GetProxyDelegates() {
		if entry != nil {
			entries[entry.Index] = struct{}{}
		}
	}
	type ballotKey struct {
		round string
		d     uint32
	}
	seen := make(map[ballotKey]struct{}, len(gs.GetProxyDelegateBallots()))
	for i, ballot := range gs.GetProxyDelegateBallots() {
		if ballot == nil {
			return fmt.Errorf("proxy_delegate_ballots[%d] cannot be nil", i)
		}
		round, ok := rounds[string(ballot.VoteRoundId)]
		switch {
		case !ok:
			return fmt.Errorf("proxy_delegate_ballots[%d]: round %x does not exist", i, ballot.VoteRoundId)
		case !round.GetProxyDelegation().GetEnabled():
			return fmt.Errorf("proxy_delegate_ballots[%d]: round %x does not have the proxy flag", i, ballot.VoteRoundId)
		}
		if _, ok := entries[ballot.DelegateIndex]; !ok {
			return fmt.Errorf("proxy_delegate_ballots[%d]: delegate %d does not exist", i, ballot.DelegateIndex)
		}
		key := ballotKey{string(ballot.VoteRoundId), ballot.DelegateIndex}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("proxy_delegate_ballots[%d]: a second ballot of delegate %d in round %x", i, ballot.DelegateIndex, ballot.VoteRoundId)
		}
		seen[key] = struct{}{}
		if err := ValidateDelegateBallotOptions(round, ballot.Options); err != nil {
			return fmt.Errorf("proxy_delegate_ballots[%d]: %w", i, err)
		}
	}
	return nil
}
