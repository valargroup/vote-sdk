package app

import (
	"context"
	"fmt"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

// MaxBlockBytes is the consensus maximum serialized block size. The limit
// leaves room for normal voting traffic while bounding proposal propagation.
const MaxBlockBytes int64 = 5 << 20

// applyConsensusLimits updates the application consensus-parameter store while
// preserving populated fields and filling any omitted required sections from
// Comet defaults.
func (app *SvoteApp) applyConsensusLimits(ctx context.Context) (cmtproto.ConsensusParams, error) {
	params, err := app.ConsensusParamsKeeper.ParamsStore.Get(ctx)
	if err != nil {
		return cmtproto.ConsensusParams{}, fmt.Errorf("load consensus parameters: %w", err)
	}
	if params.Block == nil {
		return cmtproto.ConsensusParams{}, fmt.Errorf("consensus block parameters are missing")
	}

	params.Block.MaxBytes = MaxBlockBytes
	defaults := cmttypes.DefaultConsensusParams().ToProto()
	if params.Evidence == nil {
		params.Evidence = defaults.Evidence
	}
	if params.Validator == nil {
		params.Validator = defaults.Validator
	}
	if params.Version == nil {
		params.Version = defaults.Version
	}
	if err := cmttypes.ConsensusParamsFromProto(params).ValidateBasic(); err != nil {
		return cmtproto.ConsensusParams{}, fmt.Errorf("validate consensus parameters: %w", err)
	}
	if err := app.ConsensusParamsKeeper.ParamsStore.Set(ctx, params); err != nil {
		return cmtproto.ConsensusParams{}, fmt.Errorf("store consensus parameters: %w", err)
	}
	return params, nil
}
