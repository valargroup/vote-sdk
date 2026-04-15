package app

import (
	"errors"
	"fmt"
	"time"

	storetypes "cosmossdk.io/store/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	"github.com/valargroup/vote-sdk/sentry"
	voteante "github.com/valargroup/vote-sdk/x/vote/ante"
	votekeeper "github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// DualAnteHandlerOptions configures the dual-mode ante handler that supports
// both vote transactions (ZKP/RedPallas authenticated) and standard Cosmos
// transactions (secp256k1/ed25519 signatures, fees, etc.).
type DualAnteHandlerOptions struct {
	// Standard SDK ante handler options (for Cosmos txs: staking, etc.)
	ante.HandlerOptions

	// Vote module keeper for stateful validation.
	VoteKeeper *votekeeper.Keeper

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

		msgs := tx.GetMsgs()
		if len(msgs) > 1 {
			return ctx, fmt.Errorf("multi-message transactions are not supported; got %d messages", len(msgs))
		}

		for _, msg := range msgs {
			// Defense-in-depth: reject ZKP/ceremony messages via explicit
			// type check even though MessageWhitelistDecorator (in the
			// standard ante chain) would also catch them. This fires
			// earlier — before any decorator runs — and gives a clearer
			// error message explaining the correct submission path.
			if isVoteModuleMsg(msg) {
				return ctx, fmt.Errorf("vote module message %T is not allowed in standard Cosmos transactions; use the vote tx format", msg)
			}

			// Block raw MsgCreateValidator post-genesis — validators must
			// use MsgCreateValidatorWithPallasKey to atomically register
			// their Pallas key. Allowed at genesis (height 0) for gentx.
			if _, ok := msg.(*stakingtypes.MsgCreateValidator); ok {
				if ctx.BlockHeight() > 0 {
					return ctx, fmt.Errorf("MsgCreateValidator is disabled; use MsgCreateValidatorWithPallasKey via /shielded-vote/v1/create-validator-with-pallas")
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
	k *votekeeper.Keeper,
	sigVerifier redpallas.Verifier,
	zkpVerifier zkp.Verifier,
) (sdk.Context, error) {
	// All custom txs (vote + ceremony) are free — infinite gas meter.
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())

	// Ceremony messages (ContributeDKG 0x0E, Ack 0x08, PartialDecrypt 0x0D) use
	// the custom wire format and are auto-injected by PrepareProposal. Each tag
	// gets its own proposer identity check to prevent forged submissions.
	if vtx.CeremonyMsg != nil {
		switch vtx.Tag {
		case voteapi.TagContributeDKG:
			msg := vtx.CeremonyMsg.(*types.MsgContributeDKG)
			if err := k.ValidateProposerIsCreator(ctx, msg.Creator, "MsgContributeDKG"); err != nil {
				return ctx, err
			}
		case voteapi.TagAckExecutiveAuthorityKey:
			msg := vtx.CeremonyMsg.(*types.MsgAckExecutiveAuthorityKey)
			if err := k.ValidateProposerIsCreator(ctx, msg.Creator, "MsgAckExecutiveAuthorityKey"); err != nil {
				return ctx, err
			}
		case voteapi.TagSubmitPartialDecryption:
			msg := vtx.CeremonyMsg.(*types.MsgSubmitPartialDecryption)
			if err := k.ValidateProposerIsCreator(ctx, msg.Creator, "MsgSubmitPartialDecryption"); err != nil {
				return ctx, err
			}
		default:
			return ctx, fmt.Errorf("unknown ceremony tag: 0x%02x", vtx.Tag)
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
		if errors.Is(err, types.ErrInvalidProof) || errors.Is(err, types.ErrInvalidSignature) {
			sentry.CaptureErr(err, map[string]string{
				"handler":  "ante",
				"msg_type": fmt.Sprintf("%T", vtx.VoteMsg),
			})
		}
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
func buildStandardAnteHandler(options ante.HandlerOptions, voteKeeper *votekeeper.Keeper) (sdk.AnteHandler, error) {
	anteDecorators := []sdk.AnteDecorator{
		ante.NewSetUpContextDecorator(),
		NewCeremonyFeeExemptDecorator(),
		NewMessageWhitelistDecorator(DefaultAllowedMessages()),
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

// isVoteModuleMsg returns true for messages that must only be submitted via
// the custom vote tx wire format (VoteTxWrapper). This includes:
//   - Vote messages: authenticated by ZKP/RedPallas, not Cosmos signatures.
//   - Ceremony messages: auto-injected by PrepareProposal, authenticated by
//     proposer identity check (ValidateProposerIsCreator).
//
// Allowing these in standard Cosmos txs would bypass their authentication —
// vote messages skip ZKP verification, ceremony messages skip proposer gating.
//
// Defense-in-depth: the MessageWhitelistDecorator in buildStandardAnteHandler
// also blocks these (they are not in the allowed set), but this explicit type
// check fires earlier in NewDualAnteHandler — before any decorator runs —
// and produces a more actionable error message.
func isVoteModuleMsg(msg sdk.Msg) bool {
	switch msg.(type) {
	case *types.MsgDelegateVote, *types.MsgCastVote, *types.MsgRevealShare:
		return true
	case *types.MsgContributeDKG, *types.MsgAckExecutiveAuthorityKey, *types.MsgSubmitPartialDecryption, *types.MsgSubmitTally:
		return true
	default:
		return false
	}
}
