package keeper_test

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestProxyDelegatesPaging checks ProxyDelegates paging (contracts-v1 §8):
// entries above start_after in ascending order, limit 0 meaning 100 and above
// 1000 InvalidArgument, and next_start_after 0 once nothing remains.
func (s *ProxyRegistryTestSuite) TestProxyDelegatesPaging() {
	s.seedEntries(1, 250)
	page := func(startAfter, limit uint32) ([]uint32, uint32) {
		resp, err := s.queries.ProxyDelegates(s.ctx, &types.QueryProxyDelegatesRequest{StartAfter: startAfter, Limit: limit})
		s.Require().NoError(err)
		var indices []uint32
		for _, entry := range resp.Delegates {
			indices = append(indices, entry.Index)
		}
		return indices, resp.NextStartAfter
	}
	span := func(from, to uint32) []uint32 {
		var indices []uint32
		for index := from; index <= to; index++ {
			indices = append(indices, index)
		}
		return indices
	}
	for _, c := range []struct {
		name              string
		startAfter, limit uint32
		want              []uint32
		wantNext          uint32
	}{
		{"default limit", 0, 0, span(1, 100), 100},
		{"second page", 100, 0, span(101, 200), 200},
		{"last page", 200, 0, span(201, 250), 0},
		{"a page that ends exactly at the last entry", 0, 250, span(1, 250), 0},
		{"past the end", 250, 10, nil, 0},
	} {
		s.Run(c.name, func() {
			got, next := page(c.startAfter, c.limit)
			s.Require().Equal(c.want, got)
			s.Require().Equal(c.wantNext, next)
		})
	}

	_, err := s.queries.ProxyDelegates(s.ctx, &types.QueryProxyDelegatesRequest{Limit: 1001})
	s.Require().Equal(codes.InvalidArgument, status.Code(err), "error: %v", err)
}

// TestProxyDelegateQuery checks ProxyDelegate: the stored entry, or NotFound
// for an unknown index, 0 included (§8).
func (s *ProxyRegistryTestSuite) TestProxyDelegateQuery() {
	s.openRegistry()
	d := s.registerNew(proxytest.Key("alice/1"), "alice")

	resp, err := s.queries.ProxyDelegate(s.ctx, &types.QueryProxyDelegateRequest{DelegateIndex: d})
	s.Require().NoError(err)
	s.Require().True(proto.Equal(s.entry(d), resp.Delegate))

	for _, index := range []uint32{0, d + 1} {
		_, err := s.queries.ProxyDelegate(s.ctx, &types.QueryProxyDelegateRequest{DelegateIndex: index})
		s.Require().Equal(codes.NotFound, status.Code(err), "index %d: %v", index, err)
	}
}

// TestProxyDelegateByKeyQuery checks ProxyDelegateByKey: any key ever
// registered names its entry, a retired one included, which is how a client
// tells a landed registration from a reused key (§6, code 61).
func (s *ProxyRegistryTestSuite) TestProxyDelegateByKeyQuery() {
	s.openRegistry()
	k1, k2 := proxytest.Key("alice/1"), proxytest.Key("alice/2")
	s.registerNew(proxytest.Key("bob/1"), "bob")
	retired := s.registerNew(k1, "alice")
	s.mustDeliver(s.retireOp(retired, k1))
	live := s.registerNew(k2, "alice")

	for index, key := range map[uint32][]byte{retired: proxytest.Pub(k1), live: proxytest.Pub(k2)} {
		resp, err := s.queries.ProxyDelegateByKey(s.ctx, &types.QueryProxyDelegateByKeyRequest{Dk: key})
		s.Require().NoError(err)
		s.Require().True(proto.Equal(s.entry(index), resp.Delegate))
	}

	_, err := s.queries.ProxyDelegateByKey(s.ctx, &types.QueryProxyDelegateByKeyRequest{Dk: proxytest.Pub(proxytest.Key("nobody"))})
	s.Require().Equal(codes.NotFound, status.Code(err), "error: %v", err)
	_, err = s.queries.ProxyDelegateByKey(s.ctx, &types.QueryProxyDelegateByKeyRequest{Dk: make([]byte, 31)})
	s.Require().Equal(codes.InvalidArgument, status.Code(err), "error: %v", err)
}

// TestProxyDelegateByHandleQuery checks ProxyDelegateByHandle (§8): a handle
// names its live entry, suspended or not, never a retired one; a handle
// without a live entry is NotFound and one that is not canonical is
// InvalidArgument.
func (s *ProxyRegistryTestSuite) TestProxyDelegateByHandleQuery() {
	s.openRegistry()
	k1 := proxytest.Key("alice/1")
	byHandle := func(provider uint32, handle string) (*types.ProxyDelegateEntry, codes.Code) {
		resp, err := s.queries.ProxyDelegateByHandle(s.ctx, &types.QueryProxyDelegateByHandleRequest{Provider: provider, Handle: handle})
		return resp.GetDelegate(), status.Code(err)
	}
	const x, github = types.DelegateProviderX, types.DelegateProviderGitHub

	first := s.registerNew(k1, "alice")
	got, code := byHandle(x, "alice")
	s.Require().Equal(codes.OK, code)
	s.Require().True(proto.Equal(s.entry(first), got))
	s.setStatus(first, types.DelegateStatusSuspended, 1)
	got, _ = byHandle(x, "alice")
	s.Require().Equal(types.DelegateStatusSuspended, got.GetStatus())

	s.setStatus(first, types.DelegateStatusRetired, 1)
	_, code = byHandle(x, "alice")
	s.Require().Equal(codes.NotFound, code)
	second := s.registerNew(proxytest.Key("alice/2"), "alice")
	got, _ = byHandle(x, "alice")
	s.Require().Equal(second, got.GetIndex())

	for name, c := range map[string]struct {
		provider uint32
		handle   string
		want     codes.Code
	}{
		"the same name at GitHub": {github, "alice", codes.NotFound},
		"an uppercase handle":     {x, "Alice", codes.InvalidArgument},
	} {
		_, code := byHandle(c.provider, c.handle)
		s.Require().Equal(c.want, code, name)
	}
}

// TestProxyDelegationParamsQuery checks ProxyDelegationParams: the defaults on
// a store without registry keys (§9), then the stored params and
// next_delegate_index.
func (s *ProxyRegistryTestSuite) TestProxyDelegationParamsQuery() {
	resp, err := s.queries.ProxyDelegationParams(s.ctx, &types.QueryProxyDelegationParamsRequest{})
	s.Require().NoError(err)
	s.Require().True(proto.Equal(&types.ProxyDelegationParams{}, resp.Params), "unset params: %v", resp.Params)
	s.Require().Equal(uint32(1), resp.NextDelegateIndex)
	s.requireNoRegistryKeys()

	s.setParams(1, 500, 1, 2)
	s.registerNew(proxytest.Key("alice/1"), "alice")
	s.registerNew(proxytest.Key("bob/1"), "bob")
	params, err := s.keeper.GetProxyDelegationParams(s.kv())
	s.Require().NoError(err)
	params.ProxyDcPaused = true
	s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), params))

	resp, err = s.queries.ProxyDelegationParams(s.ctx, &types.QueryProxyDelegationParamsRequest{})
	s.Require().NoError(err)
	s.Require().True(proto.Equal(params, resp.Params))
	s.Require().Equal(uint32(3), resp.NextDelegateIndex)
}
