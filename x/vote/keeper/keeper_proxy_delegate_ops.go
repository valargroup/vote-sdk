package keeper

import (
	"bytes"
	"context"
	"strconv"

	"cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ValidateDelegateOp runs the checks of a 0x0B op that passed ValidateBasic
// (docs/proxy-delegation/contracts-v1.md §5.3), in that section's order,
// without writing state. verifySignatures is false in RecheckTx, which skips
// the Ed25519 checks and keeps every other check. Each failure carries its
// registered code (§6). Callers check the dormancy gate. Delegate ballots
// are not accepted yet (errDelegateBallotsNotAccepted).
func (k *Keeper) ValidateDelegateOp(ctx context.Context, op *types.MsgDelegateOp, verifySignatures bool) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	kvStore := k.OpenKVStore(ctx)
	switch {
	case op.GetRegister() != nil:
		_, err := k.checkDelegateRegister(sdkCtx, kvStore, op.Register, verifySignatures)
		return err
	case op.GetRetire() != nil:
		_, err := k.checkDelegateRetire(sdkCtx, kvStore, op.Retire, verifySignatures)
		return err
	case op.GetBallot() != nil:
		return errDelegateBallotsNotAccepted()
	default:
		return errorsmod.Wrap(types.ErrInvalidField, "no delegate op is set")
	}
}

// checkDelegateRegister runs register steps 1 to 6 of contracts-v1 §5.3 in
// order and returns the index the new entry takes. See ValidateDelegateOp.
func (k *Keeper) checkDelegateRegister(ctx sdk.Context, kvStore store.KVStore, op *types.DelegateRegister, verifySignatures bool) (uint32, error) {
	params, err := k.GetProxyDelegationParams(kvStore)
	if err != nil {
		return 0, err
	}
	if len(params.GetVerifiers()) == 0 {
		return 0, errorsmod.Wrap(types.ErrRegistrationClosed, "the params verifier set is empty")
	}

	if err := types.ValidateEd25519Key(op.GetDk()); err != nil {
		return 0, errorsmod.Wrapf(types.ErrInvalidDelegateKey, "dk: %v", err)
	}
	owner, used, err := k.GetProxyUsedDelegateKey(kvStore, op.GetDk())
	if err != nil {
		return 0, err
	}
	if used {
		return 0, errorsmod.Wrapf(types.ErrDelegateKeyReused, "dk %x was registered to delegate %d", op.GetDk(), owner)
	}
	live, taken, err := k.GetProxyDelegateByHandle(kvStore, op.GetProvider(), op.GetHandle())
	if err != nil {
		return 0, err
	}
	if taken {
		return 0, errorsmod.Wrapf(types.ErrHandleTaken, "provider %d handle %q is delegate %d", op.GetProvider(), op.GetHandle(), live)
	}

	fields := types.DelegateRegistrationFields{
		DK:       op.GetDk(),
		Provider: op.GetProvider(),
		Handle:   op.GetHandle(),
		ProofID:  op.GetProofId(),
	}
	if verifySignatures {
		digest, err := types.ComputeDelegateRegisterDigest(ctx.ChainID(), fields)
		if err != nil {
			return 0, err
		}
		if err := types.VerifyEd25519ZIP215(op.GetDk(), digest, op.GetDkSig()); err != nil {
			return 0, errorsmod.Wrapf(types.ErrInvalidSignature, "dk_sig: ed25519 over REGISTER: %v", err)
		}
	}
	if err := checkDelegateAttestations(ctx, params, fields, op.GetAttestations(), verifySignatures); err != nil {
		return 0, err
	}

	next, err := k.GetProxyNextDelegateIndex(kvStore)
	if err != nil {
		return 0, err
	}
	if next > params.GetMaxDelegates() {
		return 0, errorsmod.Wrapf(types.ErrRegistryFull, "next index %d is above max_delegates %d", next, params.GetMaxDelegates())
	}
	return next, nil
}

