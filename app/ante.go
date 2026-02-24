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
	"github.com/z-cale/zally/crypto/zkp/halo2"
	voteante "github.com/z-cale/zally/x/vote/ante"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
)

// DualAnteHandlerOptions configures the dual-mode ante handler that supports
// both vote transactions (ZKP/RedPallas authenticated) and standard Cosmos
// transactions (secp256k1/ed25519 signatures, fees, etc.).
type DualAnteHandlerOptions struct {
	// Standard SDK ante handler options (for Cosmos txs: staking, etc.)
	ante.HandlerOptions

	// Vote module keeper for stateful validation.
	VoteKeeper votekeeper.Keeper

	// RedPallas signature verifier. Use ProductionOpts().SigVerifier in production,
	// redpallas.NewMockVerifier() in tests.
	SigVerifier redpallas.Verifier

	// ZKP verifier. Use ProductionOpts().ZKPVerifier in production,
	// zkp.NewMockVerifier() in tests.
	ZKPVerifier zkp.Verifier
}

// ProductionOpts returns ValidateOpts wired with real cryptographic verifiers
// (RedPallas via FFI, Halo2 via FFI). Only use in production binaries built
// with `make install-ffi` (-tags halo2,redpallas). Tests should use
// voteante.MockOpts() instead.
func ProductionOpts() voteante.ValidateOpts {
	return voteante.ValidateOpts{
		SigVerifier: redpallas.NewVerifier(),
		ZKPVerifier: halo2.NewVerifier(),
	}
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
	// Includes ceremony-specific decorators for validator gating and fee exemption.
	standardHandler, err := buildStandardAnteHandler(opts.HandlerOptions, opts.VoteKeeper)
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
		// ceremony flow after chain start. MsgCreateValidatorWithPallasKey is
		// allowed since it wraps MsgCreateValidator with Pallas key registration.
		for _, msg := range tx.GetMsgs() {
			if _, ok := msg.(*stakingtypes.MsgCreateValidator); ok {
				if ctx.BlockHeight() > 0 {
					return ctx, fmt.Errorf("MsgCreateValidator is disabled; use MsgCreateValidatorWithPallasKey via /zally/v1/create-validator-with-pallas")
				}
			}
		}

		// Ceremony messages pass through to the standard ante chain where
		// they get signature verification, fee exemption, and validator gating.

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

	// Only MsgAckExecutiveAuthorityKey remains on the custom wire format path.
	// It is auto-injected by PrepareProposal and must be blocked from the
	// mempool (only injectable by the block proposer). All other ceremony
	// messages now flow through standard Cosmos SDK transactions with
	// signature verification and validator gating.
	if vtx.CeremonyMsg != nil {
		if err := k.ValidateAckSubmitter(ctx); err != nil {
			return ctx, err
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
// for non-vote transactions (staking operations, bank transfers, ceremony
// messages, etc.). Ceremony messages get fee exemption and a validator gate.
func buildStandardAnteHandler(options ante.HandlerOptions, voteKeeper votekeeper.Keeper) (sdk.AnteHandler, error) {
	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(),
		NewCeremonyFeeExemptDecorator(),
		ante.NewExtensionOptionsDecorator(options.ExtensionOptionChecker),
		ante.NewValidateBasicDecorator(),
		ante.NewTxTimeoutHeightDecorator(),
		ante.NewValidateMemoDecorator(options.AccountKeeper),
		ante.NewConsumeGasForTxSizeDecorator(options.AccountKeeper),
		ante.NewSetPubKeyDecorator(options.AccountKeeper),
		ante.NewValidateSigCountDecorator(options.AccountKeeper),
		ante.NewSigGasConsumeDecorator(options.AccountKeeper, options.SigGasConsumer),
		ante.NewSigVerificationDecorator(options.AccountKeeper, options.SignModeHandler, options.SigVerifyOptions...),
		ante.NewIncrementSequenceDecorator(options.AccountKeeper),
		// After signature verification, gate ceremony messages to bonded validators.
		NewCeremonyValidatorDecorator(voteKeeper),
	}

	return sdk.ChainAnteDecorators(anteDecorators...), nil
}
