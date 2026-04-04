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

	return nil
}
