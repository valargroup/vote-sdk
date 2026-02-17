package app

import (
	"fmt"
	"time"

	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	voteapi "github.com/z-cale/zally/api"
	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp"
	voteante "github.com/z-cale/zally/x/vote/ante"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// DualAnteHandlerOptions configures the dual-mode ante handler that supports
// both vote transactions (ZKP/RedPallas authenticated) and standard Cosmos
// transactions (secp256k1/ed25519 signatures, fees, etc.).
type DualAnteHandlerOptions struct {
	// Standard SDK ante handler options (for Cosmos txs: staking, etc.)
	ante.HandlerOptions

	// Vote module keeper for stateful validation.
	VoteKeeper votekeeper.Keeper

	// RedPallas signature verifier (mock during development).
	SigVerifier redpallas.Verifier

	// ZKP verifier (mock during development).
	ZKPVerifier zkp.Verifier
}

// NewDualAnteHandler returns an AnteHandler that detects the tx type and routes
// to the appropriate validation pipeline:
//
//   - VoteTxWrapper → custom validation via ValidateVoteTx (ZKP + RedPallas)
//   - Standard sdk.Tx → standard Cosmos ante chain (sig verify, fees, etc.)
//
// This allows the chain to process both vote transactions (which bypass the
// Cosmos Tx envelope) and standard Cosmos transactions (for staking, etc.)
// through the same BaseApp instance.
func NewDualAnteHandler(opts DualAnteHandlerOptions) (sdk.AnteHandler, error) {
	// Build the standard Cosmos ante chain for non-vote transactions.
	standardHandler, err := buildStandardAnteHandler(opts.HandlerOptions)
	if err != nil {
		return nil, err
	}

	voteKeeper := opts.VoteKeeper
	sigVerifier := opts.SigVerifier
	zkpVerifier := opts.ZKPVerifier

	return func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		// Custom tx path (vote or ceremony).
		if vtx, ok := tx.(*voteapi.VoteTxWrapper); ok {
			return handleVoteAnte(ctx, vtx, voteKeeper, sigVerifier, zkpVerifier)
		}

		// Block raw MsgCreateValidator — post-genesis validators must use
		// MsgCreateValidatorWithPallasKey to atomically register their Pallas key.
		// Allow during genesis (block height 0) since gentx produces standard
		// MsgCreateValidator; genesis validators register Pallas keys via the
		// ceremony flow after chain start.
		for _, msg := range tx.GetMsgs() {
			if _, ok := msg.(*stakingtypes.MsgCreateValidator); ok {
				if ctx.BlockHeight() > 0 {
					return ctx, fmt.Errorf("MsgCreateValidator is disabled; use MsgCreateValidatorWithPallasKey via /zally/v1/create-validator-with-pallas")
				}
			}
		}

		// Standard Cosmos tx path: signature verification, fee deduction, etc.
		return standardHandler(ctx, tx, simulate)
	}, nil
}

// handleVoteAnte validates a vote transaction using the custom validation
// pipeline from x/vote/ante. Vote txs are free (infinite gas meter) and use
// ZKP/RedPallas authentication instead of standard Cosmos signatures.
func handleVoteAnte(
	ctx sdk.Context,
	vtx *voteapi.VoteTxWrapper,
	k votekeeper.Keeper,
	sigVerifier redpallas.Verifier,
	zkpVerifier zkp.Verifier,
) (sdk.Context, error) {
	// All custom txs (vote + ceremony) are free — infinite gas meter.
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())

	// Ceremony messages bypass the vote validation pipeline.
	// Most ceremony messages flow directly to MsgServer handlers for validation.
	// MsgAckExecutiveAuthorityKey is special: it must be blocked from the
	// mempool (only injectable via PrepareProposal).
	if vtx.CeremonyMsg != nil {
		if _, isAck := vtx.CeremonyMsg.(*types.MsgAckExecutiveAuthorityKey); isAck {
			if err := k.ValidateAckSubmitter(ctx); err != nil {
				return ctx, err
			}
		}
		return ctx, nil
	}

	opts := voteante.ValidateOpts{
		IsRecheck:   ctx.IsReCheckTx(),
		SigVerifier: sigVerifier,
		ZKPVerifier: zkpVerifier,
	}

	start := time.Now()
	if err := voteante.ValidateVoteTx(ctx, vtx.VoteMsg, k, opts); err != nil {
		elapsed := time.Since(start)
		k.Logger().Info("vote ante validation failed",
			"duration_ms", elapsed.Milliseconds(),
			"msg_type", fmt.Sprintf("%T", vtx.VoteMsg),
			"error", err.Error())
		return ctx, err
	}
	elapsed := time.Since(start)
	k.Logger().Info("vote ante validation completed",
		"duration_ms", elapsed.Milliseconds(),
		"msg_type", fmt.Sprintf("%T", vtx.VoteMsg))
	return ctx, nil
}

// buildStandardAnteHandler creates the standard Cosmos SDK ante handler chain
// for non-vote transactions (staking operations, bank transfers, etc.).
func buildStandardAnteHandler(options ante.HandlerOptions) (sdk.AnteHandler, error) {
	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(),
		ante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		ante.NewValidateBasicDecorator(),
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		ante.NewDeductFeeDecorator(options.AccountKeeper, options.BankKeeper, options.FeegrantKeeper, options.TxFeeChecker),
		ante.NewSetPubKeyDecorator(options.AccountKeeper),
		ante.NewValidateSigCountDecorator(options.AccountKeeper),
		ante.NewSigGasConsumeDecorator(options.AccountKeeper, options.SigGasConsumer),
		ante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler, options.SigVerifyOptions...),
		ante.NewIncrementSequenceDecorator(options.AccountKeeper),
	}

	return sdk.ChainAnteDecorators(anteDecorators...), nil
}
