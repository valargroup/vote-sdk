package types_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// svoteDomains is every SVOTE_ digest domain in the module (contracts-v1 §4.3).
var svoteDomains = []string{
	types.CastVoteSighashDomain,
	types.CastVoteBatchSighashDomain,
	types.DelegateAndCastVoteBatchSighashDomain,
	types.ProxyDelegationBatchSighashDomain,
	types.ProxyDelegateRegisterDomain,
	types.ProxyDelegateAttestDomain,
	types.ProxyDelegateRetireDomain,
	types.ProxyDelegateBallotDomain,
}

// TestSvoteDomainsArePrefixFree checks that no SVOTE_ domain is a prefix of
// another (contracts-v1 §4.3).
func TestSvoteDomainsArePrefixFree(t *testing.T) {
	for i, a := range svoteDomains {
		require.Truef(t, strings.HasPrefix(a, "SVOTE_"), "domain %q", a)
		for j, b := range svoteDomains {
			if i != j && strings.HasPrefix(b, a) {
				t.Errorf("domain %q is a prefix of %q", a, b)
			}
		}
	}
}
