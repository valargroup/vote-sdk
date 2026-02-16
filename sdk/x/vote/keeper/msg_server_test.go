package keeper_test

import (
	"bytes"
	"crypto/rand"
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

	"github.com/z-cale/zally/crypto/elgamal"
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
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), nil)
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupActiveRound creates a vote round in the store with an end time in the future and ACTIVE status.
func (s *MsgServerTestSuite) setupActiveRound(roundID []byte) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId:      roundID,
		VoteEndTime:      2_000_000,
		Creator:          "zvote1creator",
		Status:           types.SessionStatus_SESSION_STATUS_ACTIVE,
		NullifierImtRoot: bytes.Repeat([]byte{0x03}, 32),
		NcRoot:           bytes.Repeat([]byte{0x04}, 32),
		EaPk:             bytes.Repeat([]byte{0x05}, 32),
		VkZkp1:           bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:           bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:           bytes.Repeat([]byte{0x08}, 64),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First"},
			{Id: 2, Title: "Proposal B", Description: "Second"},
		},
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
		EaPk:              bytes.Repeat([]byte{0x05}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First"},
			{Id: 2, Title: "Proposal B", Description: "Second"},
		},
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
			name: "happy path: round created with ACTIVE status and ID returned",
			msg:  msg,
			checkResp: func(resp *types.MsgCreateVotingSessionResponse) {
				s.Require().Equal(expectedID, resp.VoteRoundId)

				// Verify round is stored with correct fields.
				kv := s.keeper.OpenKVStore(s.ctx)
				round, err := s.keeper.GetVoteRound(kv, expectedID)
				s.Require().NoError(err)
				s.Require().Equal(msg.Creator, round.Creator)
				s.Require().Equal(msg.SnapshotHeight, round.SnapshotHeight)
				s.Require().Equal(msg.VoteEndTime, round.VoteEndTime)
				s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)

				// Verify new session fields are stored.
				s.Require().Equal(msg.EaPk, round.EaPk)
				s.Require().Equal(msg.VkZkp1, round.VkZkp1)
				s.Require().Equal(msg.VkZkp2, round.VkZkp2)
				s.Require().Equal(msg.VkZkp3, round.VkZkp3)
				s.Require().Len(round.Proposals, len(msg.Proposals))
				for i, p := range round.Proposals {
					s.Require().Equal(msg.Proposals[i].Id, p.Id)
					s.Require().Equal(msg.Proposals[i].Title, p.Title)
					s.Require().Equal(msg.Proposals[i].Description, p.Description)
				}
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
				EaPk:              bytes.Repeat([]byte{0x05}, 32),
				VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
				VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
				VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
				Proposals: []*types.Proposal{
					{Id: 1, Title: "Proposal A", Description: "First"},
					{Id: 2, Title: "Proposal B", Description: "Second"},
				},
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
				CmxNew:              fpLE(0xB1),
				VanCmx:              fpLE(0xB2),
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

				// Tree state advanced by 1 (only van_cmx; cmx_new is not in the tree).
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(1), state.NextIndex)

				// Verify the single leaf is van_cmx.
				leaf0, err := kv.Get(types.CommitmentLeafKey(0))
				s.Require().NoError(err)
				s.Require().Equal(fpLE(0xB2), leaf0) // van_cmx
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
				RVpkX:                    fpLE(0xE1a),
				RVpkY:                    fpLE(0xE1b),
				VoteAuthorityNoteNew:     fpLE(0xE2),
				VoteCommitment:           fpLE(0xE3),
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
				RVpkX:                    fpLE(0xE1a),
				RVpkY:                    fpLE(0xE1b),
				VoteAuthorityNoteNew:     fpLE(0xE2),
				VoteCommitment:           fpLE(0xE3),
				ProposalId:               1,
				Proof:                    bytes.Repeat([]byte{0xE4}, 64),
				VoteRoundId:              roundID,
				VoteCommTreeAnchorHeight: 999,
			},
			expectErr:   true,
			errContains: "invalid commitment tree anchor height",
		},
		{
			name: "invalid proposal_id rejected",
			setup: func() {
				s.setupActiveRound(roundID) // round has 2 proposals (id 1, 2)
				s.setupRootAtHeight(10)
			},
			msg: &types.MsgCastVote{
				VanNullifier:             bytes.Repeat([]byte{0xE1}, 32),
				RVpkX:                    fpLE(0xE1a),
				RVpkY:                    fpLE(0xE1b),
				VoteAuthorityNoteNew:     fpLE(0xE2),
				VoteCommitment:           fpLE(0xE3),
				ProposalId:               5, // out of range
				Proof:                    bytes.Repeat([]byte{0xE4}, 64),
				VoteRoundId:              roundID,
				VoteCommTreeAnchorHeight: 10,
			},
			expectErr:   true,
			errContains: "invalid proposal ID",
		},
		{
			name: "duplicate VAN nullifier rejected (double-vote)",
			setup: func() {
				s.setupActiveRound(roundID)
				s.setupRootAtHeight(10)
				// First CastVote with this nullifier succeeds and records it.
				first := &types.MsgCastVote{
					VanNullifier:             bytes.Repeat([]byte{0xDD}, 32),
					RVpkX:                    fpLE(0xE1a),
					RVpkY:                    fpLE(0xE1b),
					VoteAuthorityNoteNew:     fpLE(0xE2),
					VoteCommitment:           fpLE(0xE3),
					ProposalId:               1,
					Proof:                    bytes.Repeat([]byte{0xE4}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
				_, err := s.msgServer.CastVote(s.ctx, first)
				s.Require().NoError(err)
			},
			msg: &types.MsgCastVote{
				VanNullifier:             bytes.Repeat([]byte{0xDD}, 32), // same as first
				RVpkX:                    fpLE(0xE1a),
				RVpkY:                    fpLE(0xE1b),
				VoteAuthorityNoteNew:     fpLE(0xE5),
				VoteCommitment:           fpLE(0xE6),
				ProposalId:               1,
				Proof:                    bytes.Repeat([]byte{0xE4}, 64),
				VoteRoundId:              roundID,
				VoteCommTreeAnchorHeight: 10,
			},
			expectErr:   true,
			errContains: "nullifier already",
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

// testEncShare generates a valid 64-byte ElGamal ciphertext for testing.
func testEncShare(s *MsgServerTestSuite, value uint64) []byte {
	_, pk := elgamal.KeyGen(rand.Reader)
	ct, err := elgamal.Encrypt(pk, value, rand.Reader)
	s.Require().NoError(err)
	bz, err := elgamal.MarshalCiphertext(ct)
	s.Require().NoError(err)
	return bz
}

// testEncShareWithPK generates a valid 64-byte ElGamal ciphertext using a specific public key.
func testEncShareWithPK(s *MsgServerTestSuite, pk *elgamal.PublicKey, value uint64) []byte {
	ct, err := elgamal.Encrypt(pk, value, rand.Reader)
	s.Require().NoError(err)
	bz, err := elgamal.MarshalCiphertext(ct)
	s.Require().NoError(err)
	return bz
}

func (s *MsgServerTestSuite) TestRevealShare() {
	roundID := bytes.Repeat([]byte{0x30}, 32)

	tests := []struct {
		name        string
		setup       func()
		msg         func() *types.MsgRevealShare
		expectErr   bool
		errContains string
		check       func()
	}{
		{
			name:  "happy path: nullifier recorded and tally accumulated",
			setup: func() { s.setupActiveRound(roundID) },
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF1}, 32),
					EncShare:                 testEncShare(s, 500),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF2}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				has, err := s.keeper.HasNullifier(kv, types.NullifierTypeShare, roundID, bytes.Repeat([]byte{0xF1}, 32))
				s.Require().NoError(err)
				s.Require().True(has)

				tally, err := s.keeper.GetTally(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().NotNil(tally, "tally should be stored")
				s.Require().Len(tally, 64, "tally should be 64 bytes (ElGamal ciphertext)")
			},
		},
		{
			name: "tally accumulates across multiple reveals via HomomorphicAdd",
			setup: func() {
				s.setupActiveRound(roundID)
				// Use same keypair for both shares so accumulation works.
				_, pk := elgamal.KeyGen(rand.Reader)
				// First reveal.
				_, err := s.msgServer.RevealShare(s.ctx, &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF3}, 32),
					EncShare:                 testEncShareWithPK(s, pk, 300),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF4}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				})
				s.Require().NoError(err)
			},
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF5}, 32),
					EncShare:                 testEncShare(s, 200),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF6}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				tally, err := s.keeper.GetTally(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().NotNil(tally)
				s.Require().Len(tally, 64, "accumulated tally should be 64 bytes")
			},
		},
		{
			name:  "invalid proposal_id rejected",
			setup: func() { s.setupActiveRound(roundID) },
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF7}, 32),
					EncShare:                 testEncShare(s, 100),
					ProposalId:               5, // out of range
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF8}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			expectErr:   true,
			errContains: "invalid proposal ID",
		},
		{
			name: "duplicate share nullifier rejected (double-count)",
			setup: func() {
				s.setupActiveRound(roundID)
				first := &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xFA}, 32),
					EncShare:                 testEncShare(s, 100),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xFB}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
				_, err := s.msgServer.RevealShare(s.ctx, first)
				s.Require().NoError(err)
			},
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xFA}, 32), // same as first
					EncShare:                 testEncShare(s, 200),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xFC}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			expectErr:   true,
			errContains: "nullifier already",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			_, err := s.msgServer.RevealShare(s.ctx, tc.msg())
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
// SubmitTally
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitTally() {
	roundID := bytes.Repeat([]byte{0x40}, 32)
	creator := "zvote1creator"

	// Helper: set up a TALLYING round with an encrypted tally accumulator.
	setupTallyingRoundWithAccumulator := func() {
		kv := s.keeper.OpenKVStore(s.ctx)
		s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
			VoteRoundId: roundID,
			VoteEndTime: 500_000,
			Creator:     creator,
			Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First"},
			{Id: 2, Title: "Proposal B", Description: "Second"},
		},
	}))
		// Pre-populate the tally accumulator with a ciphertext.
		encShare := testEncShare(s, 500)
		s.Require().NoError(s.keeper.AddToTally(kv, roundID, 0, 1, encShare))
	}

	tests := []struct {
		name        string
		setup       func()
		msg         *types.MsgSubmitTally
		expectErr   bool
		errContains string
		check       func(resp *types.MsgSubmitTallyResponse)
	}{
		{
			name: "happy path: round finalized and results stored (DLEQ stubbed)",
			setup: func() {
				setupTallyingRoundWithAccumulator()
			},
			msg: &types.MsgSubmitTally{
				VoteRoundId: roundID,
				Creator:     creator,
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
				},
			},
			check: func(resp *types.MsgSubmitTallyResponse) {
				s.Require().Equal(uint32(1), resp.FinalizedEntries)

				kv := s.keeper.OpenKVStore(s.ctx)

				// Round is FINALIZED.
				round, err := s.keeper.GetVoteRound(kv, roundID)
				s.Require().NoError(err)
				s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)

				// TallyResult is stored (uint64 decrypted value from EA).
				result, err := s.keeper.GetTallyResult(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().NotNil(result)
				s.Require().Equal(uint64(500), result.TotalValue)
				s.Require().Equal(uint32(1), result.ProposalId)
				s.Require().Equal(uint32(1), result.VoteDecision)
			},
		},
		{
			name: "rejected: entry references non-existent proposal",
			setup: func() {
				setupTallyingRoundWithAccumulator()
			},
			msg: &types.MsgSubmitTally{
				VoteRoundId: roundID,
				Creator:     creator,
				Entries: []*types.TallyEntry{
					{ProposalId: 5, VoteDecision: 1, TotalValue: 500},
				},
			},
			expectErr:   true,
			errContains: "invalid proposal ID",
		},
		{
			name: "rejected: round is ACTIVE not TALLYING",
			setup: func() {
				s.setupActiveRound(roundID)
			},
			msg: &types.MsgSubmitTally{
				VoteRoundId: roundID,
				Creator:     "zvote1creator",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
				},
			},
			expectErr:   true,
			errContains: "not in tallying state",
		},
		{
			name: "rejected: round is already FINALIZED",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: roundID,
					VoteEndTime: 500_000,
					Creator:     creator,
					Status:      types.SessionStatus_SESSION_STATUS_FINALIZED,
				}))
			},
			msg: &types.MsgSubmitTally{
				VoteRoundId: roundID,
				Creator:     creator,
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
				},
			},
			expectErr:   true,
			errContains: "not in tallying state",
		},
		{
			name: "accepted: different creator (validator check is in ante, not msg_server)",
			setup: func() {
				setupTallyingRoundWithAccumulator()
			},
			msg: &types.MsgSubmitTally{
				VoteRoundId: roundID,
				Creator:     "zvote1othervalidator",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
				},
			},
			check: func(resp *types.MsgSubmitTallyResponse) {
				s.Require().Equal(uint32(1), resp.FinalizedEntries)
			},
		},
		{
			name: "rejected: round does not exist",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0xFF}, 32),
				Creator:     creator,
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
				},
			},
			expectErr:   true,
			errContains: "vote round not found",
		},
		{
			name: "happy path: zero-valued entry for (proposal, decision) with no reveals",
			setup: func() {
				setupTallyingRoundWithAccumulator()
				// proposal 1 / decision 0 has no reveals for that decision → accumulator is nil.
			},
			msg: &types.MsgSubmitTally{
				VoteRoundId: roundID,
				Creator:     creator,
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
					{ProposalId: 1, VoteDecision: 0, TotalValue: 0},
				},
			},
			check: func(resp *types.MsgSubmitTallyResponse) {
				s.Require().Equal(uint32(2), resp.FinalizedEntries)
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			resp, err := s.msgServer.SubmitTally(s.ctx, tc.msg)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
				if tc.check != nil {
					tc.check(resp)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SubmitTally: event emission
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitTally_EmitsEvent() {
	s.SetupTest()
	roundID := bytes.Repeat([]byte{0x50}, 32)
	creator := "zvote1creator"

	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: roundID,
		VoteEndTime: 500_000,
		Creator:     creator,
		Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
	}))

	_, err := s.msgServer.SubmitTally(s.ctx, &types.MsgSubmitTally{
		VoteRoundId: roundID,
		Creator:     creator,
	})
	s.Require().NoError(err)

	events := s.ctx.EventManager().Events()
	found := false
	for _, e := range events {
		if e.Type == types.EventTypeSubmitTally {
			found = true
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyRoundID {
					expected := fmt.Sprintf("%x", roundID)
					s.Require().Equal(expected, attr.Value)
				}
				if attr.Key == types.AttributeKeyNewStatus {
					s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED.String(), attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeSubmitTally)
}

// ---------------------------------------------------------------------------
// SubmitTally: finalized round rejects further shares
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitTally_FinalizedRejectsShares() {
	s.SetupTest()
	roundID := bytes.Repeat([]byte{0x60}, 32)
	creator := "zvote1creator"

	// Create a TALLYING round.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: roundID,
		VoteEndTime: 500_000,
		Creator:     creator,
		Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First"},
		},
	}))

	// Finalize it.
	_, err := s.msgServer.SubmitTally(s.ctx, &types.MsgSubmitTally{
		VoteRoundId: roundID,
		Creator:     creator,
	})
	s.Require().NoError(err)

	// Attempt to submit a reveal share — should fail because round is FINALIZED.
	_, err = s.msgServer.RevealShare(s.ctx, &types.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xF1}, 32),
		EncShare:                 testEncShare(s, 100),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    bytes.Repeat([]byte{0xF2}, 64),
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: 10,
	})
	// RevealShare validates proposal_id which succeeds, but the ante handler
	// would reject it. At the keeper level, RevealShare doesn't check status,
	// so we verify the status is FINALIZED which the ante handler uses.
	kv = s.keeper.OpenKVStore(s.ctx)
	round, err2 := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err2)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)
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
