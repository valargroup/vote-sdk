package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/comet"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// HandleValidatorSignature handles a validator signature, must be called once per validator per block.
func (k Keeper) HandleValidatorSignature(ctx context.Context, addr cryptotypes.Address, power int64, signed comet.BlockIDFlag) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(ctx)
	height := sdkCtx.BlockHeight()

	// fetch the validator public key
	consAddr := sdk.ConsAddress(addr)

	// don't update missed blocks when validator's jailed
	isJailed, err := k.sk.IsValidatorJailed(ctx, consAddr)
	if err != nil {
		return err
	}

	if isJailed {
		return nil
	}

	// fetch signing info
	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return err
	}

	signedBlocksWindow, err := k.SignedBlocksWindow(ctx)
	if err != nil {
		return err
	}

	missed := signed == comet.BlockIDFlagAbsent
	if !missed {
		return nil
	}

	minSignedPerWindow, err := k.MinSignedPerWindow(ctx)
	if err != nil {
		return err
	}

	windowStart := height - signedBlocksWindow + 1
	if windowStart < signInfo.StartHeight {
		windowStart = signInfo.StartHeight
	}

	alreadyMissed, err := k.HasMissedBlockAtHeight(ctx, consAddr, height)
	if err != nil {
		return err
	}
	if !alreadyMissed {
		if err := k.SetMissedBlockAtHeight(ctx, consAddr, height); err != nil {
			return err
		}
	}
	if err := k.PruneMissedBlocksBeforeHeight(ctx, windowStart); err != nil {
		return err
	}

	missedBlocksCounter, err := k.CountMissedBlocksInWindow(ctx, consAddr, windowStart, height)
	if err != nil {
		return err
	}
	signInfo.MissedBlocksCounter = missedBlocksCounter

	sdkCtx.EventManager().EmitEvent(
		sdk.NewEvent(
			types.EventTypeLiveness,
			sdk.NewAttribute(types.AttributeKeyAddress, consAddr.String()),
			sdk.NewAttribute(types.AttributeKeyMissedBlocks, fmt.Sprintf("%d", signInfo.MissedBlocksCounter)),
			sdk.NewAttribute(types.AttributeKeyHeight, fmt.Sprintf("%d", height)),
		),
	)

	logger.Debug(
		"absent validator",
		"height", height,
		"validator", consAddr.String(),
		"missed", signInfo.MissedBlocksCounter,
		"threshold", minSignedPerWindow,
	)

	minHeight := signInfo.StartHeight + signedBlocksWindow
	maxMissed := signedBlocksWindow - minSignedPerWindow

	// if we are past the minimum height and the validator has missed too many blocks, punish them
	if height > minHeight && signInfo.MissedBlocksCounter > maxMissed {
		validator, err := k.sk.ValidatorByConsAddr(ctx, consAddr)
		if err != nil {
			return err
		}
		if validator != nil && !validator.IsJailed() {
			// Downtime confirmed: slash and jail the validator
			// We need to retrieve the stake distribution which signed the block, so we subtract ValidatorUpdateDelay from the evidence height,
			// and subtract an additional 1 since this is the LastCommit.
			// Note that this *can* result in a negative "distributionHeight" up to -ValidatorUpdateDelay-1,
			// i.e. at the end of the pre-genesis block (none) = at the beginning of the genesis block.
			// That's fine since this is just used to filter unbonding delegations & redelegations.
			distributionHeight := height - sdk.ValidatorUpdateDelay - 1

			slashFractionDowntime, err := k.SlashFractionDowntime(ctx)
			if err != nil {
				return err
			}

			coinsBurned, err := k.sk.SlashWithInfractionReason(ctx, consAddr, distributionHeight, power, slashFractionDowntime, stakingtypes.Infraction_INFRACTION_DOWNTIME)
			if err != nil {
				return err
			}

			sdkCtx.EventManager().EmitEvent(
				sdk.NewEvent(
					types.EventTypeSlash,
					sdk.NewAttribute(types.AttributeKeyAddress, consAddr.String()),
					sdk.NewAttribute(types.AttributeKeyPower, fmt.Sprintf("%d", power)),
					sdk.NewAttribute(types.AttributeKeyReason, types.AttributeValueMissingSignature),
					sdk.NewAttribute(types.AttributeKeyJailed, consAddr.String()),
					sdk.NewAttribute(types.AttributeKeyBurnedCoins, coinsBurned.String()),
				),
			)

			if err := k.sk.Jail(sdkCtx, consAddr); err != nil {
				return fmt.Errorf("failed to jail validator: %w", err)
			}

			downtimeJailDur, err := k.DowntimeJailDuration(ctx)
			if err != nil {
				return err
			}
			signInfo.JailedUntil = sdkCtx.BlockHeader().Time.Add(downtimeJailDur)

			// We need to reset the counter & bitmap so that the validator won't be
			// immediately slashed for downtime upon re-bonding.
			signInfo.MissedBlocksCounter = 0
			signInfo.IndexOffset = 0
			err = k.DeleteMissedBlockBitmap(ctx, consAddr)
			if err != nil {
				return err
			}

			logger.Info(
				"slashing and jailing validator due to liveness fault",
				"height", height,
				"validator", consAddr.String(),
				"min_height", minHeight,
				"threshold", minSignedPerWindow,
				"slashed", slashFractionDowntime.String(),
				"jailed_until", signInfo.JailedUntil,
			)
		} else {
			// validator was (a) not found or (b) already jailed so we do not slash
			logger.Info(
				"validator would have been slashed for downtime, but was either not found in store or already jailed",
				"validator", consAddr.String(),
			)
		}
	}

	// Set the updated signing info
	return k.SetValidatorSigningInfo(ctx, consAddr, signInfo)
}
