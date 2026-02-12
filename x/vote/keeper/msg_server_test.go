package keeper_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/blake2b"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

type MsgServerTestSuite struct {
	suite.Suite
	ctx       sdk.Context
	keeper    keeper.Keeper
	msgServer types.MsgServer
}

func TestMsgServerTestSuite(t *testing.T) {
	suite.Run(t, new(MsgServerTestSuite))
}

func (s *MsgServerTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.WithBlockTime(time.Unix(1_000_000, 0).UTC())
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger())
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupActiveRound creates a vote round in the store with an end time in the future.
func (s *MsgServerTestSuite) setupActiveRound(roundID []byte) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: roundID,
		VoteEndTime: 2_000_000,
		Creator:     "zvote1creator",
	}))
}

// setupRootAtHeight stores a commitment tree root at the given height.
func (s *MsgServerTestSuite) setupRootAtHeight(height uint64) {
	kv := s.keeper.OpenKVStore(s.ctx)
	root := bytes.Repeat([]byte{0xCC}, 32)
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(kv, height, root))
}

// computeExpectedRoundID mirrors the deriveRoundID function for test verification.
func computeExpectedRoundID(msg *types.MsgCreateVotingSession) []byte {
	h, _ := blake2b.New256(nil)
	var buf [8]byte

	binary.BigEndian.PutUint64(buf[:], msg.SnapshotHeight)
	h.Write(buf[:])
	h.Write(msg.SnapshotBlockhash)
	h.Write(msg.ProposalsHash)
	binary.BigEndian.PutUint64(buf[:], msg.VoteEndTime)
	h.Write(buf[:])
	h.Write(msg.NullifierImtRoot)
	h.Write(msg.NcRoot)

	return h.Sum(nil)
}

// validSetupMsg returns a valid MsgCreateVotingSession for tests.
func validSetupMsg() *types.MsgCreateVotingSession {
	return &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
		VoteEndTime:       2_000_000,
		NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
		NcRoot:            bytes.Repeat([]byte{0x04}, 32),
	}
}

