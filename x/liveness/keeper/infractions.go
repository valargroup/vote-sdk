package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/core/comet"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (k Keeper) HandleValidatorSignature(ctx context.Context, addr cryptotypes.Address, power int64, signed comet.BlockIDFlag) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	logger := k.Logger(ctx)
	height := sdkCtx.BlockHeight()
	consAddr := sdk.ConsAddress(addr)

	isJailed, err := k.sk.IsValidatorJailed(ctx, consAddr)
	if err != nil {
		return err
	}
	if isJailed {
		return nil
	}

	signInfo, err := k.GetValidatorSigningInfo(ctx, consAddr)
	if err != nil {
		return err
	}

	signedBlocksWindow, err := k.SignedBlocksWindow(ctx)
	if err != nil {
		return err
	}

	if signed != comet.BlockIDFlagAbsent {
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
			slashingtypes.EventTypeLiveness,
			sdk.NewAttribute(slashingtypes.AttributeKeyAddress, consAddr.String()),
			sdk.NewAttribute(slashingtypes.AttributeKeyMissedBlocks, fmt.Sprintf("%d", signInfo.MissedBlocksCounter)),
			sdk.NewAttribute(slashingtypes.AttributeKeyHeight, fmt.Sprintf("%d", height)),
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
	if height > minHeight && signInfo.MissedBlocksCounter > maxMissed {
		validator, err := k.sk.ValidatorByConsAddr(ctx, consAddr)
		if err != nil {
			return err
		}
		if validator != nil && !validator.IsJailed() {
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
					slashingtypes.EventTypeSlash,
					sdk.NewAttribute(slashingtypes.AttributeKeyAddress, consAddr.String()),
					sdk.NewAttribute(slashingtypes.AttributeKeyPower, fmt.Sprintf("%d", power)),
					sdk.NewAttribute(slashingtypes.AttributeKeyReason, slashingtypes.AttributeValueMissingSignature),
					sdk.NewAttribute(slashingtypes.AttributeKeyJailed, consAddr.String()),
					sdk.NewAttribute(slashingtypes.AttributeKeyBurnedCoins, coinsBurned.String()),
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
			signInfo.MissedBlocksCounter = 0
			signInfo.IndexOffset = 0
			if err := k.DeleteMissedBlocks(ctx, consAddr); err != nil {
				return err
			}
		}
	}

	return k.SetValidatorSigningInfo(ctx, consAddr, signInfo)
}
