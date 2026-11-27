package keeper_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover the 0x0B ballot op (docs/proxy-delegation/contracts-v1.md
// §5.3, §6, §7) with the dormancy gate open.

var (
	// ballotRoundA and ballotRoundB are the rounds of the ballot tests.
	ballotRoundA = bytes.Repeat([]byte{0xB1}, 32)
	ballotRoundB = bytes.Repeat([]byte{0xB2}, 32)
)

// proxyRound returns an ACTIVE proxy round created at createdAt and ending
// ten days after proxyTestTime, with three proposals of 2, 3 and 4 options.
func proxyRound(roundID []byte, createdAt uint64) *types.VoteRound {
	round := proxytest.Round(roundID, 2, 3, 4)
	round.CreatedAtTime = createdAt
	round.VoteEndTime = proxyTestTime + 10*proxyTestDay
	return round
}

// setRound stores round.
func (s *ProxyRegistryTestSuite) setRound(round *types.VoteRound) {
	s.Require().NoError(s.keeper.SetVoteRound(s.kv(), round))
}

// updateRound applies mutate to stored round roundID.
func (s *ProxyRegistryTestSuite) updateRound(roundID []byte, mutate func(*types.VoteRound)) {
	round, err := s.keeper.GetVoteRound(s.kv(), roundID)
	s.Require().NoError(err)
	mutate(round)
	s.setRound(round)
}

// ballotOp builds delegate d's ballot in round roundID, signed by dk over
// BALLOT.
func (s *ProxyRegistryTestSuite) ballotOp(roundID []byte, d uint32, dk ed25519.PrivateKey, options ...uint32) *types.MsgDelegateOp {
	return proxytest.BallotOp(s.ctx.ChainID(), roundID, d, dk, options...)
}

// ballots returns round roundID's stored ballots in index order.
func (s *ProxyRegistryTestSuite) ballots(roundID []byte) []*types.DelegateBallotRecord {
	var ballots []*types.DelegateBallotRecord
	s.Require().NoError(s.keeper.IterateDelegateBallots(s.kv(), roundID, 0, func(ballot *types.DelegateBallotRecord) bool {
		ballots = append(ballots, ballot)
		return false
	}))
	return ballots
}

// requireBallotCode fails unless op is refused with want, whose registered
// code is code, by the ante in CheckTx and FinalizeBlock and by the handler,
// and no ballot is stored for its round.
func (s *ProxyRegistryTestSuite) requireBallotCode(op *types.MsgDelegateOp, want *errorsmod.Error, code uint32) {
	s.T().Helper()
	before := s.ballots(op.Ballot.VoteRoundId)
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(s.ctx.WithIsCheckTx(true), op, true), want, code)
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(s.ctx, op, true), want, code)
	_, err := s.deliver(op)
	proxytest.RequireCode(s.T(), err, want, code)
	s.Require().Equal(before, s.ballots(op.Ballot.VoteRoundId))
}

// ---------------------------------------------------------------------------
// Accepted ballots
// ---------------------------------------------------------------------------

// TestDelegateBallot checks an accepted ballot: the ante leaves the store
// alone, and the handler stores the record at 0x1B round d as deterministic
// protobuf, emits delegate_ballot and returns d.
func (s *ProxyRegistryTestSuite) TestDelegateBallot() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	op := s.ballotOp(ballotRoundA, d, dk, 1, 2, 3)

	s.Require().NoError(s.keeper.ValidateDelegateOp(s.ctx.WithIsCheckTx(true), op, true))
	s.Require().NoError(s.keeper.ValidateDelegateOp(s.ctx.WithIsReCheckTx(true), op, false))
	s.Require().NoError(s.keeper.ValidateDelegateOp(s.ctx, op, true))
	s.Require().Empty(s.ballots(ballotRoundA), "the ante writes nothing")

	s.ctx = s.ctx.WithEventManager(sdk.NewEventManager())
	resp, err := s.deliver(op)
	s.Require().NoError(err)
	s.Require().Equal(d, resp.DelegateIndex)

	want := &types.DelegateBallotRecord{VoteRoundId: ballotRoundA, DelegateIndex: d, Options: []uint32{1, 2, 3}, Height: 100}
	ballots := s.ballots(ballotRoundA)
	s.Require().Len(ballots, 1)
	s.Require().True(proto.Equal(want, ballots[0]))
	key, err := types.DelegateBallotKey(ballotRoundA, d)
	s.Require().NoError(err)
	stored, err := s.kv().Get(key)
	s.Require().NoError(err)
	wantBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(want)
	s.Require().NoError(err)
	s.Require().Equal(wantBytes, stored)

	s.Require().Equal(map[string]string{
		types.AttributeKeyRoundID:       hex.EncodeToString(ballotRoundA),
		types.AttributeKeyDelegateIndex: fmt.Sprint(d),
		types.AttributeKeyOptions:       "1,2,3",
	}, eventAttributes(s.T(), s.ctx.EventManager().Events(), types.EventTypeDelegateBallot))
}