// ---------------------------------------------------------------------------
// CreateVotingSession
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestCreateVotingSession() {
	msg := validSetupMsg()
	expectedID := computeExpectedRoundID(msg)

	tests := []struct {
		name        string
		setup       func()
		msg         *types.MsgCreateVotingSession
		expectErr   bool
		errContains string
		checkResp   func(*types.MsgCreateVotingSessionResponse)
	}{
		{
			name: "happy path: round created and ID returned",
			msg:  msg,
			checkResp: func(resp *types.MsgCreateVotingSessionResponse) {
				s.Require().Equal(expectedID, resp.VoteRoundId)

				// Verify round is stored.
				kv := s.keeper.OpenKVStore(s.ctx)
				round, err := s.keeper.GetVoteRound(kv, expectedID)
				s.Require().NoError(err)
				s.Require().Equal(msg.Creator, round.Creator)
				s.Require().Equal(msg.SnapshotHeight, round.SnapshotHeight)
				s.Require().Equal(msg.VoteEndTime, round.VoteEndTime)
			},
		},
		{
			name: "duplicate round rejected",
			setup: func() {
				// Create the round first.
				_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
				s.Require().NoError(err)
			},
			msg:         msg,
			expectErr:   true,
			errContains: "vote round already exists",
		},
		{
			name: "different fields produce different round ID",
			msg: &types.MsgCreateVotingSession{
				Creator:           "zvote1admin",
				SnapshotHeight:    999,
				SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
				ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
				VoteEndTime:       2_000_000,
				NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
				NcRoot:            bytes.Repeat([]byte{0x04}, 32),
			},
			checkResp: func(resp *types.MsgCreateVotingSessionResponse) {
				s.Require().NotEqual(expectedID, resp.VoteRoundId)
				s.Require().Len(resp.VoteRoundId, 32)
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			resp, err := s.msgServer.CreateVotingSession(s.ctx, tc.msg)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
				if tc.checkResp != nil {
					tc.checkResp(resp)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DelegateVote
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestDelegateVote() {
	roundID := bytes.Repeat([]byte{0x10}, 32)

	tests := []struct {
		name      string
		setup     func()
		msg       *types.MsgDelegateVote
		expectErr bool
		check     func()
	}{
		{
			name:  "happy path: nullifiers recorded and commitments appended",
			setup: func() { s.setupActiveRound(roundID) },
			msg: &types.MsgDelegateVote{
				Rk:                  bytes.Repeat([]byte{0xA1}, 32),
				SpendAuthSig:        bytes.Repeat([]byte{0xA2}, 64),
				SignedNoteNullifier: bytes.Repeat([]byte{0xA3}, 32),
				CmxNew:              bytes.Repeat([]byte{0xB1}, 32),
				GovComm:             bytes.Repeat([]byte{0xB2}, 32),
				GovNullifiers: [][]byte{
					bytes.Repeat([]byte{0xC1}, 32),
					bytes.Repeat([]byte{0xC2}, 32),
				},
				Proof:       bytes.Repeat([]byte{0xD1}, 64),
				VoteRoundId: roundID,
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				// Gov nullifiers recorded (scoped to gov type + round).
				for _, nf := range [][]byte{
					bytes.Repeat([]byte{0xC1}, 32),
					bytes.Repeat([]byte{0xC2}, 32),
				} {
					has, err := s.keeper.HasNullifier(kv, types.NullifierTypeGov, roundID, nf)
					s.Require().NoError(err)
					s.Require().True(has)
				}

				// Tree state advanced by 2 (cmx_new + gov_comm).
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(2), state.NextIndex)

				// Verify leaf contents at correct indices.
				leaf0, err := kv.Get(types.CommitmentLeafKey(0))
				s.Require().NoError(err)
				s.Require().Equal(bytes.Repeat([]byte{0xB1}, 32), leaf0) // cmx_new first

				leaf1, err := kv.Get(types.CommitmentLeafKey(1))
				s.Require().NoError(err)
				s.Require().Equal(bytes.Repeat([]byte{0xB2}, 32), leaf1) // gov_comm second
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			_, err := s.msgServer.DelegateVote(s.ctx, tc.msg)
			if tc.expectErr {
				s.Require().Error(err)
			} else {
				s.Require().NoError(err)
				if tc.check != nil {
					tc.check()
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CastVote
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestCastVote() {
	roundID := bytes.Repeat([]byte{0x20}, 32)

	tests := []struct {
		name        string
		setup       func()
		msg         *types.MsgCastVote
		expectErr   bool
		errContains string
		check       func()
	}{
		{
			name: "happy path: nullifier recorded and commitments appended",
			setup: func() {
				s.setupActiveRound(roundID)
				s.setupRootAtHeight(10)
			},
			msg: &types.MsgCastVote{
				VanNullifier:             bytes.Repeat([]byte{0xE1}, 32),
				VoteAuthorityNoteNew:     bytes.Repeat([]byte{0xE2}, 32),
				VoteCommitment:           bytes.Repeat([]byte{0xE3}, 32),
				ProposalId:               1,
				Proof:                    bytes.Repeat([]byte{0xE4}, 64),
				VoteRoundId:              roundID,
				VoteCommTreeAnchorHeight: 10,
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				has, err := s.keeper.HasNullifier(kv, types.NullifierTypeVoteAuthorityNote, roundID, bytes.Repeat([]byte{0xE1}, 32))
				s.Require().NoError(err)
				s.Require().True(has)

				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(2), state.NextIndex)
			},
		},
		{
			name: "invalid anchor height: no root stored",
			setup: func() {
				s.setupActiveRound(roundID)
				// No root at height 999.
			},
			msg: &types.MsgCastVote{
				VanNullifier:             bytes.Repeat([]byte{0xE1}, 32),
				VoteAuthorityNoteNew:     bytes.Repeat([]byte{0xE2}, 32),
				VoteCommitment:           bytes.Repeat([]byte{0xE3}, 32),
				ProposalId:               1,
				Proof:                    bytes.Repeat([]byte{0xE4}, 64),
				VoteRoundId:              roundID,
				VoteCommTreeAnchorHeight: 999,
			},
			expectErr:   true,
			errContains: "invalid commitment tree anchor height",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			_, err := s.msgServer.CastVote(s.ctx, tc.msg)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
				if tc.check != nil {
					tc.check()
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RevealShare
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestRevealShare() {
	roundID := bytes.Repeat([]byte{0x30}, 32)

	tests := []struct {
		name  string
		setup func()
		msg   *types.MsgRevealShare
		check func()
	}{
		{
			name:  "happy path: nullifier recorded and tally accumulated",
			setup: func() { s.setupActiveRound(roundID) },
			msg: &types.MsgRevealShare{
				ShareNullifier:           bytes.Repeat([]byte{0xF1}, 32),
				VoteAmount:               500,
				ProposalId:               1,
				VoteDecision:             1,
				Proof:                    bytes.Repeat([]byte{0xF2}, 64),
				VoteRoundId:              roundID,
				VoteCommTreeAnchorHeight: 10,
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				has, err := s.keeper.HasNullifier(kv, types.NullifierTypeShare, roundID, bytes.Repeat([]byte{0xF1}, 32))
				s.Require().NoError(err)
				s.Require().True(has)

				tally, err := s.keeper.GetTally(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().Equal(uint64(500), tally)
			},
		},
		{
			name: "tally accumulates across multiple reveals",
			setup: func() {
				s.setupActiveRound(roundID)
				// First reveal.
				_, err := s.msgServer.RevealShare(s.ctx, &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF3}, 32),
					VoteAmount:               300,
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF4}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				})
				s.Require().NoError(err)
			},
			msg: &types.MsgRevealShare{
				ShareNullifier:           bytes.Repeat([]byte{0xF5}, 32),
				VoteAmount:               200,
				ProposalId:               1,
				VoteDecision:             1,
				Proof:                    bytes.Repeat([]byte{0xF6}, 64),
				VoteRoundId:              roundID,
				VoteCommTreeAnchorHeight: 10,
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				tally, err := s.keeper.GetTally(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().Equal(uint64(500), tally)
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			_, err := s.msgServer.RevealShare(s.ctx, tc.msg)
			s.Require().NoError(err)
			if tc.check != nil {
				tc.check()
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CreateVotingSession: deterministic round ID
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestCreateVotingSession_DeterministicID() {
	s.SetupTest()
	msg := validSetupMsg()

	resp1, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)

	// Same inputs must produce same ID.
	expected := computeExpectedRoundID(msg)
	s.Require().Equal(expected, resp1.VoteRoundId)
	s.Require().Len(resp1.VoteRoundId, 32)
}

// ---------------------------------------------------------------------------
// Event emission smoke test
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestCreateVotingSession_EmitsEvent() {
	s.SetupTest()
	msg := validSetupMsg()

	_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)

	events := s.ctx.EventManager().Events()
	found := false
	for _, e := range events {
		if e.Type == types.EventTypeCreateVotingSession {
			found = true
			// Verify round ID attribute present.
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyRoundID {
					expected := fmt.Sprintf("%x", computeExpectedRoundID(msg))
					s.Require().Equal(expected, attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeCreateVotingSession)
}
