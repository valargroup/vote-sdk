package app

import (
	"bytes"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"

	voteapi "github.com/valargroup/vote-sdk/api"
	votekeeper "github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ProcessProposalHandler returns a handler that validates injected txs
// proposed by the block proposer. For DKG contribution messages: verifies
// the round is PENDING with ceremony REGISTERING, creator is a ceremony
// validator, no duplicate contribution, and creator matches the block
// proposer. For ack messages:
// verifies the round is PENDING with ceremony DEALT, creator is a ceremony
// validator, no duplicate ack, and creator matches the block proposer. For
// partial decrypt messages: verifies the round is TALLYING in threshold
// mode, creator is a ceremony validator with matching ShamirIndex, no
// duplicate submission, and creator matches the block proposer. For tally
// messages: verifies the round is TALLYING and creator matches the block
// proposer. All other txs pass through (ACCEPT).
func ProcessProposalHandler(
	voteKeeper *votekeeper.Keeper,
	logger log.Logger,
) sdk.ProcessProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
		for _, txBytes := range req.Txs {
			if len(txBytes) < 2 {
				continue
			}

			tag := txBytes[0]

			// Validate injected DKG contribution txs.
			if tag == voteapi.TagContributeDKG {
				if err := validateInjectedDKGContribution(ctx, voteKeeper, txBytes, logger); err != nil {
					logger.Error("ProcessProposal: rejecting block — invalid DKG contribution tx", "err", err)
					return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
				}
				continue
			}

			// Validate injected ceremony ack txs.
			if tag == voteapi.TagAckExecutiveAuthorityKey {
				if err := validateInjectedAck(ctx, voteKeeper, txBytes, logger); err != nil {
					logger.Error("ProcessProposal: rejecting block — invalid ack tx", "err", err)
					return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
				}
				continue
			}

			// Validate injected partial decryption txs.
			if tag == voteapi.TagSubmitPartialDecryption {
				if err := validateInjectedPartialDecrypt(ctx, voteKeeper, txBytes, logger); err != nil {
					logger.Error("ProcessProposal: rejecting block — invalid partial decrypt tx", "err", err)
					return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
				}
				continue
			}

			// Validate injected tally txs.
			if tag == voteapi.TagSubmitTally {
				if err := validateInjectedTally(ctx, voteKeeper, txBytes, logger); err != nil {
					logger.Error("ProcessProposal: rejecting block — invalid tally tx", "err", err)
					return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
				}
				continue
			}

			// All other txs are accepted without additional validation here.
		}

		return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
	}
}

