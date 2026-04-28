package keeper

import (
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (k Keeper) InitGenesis(ctx sdk.Context, data *slashingtypes.GenesisState) {
	if err := k.sk.IterateValidators(ctx, func(_ int64, validator stakingtypes.ValidatorI) bool {
		if !validator.IsBonded() {
			return false
		}
		consAddr, err := validator.GetConsAddr()
		if err != nil {
			panic(err)
		}
		if _, err := k.GetValidatorSigningInfo(ctx, consAddr); err == nil {
			return false
		}
		info := slashingtypes.NewValidatorSigningInfo(consAddr, ctx.BlockHeight(), 0, time.Unix(0, 0), false, 0)
		if err := k.SetValidatorSigningInfo(ctx, consAddr, info); err != nil {
			panic(err)
		}
		return false
	}); err != nil {
		panic(err)
	}

	for _, info := range data.SigningInfos {
		address, err := k.sk.ConsensusAddressCodec().StringToBytes(info.Address)
		if err != nil {
			panic(err)
		}
		if err := k.SetValidatorSigningInfo(ctx, address, info.ValidatorSigningInfo); err != nil {
			panic(err)
		}
	}

	for _, array := range data.MissedBlocks {
		address, err := k.sk.ConsensusAddressCodec().StringToBytes(array.Address)
		if err != nil {
			panic(err)
		}
		for _, missed := range array.MissedBlocks {
			if missed.Missed {
				if err := k.SetMissedBlockAtHeight(ctx, address, missed.Index); err != nil {
					panic(err)
				}
			}
		}
	}

	if err := k.SetParams(ctx, data.Params); err != nil {
		panic(err)
	}
}

func (k Keeper) ExportGenesis(ctx sdk.Context) *slashingtypes.GenesisState {
	params, err := k.GetParams(ctx)
	if err != nil {
		panic(err)
	}

	signingInfos := make([]slashingtypes.SigningInfo, 0)
	missedBlocks := make([]slashingtypes.ValidatorMissedBlocks, 0)
	err = k.IterateValidatorSigningInfos(ctx, func(address sdk.ConsAddress, info slashingtypes.ValidatorSigningInfo) (stop bool) {
		bechAddr := address.String()
		signingInfos = append(signingInfos, slashingtypes.SigningInfo{
			Address:              bechAddr,
			ValidatorSigningInfo: info,
		})

		localMissedBlocks, err := k.GetValidatorMissedBlocks(ctx, address)
		if err != nil {
			panic(err)
		}
		missedBlocks = append(missedBlocks, slashingtypes.ValidatorMissedBlocks{
			Address:      bechAddr,
			MissedBlocks: localMissedBlocks,
		})
		return false
	})
	if err != nil {
		panic(err)
	}

	return slashingtypes.NewGenesisState(params, signingInfos, missedBlocks)
}
