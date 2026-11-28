package types_test

import (
	"fmt"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// registryTestKey returns a deterministic Ed25519 public key for label.
func registryTestKey(label string) []byte {
	return proxytest.Pub(proxytest.Key(label))
}

// identityKey is the Ed25519 identity, which fails the key rule (SmallOrder).
var identityKey = append([]byte{1}, make([]byte, 31)...)

// validProxyParams returns params with n verifiers at threshold n.
func validProxyParams(n int) *types.ProxyDelegationParams {
	params := &types.ProxyDelegationParams{VerifierThreshold: uint32(n), MaxDelegates: 2_000}
	for i := 1; i <= n; i++ {
		params.Verifiers = append(params.Verifiers, &types.ProxyVerifier{VerifierId: uint32(i), Pubkey: registryTestKey(fmt.Sprintf("verifier/%d", i))})
	}
	return params
}

// TestValidateProxyDelegationParams checks the params rules of contracts-v1
// §2.5 and their codes (§6): 66 for a key-rule failure, 68 otherwise.
func TestValidateProxyDelegationParams(t *testing.T) {
	for name, params := range map[string]*types.ProxyDelegationParams{
		"nil":                         nil,
		"zero value":                  {},
		"sixteen verifiers, 16-of-16": validProxyParams(16),
		"largest max_delegates":       {Verifiers: validProxyParams(2).Verifiers, VerifierThreshold: 1, MaxDelegates: types.MaxDelegates},
	} {
		require.NoError(t, types.ValidateProxyDelegationParams(params), name)
	}

	for _, c := range []struct {
		name   string
		mutate func(*types.ProxyDelegationParams)
		want   *errorsmod.Error
		code   uint32
	}{
		{"seventeen verifiers", func(p *types.ProxyDelegationParams) {
			p.Verifiers = validProxyParams(17).Verifiers
		}, types.ErrInvalidProxyParams, 68},
		{"repeated id", func(p *types.ProxyDelegationParams) { p.Verifiers[1].VerifierId = 1 }, types.ErrInvalidProxyParams, 68},
		{"short pubkey", func(p *types.ProxyDelegationParams) { p.Verifiers[1].Pubkey = p.Verifiers[1].Pubkey[:31] }, types.ErrInvalidProxyParams, 68},
		{"repeated pubkey", func(p *types.ProxyDelegationParams) { p.Verifiers[1].Pubkey = p.Verifiers[0].Pubkey }, types.ErrInvalidProxyParams, 68},
		{"pubkey fails the key rule", func(p *types.ProxyDelegationParams) { p.Verifiers[1].Pubkey = identityKey }, types.ErrInvalidDelegateKey, 66},
		{"threshold above the verifier count", func(p *types.ProxyDelegationParams) { p.VerifierThreshold = 3 }, types.ErrInvalidProxyParams, 68},
		{"threshold without verifiers", func(p *types.ProxyDelegationParams) { p.Verifiers = nil; p.VerifierThreshold = 1 }, types.ErrInvalidProxyParams, 68},
		{"max_delegates above 2000", func(p *types.ProxyDelegationParams) { p.MaxDelegates = 2001 }, types.ErrInvalidProxyParams, 68},
	} {
		t.Run(c.name, func(t *testing.T) {
			params := validProxyParams(2)
			c.mutate(params)
			err := types.ValidateProxyDelegationParams(params)
			require.ErrorIs(t, err, c.want)
			codespace, code, _ := errorsmod.ABCIInfo(err, false)
			require.Equal(t, types.ModuleName, codespace)
			require.Equal(t, c.code, code)
		})
	}
}

// validRegistryGenesis returns a registry as live state would export it:
// an ACTIVE entry, a SUSPENDED one, a RETIRED one and the ACTIVE entry that
// registered the retired one's handle again, and params.
func validRegistryGenesis() *types.GenesisState {
	entry := func(index uint32, provider uint32, handle string, status, reason uint32) *types.ProxyDelegateEntry {
		return &types.ProxyDelegateEntry{
			Index:            index,
			Dk:               registryTestKey(fmt.Sprintf("entry/%d", index)),
			Provider:         provider,
			Handle:           handle,
			ProofId:          "1790000000000000000",
			Status:           status,
			StatusReason:     reason,
			RegisteredHeight: uint64(10 * index),
		}
	}
	return &types.GenesisState{
		ProxyDelegates: []*types.ProxyDelegateEntry{
			entry(1, types.DelegateProviderX, "alice", types.DelegateStatusActive, 0),
			entry(2, types.DelegateProviderGitHub, "alice", types.DelegateStatusSuspended, 99),
			entry(3, types.DelegateProviderX, "bob", types.DelegateStatusRetired, 0),
			entry(4, types.DelegateProviderX, "bob", types.DelegateStatusActive, 0),
		},
		ProxyParams: validProxyParams(1),
	}
}

// TestValidateProxyRegistryGenesis checks the import validation of
// contracts-v1 §9: the valid registries pass, and every broken invariant is
// refused, directly and through ValidateGenesisState.
func TestValidateProxyRegistryGenesis(t *testing.T) {
	require.NoError(t, types.ValidateProxyRegistryGenesis(validRegistryGenesis()))
	require.NoError(t, types.ValidateProxyRegistryGenesis(&types.GenesisState{}), "an empty registry")

	reordered := validRegistryGenesis()
	reordered.ProxyDelegates[0], reordered.ProxyDelegates[2] = reordered.ProxyDelegates[2], reordered.ProxyDelegates[0]
	require.NoError(t, types.ValidateProxyRegistryGenesis(reordered), "entries in any order")

	for _, c := range []struct {
		name   string
		mutate func(gs *types.GenesisState)
		want   string
	}{
		{"a gap in the indices", func(gs *types.GenesisState) { gs.ProxyDelegates[3].Index = 5 }, "with no gaps"},
		{"a repeated index", func(gs *types.GenesisState) { gs.ProxyDelegates[1].Index = 1 }, "duplicate index 1"},
		{"a key that fails the key rule", func(gs *types.GenesisState) { gs.ProxyDelegates[2].Dk = identityKey }, "small order"},
		{"a key shared with a retired entry", func(gs *types.GenesisState) { gs.ProxyDelegates[3].Dk = gs.ProxyDelegates[2].Dk }, "appears more than once"},
		{"two live entries for one handle", func(gs *types.GenesisState) { gs.ProxyDelegates[2].Status = types.DelegateStatusSuspended }, `a second live entry for provider 1 handle "bob"`},
		{"a handle that is not canonical", func(gs *types.GenesisState) { gs.ProxyDelegates[0].Handle = "Alice" }, "not canonical"},
		{"a proof id that is not canonical", func(gs *types.GenesisState) { gs.ProxyDelegates[1].ProofId = "xyz" }, "not canonical"},
		{"an unknown status", func(gs *types.GenesisState) { gs.ProxyDelegates[0].Status = 3 }, "status is 3"},
		{"invalid params", func(gs *types.GenesisState) { gs.ProxyParams.VerifierThreshold = 2 }, "proxy_params"},
	} {
		t.Run(c.name, func(t *testing.T) {
			gs := validRegistryGenesis()
			c.mutate(gs)
			require.ErrorContains(t, types.ValidateProxyRegistryGenesis(gs), c.want)

			full := validGenesis()
			full.ProxyDelegates, full.ProxyParams = gs.ProxyDelegates, gs.ProxyParams
			require.ErrorContains(t, types.ValidateGenesisState(full), c.want, "through ValidateGenesisState")
		})
	}
}
