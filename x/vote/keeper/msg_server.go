package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/shielded-vote/crypto/roundid"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

var _ types.MsgServer = msgServer{}

type msgServer struct {
	types.UnimplementedMsgServer
	k *Keeper
}

// NewMsgServerImpl returns an implementation of the vote MsgServer interface.
func NewMsgServerImpl(keeper *Keeper) types.MsgServer {
	return &msgServer{k: keeper}
}

// CreateVotingSession handles MsgCreateVotingSession.
// Computes vote_round_id = Poseidon(snapshot_height, snapshot_blockhash_lo,
// snapshot_blockhash_hi, proposals_hash_lo, proposals_hash_hi, vote_end_time,
// nullifier_imt_root, nc_root) via FFI,
// stores the VoteRound in PENDING status with a ceremony validator snapshot,
// and emits an event. The round transitions to ACTIVE when its per-round
// ceremony confirms (auto-deal + auto-ack via PrepareProposal).
func (ms msgServer) CreateVotingSession(goCtx context.Context, msg *types.MsgCreateVotingSession) (*types.MsgCreateVotingSessionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Only the vote manager can create voting sessions.
	if err := ms.k.ValidateVoteManagerOnly(goCtx, msg.Creator); err != nil {
		return nil, err
	}

	kvStore := ms.k.OpenKVStore(ctx)

	// Derive vote_round_id deterministically.
	roundID, err := deriveRoundID(msg)
	if err != nil {
		return nil, err
	}

	// Reject if round already exists. GetVoteRound returns ErrRoundNotFound
	// on miss; any other error is an unexpected KV/unmarshal failure.
	existing, err := ms.k.GetVoteRound(kvStore, roundID)
	if existing != nil {
		return nil, fmt.Errorf("%w: %x", types.ErrRoundAlreadyExists, roundID)
	}
	if err != nil && !errors.Is(err, types.ErrRoundNotFound) {
		return nil, err
	}

	// Reject if another round is already PENDING (one active ceremony at a time).
	hasPending, err := ms.k.HasPendingRound(kvStore)
	if err != nil {
		return nil, err
	}
	if hasPending {
		return nil, fmt.Errorf("%w: another round ceremony is already in progress", types.ErrCeremonySessionActive)
	}

	// Snapshot eligible validators (bonded + have Pallas PK).
	eligible, err := ms.k.GetEligibleValidators(goCtx, kvStore)
	if err != nil {
		return nil, err
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("no validators have registered Pallas keys")
	}

	// Assign each validator their immutable 1-based Shamir evaluation point
	// (shamir_index = position + 1). This index must match the evaluation point
	// used during the Shamir split in the deal phase and must survive
	// StripNonAckersFromRound so that Lagrange interpolation always uses the
	// correct original x-coordinate, even after non-ackers are removed.
	ceremonyValidators := make([]*types.ValidatorPallasKey, len(eligible))
	for i, v := range eligible {
		vCopy := proto.Clone(v).(*types.ValidatorPallasKey)
		vCopy.ShamirIndex = uint32(i + 1)
		ceremonyValidators[i] = vCopy
	}

	ms.k.Logger().Info("CreateVotingSession",
		"round_id", hex.EncodeToString(roundID),
		types.SessionKeyNullifierImtRoot, hex.EncodeToString(msg.NullifierImtRoot),
		types.SessionKeyNcRoot, hex.EncodeToString(msg.NcRoot),
		"ceremony_validators", len(eligible),
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
		Status:            types.SessionStatus_SESSION_STATUS_PENDING,
		// EaPk left empty — set when ceremony confirms.
		VkZkp1:          msg.VkZkp1,
		VkZkp2:          msg.VkZkp2,
		VkZkp3:          msg.VkZkp3,
		Proposals:       msg.Proposals,
		Description:     msg.Description,
		CreatedAtHeight: uint64(ctx.BlockHeight()),
		Title:           msg.Title,
		// Per-round ceremony fields.
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: ceremonyValidators,
	}

	AppendCeremonyLog(round, uint64(ctx.BlockHeight()),
		fmt.Sprintf("round created with %d ceremony validators", len(eligible)))

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
		if err := ms.k.CheckAndSetNullifier(kvStore, types.NullifierTypeGov, msg.VoteRoundId, nf); err != nil {
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
	if err := ms.k.CheckAndSetNullifier(kvStore, types.NullifierTypeVoteAuthorityNote, msg.VoteRoundId, msg.VanNullifier); err != nil {
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

// SetVoteManager handles MsgSetVoteManager.
// Only the current vote manager can reassign the role. Transfers the caller's
// full usvote balance to the new manager atomically.
func (ms msgServer) SetVoteManager(goCtx context.Context, msg *types.MsgSetVoteManager) (*types.MsgSetVoteManagerResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if msg.NewManager == "" {
		return nil, fmt.Errorf("%w: new_manager cannot be empty", types.ErrInvalidField)
	}

	newManagerAddr, err := sdk.AccAddressFromBech32(msg.NewManager)
	if err != nil {
		return nil, fmt.Errorf("%w: new_manager is not a valid account address: %v", types.ErrInvalidField, err)
	}

	// Only the current vote manager can reassign.
	if err := ms.k.ValidateVoteManagerOnly(goCtx, msg.Creator); err != nil {
		return nil, err
	}

	// Transfer the current VM's entire bond-denom balance to the new manager.
	creatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return nil, fmt.Errorf("%w: creator is not a valid account address: %v", types.ErrInvalidField, err)
	}
	balance := ms.k.bankKeeper.GetBalance(ctx, creatorAddr, sdk.DefaultBondDenom)
	if balance.IsPositive() {
		if err := ms.k.bankKeeper.SendCoins(ctx, creatorAddr, newManagerAddr, sdk.NewCoins(balance)); err != nil {
			return nil, fmt.Errorf("failed to transfer funds to new vote manager: %w", err)
		}
	}

	kvStore := ms.k.OpenKVStore(ctx)
	if err := ms.k.SetVoteManager(kvStore, &types.VoteManagerState{
		Address: msg.NewManager,
	}); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeSetVoteManager,
		sdk.NewAttribute(types.AttributeKeyVoteManager, msg.NewManager),
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
	))

	return &types.MsgSetVoteManagerResponse{}, nil
}

// deriveRoundID computes a deterministic vote_round_id from the setup fields
// via Poseidon hash (FFI call to Rust). The output is a canonical 32-byte
// Pallas Fp element.
func deriveRoundID(msg *types.MsgCreateVotingSession) ([]byte, error) {
	rid, err := roundid.DeriveRoundID(
		msg.SnapshotHeight,
		msg.SnapshotBlockhash,
		msg.ProposalsHash,
		msg.VoteEndTime,
		msg.NullifierImtRoot,
		msg.NcRoot,
	)
	if err != nil {
		return nil, err
	}
	return rid[:], nil
}
