package keeper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"golang.org/x/crypto/blake2b"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/x/vote/types"
)

var _ types.MsgServer = msgServer{}

type msgServer struct {
	types.UnimplementedMsgServer
	k Keeper
}

// NewMsgServerImpl returns an implementation of the vote MsgServer interface.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{k: keeper}
}

// CreateVotingSession handles MsgCreateVotingSession.
// Computes vote_round_id = Blake2b-256(snapshot_height || snapshot_blockhash ||
// proposals_hash || vote_end_time || nullifier_imt_root || nc_root),
// stores the VoteRound, and emits an event.
func (ms msgServer) CreateVotingSession(goCtx context.Context, msg *types.MsgCreateVotingSession) (*types.MsgCreateVotingSessionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Only the vote manager can create voting sessions.
	if err := ms.k.ValidateVoteManagerOnly(goCtx, msg.Creator); err != nil {
		return nil, err
	}

	kvStore := ms.k.OpenKVStore(ctx)

	// Derive vote_round_id deterministically.
	roundID := deriveRoundID(msg)

	// Reject if round already exists.
	existing, err := ms.k.GetVoteRound(kvStore, roundID)
	if err != nil && existing != nil {
		return nil, fmt.Errorf("%w: %x", types.ErrRoundAlreadyExists, roundID)
	}
	// err != nil && existing == nil means ErrRoundNotFound, which is expected.
	if existing != nil {
		return nil, fmt.Errorf("%w: %x", types.ErrRoundAlreadyExists, roundID)
	}

	// Read ea_pk from confirmed ceremony state.
	ceremony, err := ms.k.GetCeremonyState(kvStore)
	if err != nil {
		return nil, err
	}
	if ceremony == nil || ceremony.Status != types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED {
		return nil, fmt.Errorf("%w", types.ErrCeremonyNotReady)
	}

	ms.k.Logger().Info("CreateVotingSession roots",
		"nullifier_imt_root", hex.EncodeToString(msg.NullifierImtRoot),
		"nc_root", hex.EncodeToString(msg.NcRoot),
	)
	round := &types.VoteRound{
		VoteRoundId:       roundID,
		SnapshotHeight:    msg.SnapshotHeight,
		SnapshotBlockhash: msg.SnapshotBlockhash,
		ProposalsHash:     msg.ProposalsHash,
		VoteEndTime:       msg.VoteEndTime,
		NullifierImtRoot:  msg.NullifierImtRoot,
		NcRoot:            msg.NcRoot,
		Creator:           msg.Creator,
		Status:            types.SessionStatus_SESSION_STATUS_ACTIVE,
		EaPk:              ceremony.EaPk,
		VkZkp1:            msg.VkZkp1,
		VkZkp2:            msg.VkZkp2,
		VkZkp3:            msg.VkZkp3,
		Proposals:         msg.Proposals,
		Description:       msg.Description,
	}

	if err := ms.k.SetVoteRound(kvStore, round); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeCreateVotingSession,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", roundID)),
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
	))

	return &types.MsgCreateVotingSessionResponse{VoteRoundId: roundID}, nil
}

// DelegateVote handles MsgDelegateVote (ZKP #1).
// Records governance nullifiers, appends van_cmx to the commitment tree,
// and emits an event.
func (ms msgServer) DelegateVote(goCtx context.Context, msg *types.MsgDelegateVote) (*types.MsgDelegateVoteResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Record each governance nullifier (scoped to gov type + round).
	for _, nf := range msg.GovNullifiers {
		has, err := ms.k.HasNullifier(kvStore, types.NullifierTypeGov, msg.VoteRoundId, nf)
		if err != nil {
			return nil, err
		}
		if has {
			return nil, fmt.Errorf("%w: nullifier already exists", types.ErrDuplicateNullifier)
		}
		if err := ms.k.SetNullifier(kvStore, types.NullifierTypeGov, msg.VoteRoundId, nf); err != nil {
			return nil, err
		}
	}

	// Only van_cmx is appended to the commitment tree. cmx_new is recorded
	// on-chain but not included in the tree — no subsequent proof references it;
	// only the VAN (van_cmx) needs a Merkle path for ZKP #2.
	vanCmxIdx, err := ms.k.AppendCommitment(kvStore, msg.VanCmx)
	if err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDelegateVote,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyLeafIndex, fmt.Sprintf("%d", vanCmxIdx)),
		sdk.NewAttribute(types.AttributeKeyNullifiers, strconv.Itoa(len(msg.GovNullifiers))),
	))

	return &types.MsgDelegateVoteResponse{}, nil
}

