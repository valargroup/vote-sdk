package keeper

import (
	"context"
	"encoding/hex"
	"strconv"
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// CheckProxyDelegationBatch runs steps 2 to 5 of the 0x09 ante
// (docs/proxy-delegation/contracts-v1.md §5.1) in order, with height as the
// admission height h, without writing state. Every failure reports its
// registered code (§0). Callers check the dormancy gate and ValidateBasic.
func (k *Keeper) CheckProxyDelegationBatch(ctx context.Context, msg *types.MsgProxyDelegationBatch, height uint64) error {
	roundID := msg.VoteRoundId
	kvStore := k.OpenKVStore(ctx)
	round, err := k.GetVoteRound(kvStore, roundID)
	if err != nil {
		return types.SurfaceRegisteredCode(err)
	}
	if !round.GetProxyDelegation().GetEnabled() {
		return errorsmod.Wrapf(types.ErrProxyDelegationDisabled, "round %x is not a proxy round", roundID)
	}

	// Nullifiers, then the expiry, both before the round status: a retry of
	// a batch that landed is always ErrDuplicateNullifier and an expired one
	// ErrProxyBatchExpired, even after the round closes.
	vanNullifiers := make([][]byte, len(msg.Dcs))
	slotNullifiers := make([][]byte, len(msg.Dcs))
	for i, dc := range msg.Dcs {
		vanNullifiers[i], slotNullifiers[i] = dc.VanNullifier, dc.DcSlotNullifier
	}
	for _, set := range []struct {
		nfType     types.NullifierType
		nullifiers [][]byte
	}{
		{types.NullifierTypeGov, msg.GetRegistration().GetGovNullifiers()},
		{types.NullifierTypeVoteAuthorityNote, vanNullifiers},
		{types.NullifierTypeProxySlot, slotNullifiers},
	} {
		if err := k.CheckNullifiersUnique(ctx, set.nfType, roundID, set.nullifiers); err != nil {
			return types.SurfaceRegisteredCode(err)
		}
	}

	if height >= msg.ExpiryHeight {
		return errorsmod.Wrapf(types.ErrProxyBatchExpired, "height %d is at or after expiry_height %d", height, msg.ExpiryHeight)
	}
	if err := k.ValidateRoundForVoting(ctx, roundID); err != nil {
		return types.SurfaceRegisteredCode(err)
	}
	params, err := k.GetProxyDelegationParams(kvStore)
	if err != nil {
		return err
	}
	if params.ProxyDcPaused {
		return errorsmod.Wrap(types.ErrProxyDelegationPaused, "DC batches are paused")
	}

	return k.EnsureCommitmentCapacity(kvStore, roundID, CommitmentAppendCount(msg))
}

// executeProxyDelegationBatch re-runs ValidateBasic and every state check of
// a 0x09 batch at the block height, the three nullifier sets all before any
// write, then applies it (contracts-v1 §5.2): it spends every nullifier,
// appends the final vote_authority_note_new and then each DC in message
// order, never an intermediate VAN or the registration's van_cmx, emits
// proxy_delegation_batch and returns batch_digest. Signatures and proofs are
// not re-verified; the FinalizeBlock ante has verified them.
func (k *Keeper) executeProxyDelegationBatch(ctx sdk.Context, msg *types.MsgProxyDelegationBatch) (*types.MsgProxyDelegationBatchResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := k.CheckProxyDelegationBatch(ctx, msg, uint64(ctx.BlockHeight())); err != nil {
		return nil, err
	}
	kvStore := k.OpenKVStore(ctx)
	roundID := msg.VoteRoundId
	if msg.Registration == nil {
		root, err := k.GetCommitmentRootAtHeight(kvStore, roundID, msg.VoteCommTreeAnchorHeight)
		if err != nil {
			return nil, err
		}
		if root == nil {
			return nil, errorsmod.Wrapf(types.ErrInvalidAnchorHeight, "no commitment tree root at height %d", msg.VoteCommTreeAnchorHeight)
		}
	}
	digest, err := types.ComputeProxyDelegationBatchDigest(ctx.ChainID(), msg.SighashFields())
	if err != nil {
		return nil, err
	}

	govNullifiers := msg.GetRegistration().GetGovNullifiers()
	for _, nf := range govNullifiers {
		if err := k.SetNullifier(kvStore, types.NullifierTypeGov, roundID, nf); err != nil {
			return nil, err
		}
	}
	vanNullifiers := make([]string, len(msg.Dcs))
	for i, dc := range msg.Dcs {
		if err := k.SetNullifier(kvStore, types.NullifierTypeVoteAuthorityNote, roundID, dc.VanNullifier); err != nil {
			return nil, err
		}
		if err := k.SetNullifier(kvStore, types.NullifierTypeProxySlot, roundID, dc.DcSlotNullifier); err != nil {
			return nil, err
		}
		vanNullifiers[i] = hex.EncodeToString(dc.VanNullifier)
	}

	finalVANIndex, err := k.AppendCommitment(kvStore, roundID, msg.Dcs[len(msg.Dcs)-1].VoteAuthorityNoteNew)
	if err != nil {
		return nil, err
	}
	dcIndices := make([]string, len(msg.Dcs))
	for i, dc := range msg.Dcs {
		index, err := k.AppendCommitment(kvStore, roundID, dc.ProxyDelegationCommitment)
		if err != nil {
			return nil, err
		}
		dcIndices[i] = strconv.FormatUint(index, 10)
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeProxyDelegationBatch,
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(roundID)),
		sdk.NewAttribute(types.AttributeKeyBatchDigest, hex.EncodeToString(digest)),
		sdk.NewAttribute(types.AttributeKeyHasRegistration, strconv.FormatBool(msg.Registration != nil)),
		sdk.NewAttribute(types.AttributeKeyNullifiers, strconv.Itoa(len(govNullifiers))),
		sdk.NewAttribute(types.AttributeKeyFinalVANLeafIndex, strconv.FormatUint(finalVANIndex, 10)),
		sdk.NewAttribute(types.AttributeKeyDCLeafIndices, strings.Join(dcIndices, ",")),
		sdk.NewAttribute(types.AttributeKeyVANNullifiers, strings.Join(vanNullifiers, ",")),
		sdk.NewAttribute(types.AttributeKeyDCCount, strconv.Itoa(len(msg.Dcs))),
	))
	return &types.MsgProxyDelegationBatchResponse{BatchDigest: digest}, nil
}
