//go:build !proxyenabled

package keeper_test

import (
	"testing"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// This test pins proxy delegation's dormant behavior
// (docs/proxy-delegation/contracts-v1.md §9), so it builds only without the
// proxyenabled tag.

// requireProxyDelegationDisabled fails unless err is ErrProxyDelegationDisabled
// with its registered code 69 in codespace vote.
func requireProxyDelegationDisabled(t *testing.T, err error) {
	t.Helper()
	proxytest.RequireCode(t, err, types.ErrProxyDelegationDisabled, 69)
}

// TestProxyHandlersDormant checks that with the gate closed the 0x09 and
// 0x0B handlers answer a bare ErrProxyDelegationDisabled before any other
// check, well-formed or not, and write nothing.
func TestProxyHandlersDormant(t *testing.T) {
	s := new(ProxyRegistryTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.openRegistry()
	_, err := s.msgServer.DelegateOp(s.ctx, s.registerOp(newEntryRegistration(proxytest.Key("alice/1"), "alice"), 1))
	requireProxyDelegationDisabled(t, err)
	_, err = s.msgServer.DelegateOp(s.ctx, &types.MsgDelegateOp{})
	requireProxyDelegationDisabled(t, err)
	s.requireNoRegistrations()

	batches := new(ProxyBatchTestSuite)
	batches.SetT(t)
	batches.SetupTest()
	before := batches.state()
	for _, msg := range []*types.MsgProxyDelegationBatch{proxyBatch(1, false), {}} {
		_, err := batches.msgServer.ProxyDelegationBatch(batches.ctx, msg)
		requireProxyDelegationDisabled(t, err)
	}
	batches.Require().Equal(before, batches.state())
}
