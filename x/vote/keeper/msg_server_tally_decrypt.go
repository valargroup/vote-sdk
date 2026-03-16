package keeper

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// RevealShare handles MsgRevealShare (ZKP #3).
// Records the share nullifier, accumulates the vote amount into the tally,
// and emits an event.
func (ms msgServer) RevealShare(goCtx context.Context, msg *types.MsgRevealShare) (*types.MsgRevealShareResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Validate proposal_id against session proposals.
	if err := ms.k.ValidateProposalId(kvStore, msg.VoteRoundId, msg.ProposalId); err != nil {
		return nil, err
	}

	// Validate vote_decision is a valid option for this proposal.
	if err := ms.k.ValidateVoteDecision(kvStore, msg.VoteRoundId, msg.ProposalId, msg.VoteDecision); err != nil {
		return nil, err
	}

	// Reject duplicate reveal: share nullifier must not already be recorded (scoped to type + round).
	if err := ms.k.CheckAndSetNullifier(kvStore, types.NullifierTypeShare, msg.VoteRoundId, msg.ShareNullifier); err != nil {
		return nil, err
	}

	// Accumulate encrypted share into tally via HomomorphicAdd.
	if err := ms.k.AddToTally(kvStore, msg.VoteRoundId, msg.ProposalId, msg.VoteDecision, msg.EncShare); err != nil {
		return nil, err
	}

	// Track share count for the VoteSummary query.
	if err := ms.k.IncrementShareCount(kvStore, msg.VoteRoundId, msg.ProposalId, msg.VoteDecision); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRevealShare,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyProposalID, strconv.FormatUint(uint64(msg.ProposalId), 10)),
		sdk.NewAttribute(types.AttributeKeyVoteDecision, strconv.FormatUint(uint64(msg.VoteDecision), 10)),
		sdk.NewAttribute(types.AttributeKeyShareNullifier, fmt.Sprintf("%x", msg.ShareNullifier)),
	))

	return &types.MsgRevealShareResponse{}, nil
}