// validateInjectedAck checks that an injected MsgAckExecutiveAuthorityKey is
// valid: the round is PENDING with ceremony in DEALT, the creator is a
// ceremony validator, the creator has not already acked, and the creator
// matches the current block proposer.
func validateInjectedAck(ctx sdk.Context, voteKeeper *votekeeper.Keeper, txBytes []byte, logger log.Logger) error {
	_, msg, err := voteapi.DecodeCeremonyTx(txBytes)
	if err != nil {
		return err
	}

	ackMsg, ok := msg.(*types.MsgAckExecutiveAuthorityKey)
	if !ok {
		return errInvalidInjectedTx("expected MsgAckExecutiveAuthorityKey")
	}

	kvStore := voteKeeper.OpenKVStore(ctx)
	round, err := voteKeeper.GetVoteRound(kvStore, ackMsg.VoteRoundId)
	if err != nil {
		return err
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_PENDING {
		return errInvalidInjectedTx("round is not PENDING")
	}
	if round.CeremonyStatus != types.CeremonyStatus_CEREMONY_STATUS_DEALT {
		return errInvalidInjectedTx("ceremony is not DEALT")
	}

	// Verify creator is a ceremony validator.
	if _, found := votekeeper.FindValidatorInRoundCeremony(round, ackMsg.Creator); !found {
		return errInvalidInjectedTx("creator is not a ceremony validator")
	}

	// Verify no duplicate ack.
	if _, found := votekeeper.FindAckInRoundCeremony(round, ackMsg.Creator); found {
		return errInvalidInjectedTx("creator has already acked")
	}

	// Validate skipped_contributors structural integrity.
	if err := votekeeper.ValidateSkippedContributors(round, ackMsg.Creator, ackMsg.SkippedContributors); err != nil {
		return errInvalidInjectedTx(err.Error())
	}

	// Verify ack binding hash matches expected value.
	expectedBinding := types.ComputeAckBinding(round.EaPk, ackMsg.Creator, ackMsg.SkippedContributors)
	if !bytes.Equal(ackMsg.AckSignature, expectedBinding) {
		return errInvalidInjectedTx("ack binding mismatch")
	}

	// Verify creator matches the block proposer.
	if err := voteKeeper.ValidateProposerIsCreator(ctx, ackMsg.Creator, "MsgAckExecutiveAuthorityKey"); err != nil {
		return errInvalidInjectedTx(err.Error())
	}

	return nil
}

// validateInjectedPartialDecrypt checks that an injected
// MsgSubmitPartialDecryption is valid: the round is in TALLYING state with
// Threshold > 0, the creator is a ceremony validator whose ShamirIndex
// matches the submitted ValidatorIndex, the validator has not already
// submitted, and the creator matches the current block proposer.
func validateInjectedPartialDecrypt(ctx sdk.Context, voteKeeper *votekeeper.Keeper, txBytes []byte, logger log.Logger) error {
	_, msg, err := voteapi.DecodeCeremonyTx(txBytes)
	if err != nil {
		return err
	}

	pdMsg, ok := msg.(*types.MsgSubmitPartialDecryption)
	if !ok {
		return errInvalidInjectedTx("expected MsgSubmitPartialDecryption")
	}

	kvStore := voteKeeper.OpenKVStore(ctx)
	round, err := voteKeeper.GetVoteRound(kvStore, pdMsg.VoteRoundId)
	if err != nil {
		return err
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_TALLYING {
		return errInvalidInjectedTx("round is not TALLYING")
	}
	if round.Threshold == 0 {
		return errInvalidInjectedTx("round is not in threshold mode")
	}

	ceremonyVal, found := votekeeper.FindValidatorInRoundCeremony(round, pdMsg.Creator)
	if !found {
		return errInvalidInjectedTx("creator is not a ceremony validator")
	}
	if pdMsg.ValidatorIndex != ceremonyVal.ShamirIndex {
		return errInvalidInjectedTx("validator_index does not match stored shamir_index")
	}

	has, err := voteKeeper.HasPartialDecryptionsFromValidator(kvStore, pdMsg.VoteRoundId, pdMsg.ValidatorIndex)
	if err != nil {
		return err
	}
	if has {
		return errInvalidInjectedTx("validator has already submitted partial decryptions")
	}

	if err := voteKeeper.ValidateProposerIsCreator(ctx, pdMsg.Creator, "MsgSubmitPartialDecryption"); err != nil {
		return errInvalidInjectedTx(err.Error())
	}

	return nil
}

// validateInjectedTally checks that an injected MsgSubmitTally is valid:
// the round exists and is in TALLYING state, the creator matches the block
// proposer, and the entries cover every non-empty tally accumulator.
//
// NOTE: This function intentionally does NOT verify DLEQ proofs
// or threshold decryption correctness. That verification happens in FinalizeBlock
// via the MsgSubmitTally keeper handler. A malicious proposer could cause a single
// block rejection (liveness impact), but invalid tallies can never be committed
// (safety preserved). Full verification here would duplicate expensive crypto
// and is not justified given CometBFT's leader rotation.
func validateInjectedTally(ctx sdk.Context, voteKeeper *votekeeper.Keeper, txBytes []byte, logger log.Logger) error {
	_, voteMsg, err := voteapi.DecodeVoteTx(txBytes)
	if err != nil {
		return err
	}

	tallyMsg, ok := voteMsg.(*types.MsgSubmitTally)
	if !ok {
		return errInvalidInjectedTx("expected MsgSubmitTally")
	}

	if err := voteKeeper.ValidateRoundForTally(ctx, tallyMsg.VoteRoundId); err != nil {
		return err
	}

	if err := voteKeeper.ValidateProposerIsCreator(ctx, tallyMsg.Creator, "MsgSubmitTally"); err != nil {
		return errInvalidInjectedTx(err.Error())
	}

	// Reject incomplete tallies: entries must cover all non-empty accumulators.
	kvStore := voteKeeper.OpenKVStore(ctx)
	round, err := voteKeeper.GetVoteRound(kvStore, tallyMsg.VoteRoundId)
	if err != nil {
		return err
	}
	if err := voteKeeper.ValidateTallyCompleteness(kvStore, round, tallyMsg.Entries); err != nil {
		return errInvalidInjectedTx(err.Error())
	}

	return nil
}

// validateInjectedDKGContribution checks that an injected MsgContributeDKG is
// valid: the round is PENDING with ceremony in REGISTERING, the creator is a
// ceremony validator, the creator has not already contributed, and the creator
// matches the current block proposer.
func validateInjectedDKGContribution(ctx sdk.Context, voteKeeper *votekeeper.Keeper, txBytes []byte, logger log.Logger) error {
	_, msg, err := voteapi.DecodeCeremonyTx(txBytes)
	if err != nil {
		return err
	}

	dkgMsg, ok := msg.(*types.MsgContributeDKG)
	if !ok {
		return errInvalidInjectedTx("expected MsgContributeDKG")
	}

	kvStore := voteKeeper.OpenKVStore(ctx)
	round, err := voteKeeper.GetVoteRound(kvStore, dkgMsg.VoteRoundId)
	if err != nil {
		return err
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_PENDING {
		return errInvalidInjectedTx("round is not PENDING")
	}
	if round.CeremonyStatus != types.CeremonyStatus_CEREMONY_STATUS_REGISTERING {
		return errInvalidInjectedTx("ceremony is not REGISTERING")
	}

	if _, found := votekeeper.FindValidatorInRoundCeremony(round, dkgMsg.Creator); !found {
		return errInvalidInjectedTx("creator is not a ceremony validator")
	}

	// Verify no duplicate contribution.
	if _, found := votekeeper.FindContributionInRound(round, dkgMsg.Creator); found {
		return errInvalidInjectedTx("creator has already contributed")
	}

	if err := voteKeeper.ValidateProposerIsCreator(ctx, dkgMsg.Creator, "MsgContributeDKG"); err != nil {
		return errInvalidInjectedTx(err.Error())
	}

	return nil
}

type invalidInjectedTxError string

func errInvalidInjectedTx(msg string) error {
	return invalidInjectedTxError(msg)
}

func (e invalidInjectedTxError) Error() string {
	return "invalid injected tx: " + string(e)
}
