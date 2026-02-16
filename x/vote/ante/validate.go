// Package ante implements the validation pipeline for vote module transactions.
//
// This pipeline runs inside the custom ABCI handlers (CheckTx/FinalizeBlock),
// NOT the standard Cosmos SDK AnteDecorator chain. Since vote transactions
// bypass the Cosmos SDK Tx envelope entirely (see Phase 5), validation is a
// direct function call rather than composable decorators.
//
// Validation order:
//  1. Basic field validation (stateless)
//  2. Vote round existence and liveness check (stateful, KV read)
//  3. Nullifier uniqueness check (stateful, KV read) — runs even on RecheckTx
//  4. RedPallas signature verification — skipped on RecheckTx
//  5. ZKP verification — skipped on RecheckTx
package ante

import (
	"bytes"
	"context"
	"fmt"

	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ValidateOpts configures the validation pipeline.
type ValidateOpts struct {
	// IsRecheck is true when running RecheckTx (mempool re-validation after
	// a new block commit). When true, expensive signature and ZKP checks are
	// skipped — only nullifier uniqueness is re-verified since nullifiers may
	// have been consumed by the newly committed block.
	IsRecheck bool

	// SigVerifier is the RedPallas signature verifier.
	// Use redpallas.NewMockVerifier() during development.
	SigVerifier redpallas.Verifier

	// ZKPVerifier is the zero-knowledge proof verifier.
	// Use zkp.NewMockVerifier() during development.
	ZKPVerifier zkp.Verifier
}

// DefaultOpts returns ValidateOpts with mock verifiers for development/testing.
func DefaultOpts() ValidateOpts {
	return ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: zkp.NewMockVerifier(),
	}
}

// ValidateVoteTx runs the full validation pipeline for a vote module transaction.
//
// The pipeline is designed to be called from:
//   - CheckTx: full validation (basic + round + nullifiers + sig + ZKP)
//   - RecheckTx: lightweight re-validation (basic + round + nullifiers only)
//   - FinalizeBlock: full validation before keeper execution
//
// MsgCreateVotingSession is special: it has no round ID, no nullifiers, no signature,
// and no ZKP, so only basic field validation runs.
func ValidateVoteTx(ctx context.Context, msg types.VoteMessage, k keeper.Keeper, opts ValidateOpts) error {
	// 1. Basic field validation (stateless).
	if err := msg.ValidateBasic(); err != nil {
		return fmt.Errorf("basic validation failed: %w", err)
	}

	// 2. Vote round existence and status check (message-type-aware).
	// MsgCreateVotingSession returns nil for GetVoteRoundId() since the round
	// doesn't exist yet — skip the check in that case.
	if roundID := msg.GetVoteRoundId(); roundID != nil {
		switch m := msg.(type) {
		case *types.MsgSubmitTally:
			// MsgSubmitTally requires strictly TALLYING status.
			if err := k.ValidateRoundForTally(ctx, roundID); err != nil {
				return err
			}
			// Creator must be the block proposer; rejected entirely in CheckTx.
			if err := k.ValidateTallySubmitter(ctx, m.Creator); err != nil {
				return err
			}
		default:
			if msg.AcceptsTallyingRound() {
				if err := k.ValidateRoundForShares(ctx, roundID); err != nil {
					return err
				}
			} else {
				if err := k.ValidateRoundForVoting(ctx, roundID); err != nil {
					return err
				}
			}
		}
	}

	// 3. Nullifier uniqueness (ALWAYS runs, even on RecheckTx).
	// Nullifiers may have been consumed by the block that was just committed,
	// so we must re-check every time. Nullifiers are scoped by type + round.
	if nullifiers := msg.GetNullifiers(); len(nullifiers) > 0 {
		if err := k.CheckNullifiersUnique(ctx, msg.GetNullifierType(), msg.GetVoteRoundId(), nullifiers); err != nil {
			return err
		}
	}

	// 4. Skip expensive cryptographic checks on RecheckTx.
	if opts.IsRecheck {
		return nil
	}

	// 5. Per-message-type signature and ZKP verification.
	return verifyProofs(ctx, msg, k, opts)
}

// verifyProofs dispatches to the appropriate signature and ZKP verifier
// based on the concrete message type.
func verifyProofs(ctx context.Context, msg types.VoteMessage, k keeper.Keeper, opts ValidateOpts) error {
	switch m := msg.(type) {
	case *types.MsgCreateVotingSession:
		// No cryptographic verification needed for session setup.
		return nil

	case *types.MsgDelegateVote:
		return verifyDelegation(ctx, m, k, opts)

	case *types.MsgCastVote:
		return verifyCastVote(ctx, m, k, opts)

	case *types.MsgRevealShare:
		return verifyRevealShare(m, opts)

	case *types.MsgSubmitTally:
		// No cryptographic verification needed for tally submission.
		// Authorization is checked via creator match in step 2.
		return nil

	default:
		return fmt.Errorf("unknown vote message type: %T", msg)
	}
}

