package keeper_test

import (
	"bytes"
	"math"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestDelegateBallotsPaging checks DelegateBallots paging (contracts-v1 §8):
// a round's ballots above start_after in ascending delegate index, limit 0
// meaning 100 and above 1000 InvalidArgument, next_start_after 0 once
// nothing remains, and never a ballot of another round.
func (s *ProxyRegistryTestSuite) TestDelegateBallotsPaging() {
	before, after := bytes.Repeat([]byte{0xB0}, 32), bytes.Repeat([]byte{0xB3}, 32)
	for _, roundID := range [][]byte{before, ballotRoundA, after} {
		s.setRound(proxyRound(roundID, proxyTestTime))
	}
	// Round A holds a ballot for every even index from 2 to 500; the rounds
	// whose keys sit on either side of it hold one ballot each, at the ends
	// of the index range.
	kv := s.kv()
	for d := uint32(2); d <= 500; d += 2 {
		s.Require().NoError(s.keeper.SetDelegateBallot(kv, &types.DelegateBallotRecord{VoteRoundId: ballotRoundA, DelegateIndex: d, Options: []uint32{d % 3}, Height: uint64(d)}))
	}
	s.Require().NoError(s.keeper.SetDelegateBallot(kv, &types.DelegateBallotRecord{VoteRoundId: before, DelegateIndex: math.MaxUint32, Options: []uint32{0}}))
	s.Require().NoError(s.keeper.SetDelegateBallot(kv, &types.DelegateBallotRecord{VoteRoundId: after, DelegateIndex: 1, Options: []uint32{0}}))

	page := func(startAfter, limit uint32) ([]uint32, uint32) {
		resp, err := s.queries.DelegateBallots(s.ctx, &types.QueryDelegateBallotsRequest{VoteRoundId: ballotRoundA, StartAfter: startAfter, Limit: limit})
		s.Require().NoError(err)
		var indices []uint32
		for _, ballot := range resp.Ballots {
			s.Require().Equal(ballotRoundA, ballot.VoteRoundId)
			indices = append(indices, ballot.DelegateIndex)
		}
		return indices, resp.NextStartAfter
	}
	evens := func(from, to uint32) []uint32 {
		var indices []uint32
		for d := from; d <= to; d += 2 {
			indices = append(indices, d)
		}
		return indices
	}
	for _, c := range []struct {
		name              string
		startAfter, limit uint32
		want              []uint32
		wantNext          uint32
	}{
		{"default limit", 0, 0, evens(2, 200), 200},
		{"last page", 400, 0, evens(402, 500), 0},
		{"start_after between ballots", 201, 0, evens(202, 400), 400},
		{"a page that ends exactly at the last ballot", 0, 250, evens(2, 500), 0},
		{"start_after at the largest index", math.MaxUint32, 10, nil, 0},
	} {
		s.Run(c.name, func() {
			got, next := page(c.startAfter, c.limit)
			s.Require().Equal(c.want, got)
			s.Require().Equal(c.wantNext, next)
		})
	}

	resp, err := s.queries.DelegateBallots(s.ctx, &types.QueryDelegateBallotsRequest{VoteRoundId: ballotRoundA, StartAfter: 9, Limit: 1})
	s.Require().NoError(err)
	s.Require().True(proto.Equal(&types.DelegateBallotRecord{VoteRoundId: ballotRoundA, DelegateIndex: 10, Options: []uint32{1}, Height: 10}, resp.Ballots[0]))

	for name, req := range map[string]*types.QueryDelegateBallotsRequest{
		"a limit above 1000": {VoteRoundId: ballotRoundA, Limit: 1001},
		"a short round id":   {VoteRoundId: ballotRoundA[:31]},
	} {
		_, err := s.queries.DelegateBallots(s.ctx, req)
		s.Require().Equal(codes.InvalidArgument, status.Code(err), "%s: %v", name, err)
	}
}

// TestDelegateBallotsRounds checks DelegateBallots by round (§8): an unknown
// round is NotFound, and a known round without ballots, proxy round or not,
// has an empty page.
func (s *ProxyRegistryTestSuite) TestDelegateBallotsRounds() {
	_, err := s.queries.DelegateBallots(s.ctx, &types.QueryDelegateBallotsRequest{VoteRoundId: ballotRoundA})
	s.Require().Equal(codes.NotFound, status.Code(err), "error: %v", err)

	plain := proxyRound(ballotRoundB, proxyTestTime)
	plain.ProxyDelegation = nil
	s.setRound(plain)
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	for _, roundID := range [][]byte{ballotRoundA, ballotRoundB} {
		resp, err := s.queries.DelegateBallots(s.ctx, &types.QueryDelegateBallotsRequest{VoteRoundId: roundID})
		s.Require().NoError(err)
		s.Require().Empty(resp.Ballots)
		s.Require().Zero(resp.NextStartAfter)
	}
}
