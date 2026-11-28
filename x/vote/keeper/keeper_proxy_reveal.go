package keeper

import (
	"context"
	"encoding/hex"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// CheckPoolReveal runs the proposal-0 checks of a 0x04 reveal
// (docs/proxy-delegation/contracts-v1.md §5.4) for the ante and the handler
// to share, without writing state: the round is a proxy round
// (ErrProxyDelegationDisabled), then 1 <= d < next_index now, where d is the
// reveal's vote_decision (ErrDelegateNotFound). Delegate status and
// proxy_dc_paused are ignored, so a suspended or retired delegate's pool
// still fills, and an index registered after the round was created counts.
// Callers check the dormancy gate, ValidateBasic and ValidateRoundForVoting.
func (k *Keeper) CheckPoolReveal(ctx context.Context, msg *types.MsgRevealShare) error {
	kvStore := k.OpenKVStore(ctx)
	round, err := k.GetVoteRound(kvStore, msg.VoteRoundId)
	if err != nil {
		return err
	}
	if !round.GetProxyDelegation().GetEnabled() {
		return errorsmod.Wrapf(types.ErrProxyDelegationDisabled, "round %x is not a proxy round", msg.VoteRoundId)
	}
	next, err := k.GetProxyNextDelegateIndex(kvStore)
	if err != nil {
		return err
	}
	if d := msg.VoteDecision; d == 0 || d >= next {
		return errorsmod.Wrapf(types.ErrDelegateNotFound, "delegate %d is not below next_index %d", d, next)
	}
	return nil
}

// revealPoolShare applies a proposal-0 reveal (contracts-v1 §5.4): it re-runs
// ValidateBasic with the gate open, the round check and CheckPoolReveal, then
// spends the share nullifier, adds enc_share to Pool[d] at
// TallyKey(round, 0, d), counts it at ShareCountKey(round, 0, d) and emits
// reveal_pool_share, never reveal_share. Every failure reports its
// registered code (§0). ZKP3 is not re-verified; the FinalizeBlock ante has
// verified it.
func (k *Keeper) revealPoolShare(ctx sdk.Context, msg *types.MsgRevealShare) (*types.MsgRevealShareResponse, error) {
	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	if err := k.ValidateRoundForVoting(ctx, msg.VoteRoundId); err != nil {
		return nil, types.SurfaceRegisteredCode(err)
	}
	if err := k.CheckPoolReveal(ctx, msg); err != nil {
		return nil, err
	}
	kvStore := k.OpenKVStore(ctx)
	d := msg.VoteDecision
	if err := k.CheckAndSetNullifier(kvStore, types.NullifierTypeShare, msg.VoteRoundId, msg.ShareNullifier); err != nil {
		return nil, types.SurfaceRegisteredCode(err)
	}
	if err := k.AddToTally(kvStore, msg.VoteRoundId, types.PoolProposalID, d, msg.EncShare); err != nil {
		return nil, err
	}
	if err := k.IncrementShareCount(kvStore, msg.VoteRoundId, types.PoolProposalID, d); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeRevealPoolShare,
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(msg.VoteRoundId)),
		sdk.NewAttribute(types.AttributeKeyDelegateIndex, strconv.FormatUint(uint64(d), 10)),
		sdk.NewAttribute(types.AttributeKeyShareNullifier, hex.EncodeToString(msg.ShareNullifier)),
	))
	return &types.MsgRevealShareResponse{}, nil
}