// verifyDelegation verifies both the RedPallas signature and ZKP #1 for
// a MsgDelegateVote. It looks up the session to pass nc_root and
// nullifier_imt_root as ZKP public inputs.
func verifyDelegation(ctx context.Context, msg *types.MsgDelegateVote, k keeper.Keeper, opts ValidateOpts) error {
	// Require client-provided sighash to match the canonical hash of the message.
	expectedSighash := types.ComputeDelegationSighash(msg)
	if len(msg.Sighash) != 32 || !bytes.Equal(msg.Sighash, expectedSighash) {
		return types.ErrSighashMismatch
	}
	// RedPallas signature verification over the verified sighash.
	if err := opts.SigVerifier.Verify(msg.Rk, msg.Sighash, msg.SpendAuthSig); err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidSignature, err)
	}

	// Look up the session to get nc_root and nullifier_imt_root for ZKP inputs.
	kvStore := k.OpenKVStore(ctx)
	round, err := k.GetVoteRound(kvStore, msg.VoteRoundId)
	if err != nil {
		return fmt.Errorf("failed to look up round for ZKP inputs: %w", err)
	}

	// ZKP #1: delegation proof.
	if err := opts.ZKPVerifier.VerifyDelegation(msg.Proof, zkp.DelegationInputs{
		Rk:                  msg.Rk,
		SignedNoteNullifier: msg.SignedNoteNullifier,
		CmxNew:              msg.CmxNew,
		EncMemo:             msg.EncMemo,
		GovComm:             msg.GovComm,
		GovNullifiers:       msg.GovNullifiers,
		VoteRoundId:         msg.VoteRoundId,
		NcRoot:              round.NcRoot,
		NullifierImtRoot:    round.NullifierImtRoot,
	}); err != nil {
		return fmt.Errorf("%w: delegation: %v", types.ErrInvalidProof, err)
	}

	return nil
}

// verifyCastVote verifies ZKP #2 for a MsgCastVote.
// It looks up the session to get ea_pk and the commitment tree root at the
// anchor height, mirroring how verifyDelegation fetches nc_root and nf_imt_root.
func verifyCastVote(ctx context.Context, msg *types.MsgCastVote, k keeper.Keeper, opts ValidateOpts) error {
	kvStore := k.OpenKVStore(ctx)

	// Fetch vote commitment tree root at the anchor height.
	root, err := k.GetCommitmentRootAtHeight(kvStore, msg.VoteCommTreeAnchorHeight)
	if err != nil {
		return fmt.Errorf("failed to get commitment tree root at height %d: %w", msg.VoteCommTreeAnchorHeight, err)
	}
	if root == nil {
		return fmt.Errorf("%w: no commitment tree root at height %d", types.ErrInvalidAnchorHeight, msg.VoteCommTreeAnchorHeight)
	}

	// Fetch the session to get the election authority public key.
	round, err := k.GetVoteRound(kvStore, msg.VoteRoundId)
	if err != nil {
		return fmt.Errorf("failed to look up round for ZKP inputs: %w", err)
	}

	if err := opts.ZKPVerifier.VerifyVoteCommitment(msg.Proof, zkp.VoteCommitmentInputs{
		VanNullifier:         msg.VanNullifier,
		RVpkX:                msg.RVpkX,
		RVpkY:                msg.RVpkY,
		VoteAuthorityNoteNew: msg.VoteAuthorityNoteNew,
		VoteCommitment:       msg.VoteCommitment,
		ProposalId:           msg.ProposalId,
		VoteRoundId:          msg.VoteRoundId,
		AnchorHeight:         msg.VoteCommTreeAnchorHeight,
		VoteCommTreeRoot:     root,
		EaPk:                 round.EaPk,
	}); err != nil {
		return fmt.Errorf("%w: vote commitment: %v", types.ErrInvalidProof, err)
	}

	return nil
}

// verifyRevealShare verifies ZKP #3 for a MsgRevealShare.
func verifyRevealShare(msg *types.MsgRevealShare, opts ValidateOpts) error {
	if err := opts.ZKPVerifier.VerifyVoteShare(msg.Proof, zkp.VoteShareInputs{
		ShareNullifier: msg.ShareNullifier,
		EncShare:       msg.EncShare,
		ProposalId:     msg.ProposalId,
		VoteDecision:   msg.VoteDecision,
		VoteRoundId:    msg.VoteRoundId,
		AnchorHeight:   msg.VoteCommTreeAnchorHeight,
	}); err != nil {
		return fmt.Errorf("%w: vote share: %v", types.ErrInvalidProof, err)
	}

	return nil
}