// SubmitTally handles MsgSubmitTally.
// Validates that the round is in TALLYING state, verifies each entry against
// the on-chain accumulator, stores finalized tally results, transitions the
// round to FINALIZED, and emits an event.
//
// Threshold mode (round.Threshold > 0): reads stored partial decryptions from
// KV, Lagrange-combines them per accumulator, and checks
// C2 - combined == totalValue * G. No DLEQ proof is required (Step 1).
//
// Legacy mode (round.Threshold == 0): verifies the Chaum-Pedersen DLEQ proof
// submitted with each entry.
func (ms msgServer) SubmitTally(goCtx context.Context, msg *types.MsgSubmitTally) (*types.MsgSubmitTallyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	round, err := ms.k.GetVoteRound(kvStore, msg.VoteRoundId)
	if err != nil {
		return nil, err
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_TALLYING {
		return nil, fmt.Errorf("%w: status is %s", types.ErrRoundNotTallying, round.Status)
	}

	// In threshold mode, pre-load all stored partial decryptions once so we
	// can look up each accumulator's partials during per-entry verification
	// without repeated full-range KV scans.
	var pdMap map[uint64][]PartialDecryptionWithIndex
	if round.Threshold > 0 {
		pdMap, err = ms.k.GetPartialDecryptionsForRound(kvStore, msg.VoteRoundId)
		if err != nil {
			return nil, fmt.Errorf("failed to load partial decryptions: %w", err)
		}
	}

	for i, entry := range msg.Entries {
		if err := ValidateEntryBounds(round, entry.ProposalId, entry.VoteDecision); err != nil {
			return nil, fmt.Errorf("entry[%d]: %w", i, err)
		}

		accBytes, err := ms.k.GetTally(kvStore, msg.VoteRoundId, entry.ProposalId, entry.VoteDecision)
		if err != nil {
			return nil, fmt.Errorf("failed to get tally for entry[%d]: %w", i, err)
		}

		if accBytes == nil {
			// No votes were revealed for this accumulator — require zero value.
			if entry.TotalValue != 0 {
				return nil, fmt.Errorf("%w: entry[%d] claims value %d but no accumulator exists",
					types.ErrTallyMismatch, i, entry.TotalValue)
			}
		} else if round.Threshold > 0 {
			ct, err := elgamal.UnmarshalCiphertext(accBytes)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal accumulator for entry[%d]: %w", i, err)
			}
			// Threshold mode: verify by Lagrange-combining the stored partial
			// decryptions and checking C2 - combined == totalValue * G.
			accKey := AccumulatorKey(entry.ProposalId, entry.VoteDecision)
			storedPartials := pdMap[accKey]
			if len(storedPartials) == 0 {
				return nil, fmt.Errorf("%w: entry[%d] no partial decryptions stored for (proposal=%d, decision=%d)",
					types.ErrTallyMismatch, i, entry.ProposalId, entry.VoteDecision)
			}

			shamirPartials := make([]shamir.PartialDecryption, len(storedPartials))
			for j, pd := range storedPartials {
				point, err := elgamal.UnmarshalPoint(pd.PartialDecrypt)
				if err != nil {
					return nil, fmt.Errorf("entry[%d]: invalid partial_decrypt for validator %d: %w",
						i, pd.ValidatorIndex, err)
				}
				shamirPartials[j] = shamir.PartialDecryption{
					Index: int(pd.ValidatorIndex),
					Di:    point,
				}
			}

			skC1, err := shamir.CombinePartials(shamirPartials, int(round.Threshold))
			if err != nil {
				return nil, fmt.Errorf("%w: entry[%d] Lagrange combination failed: %v",
					types.ErrTallyMismatch, i, err)
			}

			// C2 - ea_sk*C1 must equal totalValue * G.
			vG := ct.C2.Sub(skC1)
			if !bytes.Equal(vG.ToAffineCompressed(), elgamal.ValuePoint(entry.TotalValue).ToAffineCompressed()) {
				return nil, fmt.Errorf("%w: entry[%d] C2 - combined_partial != totalValue*G",
					types.ErrTallyMismatch, i)
			}
		} else {
			// Legacy mode: verify the Chaum-Pedersen DLEQ proof.
			ct, err := elgamal.UnmarshalCiphertext(accBytes)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal accumulator for entry[%d]: %w", i, err)
			}
			pk, err := elgamal.UnmarshalPublicKey(round.EaPk)
			if err != nil {
				return nil, fmt.Errorf("failed to unmarshal EA public key: %w", err)
			}
			if err := elgamal.VerifyDLEQProof(entry.DecryptionProof, pk, ct, entry.TotalValue); err != nil {
				return nil, fmt.Errorf("%w: entry[%d] DLEQ verification failed: %v",
					types.ErrTallyMismatch, i, err)
			}
		}

		if err := ms.k.SetTallyResult(kvStore, &types.TallyResult{
			VoteRoundId:  msg.VoteRoundId,
			ProposalId:   entry.ProposalId,
			VoteDecision: entry.VoteDecision,
			TotalValue:   entry.TotalValue,
		}); err != nil {
			return nil, fmt.Errorf("failed to store tally result for entry[%d]: %w", i, err)
		}
	}

	// Verify completeness: entries must cover every non-empty accumulator.
	// Without this check a malicious proposer could finalize a round with
	// missing entries, permanently omitting vote results.
	if err := ms.k.ValidateTallyCompleteness(kvStore, round, msg.Entries); err != nil {
		return nil, err
	}

	// Transition to FINALIZED.
	round.Status = types.SessionStatus_SESSION_STATUS_FINALIZED
	if err := ms.k.SetVoteRound(kvStore, round); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeSubmitTally,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyOldStatus, types.SessionStatus_SESSION_STATUS_TALLYING.String()),
		sdk.NewAttribute(types.AttributeKeyNewStatus, types.SessionStatus_SESSION_STATUS_FINALIZED.String()),
		sdk.NewAttribute(types.AttributeKeyFinalizedEntries, strconv.Itoa(len(msg.Entries))),
	))

	return &types.MsgSubmitTallyResponse{
		FinalizedEntries: uint32(len(msg.Entries)),
	}, nil
}

