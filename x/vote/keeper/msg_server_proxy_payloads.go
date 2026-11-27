package keeper

import (
	"context"
	"strconv"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// executeSetProxyDelegationParams applies an approved params payload
// (docs/proxy-delegation/contracts-v1.md §5.5): it replaces the verifier set,
// verifier_threshold and max_delegates, keeps proxy_dc_paused, and emits
// proxy_delegation_params.
func (ms msgServer) executeSetProxyDelegationParams(goCtx context.Context, msg *types.MsgSetProxyDelegationParams) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)
	current, err := ms.k.GetProxyDelegationParams(kvStore)
	if err != nil {
		return err
	}
	params := &types.ProxyDelegationParams{
		Verifiers:         msg.Verifiers,
		VerifierThreshold: msg.VerifierThreshold,
		MaxDelegates:      msg.MaxDelegates,
		ProxyDcPaused:     current.ProxyDcPaused,
	}
	if err := ms.k.SetProxyDelegationParams(kvStore, params); err != nil {
		return err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeProxyDelegationParams,
		sdk.NewAttribute(types.AttributeKeyVerifierCount, strconv.Itoa(len(params.Verifiers))),
		sdk.NewAttribute(types.AttributeKeyVerifierThreshold, strconv.FormatUint(uint64(params.VerifierThreshold), 10)),
		sdk.NewAttribute(types.AttributeKeyMaxDelegates, strconv.FormatUint(uint64(params.MaxDelegates), 10)),
	))
	return nil
}

// executeSetProxyDelegationPause applies an approved pause payload
// (contracts-v1 §5.5): it sets proxy_dc_paused and nothing else, and emits
// proxy_delegation_pause.
func (ms msgServer) executeSetProxyDelegationPause(goCtx context.Context, msg *types.MsgSetProxyDelegationPause) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)
	params, err := ms.k.GetProxyDelegationParams(kvStore)
	if err != nil {
		return err
	}
	params.ProxyDcPaused = msg.Paused
	if err := ms.k.SetProxyDelegationParams(kvStore, params); err != nil {
		return err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeProxyDelegationPause,
		sdk.NewAttribute(types.AttributeKeyPaused, strconv.FormatBool(msg.Paused)),
	))
	return nil
}

// executeSetDelegateStatus applies an approved status payload (contracts-v1
// §5.5) to every named entry, or to none if one fails: suspend sets a live
// entry SUSPENDED with the reason, lift sets a SUSPENDED entry ACTIVE and
// leaves an ACTIVE one alone, and retire sets a live entry RETIRED with the
// reason and frees its handle. An unknown entry is ErrDelegateNotFound, and
// suspending or lifting a RETIRED one is ErrDelegateRetired. It emits one
// delegate_status event per entry.
func (ms msgServer) executeSetDelegateStatus(goCtx context.Context, msg *types.MsgSetDelegateStatus) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := ms.k.OpenKVStore(ctx)

	// Every entry is checked before any write, so a failing index leaves the
	// store as it was.
	entries := make([]*types.ProxyDelegateEntry, len(msg.DelegateIndexes))
	for i, d := range msg.DelegateIndexes {
		entry, err := ms.k.GetProxyDelegateEntry(kvStore, d)
		if err != nil {
			return errorsmod.Wrapf(err, "delegate_indexes[%d]", i)
		}
		if entry.Status == types.DelegateStatusRetired && msg.Action != types.DelegateStatusActionRetire {
			return errorsmod.Wrapf(types.ErrDelegateRetired, "delegate_indexes[%d]: delegate %d", i, d)
		}
		entries[i] = entry
	}
	for _, entry := range entries {
		var err error
		switch {
		case entry.Status == types.DelegateStatusRetired:
			// Only a retire reaches here, and it is a no-op.
		case msg.Action == types.DelegateStatusActionRetire:
			err = ms.k.retireProxyDelegate(kvStore, entry, msg.Reason)
		case msg.Action == types.DelegateStatusActionSuspend:
			entry.Status, entry.StatusReason = types.DelegateStatusSuspended, msg.Reason
			err = ms.k.SetProxyDelegateEntry(kvStore, entry)
		case entry.Status == types.DelegateStatusSuspended:
			entry.Status, entry.StatusReason = types.DelegateStatusActive, 0
			err = ms.k.SetProxyDelegateEntry(kvStore, entry)
		}
		if err != nil {
			return err
		}
		ctx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeDelegateStatus,
			sdk.NewAttribute(types.AttributeKeyDelegateIndex, strconv.FormatUint(uint64(entry.Index), 10)),
			sdk.NewAttribute(types.AttributeKeyAction, strconv.FormatUint(uint64(msg.Action), 10)),
			sdk.NewAttribute(types.AttributeKeyReason, strconv.FormatUint(uint64(msg.Reason), 10)),
		))
	}
	return nil
}
