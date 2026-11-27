package keeper_test

import (
	"bytes"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// registryKV returns every raw key and value under 0x1A.
func (s *ProxyRegistryTestSuite) registryKV() map[string][]byte {
	iter, err := s.kv().Iterator([]byte{0x1A}, []byte{0x1B})
	s.Require().NoError(err)
	defer iter.Close()
	values := map[string][]byte{}
	for ; iter.Valid(); iter.Next() {
		values[string(iter.Key())] = bytes.Clone(iter.Value())
	}
	return values
}

// registryGenesis returns only the registry fields of gs.
func registryGenesis(gs *types.GenesisState) *types.GenesisState {
	return &types.GenesisState{ProxyDelegates: gs.ProxyDelegates, ProxyParams: gs.ProxyParams}
}

// TestProxyRegistryGenesisRoundTrip checks export and import of the entries
// and the params (contracts-v1 §9) through the module's JSON: the export
// imports to the same raw state, with the key records, the live entries'
// handle records and next_index rebuilt, and keeps working.
func (s *ProxyRegistryTestSuite) TestProxyRegistryGenesisRoundTrip() {
	s.setParams(1, 50, 1, 2)
	alice := s.registerNew(proxytest.Key("alice/1"), "alice")
	bob := s.registerNew(proxytest.Key("bob/1"), "bob")
	s.registerNew(proxytest.Key("carol/1"), "carol")
	s.mustDeliver(s.retireOp(alice, proxytest.Key("alice/1")))
	s.Require().Equal(uint32(4), s.registerNew(proxytest.Key("alice/2"), "alice"))
	s.setStatus(bob, types.DelegateStatusSuspended, 1)
	params, err := s.keeper.GetProxyDelegationParams(s.kv())
	s.Require().NoError(err)
	params.ProxyDcPaused = true
	s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), params))

	exported, err := s.keeper.ExportGenesis(s.kv())
	s.Require().NoError(err)
	var indices []uint32
	for _, e := range exported.ProxyDelegates {
		indices = append(indices, e.Index)
		s.Require().True(proto.Equal(s.entry(e.Index), e))
	}
	s.Require().Equal([]uint32{1, 2, 3, 4}, indices)
	s.Require().True(proto.Equal(params, exported.ProxyParams))
	s.Require().NoError(types.ValidateProxyRegistryGenesis(exported))
	original := s.registryKV()
	s.Require().Contains(original, string(types.ProxyNextDelegateIndexKey))

	bz, err := json.Marshal(registryGenesis(exported))
	s.Require().NoError(err)
	var imported types.GenesisState
	s.Require().NoError(json.Unmarshal(bz, &imported))

	s.SetupTest()
	s.Require().NoError(s.keeper.InitGenesis(s.kv(), &imported))
	s.Require().Equal(original, s.registryKV(), "the import rebuilds the same raw registry")
	s.Require().Equal(uint32(4), s.handleOwner("alice"), "the live entry, not the retired one")
	s.Require().Equal(uint32(5), s.nextIndex())
	reexported, err := s.keeper.ExportGenesis(s.kv())
	s.Require().NoError(err)
	s.Require().True(proto.Equal(registryGenesis(exported), registryGenesis(reexported)))

	s.Require().Equal(uint32(5), s.registerNew(proxytest.Key("dave/1"), "dave"))
	_, err = s.deliver(s.registerOp(newEntryRegistration(proxytest.Key("alice/1"), "mallory"), 1))
	proxytest.RequireCode(s.T(), err, types.ErrDelegateKeyReused, 61)
	_, err = s.deliver(s.registerOp(newEntryRegistration(proxytest.Key("bob/2"), "bob"), 1))
	proxytest.RequireCode(s.T(), err, types.ErrHandleTaken, 62)
}

// TestProxyRegistryGenesisWithoutRegistry checks a store without registry
// keys, such as one from before v1.7.0: the export carries no registry, and
// importing it writes no registry key, so every value reads as its default.
func (s *ProxyRegistryTestSuite) TestProxyRegistryGenesisWithoutRegistry() {
	exported, err := s.keeper.ExportGenesis(s.kv())
	s.Require().NoError(err)
	s.Require().Empty(exported.ProxyDelegates)
	s.Require().Nil(exported.ProxyParams)
	s.Require().Empty(exported.ProxyDelegateBallots)

	s.Require().NoError(s.keeper.InitGenesis(s.kv(), exported))
	s.requireNoRegistryKeys()
	s.Require().Equal(uint32(1), s.nextIndex())
}