// checkDelegateAttestations runs register step 5 of contracts-v1 §5.3: each
// attestation's verifier, window and, when verifySignatures is set,
// signature (ErrInvalidAttestation), then the distinct verifier threshold
// (ErrInsufficientAttestations).
func checkDelegateAttestations(ctx sdk.Context, params *types.ProxyDelegationParams, fields types.DelegateRegistrationFields, attestations []*types.DelegateAttestation, verifySignatures bool) error {
	blockTime, checkWindows, err := delegateOpBlockTime(ctx)
	if err != nil {
		return err
	}
	verifierKeys := make(map[uint32][]byte, len(params.GetVerifiers()))
	for _, verifier := range params.GetVerifiers() {
		verifierKeys[verifier.GetVerifierId()] = verifier.GetPubkey()
	}
	// An id names one key, so counting distinct keys dedupes by id and by
	// pubkey: a repeated attestation, or two ids sharing a key, count once.
	attested := make(map[string]struct{}, len(attestations))
	for i, attestation := range attestations {
		pubkey, ok := verifierKeys[attestation.GetVerifierId()]
		if !ok {
			return errorsmod.Wrapf(types.ErrInvalidAttestation, "attestations[%d]: verifier %d is not in the params verifier set", i, attestation.GetVerifierId())
		}
		if checkWindows && (blockTime < attestation.GetNotBefore() || blockTime >= attestation.GetExpiresAt()) {
			return errorsmod.Wrapf(types.ErrInvalidAttestation, "attestations[%d]: block time %d is outside [not_before %d, expires_at %d)",
				i, blockTime, attestation.GetNotBefore(), attestation.GetExpiresAt())
		}
		if verifySignatures {
			digest, err := types.ComputeDelegateAttestDigest(ctx.ChainID(), fields, attestation.GetNotBefore(), attestation.GetExpiresAt())
			if err != nil {
				return err
			}
			if err := types.VerifyEd25519ZIP215(pubkey, digest, attestation.GetSig()); err != nil {
				return errorsmod.Wrapf(types.ErrInvalidAttestation, "attestations[%d]: verifier %d's ed25519 signature over ATTEST: %v", i, attestation.GetVerifierId(), err)
			}
		}
		attested[string(pubkey)] = struct{}{}
	}
	if distinct := len(attested); uint64(distinct) < uint64(params.GetVerifierThreshold()) {
		return errorsmod.Wrapf(types.ErrInsufficientAttestations, "%d distinct verifiers attested, the threshold is %d", distinct, params.GetVerifierThreshold())
	}
	return nil
}

// delegateOpBlockTime returns the block time in unix seconds. known is false
// only in CheckTx or RecheckTx with the header time unset after a restart
// (pinned SDK fork, baseapp.go:444-447), which skips the attestation windows
// as ValidateRoundForVoting skips the round end (contracts-v1 §5.3 step 5).
// Any other negative time is ErrInvalidAttestation.
func delegateOpBlockTime(ctx sdk.Context) (blockTime uint64, known bool, err error) {
	unix := ctx.BlockTime().Unix()
	switch {
	case unix >= 0:
		return uint64(unix), true, nil
	case ctx.IsCheckTx() || ctx.IsReCheckTx():
		return 0, false, nil
	default:
		return 0, false, errorsmod.Wrapf(types.ErrInvalidAttestation, "invalid block time %d", unix)
	}
}

// executeDelegateRegister re-runs every register check, signatures included,
// then writes the new entry as ACTIVE with its key and handle records, moves
// next_index on, emits delegate_registered and returns the entry's index
// (contracts-v1 §5.3).
func (k *Keeper) executeDelegateRegister(ctx sdk.Context, op *types.DelegateRegister) (uint32, error) {
	kvStore := k.OpenKVStore(ctx)
	index, err := k.checkDelegateRegister(ctx, kvStore, op, true)
	if err != nil {
		return 0, err
	}
	if err := k.SetProxyDelegateEntry(kvStore, &types.ProxyDelegateEntry{
		Index:            index,
		Dk:               bytes.Clone(op.Dk),
		Provider:         op.Provider,
		Handle:           op.Handle,
		ProofId:          op.ProofId,
		RegisteredHeight: uint64(ctx.BlockHeight()),
	}); err != nil {
		return 0, err
	}
	if err := k.SetProxyUsedDelegateKey(kvStore, op.Dk, index); err != nil {
		return 0, err
	}
	if err := k.SetProxyDelegateByHandle(kvStore, op.Provider, op.Handle, index); err != nil {
		return 0, err
	}
	if err := k.SetProxyNextDelegateIndex(kvStore, index+1); err != nil {
		return 0, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDelegateRegistered,
		sdk.NewAttribute(types.AttributeKeyDelegateIndex, strconv.FormatUint(uint64(index), 10)),
		sdk.NewAttribute(types.AttributeKeyProvider, strconv.FormatUint(uint64(op.Provider), 10)),
		sdk.NewAttribute(types.AttributeKeyHandle, op.Handle),
		sdk.NewAttribute(types.AttributeKeyProofID, op.ProofId),
	))
	return index, nil
}

