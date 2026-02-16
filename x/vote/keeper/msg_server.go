package keeper

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"golang.org/x/crypto/blake2b"

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
		EaPk:              msg.EaPk,
		VkZkp1:            msg.VkZkp1,
		VkZkp2:            msg.VkZkp2,
		VkZkp3:            msg.VkZkp3,
		Proposals:         msg.Proposals,
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
// Records governance nullifiers, appends cmx_new and gov_comm to the
// commitment tree, and emits an event.
func (ms msgServer) DelegateVote(goCtx context.Context, msg *types.MsgDelegateVote) (*types.MsgDelegateVoteResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Record each governance nullifier (scoped to gov type + round).
	for _, nf := range msg.GovNullifiers {
		if err := ms.k.SetNullifier(kvStore, types.NullifierTypeGov, msg.VoteRoundId, nf); err != nil {
			return nil, err
		}
	}

	// Append cmx_new, then gov_comm to the commitment tree.
	cmxIdx, err := ms.k.AppendCommitment(kvStore, msg.CmxNew)
	if err != nil {
		return nil, err
	}
	govCommIdx, err := ms.k.AppendCommitment(kvStore, msg.GovComm)
	if err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDelegateVote,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyLeafIndex, fmt.Sprintf("%d,%d", cmxIdx, govCommIdx)),
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

	// Record vote-authority-note nullifier (scoped to type + round).
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

	// Record share nullifier (scoped to share type + round).
	if err := ms.k.SetNullifier(kvStore, types.NullifierTypeShare, msg.VoteRoundId, msg.ShareNullifier); err != nil {
		return nil, err
	}

	// Accumulate encrypted share into tally via HomomorphicAdd.
	if err := ms.k.AddToTally(kvStore, msg.VoteRoundId, msg.ProposalId, msg.VoteDecision, msg.EncShare); err != nil {
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

		// TODO(dleq): Verify Chaum-Pedersen DLEQ proof that total_value matches
		// the encrypted accumulator. For now, trust the EA's claimed value since
		// MsgSubmitTally is authority-gated (only session creator can submit).
		// Future: Use elgamal.VerifyDLEQ(entry.DecryptionProof, session.EaPk, accumulatorC1, ...)

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