// CastVote handles MsgCastVote (ZKP #2).
// Validates the anchor height, records the vote-authority-note nullifier, appends
// vote_authority_note_new and vote_commitment to the tree, and emits an event.
func (ms msgServer) CastVote(goCtx context.Context, msg *types.MsgCastVote) (*types.MsgCastVoteResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Validate proposal_id against session proposals.
	if err := ms.k.ValidateProposalId(kvStore, msg.VoteRoundId, msg.ProposalId); err != nil {
		return nil, err
	}

	// Validate anchor height references a stored root.
	root, err := ms.k.GetCommitmentRootAtHeight(kvStore, msg.VoteCommTreeAnchorHeight)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("%w: no root at height %d", types.ErrInvalidAnchorHeight, msg.VoteCommTreeAnchorHeight)
	}

	// Reject double-vote: VAN nullifier must not already be recorded (scoped to type + round).
	has, err := ms.k.HasNullifier(kvStore, types.NullifierTypeVoteAuthorityNote, msg.VoteRoundId, msg.VanNullifier)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, fmt.Errorf("%w: nullifier already exists", types.ErrDuplicateNullifier)
	}
	if err := ms.k.SetNullifier(kvStore, types.NullifierTypeVoteAuthorityNote, msg.VoteRoundId, msg.VanNullifier); err != nil {
		return nil, err
	}

	// Append vote_authority_note_new, then vote_commitment.
	vanIdx, err := ms.k.AppendCommitment(kvStore, msg.VoteAuthorityNoteNew)
	if err != nil {
		return nil, err
	}
	vcIdx, err := ms.k.AppendCommitment(kvStore, msg.VoteCommitment)
	if err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeCastVote,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyLeafIndex, fmt.Sprintf("%d,%d", vanIdx, vcIdx)),
	))

	return &types.MsgCastVoteResponse{}, nil
}

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
	has, err := ms.k.HasNullifier(kvStore, types.NullifierTypeShare, msg.VoteRoundId, msg.ShareNullifier)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, fmt.Errorf("%w: nullifier already exists", types.ErrDuplicateNullifier)
	}
	if err := ms.k.SetNullifier(kvStore, types.NullifierTypeShare, msg.VoteRoundId, msg.ShareNullifier); err != nil {
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
// Validates that the round is in TALLYING state and the creator matches,
// then validates each tally entry against the on-chain accumulator,
// stores finalized tally results, transitions the round to FINALIZED,
// and emits an event.
func (ms msgServer) SubmitTally(goCtx context.Context, msg *types.MsgSubmitTally) (*types.MsgSubmitTallyResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Validate the round exists, is in TALLYING state, and creator matches.
	round, err := ms.k.GetVoteRound(kvStore, msg.VoteRoundId)
	if err != nil {
		return nil, err
	}

	if round.Status != types.SessionStatus_SESSION_STATUS_TALLYING {
		return nil, fmt.Errorf("%w: status is %s", types.ErrRoundNotTallying, round.Status)
	}

	// Validate each entry and store finalized tally results.
	for i, entry := range msg.Entries {
		// Validate proposal_id is within range (1-indexed).
		if entry.ProposalId < 1 || int(entry.ProposalId) > len(round.Proposals) {
			return nil, fmt.Errorf("%w: entry[%d] proposal_id %d out of range [1, %d]",
				types.ErrInvalidProposalID, i, entry.ProposalId, len(round.Proposals))
		}

		// Validate vote_decision is a valid option for this proposal.
		proposal := round.Proposals[entry.ProposalId-1]
		if int(entry.VoteDecision) >= len(proposal.Options) {
			return nil, fmt.Errorf("%w: entry[%d] vote_decision %d out of range [0, %d) for proposal %d",
				types.ErrInvalidField, i, entry.VoteDecision, len(proposal.Options), entry.ProposalId)
		}

		// Verify Chaum-Pedersen DLEQ proof that total_value matches the
		// encrypted accumulator.
		accBytes, err := ms.k.GetTally(kvStore, msg.VoteRoundId, entry.ProposalId, entry.VoteDecision)
		if err != nil {
			return nil, fmt.Errorf("failed to get tally for entry[%d]: %w", i, err)
		}
		if accBytes == nil {
			// No shares revealed for this (proposal, decision) — require zero value and no proof.
			if entry.TotalValue != 0 {
				return nil, fmt.Errorf("%w: entry[%d] claims value %d but no accumulator exists",
					types.ErrTallyMismatch, i, entry.TotalValue)
			}
		} else {
			// Shares are revealed, verify the DLEQ proof.
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

		// Store the finalized tally result (decrypted plaintext from EA).
		if err := ms.k.SetTallyResult(kvStore, &types.TallyResult{
			VoteRoundId:  msg.VoteRoundId,
			ProposalId:   entry.ProposalId,
			VoteDecision: entry.VoteDecision,
			TotalValue:   entry.TotalValue,
		}); err != nil {
			return nil, fmt.Errorf("failed to store tally result for entry[%d]: %w", i, err)
		}
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

// deriveRoundID computes a deterministic vote_round_id from the setup fields.
// Blake2b-256(snapshot_height || snapshot_blockhash || proposals_hash ||
//
//	vote_end_time || nullifier_imt_root || nc_root)
//
// uint64 fields are encoded as 8 bytes big-endian.
func deriveRoundID(msg *types.MsgCreateVotingSession) []byte {
	h, _ := blake2b.New256(nil) // nil key = unkeyed; never errors
	var buf [8]byte

	binary.BigEndian.PutUint64(buf[:], msg.SnapshotHeight)
	h.Write(buf[:])
	h.Write(msg.SnapshotBlockhash)
	h.Write(msg.ProposalsHash)
	binary.BigEndian.PutUint64(buf[:], msg.VoteEndTime)
	h.Write(buf[:])
	h.Write(msg.NullifierImtRoot)
	h.Write(msg.NcRoot)

	return h.Sum(nil)
}