// TestDelegateBallotMidRoundRegistrant checks that a delegate registered
// after the round was created can ballot in it: the ballot check reads the
// entry's key, never the round's creation time.
func (s *ProxyRegistryTestSuite) TestDelegateBallotMidRoundRegistrant() {
	s.openRegistry()
	s.setRound(proxyRound(ballotRoundA, proxyTestTime-proxyTestDay))
	s.setBlockTime(proxyTestTime + 3600)
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	s.mustDeliver(s.ballotOp(ballotRoundA, d, dk, 1, 0, 2))
	s.Require().Len(s.ballots(ballotRoundA), 1)
}

// TestDelegateBallotIgnoresPause checks that proxy_dc_paused does not affect
// ballots (§5.3).
func (s *ProxyRegistryTestSuite) TestDelegateBallotIgnoresPause() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	params, err := s.keeper.GetProxyDelegationParams(s.kv())
	s.Require().NoError(err)
	params.ProxyDcPaused = true
	s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), params))
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	s.mustDeliver(s.ballotOp(ballotRoundA, d, dk, 0, 0, 0))
	s.Require().Len(s.ballots(ballotRoundA), 1)
}

// TestDelegateBallotOnePerRoundAndDelegate checks that ballots are keyed by
// (round, d): one delegate ballots in two rounds and two delegates in one.
func (s *ProxyRegistryTestSuite) TestDelegateBallotOnePerRoundAndDelegate() {
	s.openRegistry()
	alice, bob := proxytest.Key("alice/1"), proxytest.Key("bob/1")
	a, b := s.registerNew(alice, "alice"), s.registerNew(bob, "bob")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	s.setRound(proxyRound(ballotRoundB, proxyTestTime))
	s.mustDeliver(s.ballotOp(ballotRoundA, b, bob, 1, 1, 1))
	s.mustDeliver(s.ballotOp(ballotRoundA, a, alice, 0, 1, 2))
	s.mustDeliver(s.ballotOp(ballotRoundB, a, alice, 1, 2, 3))

	var indices []uint32
	for _, ballot := range s.ballots(ballotRoundA) {
		indices = append(indices, ballot.DelegateIndex)
	}
	s.Require().Equal([]uint32{a, b}, indices, "in index order")
	s.Require().Len(s.ballots(ballotRoundB), 1)
	s.Require().Equal([]uint32{1, 2, 3}, s.ballots(ballotRoundB)[0].Options)
}

// ---------------------------------------------------------------------------
// The stored-ballot check and the check order
// ---------------------------------------------------------------------------

// TestDelegateBallotSecondBallot checks that a second ballot for (round, d),
// identical or not, is ErrDelegateBallotExists (57), and the first stands.
func (s *ProxyRegistryTestSuite) TestDelegateBallotSecondBallot() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	first := s.ballotOp(ballotRoundA, d, dk, 0, 1, 2)
	s.mustDeliver(first)
	landed := s.ballots(ballotRoundA)

	s.requireBallotCode(first, types.ErrDelegateBallotExists, 57)
	s.requireBallotCode(s.ballotOp(ballotRoundA, d, dk, 1, 2, 3), types.ErrDelegateBallotExists, 57)
	s.Require().Equal(landed, s.ballots(ballotRoundA))
}

