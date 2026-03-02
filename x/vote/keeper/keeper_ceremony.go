package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/store"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/types"
)

// GetCeremonyState retrieves the singleton ceremony state from the KV store.
// Returns nil, nil if no ceremony has been initialized yet.
func (k *Keeper) GetCeremonyState(kvStore store.KVStore) (*types.CeremonyState, error) {
	bz, err := kvStore.Get(types.CeremonyStateKey)
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, nil
	}
	var state types.CeremonyState
	if err := unmarshal(bz, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// AppendCeremonyLog appends a timestamped entry to the round's ceremony log.
// The entry is prefixed with the block height for chronological context.
func AppendCeremonyLog(round *types.VoteRound, blockHeight uint64, msg string) {
	entry := fmt.Sprintf("[height=%d] %s", blockHeight, msg)
	round.CeremonyLog = append(round.CeremonyLog, entry)
}

// SetCeremonyState stores the singleton ceremony state in the KV store.
func (k *Keeper) SetCeremonyState(kvStore store.KVStore, state *types.CeremonyState) error {
	bz, err := marshal(state)
	if err != nil {
		return err
	}
	return kvStore.Set(types.CeremonyStateKey, bz)
}

// ---------------------------------------------------------------------------
// Per-round ceremony helpers (operate on VoteRound ceremony fields)
// ---------------------------------------------------------------------------

// HalfAcked returns true if at least 1/2 of round ceremony validators have
// acknowledged. Uses integer arithmetic: acks * 2 >= validators.
func HalfAcked(round *types.VoteRound) bool {
	n := len(round.CeremonyValidators)
	if n == 0 {
		return false
	}
	return len(round.CeremonyAcks)*2 >= n
}

// FindValidatorInRoundCeremony returns the ValidatorPallasKey and true if
// valAddr is found in the round's ceremony_validators list, or (nil, false)
// otherwise. Callers that need the original Shamir evaluation point must use
// the returned validator's ShamirIndex field rather than the array position,
// which changes after StripNonAckersFromRound removes non-acking validators.
func FindValidatorInRoundCeremony(round *types.VoteRound, valAddr string) (*types.ValidatorPallasKey, bool) {
	for _, v := range round.CeremonyValidators {
		if v.ValidatorAddress == valAddr {
			return v, true
		}
	}
	return nil, false
}

// FindAckInRoundCeremony returns the index and true if valAddr has an ack entry
// in the round's ceremony, or (-1, false) otherwise.
func FindAckInRoundCeremony(round *types.VoteRound, valAddr string) (int, bool) {
	for i, a := range round.CeremonyAcks {
		if a.ValidatorAddress == valAddr {
			return i, true
		}
	}
	return -1, false
}

// StripNonAckersFromRound removes non-acking validators from the round's
// CeremonyValidators and CeremonyPayloads. After this call, only validators
// with a matching ack remain.
func StripNonAckersFromRound(round *types.VoteRound) {
	acked := make(map[string]bool, len(round.CeremonyAcks))
	for _, a := range round.CeremonyAcks {
		acked[a.ValidatorAddress] = true
	}

	kept := round.CeremonyValidators[:0]
	for _, v := range round.CeremonyValidators {
		if acked[v.ValidatorAddress] {
			kept = append(kept, v)
		}
	}
	round.CeremonyValidators = kept

	keptPayloads := round.CeremonyPayloads[:0]
	for _, p := range round.CeremonyPayloads {
		if acked[p.ValidatorAddress] {
			keptPayloads = append(keptPayloads, p)
		}
	}
	round.CeremonyPayloads = keptPayloads
}

// ---------------------------------------------------------------------------
// Ceremony submission validation
// ---------------------------------------------------------------------------

// ValidateAckSubmitter checks that MsgAckExecutiveAuthorityKey is only
// submitted during block execution (not via mempool). This ensures acks
// can only be injected by the block proposer via PrepareProposal,
// mirroring the pattern used by ValidateTallySubmitter.
func (k *Keeper) ValidateAckSubmitter(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// MsgAckExecutiveAuthorityKey must never enter the mempool — it can only
	// be injected by the block proposer via PrepareProposal.
	if sdkCtx.IsCheckTx() || sdkCtx.IsReCheckTx() {
		return fmt.Errorf("%w: MsgAckExecutiveAuthorityKey cannot be submitted via mempool", types.ErrInvalidField)
	}

	return nil
}