// checkDelegateRetire runs the retire checks of contracts-v1 §5.3 in order
// and returns the entry. A suspended entry cannot retire itself, so a
// suspension is not escaped by registering again. See ValidateDelegateOp.
func (k *Keeper) checkDelegateRetire(ctx sdk.Context, kvStore store.KVStore, op *types.DelegateRetire, verifySignatures bool) (*types.ProxyDelegateEntry, error) {
	entry, err := k.GetProxyDelegateEntry(kvStore, op.GetDelegateIndex())
	if err != nil {
		return nil, err
	}
	if err := requireActiveDelegate(entry); err != nil {
		return nil, err
	}
	if verifySignatures {
		digest, err := types.ComputeDelegateRetireDigest(ctx.ChainID(), entry.Index, entry.Dk)
		if err != nil {
			return nil, err
		}
		if err := types.VerifyEd25519ZIP215(entry.Dk, digest, op.GetDkSig()); err != nil {
			return nil, errorsmod.Wrapf(types.ErrInvalidSignature, "dk_sig: ed25519 over RETIRE under delegate %d's key: %v", entry.Index, err)
		}
	}
	return entry, nil
}

// executeDelegateRetire re-runs every retire check, the signature included,
// then retires the entry with reason 0, emits delegate_retired and returns
// the entry's index (contracts-v1 §5.3).
func (k *Keeper) executeDelegateRetire(ctx sdk.Context, op *types.DelegateRetire) (uint32, error) {
	kvStore := k.OpenKVStore(ctx)
	entry, err := k.checkDelegateRetire(ctx, kvStore, op, true)
	if err != nil {
		return 0, err
	}
	if err := k.retireProxyDelegate(kvStore, entry, 0); err != nil {
		return 0, err
	}
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeDelegateRetired,
		sdk.NewAttribute(types.AttributeKeyDelegateIndex, strconv.FormatUint(uint64(entry.Index), 10)),
	))
	return entry.Index, nil
}

// retireProxyDelegate stores entry as RETIRED with reason and deletes its
// handle record, so the handle can be registered again (contracts-v1 §5.3).
func (k *Keeper) retireProxyDelegate(kvStore store.KVStore, entry *types.ProxyDelegateEntry, reason uint32) error {
	entry.Status, entry.StatusReason = types.DelegateStatusRetired, reason
	if err := k.SetProxyDelegateEntry(kvStore, entry); err != nil {
		return err
	}
	return kvStore.Delete(types.ProxyDelegateHandleKey(entry.Provider, entry.Handle))
}

// requireActiveDelegate returns ErrDelegateSuspended for a SUSPENDED entry
// and ErrDelegateRetired for a RETIRED one.
func requireActiveDelegate(entry *types.ProxyDelegateEntry) error {
	switch entry.Status {
	case types.DelegateStatusActive:
		return nil
	case types.DelegateStatusSuspended:
		return errorsmod.Wrapf(types.ErrDelegateSuspended, "delegate %d, reason %d", entry.Index, entry.StatusReason)
	default:
		return errorsmod.Wrapf(types.ErrDelegateRetired, "delegate %d", entry.Index)
	}
}

// errDelegateBallotsNotAccepted refuses a 0x0B ballot. Ballots are not
// accepted yet, since there are no proxy rounds to ballot in.
func errDelegateBallotsNotAccepted() error {
	return errorsmod.Wrap(types.ErrProxyDelegationDisabled, "delegate ballots are not accepted yet")
}
