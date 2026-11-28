package votingconfig

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"slices"
)

// ProxyDelegationV1Key names the proxy delegation extension inside the
// dynamic config's "extensions" object (contracts-v1 §12.1). Its v1 is the
// chain's proxy delegation version, not the contract version.
const ProxyDelegationV1Key = "proxy_delegation_v1"

// MinProxyProofRPCURLs is the fewest proof_rpc_urls the extension lists, so
// Vizor can fail over between RPC hosts run by different operators.
const MinProxyProofRPCURLs = 2

// ProxyDelegationV1 is extensions.proxy_delegation_v1 (contracts-v1
// §12.1): unsigned URL lists. Wallets ignore members they do not know, but
// config CI refuses them.
type ProxyDelegationV1 struct {
	VerifierAPIURLs []string `json:"verifier_api_urls"`
	ProofRPCURLs    []string `json:"proof_rpc_urls"`
}

// ProxyDelegationV1 decodes extensions.proxy_delegation_v1, or returns nil
// when the config has none. Decoding is strict (see decodeStrictObject), so
// an unknown, miscased, repeated or null member of the extension is an
// error. It does not check the URL lists; see ValidateProxyDelegationV1.
func (c *SignedConfig) ProxyDelegationV1() (*ProxyDelegationV1, error) {
	if len(c.Extensions) == 0 || isJSONNull(c.Extensions) {
		return nil, nil
	}
	members, err := decodeObjectMembers(c.Extensions)
	if err != nil {
		return nil, fmt.Errorf("extensions: %w", err)
	}
	i := slices.IndexFunc(members, func(m RawMember) bool { return m.Key == ProxyDelegationV1Key })
	if i < 0 {
		return nil, nil
	}
	raw := members[i].Value
	if bytes.TrimSpace(raw)[0] != '{' {
		return nil, errors.New("extensions.proxy_delegation_v1 must be an object")
	}
	var ext ProxyDelegationV1
	if err := decodeStrictObject(raw, map[string]any{
		"verifier_api_urls": &ext.VerifierAPIURLs,
		"proof_rpc_urls":    &ext.ProofRPCURLs,
	}); err != nil {
		return nil, fmt.Errorf("extensions.proxy_delegation_v1: %w", err)
	}
	return &ext, nil
}

// ValidateProxyDelegationV1 checks the extension's URL lists: each holds
// distinct https URLs, at least one (MinProxyProofRPCURLs for
// proof_rpc_urls).
func ValidateProxyDelegationV1(ext *ProxyDelegationV1) error {
	for _, list := range []struct {
		field string
		urls  []string
		min   int
	}{
		{"verifier_api_urls", ext.VerifierAPIURLs, 1},
		{"proof_rpc_urls", ext.ProofRPCURLs, MinProxyProofRPCURLs},
	} {
		if len(list.urls) < list.min {
			return fmt.Errorf("extensions.proxy_delegation_v1.%s must list at least %d URL(s)", list.field, list.min)
		}
		seen := make(map[string]bool, len(list.urls))
		for _, raw := range list.urls {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return fmt.Errorf("extensions.proxy_delegation_v1.%s: %q is not an https URL", list.field, raw)
			}
			if seen[raw] {
				return fmt.Errorf("extensions.proxy_delegation_v1.%s repeats %q", list.field, raw)
			}
			seen[raw] = true
		}
	}
	return nil
}