// TestDelegateBallotRetryAfterChanges checks that the stored-ballot check
// comes before anything that can change after a ballot lands (§5.3 step 2):
// a retry is ErrDelegateBallotExists (57) after the round closes and after a
// suspension or a retirement, in CheckTx, RecheckTx and FinalizeBlock, and
// the ballot stands.
func (s *ProxyRegistryTestSuite) TestDelegateBallotRetryAfterChanges() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	op := s.ballotOp(ballotRoundA, d, dk, 0, 0, 0)
	s.mustDeliver(op)
	landed := s.ballots(ballotRoundA)

	for _, c := range []struct {
		after string
		apply func()
	}{
		{"vote_end_time", func() { s.setBlockTime(proxyTestTime + 10*proxyTestDay) }},
		{"the round turns TALLYING", func() {
			s.updateRound(ballotRoundA, func(r *types.VoteRound) { r.Status = types.SessionStatus_SESSION_STATUS_TALLYING })
		}},
		{"a suspension", func() { s.setStatus(d, types.DelegateStatusSuspended, 1) }},
		{"a retirement", func() { s.setStatus(d, types.DelegateStatusRetired, 1) }},
		{"the round is FINALIZED", func() {
			s.updateRound(ballotRoundA, func(r *types.VoteRound) { r.Status = types.SessionStatus_SESSION_STATUS_FINALIZED })
		}},
	} {
		c.apply()
		s.requireBallotCode(op, types.ErrDelegateBallotExists, 57)
		proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(s.ctx.WithIsReCheckTx(true), op, false), types.ErrDelegateBallotExists, 57)
		s.Require().Equal(landed, s.ballots(ballotRoundA), "after %s", c.after)
	}
}

// TestDelegateBallotCheckOrder checks that when several ballot checks fail,
// the first in §5.3's order decides the code.
func (s *ProxyRegistryTestSuite) TestDelegateBallotCheckOrder() {
	alice, mallory := proxytest.Key("alice/1"), proxytest.Key("mallory")
	garbageSig := bytes.Repeat([]byte{0x07}, 64)
	for _, c := range []struct {
		name     string
		setup    func()
		op       func() *types.MsgDelegateOp
		wantErr  *errorsmod.Error
		wantCode uint32
	}{
		{
			name:  "round before everything",
			setup: func() {},
			op: func() *types.MsgDelegateOp {
				op := s.ballotOp(ballotRoundB, 9, mallory, 7)
				op.Ballot.DkSig = garbageSig
				return op
			},
			wantErr: types.ErrRoundNotFound, wantCode: 3,
		},
		{
			name: "stored ballot before the round status, flag and entry",
			setup: func() {
				s.mustDeliver(s.ballotOp(ballotRoundA, 1, alice, 0, 0, 0))
				s.updateRound(ballotRoundA, func(r *types.VoteRound) {
					r.Status, r.ProxyDelegation = types.SessionStatus_SESSION_STATUS_TALLYING, nil
				})
				s.setStatus(1, types.DelegateStatusRetired, 0)
			},
			op: func() *types.MsgDelegateOp {
				op := s.ballotOp(ballotRoundA, 1, mallory, 9)
				op.Ballot.DkSig = garbageSig
				return op
			},
			wantErr: types.ErrDelegateBallotExists, wantCode: 57,
		},
		{
			name: "round status before the flag",
			setup: func() {
				s.updateRound(ballotRoundA, func(r *types.VoteRound) {
					r.Status, r.ProxyDelegation = types.SessionStatus_SESSION_STATUS_TALLYING, nil
				})
			},
			op:      func() *types.MsgDelegateOp { return s.ballotOp(ballotRoundA, 1, alice, 0, 0, 0) },
			wantErr: types.ErrRoundNotActive, wantCode: 4,
		},
		{
			name: "vote_end_time before the flag",
			setup: func() {
				s.updateRound(ballotRoundA, func(r *types.VoteRound) { r.ProxyDelegation = nil })
				s.setBlockTime(proxyTestTime + 10*proxyTestDay)
			},
			op:      func() *types.MsgDelegateOp { return s.ballotOp(ballotRoundA, 1, alice, 0, 0, 0) },
			wantErr: types.ErrRoundNotActive, wantCode: 4,
		},
		{
			name:    "flag before the entry",
			setup:   func() { s.updateRound(ballotRoundA, func(r *types.VoteRound) { r.ProxyDelegation = nil }) },
			op:      func() *types.MsgDelegateOp { return s.ballotOp(ballotRoundA, 9, mallory, 0, 0, 0) },
			wantErr: types.ErrProxyDelegationDisabled, wantCode: 69,
		},
		{
			name:  "entry before the key and options",
			setup: func() {},
			op: func() *types.MsgDelegateOp {
				op := s.ballotOp(ballotRoundA, 9, mallory, 9)
				op.Ballot.DkSig = garbageSig
				return op
			},
			wantErr: types.ErrDelegateNotFound, wantCode: 53,
		},
		{
			name:    "suspension before the signature",
			setup:   func() { s.setStatus(1, types.DelegateStatusSuspended, 1) },
			op:      func() *types.MsgDelegateOp { return s.ballotOp(ballotRoundA, 1, mallory, 0, 0, 0) },
			wantErr: types.ErrDelegateSuspended, wantCode: 54,
		},
		{
			name:    "retirement before the signature",
			setup:   func() { s.setStatus(1, types.DelegateStatusRetired, 0) },
			op:      func() *types.MsgDelegateOp { return s.ballotOp(ballotRoundA, 1, mallory, 0, 0, 0) },
			wantErr: types.ErrDelegateRetired, wantCode: 55,
		},
		{
			name:    "signature before the options",
			setup:   func() {},
			op:      func() *types.MsgDelegateOp { return s.ballotOp(ballotRoundA, 1, mallory, 9) },
			wantErr: types.ErrInvalidSignature, wantCode: 6,
		},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.openRegistry()
			s.Require().Equal(uint32(1), s.registerNew(alice, "alice"))
			s.setRound(proxyRound(ballotRoundA, proxyTestTime))
			c.setup()
			s.requireBallotCode(c.op(), c.wantErr, c.wantCode)
		})
	}
}