// TestInitGenesisRejectsInvalidRegistry checks that the import itself, not
// only ValidateGenesisState, refuses a registry that breaks an invariant
// (§9), before it writes anything.
func (s *ProxyRegistryTestSuite) TestInitGenesisRejectsInvalidRegistry() {
	entry := func(index uint32, handle string) *types.ProxyDelegateEntry {
		return &types.ProxyDelegateEntry{
			Index:    index,
			Dk:       proxytest.Pub(proxytest.Key(fmt.Sprintf("genesis/%d", index))),
			Provider: types.DelegateProviderX,
			Handle:   handle,
			ProofId:  "1790000000000000000",
		}
	}
	valid := func() *types.GenesisState {
		return &types.GenesisState{
			Rounds:         []*types.VoteRound{{VoteRoundId: bytes.Repeat([]byte{0xAA}, 32), VoteEndTime: 2_000_000}},
			ProxyDelegates: []*types.ProxyDelegateEntry{entry(1, "a"), entry(2, "b")},
		}
	}
	s.Require().NoError(s.keeper.InitGenesis(s.kv(), valid()))

	for name, mutate := range map[string]func(gs *types.GenesisState){
		"an index gap":                    func(gs *types.GenesisState) { gs.ProxyDelegates[1].Index = 3 },
		"two entries with one key":        func(gs *types.GenesisState) { gs.ProxyDelegates[1].Dk = gs.ProxyDelegates[0].Dk },
		"two live entries for one handle": func(gs *types.GenesisState) { gs.ProxyDelegates[1].Handle = "a" },
		"params that fail the key rule": func(gs *types.GenesisState) {
			gs.ProxyParams = &types.ProxyDelegationParams{Verifiers: []*types.ProxyVerifier{{VerifierId: 1, Pubkey: make([]byte, 32)}}, VerifierThreshold: 1}
		},
	} {
		s.Run(name, func() {
			s.SetupTest()
			gs := valid()
			mutate(gs)
			s.Require().Error(s.keeper.InitGenesis(s.kv(), gs))
			s.requireNoRegistryKeys()
			_, err := s.keeper.GetVoteRound(s.kv(), gs.Rounds[0].VoteRoundId)
			s.Require().ErrorIs(err, types.ErrRoundNotFound, "nothing is written")
		})
	}
}

// ballotKV returns every raw key and value under 0x1B.
func (s *ProxyRegistryTestSuite) ballotKV() map[string][]byte {
	iter, err := s.kv().Iterator([]byte{0x1B}, []byte{0x1C})
	s.Require().NoError(err)
	defer iter.Close()
	values := map[string][]byte{}
	for ; iter.Valid(); iter.Next() {
		values[string(iter.Key())] = bytes.Clone(iter.Value())
	}
	return values
}

// proxyGenesis returns the rounds and the proxy delegation fields of gs.
func proxyGenesis(gs *types.GenesisState) *types.GenesisState {
	return &types.GenesisState{
		Rounds:               gs.Rounds,
		ProxyDelegates:       gs.ProxyDelegates,
		ProxyDelegateBallots: gs.ProxyDelegateBallots,
		ProxyParams:          gs.ProxyParams,
	}
}

// TestProxyBallotGenesisRoundTrip checks export and import of the ballots and
// the rounds' field 31 (contracts-v1 §9) through the module's JSON, with an
// active and a finished proxy round: the export lists the ballots in key
// order, the import reproduces the raw ballots and the rounds, and the
// imported store keeps working, so a retried ballot is still 57.
func (s *ProxyRegistryTestSuite) TestProxyBallotGenesisRoundTrip() {
	s.openRegistry()
	alice, bob := proxytest.Key("alice/1"), proxytest.Key("bob/1")
	a, b := s.registerNew(alice, "alice"), s.registerNew(bob, "bob")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	s.setRound(proxyRound(ballotRoundB, proxyTestTime))
	plain := proxyRound(bytes.Repeat([]byte{0xB3}, 32), proxyTestTime)
	plain.ProxyDelegation = nil
	s.setRound(plain)
	retry := s.ballotOp(ballotRoundA, b, bob, 1, 2, 3)
	s.mustDeliver(retry)
	s.mustDeliver(s.ballotOp(ballotRoundA, a, alice, 0, 0, 0))
	s.mustDeliver(s.ballotOp(ballotRoundB, a, alice, 1, 1, 1))
	s.updateRound(ballotRoundB, func(r *types.VoteRound) {
		r.Status, r.ProxyDelegation.PoolsRouted = types.SessionStatus_SESSION_STATUS_FINALIZED, true
	})

	exported, err := s.keeper.ExportGenesis(s.kv())
	s.Require().NoError(err)
	var keys []string
	for _, ballot := range exported.ProxyDelegateBallots {
		keys = append(keys, fmt.Sprintf("%x/%d", ballot.VoteRoundId[0], ballot.DelegateIndex))
	}
	s.Require().Equal([]string{"b1/1", "b1/2", "b2/1"}, keys, "in key order")
	s.Require().NoError(types.ValidateProxyRoundGenesis(exported))
	original := s.ballotKV()
	s.Require().Len(original, 3)

	bz, err := json.Marshal(proxyGenesis(exported))
	s.Require().NoError(err)
	var imported types.GenesisState
	s.Require().NoError(json.Unmarshal(bz, &imported))

	s.SetupTest()
	s.Require().NoError(s.keeper.InitGenesis(s.kv(), &imported))
	s.Require().Equal(original, s.ballotKV(), "the import rebuilds the same raw ballots")
	for _, round := range exported.Rounds {
		got, err := s.keeper.GetVoteRound(s.kv(), round.VoteRoundId)
		s.Require().NoError(err)
		s.Require().True(proto.Equal(round, got), "round %x", round.VoteRoundId)
	}
	reexported, err := s.keeper.ExportGenesis(s.kv())
	s.Require().NoError(err)
	s.Require().True(proto.Equal(proxyGenesis(exported), proxyGenesis(reexported)))

	_, err = s.deliver(retry)
	proxytest.RequireCode(s.T(), err, types.ErrDelegateBallotExists, 57)
	carol := proxytest.Key("carol/1")
	s.mustDeliver(s.ballotOp(ballotRoundA, s.registerNew(carol, "carol"), carol, 0, 0, 0))
}