// SubmitPartialDecryption handles MsgSubmitPartialDecryption.
//
// This message is auto-injected by PrepareProposal during the TALLYING phase
// of a threshold-mode voting round and must never enter the mempool.
//
// Each entry's DLEQ proof is verified against the validator's on-chain
// verification key VK_i, proving log_G(VK_i) == log_{C1}(D_i). This prevents
// a malicious validator from submitting a bogus partial decryption.
func (ms msgServer) SubmitPartialDecryption(goCtx context.Context, msg *types.MsgSubmitPartialDecryption) (*types.MsgSubmitPartialDecryptionResponse, error) {
	// Block mempool submission and verify creator is the block proposer.
	if err := ms.k.ValidateProposerIsCreator(goCtx, msg.Creator, "MsgSubmitPartialDecryption"); err != nil {
		return nil, err
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	round, err := ms.k.GetVoteRound(kvStore, msg.VoteRoundId)
	if err != nil {
		return nil, err
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_TALLYING {
		return nil, fmt.Errorf("%w: status is %s, expected TALLYING", types.ErrRoundNotTallying, round.Status)
	}

	if round.Threshold == 0 {
		return nil, fmt.Errorf("%w: MsgSubmitPartialDecryption requires threshold mode (threshold > 0)", types.ErrInvalidField)
	}

	// Validate validator_index against the creator's stored ShamirIndex.
	// We look up by address rather than by array position because
	// StripNonAckersFromRound may have compacted CeremonyValidators after
	// some validators failed to ack, making array positions unreliable.
	ceremonyVal, found := FindValidatorInRoundCeremony(round, msg.Creator)
	if !found {
		return nil, fmt.Errorf("%w: %s is not a ceremony validator for this round",
			types.ErrInvalidField, msg.Creator)
	}
	if msg.ValidatorIndex != ceremonyVal.ShamirIndex {
		return nil, fmt.Errorf("%w: validator_index %d does not match stored shamir_index %d for %s",
			types.ErrInvalidField, msg.ValidatorIndex, ceremonyVal.ShamirIndex, msg.Creator)
	}

	// Reject duplicate submissions — one submission per validator per round.
	has, err := ms.k.HasPartialDecryptionsFromValidator(kvStore, msg.VoteRoundId, msg.ValidatorIndex)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, fmt.Errorf("%w: validator %s (index %d) already submitted partial decryptions for round %x",
			types.ErrInvalidField, msg.Creator, msg.ValidatorIndex, msg.VoteRoundId)
	}

	if len(msg.Entries) == 0 {
		return nil, fmt.Errorf("%w: entries cannot be empty", types.ErrInvalidField)
	}

	// Look up VK_i for DLEQ verification. VerificationKeys uses the original
	// 0-based array ordering from deal time; ShamirIndex is 1-based.
	vkIdx := int(msg.ValidatorIndex) - 1
	if vkIdx < 0 || vkIdx >= len(round.VerificationKeys) {
		return nil, fmt.Errorf("%w: validator_index %d out of range for verification_keys (len=%d)",
			types.ErrInvalidField, msg.ValidatorIndex, len(round.VerificationKeys))
	}
	vkPk, err := elgamal.UnmarshalPublicKey(round.VerificationKeys[vkIdx])
	if err != nil {
		return nil, fmt.Errorf("%w: failed to unmarshal VK_%d: %v",
			types.ErrInvalidField, msg.ValidatorIndex, err)
	}

	for i, entry := range msg.Entries {
		Di, err := elgamal.UnmarshalPoint(entry.PartialDecrypt)
		if err != nil {
			return nil, fmt.Errorf("%w: entry[%d] partial_decrypt is not a valid Pallas point: %v",
				types.ErrInvalidField, i, err)
		}
		if err := ValidateEntryBounds(round, entry.ProposalId, entry.VoteDecision); err != nil {
			return nil, fmt.Errorf("entry[%d]: %w", i, err)
		}

		// Load the tally accumulator to get C1 for DLEQ verification.
		accBytes, err := ms.k.GetTally(kvStore, msg.VoteRoundId, entry.ProposalId, entry.VoteDecision)
		if err != nil {
			return nil, fmt.Errorf("entry[%d]: failed to load tally accumulator: %w", i, err)
		}
		if accBytes == nil {
			return nil, fmt.Errorf("%w: entry[%d] no accumulator for (proposal=%d, decision=%d)",
				types.ErrInvalidField, i, entry.ProposalId, entry.VoteDecision)
		}
		ct, err := elgamal.UnmarshalCiphertext(accBytes)
		if err != nil {
			return nil, fmt.Errorf("entry[%d]: failed to unmarshal accumulator: %w", i, err)
		}

		// Verify DLEQ proof: log_G(VK_i) == log_{C1}(D_i)
		if err := elgamal.VerifyPartialDecryptDLEQ(entry.DleqProof, vkPk.Point, ct.C1, Di); err != nil {
			return nil, fmt.Errorf("%w: entry[%d] DLEQ verification failed for validator %d: %v",
				types.ErrInvalidField, i, msg.ValidatorIndex, err)
		}
	}

	if err := ms.k.SetPartialDecryptions(kvStore, msg.VoteRoundId, msg.ValidatorIndex, msg.Entries); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeSubmitPartialDecryption,
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
		sdk.NewAttribute(types.AttributeKeyValidatorIndex, strconv.Itoa(int(msg.ValidatorIndex))),
		sdk.NewAttribute(types.AttributeKeyEntryCount, strconv.Itoa(len(msg.Entries))),
	))

	return &types.MsgSubmitPartialDecryptionResponse{}, nil
}