// ---------------------------------------------------------------------------
// The round, entry and option checks
// ---------------------------------------------------------------------------

// TestDelegateBallotRoundChecks checks ballot step 3 (§5.3): the round must
// be ACTIVE with the block time before vote_end_time, and have the proxy
// flag.
func (s *ProxyRegistryTestSuite) TestDelegateBallotRoundChecks() {
	dk := proxytest.Key("alice/1")
	endTime := proxyTestTime + 10*proxyTestDay
	for _, c := range []struct {
		name      string
		blockTime uint64
		mutate    func(*types.VoteRound)
		wantErr   *errorsmod.Error
		wantCode  uint32
	}{
		{"one second before vote_end_time", endTime - 1, func(*types.VoteRound) {}, nil, 0},
		{"at vote_end_time", endTime, func(*types.VoteRound) {}, types.ErrRoundNotActive, 4},
		{"PENDING", proxyTestTime, func(r *types.VoteRound) { r.Status = types.SessionStatus_SESSION_STATUS_PENDING }, types.ErrRoundNotActive, 4},
		{"TALLYING", proxyTestTime, func(r *types.VoteRound) { r.Status = types.SessionStatus_SESSION_STATUS_TALLYING }, types.ErrRoundNotActive, 4},
		{"FINALIZED", proxyTestTime, func(r *types.VoteRound) { r.Status = types.SessionStatus_SESSION_STATUS_FINALIZED }, types.ErrRoundNotActive, 4},
		{"CEREMONY_FAILED", proxyTestTime, func(r *types.VoteRound) { r.Status = types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED }, types.ErrRoundNotActive, 4},
		{"no field 31", proxyTestTime, func(r *types.VoteRound) { r.ProxyDelegation = nil }, types.ErrProxyDelegationDisabled, 69},
		{"field 31 not enabled", proxyTestTime, func(r *types.VoteRound) { r.ProxyDelegation.Enabled = false }, types.ErrProxyDelegationDisabled, 69},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.openRegistry()
			d := s.registerNew(dk, "alice")
			round := proxyRound(ballotRoundA, proxyTestTime)
			c.mutate(round)
			s.setRound(round)
			s.setBlockTime(c.blockTime)
			op := s.ballotOp(ballotRoundA, d, dk, 0, 0, 0)
			if c.wantErr == nil {
				s.mustDeliver(op)
				return
			}
			s.requireBallotCode(op, c.wantErr, c.wantCode)
		})
	}
}

