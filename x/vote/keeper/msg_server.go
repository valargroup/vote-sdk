package keeper

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/ffi/roundid"
	"github.com/valargroup/vote-sdk/x/vote/types"
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

// executeCreateVotingSession computes vote_round_id = Poseidon(created_at_height,
// snapshot_blockhash_lo, snapshot_blockhash_hi, proposals_hash_lo,
// proposals_hash_hi, vote_end_time, nullifier_imt_root, nc_root) via FFI, stores
// the VoteRound in PENDING status with a ceremony validator snapshot, and emits
// an event. The round transitions to ACTIVE when its per-round ceremony
// confirms.
func (ms msgServer) executeCreateVotingSession(goCtx context.Context, msg *types.MsgCreateVotingSession) (*types.MsgCreateVotingSessionResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	createdAtHeight := uint64(ctx.BlockHeight())

	// Derive vote_round_id deterministically from the creation height and setup
	// fields. Using the creation height allows the same vote metadata to be
	// retried after a failed ceremony without colliding with the expired round.
	roundID, err := deriveRoundID(msg, createdAtHeight)
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
	minVal, err := ms.k.GetMinCeremonyValidators(kvStore)
	if err != nil {
		return nil, err
	}
	if uint32(len(eligible)) < minVal {
		return nil, fmt.Errorf("%w: at least %d validators with registered Pallas keys required, got %d",
			types.ErrInsufficientValidators, minVal, len(eligible))
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
		Proposals:       msg.Proposals,
		Description:     msg.Description,
		CreatedAtHeight: createdAtHeight,
		CreatedAtTime:   uint64(ctx.BlockTime().Unix()),
		Title:           msg.Title,
		DiscussionUrl:   msg.DiscussionUrl,
		// Per-round ceremony fields.
		CeremonyStatus:       types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators:   ceremonyValidators,
		CeremonyPhaseStart:   uint64(ctx.BlockTime().Unix()),
		CeremonyPhaseTimeout: types.DefaultContributionTimeout,
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

	// Only van_cmx is appended to the round's commitment tree. cmx_new is
	// recorded on-chain but not included in the tree — no subsequent proof
	// references it; only the VAN (van_cmx) needs a Merkle path for ZKP #2.
	vanCmxIdx, err := ms.k.AppendCommitment(kvStore, msg.VoteRoundId, msg.VanCmx)
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

	// Validate anchor height references a stored root in this round's tree.
	root, err := ms.k.GetCommitmentRootAtHeight(kvStore, msg.VoteRoundId, msg.VoteCommTreeAnchorHeight)
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

	// Append vote_authority_note_new, then vote_commitment to the round's tree.
	vanIdx, err := ms.k.AppendCommitment(kvStore, msg.VoteRoundId, msg.VoteAuthorityNoteNew)
	if err != nil {
		return nil, err
	}
	vcIdx, err := ms.k.AppendCommitment(kvStore, msg.VoteRoundId, msg.VoteCommitment)
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

// CastVoteBatch handles an ordered, atomic sequence of ZKP #2 actions. Only
// the final successor VAN is appended globally; every vote commitment is
// retained in action order for later share reveals.
func (ms msgServer) CastVoteBatch(goCtx context.Context, msg *types.MsgCastVoteBatch) (*types.MsgCastVoteBatchResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)
	first := msg.Votes[0]

	root, err := ms.k.GetCommitmentRootAtHeight(kvStore, first.VoteRoundId, first.VoteCommTreeAnchorHeight)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("%w: no root at height %d", types.ErrInvalidAnchorHeight, first.VoteCommTreeAnchorHeight)
	}

	for _, vote := range msg.Votes {
		if err := ms.k.ValidateProposalId(kvStore, vote.VoteRoundId, vote.ProposalId); err != nil {
			return nil, err
		}
	}
	if err := ms.k.CheckNullifiersUnique(ctx, types.NullifierTypeVoteAuthorityNote, first.VoteRoundId, msg.GetNullifiers()); err != nil {
		return nil, err
	}

	for _, vote := range msg.Votes {
		if err := ms.k.SetNullifier(kvStore, types.NullifierTypeVoteAuthorityNote, first.VoteRoundId, vote.VanNullifier); err != nil {
			return nil, err
		}
	}

	finalVote := msg.Votes[len(msg.Votes)-1]
	finalVANIndex, err := ms.k.AppendCommitment(kvStore, first.VoteRoundId, finalVote.VoteAuthorityNoteNew)
	if err != nil {
		return nil, err
	}

	vcIndices := make([]string, 0, len(msg.Votes))
	proposalIDs := make([]string, 0, len(msg.Votes))
	nullifiers := make([]string, 0, len(msg.Votes))
	for _, vote := range msg.Votes {
		vcIndex, err := ms.k.AppendCommitment(kvStore, first.VoteRoundId, vote.VoteCommitment)
		if err != nil {
			return nil, err
		}
		vcIndices = append(vcIndices, strconv.FormatUint(vcIndex, 10))
		proposalIDs = append(proposalIDs, strconv.FormatUint(uint64(vote.ProposalId), 10))
		nullifiers = append(nullifiers, fmt.Sprintf("%x", vote.VanNullifier))
	}

	batchDigest := types.ComputeCastVoteBatchSighash(msg)
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeCastVoteBatch,
		sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", first.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyBatchDigest, fmt.Sprintf("%x", batchDigest)),
		sdk.NewAttribute(types.AttributeKeyBatchSize, strconv.Itoa(len(msg.Votes))),
		sdk.NewAttribute(types.AttributeKeyFinalVANLeafIndex, strconv.FormatUint(finalVANIndex, 10)),
		sdk.NewAttribute(types.AttributeKeyVoteCommitmentLeafIndices, strings.Join(vcIndices, ",")),
		sdk.NewAttribute(types.AttributeKeyProposalIDs, strings.Join(proposalIDs, ",")),
		sdk.NewAttribute(types.AttributeKeyVANNullifiers, strings.Join(nullifiers, ",")),
	))

	return &types.MsgCastVoteBatchResponse{BatchDigest: batchDigest}, nil
}

