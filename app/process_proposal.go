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
// proposed by the block proposer. For ack messages: verifies the creator
// is a registered validator, the ceremony is DEALT, and no duplicate ack
// exists. For tally messages: verifies the round is TALLYING. All other
// txs pass through (ACCEPT).
func ProcessProposalHandler(
	voteKeeper votekeeper.Keeper,
	logger log.Logger,
) sdk.ProcessProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
		for _, txBytes := range req.Txs {
			if len(txBytes) < 2 {
				continue
			}

			tag := txBytes[0]

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

// validateInjectedAck checks that an injected MsgAckExecutiveAuthorityKey is
// valid: the ceremony is DEALT, the creator is a registered validator, and
// the creator has not already acked.
func validateInjectedAck(ctx sdk.Context, voteKeeper votekeeper.Keeper, txBytes []byte, logger log.Logger) error {
	_, msg, err := voteapi.DecodeCeremonyTx(txBytes)
	if err != nil {
		return err
	}

	ackMsg, ok := msg.(*types.MsgAckExecutiveAuthorityKey)
	if !ok {
		return errInvalidInjectedTx("expected MsgAckExecutiveAuthorityKey")
	}

	kvStore := voteKeeper.OpenKVStore(ctx)
	state, err := voteKeeper.GetCeremonyState(kvStore)
	if err != nil {
		return err
	}
	if state == nil || state.Status != types.CeremonyStatus_CEREMONY_STATUS_DEALT {
		return errInvalidInjectedTx("ceremony is not in DEALT state")
	}

	// Verify creator is a registered validator.
	if _, found := votekeeper.FindValidatorInCeremony(state, ackMsg.Creator); !found {
		return errInvalidInjectedTx("creator is not a registered validator")
	}

	// Verify no duplicate ack.
	if _, found := votekeeper.FindAckForValidator(state, ackMsg.Creator); found {
		return errInvalidInjectedTx("creator has already acked")
	}

	return nil
}

// validateInjectedTally checks that an injected MsgSubmitTally is valid:
// the round exists and is in TALLYING state.
func validateInjectedTally(ctx sdk.Context, voteKeeper votekeeper.Keeper, txBytes []byte, logger log.Logger) error {
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

	return nil
}

type invalidInjectedTxError string

func errInvalidInjectedTx(msg string) error {
	return invalidInjectedTxError(msg)
}

func (e invalidInjectedTxError) Error() string {
	return "invalid injected tx: " + string(e)
}
