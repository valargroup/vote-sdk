package keeper

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"cosmossdk.io/core/store"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/mikelodder7/curvey"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/x/vote/types"
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

// ThresholdForN computes the required threshold t = ceil(n/2) for n validators.
// The confirmation quorum (HalfAcked, strict majority) is always >= t, so the
// set of validators that survives ceremony stripping is always large enough to
// reconstruct the EA key during tally.
//
// For n = 1 returns t = 1 (trivial single-share scheme with no threshold
// security — used for local testing). Returns an error if n < 1.
func ThresholdForN(n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("ThresholdForN: n must be >= 1, got %d", n)
	}
	if n == 1 {
		return 1, nil
	}
	t := (n + 1) / 2
	if t < 2 {
		t = 2
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// Per-round ceremony helpers (operate on VoteRound ceremony fields)
// ---------------------------------------------------------------------------

// HalfAcked returns true if strictly more than half of round ceremony
// validators have acknowledged. Uses integer arithmetic: acks * 2 > validators.
// Strict inequality ensures that when n is even, exactly n/2 acks are
// insufficient — preventing a scenario where n/2 colluding validators
// confirm a ceremony using only their own contributions.
func HalfAcked(round *types.VoteRound) bool {
	n := len(round.CeremonyValidators)
	if n == 0 {
		return false
	}
	return len(round.CeremonyAcks)*2 > n
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

// FindContributionInRound returns the DKGContribution and true if valAddr has
// already submitted a contribution in this round, or (nil, false) otherwise.
func FindContributionInRound(round *types.VoteRound, valAddr string) (*types.DKGContribution, bool) {
	for _, c := range round.DkgContributions {
		if c.ValidatorAddress == valAddr {
			return c, true
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
// CeremonyValidators and DkgContributions. After this call, only validators
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

	keptContribs := round.DkgContributions[:0]
	for _, c := range round.DkgContributions {
		if acked[c.ValidatorAddress] {
			keptContribs = append(keptContribs, c)
		}
	}
	round.DkgContributions = keptContribs
}

// ---------------------------------------------------------------------------
// Majority-vote skip set helpers
// ---------------------------------------------------------------------------

// ValidateSkippedContributors checks structural validity of a skip set:
// sorted, no duplicates, all addresses are ceremony contributors, the acker
// does not include themselves, and the remaining contributors (after
// skipping) still meet the round threshold.
func ValidateSkippedContributors(round *types.VoteRound, acker string, skipped []string) error {
	if len(skipped) == 0 {
		return nil
	}
	if !sort.StringsAreSorted(skipped) {
		return fmt.Errorf("%w: skipped_contributors must be sorted", types.ErrInvalidField)
	}
	nContributors := len(round.DkgContributions)
	maxSkip := nContributors - int(round.Threshold)
	if len(skipped) > maxSkip {
		return fmt.Errorf("%w: skipped_contributors length %d exceeds maximum %d (n=%d, threshold=%d)",
			types.ErrInvalidField, len(skipped), maxSkip, nContributors, round.Threshold)
	}
	seen := make(map[string]bool, len(skipped))
	for _, addr := range skipped {
		if addr == acker {
			return fmt.Errorf("%w: skipped_contributors must not include self", types.ErrInvalidField)
		}
		if seen[addr] {
			return fmt.Errorf("%w: duplicate in skipped_contributors: %s", types.ErrInvalidField, addr)
		}
		seen[addr] = true
		if _, found := FindContributionInRound(round, addr); !found {
			return fmt.Errorf("%w: skipped_contributors references non-contributor %s",
				types.ErrNotRegisteredValidator, addr)
		}
	}
	return nil
}

// ComputeCanonicalSkipSet determines the majority-vote skip set from all acks.
// A contributor is in the canonical set if strictly more than half of the acks
// include it in their skip set.
func ComputeCanonicalSkipSet(acks []*types.AckEntry) []string {
	if len(acks) == 0 {
		return nil
	}
	counts := make(map[string]int)
	// Count the number of times each contributor is skipped.
	for _, a := range acks {
		for _, s := range a.SkippedContributors {
			counts[s]++
		}
	}
	nAcks := len(acks)
	// Compute the canonical skip set.
	var canonical []string
	for addr, cnt := range counts {
		// If the contributor is skipped in strictly more than half of the acks,
		// include them in the canonical skip set.
		if cnt*2 > nAcks {
			canonical = append(canonical, addr)
		}
	}
	sort.Strings(canonical)
	return canonical
}

// FilterCompatibleAcks returns only acks whose SkippedContributors exactly
// matches the canonical skip set.
func FilterCompatibleAcks(acks []*types.AckEntry, canonical []string) []*types.AckEntry {
	var out []*types.AckEntry
	for _, a := range acks {
		if slices.Equal(a.SkippedContributors, canonical) {
			out = append(out, a)
		}
	}
	return out
}

// RecomputeCommitmentsExcluding recomputes combined Feldman commitments and
// ea_pk from the round's DKG contributions, excluding the given skip set.
// Returns the new ea_pk and serialized commitment vector.
func RecomputeCommitmentsExcluding(round *types.VoteRound, skipSet []string) ([]byte, [][]byte, error) {
	// Create a map of skipped contributors to exclude from the recomputation.
	excluded := make(map[string]bool, len(skipSet))
	for _, s := range skipSet {
		excluded[s] = true
	}

	// Recompute the combined Feldman commitments.
	var allCommitments [][]curvey.Point
	for _, contrib := range round.DkgContributions {
		if excluded[contrib.ValidatorAddress] {
			continue
		}
		vec := make([]curvey.Point, len(contrib.FeldmanCommitments))
		for j, raw := range contrib.FeldmanCommitments {
			pt, err := elgamal.UnmarshalPublicKey(raw)
			if err != nil {
				return nil, nil, fmt.Errorf("contributor %s commitment %d: %w",
					contrib.ValidatorAddress, j, err)
			}
			vec[j] = pt.Point
		}
		allCommitments = append(allCommitments, vec)
	}
	if len(allCommitments) == 0 {
		return nil, nil, fmt.Errorf("no contributions remain after excluding skip set")
	}

	combined, err := shamir.CombineCommitments(allCommitments)
	if err != nil {
		return nil, nil, fmt.Errorf("combine commitments: %w", err)
	}

	eaPk := combined[0].ToAffineCompressed()
	commitments := make([][]byte, len(combined))
	for j, c := range combined {
		commitments[j] = c.ToAffineCompressed()
	}
	return eaPk, commitments, nil
}

// StripRoundToCompatible removes all validators and contributions that are
// not in the compatible ack set OR are in the canonical skip set. This is a
// superset of StripNonAckersFromRound: it also removes malicious contributors
// even if they themselves acked (their combined share would be incompatible).
func StripRoundToCompatible(round *types.VoteRound, compatible []*types.AckEntry, skipSet []string) {
	keep := make(map[string]bool, len(compatible))
	for _, a := range compatible {
		keep[a.ValidatorAddress] = true
	}
	excluded := make(map[string]bool, len(skipSet))
	for _, s := range skipSet {
		excluded[s] = true
		delete(keep, s)
	}

	kept := round.CeremonyValidators[:0]
	for _, v := range round.CeremonyValidators {
		if keep[v.ValidatorAddress] {
			kept = append(kept, v)
		}
	}
	round.CeremonyValidators = kept

	keptContribs := round.DkgContributions[:0]
	for _, c := range round.DkgContributions {
		if keep[c.ValidatorAddress] {
			keptContribs = append(keptContribs, c)
		}
	}
	round.DkgContributions = keptContribs

	keptAcks := round.CeremonyAcks[:0]
	for _, a := range round.CeremonyAcks {
		if keep[a.ValidatorAddress] {
			keptAcks = append(keptAcks, a)
		}
	}
	round.CeremonyAcks = keptAcks
}

// IsValidatorInPendingCeremony returns true if the given validator address
// appears in the ceremony_validators list of any PENDING round. Used to block
// Pallas key rotation while the validator is participating in an active ceremony.
func (k *Keeper) IsValidatorInPendingCeremony(kvStore store.KVStore, valAddr string) (bool, error) {
	found := false
	err := k.IteratePendingRounds(kvStore, func(round *types.VoteRound) bool {
		if _, ok := FindValidatorInRoundCeremony(round, valAddr); ok {
			found = true
			return true
		}
		return false
	})
	return found, err
}

// GetPendingRoundWithCeremony loads a vote round and verifies it is PENDING
// with the specified ceremony status. Used by Deal (REGISTERING) and Ack (DEALT).
func (k *Keeper) GetPendingRoundWithCeremony(kvStore store.KVStore, roundID []byte, wantCeremony types.CeremonyStatus) (*types.VoteRound, error) {
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return nil, err
	}
	if round.Status != types.SessionStatus_SESSION_STATUS_PENDING {
		return nil, fmt.Errorf("%w: round is %s", types.ErrCeremonyWrongStatus, round.Status)
	}
	if round.CeremonyStatus != wantCeremony {
		return nil, fmt.Errorf("%w: ceremony is %s", types.ErrCeremonyWrongStatus, round.CeremonyStatus)
	}
	return round, nil
}

// ---------------------------------------------------------------------------
// Injected-tx proposer validation
// ---------------------------------------------------------------------------

// ValidateProposerIsCreator checks that a proposer-injected message is only
// submitted during block execution (not via mempool) and that creator matches
// the current block proposer. msgName is used in error messages for diagnostics
// (e.g. "MsgContributeDKG").
func (k *Keeper) ValidateProposerIsCreator(ctx context.Context, creator, msgName string) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if sdkCtx.IsCheckTx() || sdkCtx.IsReCheckTx() {
		return fmt.Errorf("%w: %s cannot be submitted via mempool", types.ErrInvalidField, msgName)
	}

	proposerConsAddr := sdk.ConsAddress(sdkCtx.BlockHeader().ProposerAddress)
	val, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, proposerConsAddr)
	if err != nil {
		return fmt.Errorf("%w: failed to resolve block proposer: %v", types.ErrInvalidField, err)
	}
	if val.OperatorAddress != creator {
		return fmt.Errorf("%w: %s creator %s does not match block proposer %s",
			types.ErrInvalidField, msgName, creator, val.OperatorAddress)
	}
	return nil
}