// TestDelegateBallotUnsetBlockTime checks the vote_end_time check right after
// a restart, when CheckTx runs with the header time unset: CheckTx and
// RecheckTx skip it, as ValidateRoundForVoting does, and FinalizeBlock never
// does.
func (s *ProxyRegistryTestSuite) TestDelegateBallotUnsetBlockTime() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	op := s.ballotOp(ballotRoundA, d, dk, 0, 0, 0)

	unset := s.ctx.WithBlockTime(time.Time{})
	s.Require().Negative(unset.BlockTime().Unix())
	s.Require().NoError(s.keeper.ValidateDelegateOp(unset.WithIsCheckTx(true), op, true))
	s.Require().NoError(s.keeper.ValidateDelegateOp(unset.WithIsReCheckTx(true), op, false))
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(unset, op, true), types.ErrRoundNotActive, 4)
	s.Require().Empty(s.ballots(ballotRoundA))
}

// TestDelegateBallotEntryChecks checks ballot step 4 (§5.3): the entry must
// exist and be ACTIVE. A SUSPENDED entry is 54 and ballots again once the
// suspension is lifted; a RETIRED one is 55 for good.
func (s *ProxyRegistryTestSuite) TestDelegateBallotEntryChecks() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))

	s.requireBallotCode(s.ballotOp(ballotRoundA, d+1, dk, 0, 0, 0), types.ErrDelegateNotFound, 53)
	s.setStatus(d, types.DelegateStatusSuspended, 3)
	s.requireBallotCode(s.ballotOp(ballotRoundA, d, dk, 0, 0, 0), types.ErrDelegateSuspended, 54)
	s.setStatus(d, types.DelegateStatusActive, 0)
	s.mustDeliver(s.ballotOp(ballotRoundA, d, dk, 0, 0, 0))

	s.setStatus(d, types.DelegateStatusRetired, 0)
	s.requireBallotCode(s.ballotOp(ballotRoundB, d, dk, 0, 0, 0), types.ErrRoundNotFound, 3)
	s.setRound(proxyRound(ballotRoundB, proxyTestTime))
	s.requireBallotCode(s.ballotOp(ballotRoundB, d, dk, 0, 0, 0), types.ErrDelegateRetired, 55)
}

// TestDelegateBallotOptions checks ballot step 6 (§5.3) on a round whose
// proposals have 2, 3 and 4 options: an incomplete ballot, an extra option
// and any option out of range are ErrInvalidBallot (58).
func (s *ProxyRegistryTestSuite) TestDelegateBallotOptions() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	d := s.registerNew(dk, "alice")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))

	for _, options := range [][]uint32{
		{0},
		{0, 0},
		{0, 0, 0, 0},
		{2, 0, 0},
		{0, 3, 0},
		{0, 0, 4},
		{0, 0, 4294967295},
	} {
		s.Run(fmt.Sprint(options), func() {
			s.requireBallotCode(s.ballotOp(ballotRoundA, d, dk, options...), types.ErrInvalidBallot, 58)
		})
	}
	s.mustDeliver(s.ballotOp(ballotRoundA, d, dk, 1, 2, 3))
}

// ---------------------------------------------------------------------------
// The signature
// ---------------------------------------------------------------------------

