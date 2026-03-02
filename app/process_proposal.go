package app

import (
	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"

	voteapi "github.com/z-cale/zally/api"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ProcessProposalHandler returns a handler that validates injected txs
// proposed by the block proposer. For deal messages: verifies the round is
// PENDING with ceremony REGISTERING, payload count matches, creator is a
// ceremony validator, and creator matches the block proposer. For ack
// messages: verifies the round is PENDING with ceremony DEALT, creator is a
// ceremony validator, and no duplicate ack. For tally messages: verifies the
// round is TALLYING and creator matches the block proposer. All other txs
// pass through (ACCEPT).
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

			// Validate injected ceremony deal txs.
			if tag == voteapi.TagDealExecutiveAuthorityKey {
				if err := validateInjectedDeal(ctx, voteKeeper, txBytes, logger); err != nil {
					logger.Error("ProcessProposal: rejecting block — invalid deal tx", "err", err)
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

// validateInjectedDeal checks that an injected MsgDealExecutiveAuthorityKey
// is valid: the round is PENDING with ceremony in REGISTERING, the payload
// count matches the ceremony validator count, the creator is a ceremony
// validator, and the creator matches the current block proposer.
func validateInjectedDeal(ctx sdk.Context, voteKeeper *votekeeper.Keeper, txBytes []byte, logger log.Logger) error {
	_, msg, err := voteapi.DecodeCeremonyTx(txBytes)
	if err != nil {
		return err
	}

	dealMsg, ok := msg.(*types.MsgDealExecutiveAuthorityKey)
	if !ok {
		return errInvalidInjectedTx("expected MsgDealExecutiveAuthorityKey")
	}

	kvStore := voteKeeper.OpenKVStore(ctx)
	round, err := voteKeeper.GetVoteRound(kvStore, dealMsg.VoteRoundId)
	if err != nil {
		return err
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_PENDING {
		return errInvalidInjectedTx("round is not PENDING")
	}
	if round.CeremonyStatus != types.CeremonyStatus_CEREMONY_STATUS_REGISTERING {
		return errInvalidInjectedTx("ceremony is not REGISTERING")
	}
	if len(dealMsg.Payloads) != len(round.CeremonyValidators) {
		return errInvalidInjectedTx("payload count does not match validator count")
	}

	// Verify creator is a ceremony validator.
	if _, found := votekeeper.FindValidatorInRoundCeremony(round, dealMsg.Creator); !found {
		return errInvalidInjectedTx("creator is not a ceremony validator")
	}

	// Verify creator matches the block proposer.
	if err := voteKeeper.ValidateDealSubmitter(ctx, dealMsg.Creator); err != nil {
		return errInvalidInjectedTx(err.Error())
	}

	return nil
}

// validateInjectedAck checks that an injected MsgAckExecutiveAuthorityKey is
// valid: the round is PENDING with ceremony in DEALT, the creator is a
// ceremony validator, and the creator has not already acked.
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

	return nil
}

// validateInjectedTally checks that an injected MsgSubmitTally is valid:
// the round exists and is in TALLYING state, and the creator matches the
// current block proposer.
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

	if err := voteKeeper.ValidateTallySubmitter(ctx, tallyMsg.Creator); err != nil {
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
