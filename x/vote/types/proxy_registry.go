package types

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
)

// ValidateProxyDelegationParams applies the stateless rules of
// MsgSetProxyDelegationParams (docs/proxy-delegation/contracts-v1.md §2.5) to
// registry params. A key-rule failure is ErrInvalidDelegateKey and any other
// failure is ErrInvalidProxyParams (§6). nil is the zero value, which is valid.
func ValidateProxyDelegationParams(params *ProxyDelegationParams) error {
	verifiers := params.GetVerifiers()
	if len(verifiers) > MaxProxyVerifiers {
		return errorsmod.Wrapf(ErrInvalidProxyParams, "at most %d verifiers, got %d", MaxProxyVerifiers, len(verifiers))
	}
	ids := make(map[uint32]struct{}, len(verifiers))
	pubkeys := make(map[string]struct{}, len(verifiers))
	for i, verifier := range verifiers {
		if verifier == nil {
			return errorsmod.Wrapf(ErrInvalidProxyParams, "verifiers[%d] cannot be nil", i)
		}
		if _, dup := ids[verifier.VerifierId]; dup {
			return errorsmod.Wrapf(ErrInvalidProxyParams, "verifiers[%d] repeats verifier_id %d", i, verifier.VerifierId)
		}
		ids[verifier.VerifierId] = struct{}{}
		if len(verifier.Pubkey) != 32 {
			return errorsmod.Wrapf(ErrInvalidProxyParams, "verifiers[%d].pubkey must be 32 bytes, got %d", i, len(verifier.Pubkey))
		}
		if _, dup := pubkeys[string(verifier.Pubkey)]; dup {
			return errorsmod.Wrapf(ErrInvalidProxyParams, "verifiers[%d] repeats a pubkey", i)
		}
		pubkeys[string(verifier.Pubkey)] = struct{}{}
		if err := ValidateEd25519Key(verifier.Pubkey); err != nil {
			return errorsmod.Wrapf(ErrInvalidDelegateKey, "verifiers[%d].pubkey: %v", i, err)
		}
	}
	threshold := params.GetVerifierThreshold()
	if len(verifiers) == 0 && threshold != 0 {
		return errorsmod.Wrapf(ErrInvalidProxyParams, "verifier_threshold must be 0 without verifiers, got %d", threshold)
	}
	if len(verifiers) > 0 && (threshold == 0 || threshold > uint32(len(verifiers))) {
		return errorsmod.Wrapf(ErrInvalidProxyParams, "verifier_threshold must be 1..%d, got %d", len(verifiers), threshold)
	}
	if maxDelegates := params.GetMaxDelegates(); maxDelegates > MaxDelegates {
		return errorsmod.Wrapf(ErrInvalidProxyParams, "max_delegates must be at most %d, got %d", MaxDelegates, maxDelegates)
	}
	return nil
}

// ValidateProxyRegistryGenesis checks the delegate registry of a genesis
// state, the entries and the params, against contracts-v1 §9: indices run
// 1..N with no gaps (the p=0 reveal check d < next_index assumes it), no two
// entries share a key and no two live entries share a handle, so the
// 0x1A 0x02 and 0x1A 0x03 records import rebuilds agree with the entries. It
// also rejects any entry a registration could not have written.
func ValidateProxyRegistryGenesis(gs *GenesisState) error {
	if err := ValidateProxyDelegationParams(gs.GetProxyParams()); err != nil {
		return errorsmod.Wrap(err, "proxy_params")
	}
	entries := gs.GetProxyDelegates()
	if uint64(len(entries)) > uint64(MaxDelegates) {
		return fmt.Errorf("proxy_delegates has %d entries, more than %d", len(entries), MaxDelegates)
	}
	n := uint32(len(entries))

	seenIndices := make(map[uint32]struct{}, len(entries))
	seenKeys := make(map[string]struct{}, len(entries))
	liveHandles := make(map[string]struct{}, len(entries))
	for i, entry := range entries {
		if entry == nil {
			return fmt.Errorf("proxy_delegates[%d] cannot be nil", i)
		}
		// N distinct indices in 1..N are exactly 1..N.
		if entry.Index == 0 || entry.Index > n {
			return fmt.Errorf("proxy_delegates[%d].index %d is outside 1..%d; indices must run 1..N with no gaps", i, entry.Index, n)
		}
		if _, dup := seenIndices[entry.Index]; dup {
			return fmt.Errorf("proxy_delegates[%d]: duplicate index %d", i, entry.Index)
		}
		seenIndices[entry.Index] = struct{}{}
		if err := ValidateEd25519Key(entry.Dk); err != nil {
			return fmt.Errorf("proxy_delegates[%d].dk: %v", i, err)
		}
		if _, dup := seenKeys[string(entry.Dk)]; dup {
			return fmt.Errorf("proxy_delegates[%d]: dk %x appears more than once", i, entry.Dk)
		}
		seenKeys[string(entry.Dk)] = struct{}{}
		if err := ValidateDelegateHandle(entry.Provider, entry.Handle); err != nil {
			return fmt.Errorf("proxy_delegates[%d]: %w", i, err)
		}
		if err := ValidateDelegateProofID(entry.Provider, entry.ProofId); err != nil {
			return fmt.Errorf("proxy_delegates[%d]: %w", i, err)
		}
		switch entry.Status {
		case DelegateStatusActive, DelegateStatusSuspended:
			handle := string(ProxyDelegateHandleKey(entry.Provider, entry.Handle))
			if _, dup := liveHandles[handle]; dup {
				return fmt.Errorf("proxy_delegates[%d]: a second live entry for provider %d handle %q", i, entry.Provider, entry.Handle)
			}
			liveHandles[handle] = struct{}{}
		case DelegateStatusRetired:
		default:
			return fmt.Errorf("proxy_delegates[%d].status is %d, expected 0, 1 or 2", i, entry.Status)
		}
	}
	return nil
}
