package keeper

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// CeremonyJailResult records a validator jailed for missing a REGISTERING
// ceremony contribution.
type CeremonyJailResult struct {
	ValidatorAddress string
	ConsAddress      sdk.ConsAddress
	JailedUntil      time.Time
}

// MissingCeremonyContributors returns validators snapshotted into the round
// that did not submit a DKG contribution.
func MissingCeremonyContributors(round *types.VoteRound) []string {
	contributed := make(map[string]struct{}, len(round.GetDkgContributions()))
	for _, contribution := range round.GetDkgContributions() {
		if contribution == nil {
			continue
		}
		contributed[contribution.ValidatorAddress] = struct{}{}
	}
	return missingCeremonyValidators(round, contributed)
}

func missingCeremonyValidators(round *types.VoteRound, seen map[string]struct{}) []string {
	missing := make([]string, 0)
	added := make(map[string]struct{}, len(round.GetCeremonyValidators()))
	for _, validator := range round.GetCeremonyValidators() {
		addr := ""
		if validator != nil {
			addr = validator.ValidatorAddress
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		if _, ok := added[addr]; ok {
			continue
		}
		missing = append(missing, addr)
		added[addr] = struct{}{}
	}
	return missing
}

// JailCeremonyNonContributors jails validators that were snapshotted into a
// timed-out REGISTERING ceremony but never submitted a DKG contribution.
func (k *Keeper) JailCeremonyNonContributors(
	ctx context.Context,
	round *types.VoteRound,
) ([]CeremonyJailResult, error) {
	return k.jailCeremonyValidators(
		ctx,
		round,
		types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		MissingCeremonyContributors(round),
	)
}

func (k *Keeper) jailCeremonyValidators(
	ctx context.Context,
	round *types.VoteRound,
	phase types.CeremonyStatus,
	valoperAddrs []string,
) ([]CeremonyJailResult, error) {
	if len(valoperAddrs) == 0 {
		return nil, nil
	}
	if k.stakingKeeper == nil {
		return nil, fmt.Errorf("staking keeper is not configured")
	}
	if k.slashingKeeper == nil {
		return nil, fmt.Errorf("slashing keeper is not configured")
	}

	jailDuration, err := k.slashingKeeper.DowntimeJailDuration(ctx)
	if err != nil {
		return nil, fmt.Errorf("read downtime jail duration: %w", err)
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	jailedUntil := sdkCtx.BlockTime().Add(jailDuration)

	results := make([]CeremonyJailResult, 0, len(valoperAddrs))
	for _, valoperAddr := range valoperAddrs {
		valAddr, err := sdk.ValAddressFromBech32(valoperAddr)
		if err != nil {
			return nil, fmt.Errorf("parse ceremony validator address %q: %w", valoperAddr, err)
		}
		validator, err := k.stakingKeeper.GetValidator(ctx, valAddr)
		if err != nil {
			return nil, fmt.Errorf("get ceremony validator %s: %w", valoperAddr, err)
		}
		if validator.Jailed {
			continue
		}
		consAddrBytes, err := validator.GetConsAddr()
		if err != nil {
			return nil, fmt.Errorf("get consensus address for ceremony validator %s: %w", valoperAddr, err)
		}
		consAddr := sdk.ConsAddress(consAddrBytes)

		if err := k.slashingKeeper.Jail(ctx, consAddr); err != nil {
			return nil, fmt.Errorf("jail ceremony validator %s: %w", valoperAddr, err)
		}
		if err := k.slashingKeeper.JailUntil(ctx, consAddr, jailedUntil); err != nil {
			return nil, fmt.Errorf("set ceremony jail duration for %s: %w", valoperAddr, err)
		}

		result := CeremonyJailResult{
			ValidatorAddress: valoperAddr,
			ConsAddress:      consAddr,
			JailedUntil:      jailedUntil,
		}
		results = append(results, result)

		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeCeremonyValidatorJailed,
			sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", round.GetVoteRoundId())),
			sdk.NewAttribute(types.AttributeKeyCeremonyPhase, phase.String()),
			sdk.NewAttribute(types.AttributeKeyValidatorAddress, valoperAddr),
			sdk.NewAttribute(types.AttributeKeyJailedUntil, jailedUntil.Format(time.RFC3339)),
		))
	}

	return results, nil
}