// TestInitGenesisRejectsInvalidProxyRounds checks that the import itself, not
// only ValidateGenesisState, refuses rounds and ballots that break a §9 rule,
// before it writes anything.
func (s *ProxyRegistryTestSuite) TestInitGenesisRejectsInvalidProxyRounds() {
	key := proxytest.Pub(proxytest.Key("a/1"))
	valid := func() *types.GenesisState {
		finished := proxyRound(ballotRoundB, proxyTestTime)
		finished.Status, finished.ProxyDelegation.PoolsRouted = types.SessionStatus_SESSION_STATUS_TALLYING, true
		return &types.GenesisState{
			Rounds: []*types.VoteRound{proxyRound(ballotRoundA, proxyTestTime), finished},
			ProxyDelegates: []*types.ProxyDelegateEntry{{
				Index: 1, Dk: key, Provider: types.DelegateProviderX, Handle: "a", ProofId: "1790000000000000000",
			}},
			ProxyDelegateBallots: []*types.DelegateBallotRecord{
				{VoteRoundId: ballotRoundA, DelegateIndex: 1, Options: []uint32{0, 1, 2}},
				{VoteRoundId: ballotRoundB, DelegateIndex: 1, Options: []uint32{1, 2, 3}},
			},
		}
	}
	s.Require().NoError(s.keeper.InitGenesis(s.kv(), valid()))

	for name, mutate := range map[string]func(gs *types.GenesisState){
		"a ballot in a missing round":          func(gs *types.GenesisState) { gs.Rounds = gs.Rounds[:1] },
		"a ballot in a round without the flag": func(gs *types.GenesisState) { gs.Rounds[0].ProxyDelegation = nil },
		"a ballot of a missing delegate":       func(gs *types.GenesisState) { gs.ProxyDelegateBallots[0].DelegateIndex = 2 },
		"two ballots for one (round, d)":       func(gs *types.GenesisState) { gs.ProxyDelegateBallots[1].VoteRoundId = ballotRoundA },
		"an incomplete ballot":                 func(gs *types.GenesisState) { gs.ProxyDelegateBallots[1].Options = []uint32{1, 2} },
		"an option out of range":               func(gs *types.GenesisState) { gs.ProxyDelegateBallots[0].Options = []uint32{2, 0, 0} },
		"pools not routed in a TALLYING round": func(gs *types.GenesisState) { gs.Rounds[1].ProxyDelegation.PoolsRouted = false },
		"pools routed in an ACTIVE round":      func(gs *types.GenesisState) { gs.Rounds[0].ProxyDelegation.PoolsRouted = true },
		"a proposal id out of position":        func(gs *types.GenesisState) { gs.Rounds[0].Proposals[0].Id = 7 },
		"an option index out of position":      func(gs *types.GenesisState) { gs.Rounds[1].Proposals[2].Options[3].Index = 0 },
	} {
		s.Run(name, func() {
			s.SetupTest()
			gs := valid()
			mutate(gs)
			s.Require().Error(s.keeper.InitGenesis(s.kv(), gs))
			s.requireNoRegistryKeys()
			s.requireNoKeysUnder(types.DelegateBallotPrefix)
			_, err := s.keeper.GetVoteRound(s.kv(), ballotRoundA)
			s.Require().ErrorIs(err, types.ErrRoundNotFound, "nothing is written")
		})
	}
}

// TestInitGenesisAcceptsPlainRoundNumbering checks that the import still
// accepts a round without the proxy flag however its proposals and options
// are numbered; ValidateProxyRoundGenesis checks only flagged rounds.
func (s *ProxyRegistryTestSuite) TestInitGenesisAcceptsPlainRoundNumbering() {
	plain := proxyRound(ballotRoundA, proxyTestTime)
	plain.ProxyDelegation = nil
	plain.Proposals[0].Id, plain.Proposals[1].Options[2].Index = 7, 9
	s.Require().NoError(s.keeper.InitGenesis(s.kv(), &types.GenesisState{Rounds: []*types.VoteRound{plain}}))
	got, err := s.keeper.GetVoteRound(s.kv(), ballotRoundA)
	s.Require().NoError(err)
	s.Require().True(proto.Equal(plain, got))
}