// executeUpdateVoteManagers atomically replaces the vote-manager set, threshold,
// and min_ceremony_validators. See proto for semantics. Allows the caller to
// remove themselves from the new set — the non-empty check is the only liveness
// guarantee.
func (ms msgServer) executeUpdateVoteManagers(goCtx context.Context, msg *types.MsgUpdateVoteManagers) (*types.MsgUpdateVoteManagersResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	normalized, threshold, err := types.ValidateAndNormalizeVoteManagerPolicy(msg.NewVoteManagers, msg.NewThreshold)
	if err != nil {
		return nil, fmt.Errorf("new_vote_manager_policy: %w", err)
	}
	minCeremonyValidators := types.NormalizeMinCeremonyValidators(msg.NewMinCeremonyValidators)

	for _, addr := range normalized {
		accAddr, err := sdk.AccAddressFromBech32(addr)
		if err != nil {
			return nil, fmt.Errorf("new_vote_manager_policy: address %q is not a valid bech32 address: %w", addr, err)
		}
		if err := ms.k.ensureAccountExists(goCtx, accAddr); err != nil {
			return nil, err
		}
	}

	kvStore := ms.k.OpenKVStore(ctx)
	if err := ms.k.SetVoteManagers(kvStore, &types.VoteManagerSet{Addresses: normalized, Threshold: threshold}); err != nil {
		return nil, err
	}
	if err := ms.k.SetMinCeremonyValidators(kvStore, minCeremonyValidators); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeUpdateVoteManagers,
		sdk.NewAttribute(types.AttributeKeyVoteManagers, strings.Join(normalized, ",")),
		sdk.NewAttribute(types.AttributeKeyThreshold, fmt.Sprintf("%d", threshold)),
		sdk.NewAttribute(types.AttributeKeyMinCeremonyValidators, fmt.Sprintf("%d", minCeremonyValidators)),
		sdk.NewAttribute(types.AttributeKeyCreator, msg.Creator),
	))

	return &types.MsgUpdateVoteManagersResponse{}, nil
}

// deriveRoundID computes a deterministic vote_round_id from the creation
// height and setup fields via Poseidon hash (FFI call to Rust). The output is a
// canonical 32-byte Pallas Fp element.
func deriveRoundID(msg *types.MsgCreateVotingSession, createdAtHeight uint64) ([]byte, error) {
	rid, err := roundid.DeriveRoundID(
		createdAtHeight,
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
