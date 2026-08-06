package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ValidateAndNormalizeVoteManagerSet parses each address through bech32, rejects
// duplicates (on the canonical form, so mixed-case variants don't slip
// through), and returns the canonical list. The input list is not mutated.
// Returns ErrEmptyVoteManagerSet when the list is empty.
//
// Shared by ValidateGenesisState and the MsgUpdateVoteManagers handler so both
// paths apply the same admissibility rules.
func ValidateAndNormalizeVoteManagerSet(addrs []string) ([]string, error) {
	normalized, _, err := ValidateAndNormalizeVoteManagerPolicy(addrs, 1)
	return normalized, err
}

// NormalizeVoteManagerThreshold applies the genesis/config default. A zero
// threshold means "use default 1"; any non-zero value is preserved for
// validation against the manager set.
func NormalizeVoteManagerThreshold(threshold uint32) uint32 {
	if threshold == 0 {
		return 1
	}
	return threshold
}

// NormalizeMinCeremonyValidators applies the default minimum ceremony validator
// count. Zero means "use default 1".
func NormalizeMinCeremonyValidators(minValidators uint32) uint32 {
	if minValidators == 0 {
		return 1
	}
	return minValidators
}

// ValidateAndNormalizeVoteManagerPolicy parses each manager address through
// bech32, rejects duplicates, applies the default threshold, and enforces
// 1 <= threshold <= len(addrs). The input list is not mutated.
func ValidateAndNormalizeVoteManagerPolicy(addrs []string, threshold uint32) ([]string, uint32, error) {
	if len(addrs) == 0 {
		return nil, 0, fmt.Errorf("%w", ErrEmptyVoteManagerSet)
	}
	seen := make(map[string]struct{}, len(addrs))
	normalized := make([]string, 0, len(addrs))
	for i, addr := range addrs {
		acc, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, 0, fmt.Errorf("[%d] %q is not a valid bech32 address: %w", i, addr, err)
		}
		canonical := acc.String()
		if _, dup := seen[canonical]; dup {
			return nil, 0, fmt.Errorf("%w: %s", ErrDuplicateVoteManager, canonical)
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	effectiveThreshold := NormalizeVoteManagerThreshold(threshold)
	if effectiveThreshold < 1 {
		return nil, 0, fmt.Errorf("%w: vote-manager threshold must be at least 1", ErrInvalidThreshold)
	}
	if effectiveThreshold > uint32(len(normalized)) {
		return nil, 0, fmt.Errorf("%w: vote-manager threshold %d exceeds manager count %d", ErrInvalidThreshold, effectiveThreshold, len(normalized))
	}
	return normalized, effectiveThreshold, nil
}

// ValidateGenesisState performs structural validation of the vote module genesis state.
func ValidateGenesisState(gs *GenesisState) error {
	if gs == nil {
		return nil
	}

	// Validate rounds: IDs must be 32 bytes, no duplicates.
	seenRounds := make(map[string]struct{}, len(gs.Rounds))
	for i, round := range gs.Rounds {
		if len(round.VoteRoundId) != RoundIDLen {
			return fmt.Errorf("rounds[%d].vote_round_id is %d bytes, expected %d", i, len(round.VoteRoundId), RoundIDLen)
		}
		if round.VoteEndTime == 0 {
			return fmt.Errorf("rounds[%d].vote_end_time cannot be zero", i)
		}
		key := string(round.VoteRoundId)
		if _, dup := seenRounds[key]; dup {
			return fmt.Errorf("rounds[%d]: duplicate vote_round_id %x", i, round.VoteRoundId)
		}
		seenRounds[key] = struct{}{}
	}

	// Validate nullifiers: type in {0,1,2}, round_id is 32 bytes, nullifier is non-empty.
	for i, entry := range gs.Nullifiers {
		if entry.NullifierType > 2 {
			return fmt.Errorf("nullifiers[%d].nullifier_type is %d, expected 0-2", i, entry.NullifierType)
		}
		if len(entry.RoundId) != RoundIDLen {
			return fmt.Errorf("nullifiers[%d].round_id is %d bytes, expected %d", i, len(entry.RoundId), RoundIDLen)
		}
		if len(entry.Nullifier) == 0 {
			return fmt.Errorf("nullifiers[%d].nullifier is empty", i)
		}
	}

	// Validate globally used delegation authorization keys.
	seenRks := make(map[string]struct{}, len(gs.UsedDelegationRks))
	for i, rk := range gs.UsedDelegationRks {
		if len(rk) != DelegationRkLen {
			return fmt.Errorf("used_delegation_rks[%d] is %d bytes, expected %d", i, len(rk), DelegationRkLen)
		}
		key := string(rk)
		if _, dup := seenRks[key]; dup {
			return fmt.Errorf("used_delegation_rks[%d]: duplicate rk %x", i, rk)
		}
		seenRks[key] = struct{}{}
	}

	// Vote-manager set is required in genesis — there is no bootstrap path.
	if _, _, err := ValidateAndNormalizeVoteManagerPolicy(gs.VoteManagerAddresses, gs.VoteManagerThreshold); err != nil {
		return fmt.Errorf("vote_manager_addresses: %w", err)
	}

	// min_ceremony_validators must be at least 1 when explicitly set.
	// A zero value means "use default (1)", so we only reject values that
	// are explicitly invalid once we enforce a minimum.
	// (No explicit validation needed: 0 is treated as default 1 in InitGenesis.)

	// Validate tally results.
	for i, result := range gs.TallyResults {
		if len(result.VoteRoundId) != RoundIDLen {
			return fmt.Errorf("tally_results[%d].vote_round_id is %d bytes, expected %d", i, len(result.VoteRoundId), RoundIDLen)
		}
	}

	// Validate Pallas keys.
	for i, vpk := range gs.PallasKeys {
		if vpk.ValidatorAddress == "" {
			return fmt.Errorf("pallas_keys[%d].validator_address is empty", i)
		}
		if len(vpk.PallasPk) != 32 {
			return fmt.Errorf("pallas_keys[%d].pallas_pk is %d bytes, expected 32", i, len(vpk.PallasPk))
		}
	}

	// Validate tally accumulators.
	for i, acc := range gs.TallyAccumulators {
		if len(acc.RoundId) != RoundIDLen {
			return fmt.Errorf("tally_accumulators[%d].round_id is %d bytes, expected %d", i, len(acc.RoundId), RoundIDLen)
		}
		if len(acc.Ciphertext) != 64 {
			return fmt.Errorf("tally_accumulators[%d].ciphertext is %d bytes, expected 64", i, len(acc.Ciphertext))
		}
	}

	// Validate share counts.
	for i, sc := range gs.ShareCounts {
		if len(sc.RoundId) != RoundIDLen {
			return fmt.Errorf("share_counts[%d].round_id is %d bytes, expected %d", i, len(sc.RoundId), RoundIDLen)
		}
	}

	// Validate partial decryptions.
	for i, pd := range gs.PartialDecryptions {
		if len(pd.RoundId) != RoundIDLen {
			return fmt.Errorf("partial_decryptions[%d].round_id is %d bytes, expected %d", i, len(pd.RoundId), RoundIDLen)
		}
		if pd.ValidatorIndex == 0 {
			return fmt.Errorf("partial_decryptions[%d].validator_index must be >= 1", i)
		}
		if len(pd.PartialDecrypt) != 32 {
			return fmt.Errorf("partial_decryptions[%d].partial_decrypt is %d bytes, expected 32", i, len(pd.PartialDecrypt))
		}
	}

	// Validate endorser mappings.
	seenEndorsers := make(map[string]struct{}, len(gs.Endorsers))
	for i, endorser := range gs.Endorsers {
		if err := ValidateEndorserID(endorser.EndorserId); err != nil {
			return fmt.Errorf("endorsers[%d].endorser_id: %w", i, err)
		}
		if _, dup := seenEndorsers[endorser.EndorserId]; dup {
			return fmt.Errorf("endorsers[%d]: duplicate endorser_id %q", i, endorser.EndorserId)
		}
		seenEndorsers[endorser.EndorserId] = struct{}{}
		if _, err := sdk.AccAddressFromBech32(endorser.Address); err != nil {
			return fmt.Errorf("endorsers[%d].address %q is not a valid bech32 address: %w", i, endorser.Address, err)
		}
	}

	// Validate append-only endorsements.
	seenEndorsedRounds := make(map[string]struct{}, len(gs.EndorsedRounds))
	for i, endorsed := range gs.EndorsedRounds {
		if err := ValidateEndorserID(endorsed.EndorserId); err != nil {
			return fmt.Errorf("endorsed_rounds[%d].endorser_id: %w", i, err)
		}
		if len(endorsed.VoteRoundId) != RoundIDLen {
			return fmt.Errorf("endorsed_rounds[%d].vote_round_id is %d bytes, expected %d", i, len(endorsed.VoteRoundId), RoundIDLen)
		}
		key := endorsed.EndorserId + "\x00" + string(endorsed.VoteRoundId)
		if _, dup := seenEndorsedRounds[key]; dup {
			return fmt.Errorf("endorsed_rounds[%d]: duplicate endorsement for %q/%x", i, endorsed.EndorserId, endorsed.VoteRoundId)
		}
		seenEndorsedRounds[key] = struct{}{}
	}

	seenCoordinatorActions := make(map[uint64]struct{}, len(gs.CoordinatorActions))
	for i, action := range gs.CoordinatorActions {
		if action == nil {
			return fmt.Errorf("coordinator_actions[%d] cannot be nil", i)
		}
		if action.ActionId == 0 {
			return fmt.Errorf("coordinator_actions[%d].action_id cannot be zero", i)
		}
		if _, dup := seenCoordinatorActions[action.ActionId]; dup {
			return fmt.Errorf("coordinator_actions[%d]: duplicate action_id %d", i, action.ActionId)
		}
		seenCoordinatorActions[action.ActionId] = struct{}{}
		if action.Payload == nil || action.Payload.TypeUrl == "" || len(action.Payload.Value) == 0 {
			return fmt.Errorf("coordinator_actions[%d].payload cannot be empty", i)
		}
		if _, err := sdk.AccAddressFromBech32(action.Proposer); err != nil {
			return fmt.Errorf("coordinator_actions[%d].proposer %q is not a valid bech32 address: %w", i, action.Proposer, err)
		}
		switch action.Status {
		case CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING,
			CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXECUTED,
			CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXPIRED:
		default:
			return fmt.Errorf("coordinator_actions[%d].status is invalid: %s", i, action.Status.String())
		}
		seenApprovals := make(map[string]struct{}, len(action.Approvals))
		for j, approval := range action.Approvals {
			acc, err := sdk.AccAddressFromBech32(approval)
			if err != nil {
				return fmt.Errorf("coordinator_actions[%d].approvals[%d] %q is not a valid bech32 address: %w", i, j, approval, err)
			}
			canonical := acc.String()
			if _, dup := seenApprovals[canonical]; dup {
				return fmt.Errorf("coordinator_actions[%d].approvals[%d]: duplicate approval %s", i, j, canonical)
			}
			seenApprovals[canonical] = struct{}{}
		}
	}
	if gs.NextCoordinatorActionId != 0 {
		for actionID := range seenCoordinatorActions {
			if actionID >= gs.NextCoordinatorActionId {
				return fmt.Errorf("next_coordinator_action_id %d must be greater than imported action_id %d", gs.NextCoordinatorActionId, actionID)
			}
		}
	}

	return nil
}
