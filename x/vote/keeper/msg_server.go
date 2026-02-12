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

	// Record share nullifier (scoped to share type + round).
	if err := ms.k.SetNullifier(kvStore, types.NullifierTypeShare, msg.VoteRoundId, msg.ShareNullifier); err != nil {
		return nil, err
	}

	// Accumulate tally.
	if err := ms.k.AddToTally(kvStore, msg.VoteRoundId, msg.ProposalId, msg.VoteDecision, msg.VoteAmount); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRevealShare,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyProposalID, strconv.FormatUint(uint64(msg.ProposalId), 10)),
		sdk.NewAttribute(types.AttributeKeyVoteDecision, strconv.FormatUint(uint64(msg.VoteDecision), 10)),
		sdk.NewAttribute(types.AttributeKeyVoteAmount, strconv.FormatUint(msg.VoteAmount, 10)),
	))

	return &types.MsgRevealShareResponse{}, nil
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