// TestDelegateBallotSignature checks that dk_sig must be the entry's key's
// signature over this ballot's BALLOT, which binds the chain, the round, d
// and every option, and that RecheckTx skips only the signature.
func (s *ProxyRegistryTestSuite) TestDelegateBallotSignature() {
	s.openRegistry()
	alice, bob := proxytest.Key("alice/1"), proxytest.Key("bob/1")
	a, b := s.registerNew(alice, "alice"), s.registerNew(bob, "bob")
	s.setRound(proxyRound(ballotRoundA, proxyTestTime))
	s.setRound(proxyRound(ballotRoundB, proxyTestTime))

	for name, sig := range map[string][]byte{
		"another delegate's key": proxytest.BallotSig(proxyTestChainID, ballotRoundA, a, bob, 0, 1, 2),
		"another chain":          proxytest.BallotSig("svote-1", ballotRoundA, a, alice, 0, 1, 2),
		"another round":          proxytest.BallotSig(proxyTestChainID, ballotRoundB, a, alice, 0, 1, 2),
		"another index":          proxytest.BallotSig(proxyTestChainID, ballotRoundA, b, alice, 0, 1, 2),
		"other options":          proxytest.BallotSig(proxyTestChainID, ballotRoundA, a, alice, 0, 1, 3),
		"garbage":                bytes.Repeat([]byte{0x07}, 64),
	} {
		s.Run(name, func() {
			op := s.ballotOp(ballotRoundA, a, alice, 0, 1, 2)
			op.Ballot.DkSig = sig
			s.requireBallotCode(op, types.ErrInvalidSignature, 6)
			s.Require().NoError(s.keeper.ValidateDelegateOp(s.ctx.WithIsReCheckTx(true), op, false))
		})
	}
	s.mustDeliver(s.ballotOp(ballotRoundA, a, alice, 0, 1, 2))
}

// ---------------------------------------------------------------------------
// The shared vectors (contracts-v1 §13)
// ---------------------------------------------------------------------------

// TestDelegateBallotVectors runs the shared BALLOT vectors through the
// handler, with their key as entry 17's: each complete in-range vector
// ballot lands, and the largest option value is ErrInvalidBallot.
func (s *ProxyRegistryTestSuite) TestDelegateBallotVectors() {
	type ballotCase struct {
		Name   string `json:"name"`
		Inputs struct {
			ChainID       string   `json:"chain_id"`
			VoteRoundID   string   `json:"vote_round_id"`
			DelegateIndex uint32   `json:"delegate_index"`
			Options       []uint32 `json:"options"`
		} `json:"inputs"`
		Expected struct {
			DK    string `json:"dk"`
			DkSig string `json:"dk_sig"`
		} `json:"expected"`
	}
	var vectors struct {
		Ballot []ballotCase `json:"ballot"`
	}
	s.Require().NoError(json.Unmarshal(svtest.ProxyDelegationV1Vectors(), &vectors))
	s.Require().NotEmpty(vectors.Ballot)
	hexOf := func(h string) []byte { return routingVectorHex(s.T(), h) }

	for _, c := range vectors.Ballot {
		s.Run(c.Name, func() {
			s.SetupTest()
			s.Require().Equal(proxyTestChainID, c.Inputs.ChainID)
			s.seedEntries(1, 16)
			d := c.Inputs.DelegateIndex
			s.Require().Equal(uint32(17), d)
			s.Require().NoError(s.keeper.SetProxyDelegateEntry(s.kv(), &types.ProxyDelegateEntry{
				Index:    d,
				Dk:       hexOf(c.Expected.DK),
				Provider: types.DelegateProviderX,
				Handle:   "vector",
				ProofId:  "1790000000000000000",
			}))
			// Each proposal has the most options the chain allows, so
			// every vector option below 8 is in range.
			counts := make([]int, len(c.Inputs.Options))
			for i := range counts {
				counts[i] = types.MaxVoteOptions
			}
			round := proxyRound(hexOf(c.Inputs.VoteRoundID), proxyTestTime)
			round.Proposals = proxytest.Proposals(counts...)
			s.setRound(round)

			op := &types.MsgDelegateOp{Ballot: &types.DelegateBallot{
				VoteRoundId:   round.VoteRoundId,
				DelegateIndex: d,
				Options:       c.Inputs.Options,
				DkSig:         hexOf(c.Expected.DkSig),
			}}
			if types.ValidateDelegateBallotOptions(round, c.Inputs.Options) != nil {
				s.requireBallotCode(op, types.ErrInvalidBallot, 58)
				return
			}
			s.Require().Equal(d, s.mustDeliver(op))
			s.Require().Equal(c.Inputs.Options, s.ballots(round.VoteRoundId)[0].Options)
		})
	}
}
