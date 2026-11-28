package keeper_test

import (
	"bytes"

	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// testCircuitFingerprints returns stand-ins for capability field 5's four
// fingerprints.
func testCircuitFingerprints() []*types.CircuitFingerprint {
	var fingerprints []*types.CircuitFingerprint
	for i, name := range []string{"zkp1", "zkp2", "zkp3", "zkp4"} {
		fingerprints = append(fingerprints, &types.CircuitFingerprint{Name: name, VkBlake2B: bytes.Repeat([]byte{byte(i + 1)}, 32)})
	}
	return fingerprints
}

// TestProtocolCapabilities_ProxyDelegationOn checks capability fields 4 and 5
// with the gate open (docs/proxy-delegation/contracts-v1.md §8): version 1,
// and the fingerprints the app published, none before it has. Fields 1-3 are
// unchanged.
func (s *QueryServerTestSuite) TestProtocolCapabilities_ProxyDelegationOn() {
	proxytest.SkipUnlessEnabled(s.T())
	queries := s.queryServer
	query := func() *types.QueryProtocolCapabilitiesResponse {
		resp, err := queries.ProtocolCapabilities(s.ctx, &types.QueryProtocolCapabilitiesRequest{})
		s.Require().NoError(err)
		s.Require().True(resp.AtomicCastVoteBatch)
		s.Require().True(resp.AtomicDelegateAndCastVoteBatch)
		s.Require().Equal(uint32(0x07), resp.AtomicDelegateAndCastVoteBatchWireTag)
		s.Require().Equal(uint32(1), resp.ProxyDelegationVersion)
		return resp
	}

	s.Require().Empty(query().CircuitFingerprints, "nothing published yet")

	published := testCircuitFingerprints()
	s.keeper.SetCircuitFingerprints(published)
	resp := query()
	s.Require().Len(resp.CircuitFingerprints, 4)
	for i, f := range resp.CircuitFingerprints {
		s.Require().True(proto.Equal(published[i], f), "%v", f)
	}
}
