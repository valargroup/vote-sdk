package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ValidateGenesisState performs structural validation of the vote module genesis state.
func ValidateGenesisState(gs *GenesisState) error {
	if gs == nil {
		return nil
	}

	// Validate tree state consistency with commitment leaves.
	if gs.TreeState != nil && gs.TreeState.NextIndex > 0 {
		if uint64(len(gs.CommitmentLeaves)) != gs.TreeState.NextIndex {
			return fmt.Errorf("tree_state.next_index is %d but commitment_leaves has %d entries",
				gs.TreeState.NextIndex, len(gs.CommitmentLeaves))
		}
	}

	// Validate commitment leaves are sequential and well-formed.
	for i, leaf := range gs.CommitmentLeaves {
		if leaf.Index != uint64(i) {
			return fmt.Errorf("commitment_leaves[%d].index is %d, expected %d", i, leaf.Index, i)
		}
		if len(leaf.Value) == 0 {
			return fmt.Errorf("commitment_leaves[%d].value is empty", i)
		}
		if len(leaf.Value) != 32 {
			return fmt.Errorf("commitment_leaves[%d].value is %d bytes, expected 32", i, len(leaf.Value))
		}
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

	// Vote manager is required in genesis — there is no bootstrap path.
	if gs.VoteManager == "" {
		return fmt.Errorf("vote_manager is required in genesis")
	}
	if _, err := sdk.AccAddressFromBech32(gs.VoteManager); err != nil {
		return fmt.Errorf("vote_manager %q is not a valid bech32 address: %w", gs.VoteManager, err)
	}

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

	// Validate commitment roots.
	for i, cr := range gs.CommitmentRoots {
		if cr.Height == 0 {
			return fmt.Errorf("commitment_roots[%d].height is zero", i)
		}
		if len(cr.Root) == 0 {
			return fmt.Errorf("commitment_roots[%d].root is empty", i)
		}
	}

	return nil
}
