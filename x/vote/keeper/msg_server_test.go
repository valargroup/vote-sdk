package keeper_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/z-cale/zally/crypto/roundid"
	zallytest "github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

type MsgServerTestSuite struct {
	suite.Suite
	ctx       sdk.Context
	keeper    *keeper.Keeper
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
// Shared helpers
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
			{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
			{Id: 2, Title: "Proposal B", Description: "Second", Options: zallytest.DefaultOptions()},
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
	rid, err := roundid.DeriveRoundID(
		msg.SnapshotHeight,
		msg.SnapshotBlockhash,
		msg.ProposalsHash,
		msg.VoteEndTime,
		msg.NullifierImtRoot,
		msg.NcRoot,
	)
	if err != nil {
		panic(fmt.Sprintf("computeExpectedRoundID: %v", err))
	}
	return rid[:]
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
		VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
			{Id: 2, Title: "Proposal B", Description: "Second", Options: zallytest.DefaultOptions()},
		},
	}
}

// seedEligibleValidators registers Pallas keys for n validators and sets up
// a mock staking keeper that recognizes them as bonded. Returns the valoper addresses.
func (s *MsgServerTestSuite) seedEligibleValidators(n int) []string {
	addrs, _ := s.registerValidators(n)
	s.setupWithMockStaking(addrs...)
	return addrs
}

// ---------------------------------------------------------------------------
// Mock staking keeper
// ---------------------------------------------------------------------------

// testValAddr generates a deterministic valid bech32 validator address from a seed byte.
func testValAddr(seed byte) string {
	addr := make([]byte, 20)
	addr[0] = seed
	return sdk.ValAddress(addr).String()
}

// testAccAddr generates a deterministic valid bech32 account address from a seed byte.
func testAccAddr(seed byte) string {
	addr := make([]byte, 20)
	addr[0] = seed
	return sdk.AccAddress(addr).String()
}

// mockStakingKeeper implements keeper.StakingKeeper for tests.
// validators maps bech32 operator address -> validator.
type mockStakingKeeper struct {
	validators       map[string]stakingtypes.Validator
	proposerOperator string // operator address returned by GetValidatorByConsAddr
}

func newMockStakingKeeper(valAddrs ...string) *mockStakingKeeper {
	mk := &mockStakingKeeper{validators: make(map[string]stakingtypes.Validator)}
	for _, addr := range valAddrs {
		mk.validators[addr] = stakingtypes.Validator{
			OperatorAddress: addr,
			Status:          stakingtypes.Bonded,
		}
	}
	return mk
}

func (mk *mockStakingKeeper) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	v, ok := mk.validators[addr.String()]
	if !ok {
		return stakingtypes.Validator{}, fmt.Errorf("validator %s not found", addr)
	}
	return v, nil
}

func (mk *mockStakingKeeper) GetValidatorByConsAddr(_ context.Context, _ sdk.ConsAddress) (stakingtypes.Validator, error) {
	if mk.proposerOperator == "" {
		return stakingtypes.Validator{}, fmt.Errorf("proposer not configured in mock")
	}
	return stakingtypes.Validator{OperatorAddress: mk.proposerOperator}, nil
}

func (mk *mockStakingKeeper) Jail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

func (mk *mockStakingKeeper) Unjail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

// setupWithMockStaking replaces the keeper's staking keeper with a mock that
// recognizes the given addresses as validators.
func (s *MsgServerTestSuite) setupWithMockStaking(valAddrs ...string) {
	s.setupWithMockStakingKeeper(newMockStakingKeeper(valAddrs...))
}

// seedVoteManager sets the vote manager address in the KV store for tests.
func (s *MsgServerTestSuite) seedVoteManager(addr string) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: addr}))
}

// setBlockProposer configures the mock staking keeper so that
// ValidateProposerIsCreator sees creator as the block proposer.
func (s *MsgServerTestSuite) setBlockProposer(creator string) {
	mk := newMockStakingKeeper()
	mk.proposerOperator = creator
	s.setupWithMockStakingKeeper(mk)
}

// setupWithMockStakingKeeper replaces the keeper's staking keeper with the
// given mock and rebuilds the msgServer so it uses the updated keeper.
func (s *MsgServerTestSuite) setupWithMockStakingKeeper(sk keeper.StakingKeeper) {
	s.keeper.SetStakingKeeper(sk)
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)
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
			name: "happy path: round created with PENDING status and validator snapshot",
			setup: func() {
				s.seedEligibleValidators(3)
				s.seedVoteManager("zvote1admin")
			},
			msg: msg,
			checkResp: func(resp *types.MsgCreateVotingSessionResponse) {
				s.Require().Equal(expectedID, resp.VoteRoundId)

				// Verify round is stored with correct fields.
				kv := s.keeper.OpenKVStore(s.ctx)
				round, err := s.keeper.GetVoteRound(kv, expectedID)
				s.Require().NoError(err)
				s.Require().Equal(msg.Creator, round.Creator)
				s.Require().Equal(msg.SnapshotHeight, round.SnapshotHeight)
				s.Require().Equal(msg.VoteEndTime, round.VoteEndTime)
				s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
				s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus)

				// EaPk left empty until ceremony confirms.
				s.Require().Empty(round.EaPk)

				// Ceremony validators snapshotted.
				s.Require().Len(round.CeremonyValidators, 3)

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
				s.seedEligibleValidators(1)
				s.seedVoteManager("zvote1admin")
				_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
				s.Require().NoError(err)
			},
			msg:         msg,
			expectErr:   true,
			errContains: "vote round already exists",
		},
		{
			name: "different fields produce different round ID",
			setup: func() {
				s.seedEligibleValidators(1)
				s.seedVoteManager("zvote1admin")
			},
			msg: &types.MsgCreateVotingSession{
				Creator:           "zvote1admin",
				SnapshotHeight:    999,
				SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
				ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
				VoteEndTime:       2_000_000,
				NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
				NcRoot:            bytes.Repeat([]byte{0x04}, 32),
				VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
				VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
				VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
				Proposals: []*types.Proposal{
					{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
					{Id: 2, Title: "Proposal B", Description: "Second", Options: zallytest.DefaultOptions()},
				},
			},
			checkResp: func(resp *types.MsgCreateVotingSessionResponse) {
				s.Require().NotEqual(expectedID, resp.VoteRoundId)
				s.Require().Len(resp.VoteRoundId, 32)
			},
		},
		{
			name: "rejected: no validators have registered Pallas keys",
			setup: func() {
				s.seedVoteManager("zvote1admin")
				// Mock staking with no validators.
				s.setupWithMockStaking()
			},
			msg:         msg,
			expectErr:   true,
			errContains: "no validators have registered Pallas keys",
		},
		{
			name: "rejected: another PENDING round already exists",
			setup: func() {
				s.seedEligibleValidators(1)
				s.seedVoteManager("zvote1admin")
				// Create a different round first to put it in PENDING.
				_, err := s.msgServer.CreateVotingSession(s.ctx, &types.MsgCreateVotingSession{
					Creator:           "zvote1admin",
					SnapshotHeight:    999,
					SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
					ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
					VoteEndTime:       2_000_000,
					NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
					NcRoot:            bytes.Repeat([]byte{0x04}, 32),
				})
				s.Require().NoError(err)
			},
			msg:         msg,
			expectErr:   true,
			errContains: "another round ceremony is already in progress",
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

func (s *MsgServerTestSuite) TestCreateVotingSession_DeterministicID() {
	s.SetupTest()
	s.seedEligibleValidators(1)
	s.seedVoteManager("zvote1admin")
	msg := validSetupMsg()

	resp1, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)

	// Same inputs must produce same ID.
	expected := computeExpectedRoundID(msg)
	s.Require().Equal(expected, resp1.VoteRoundId)
	s.Require().Len(resp1.VoteRoundId, 32)
}

func (s *MsgServerTestSuite) TestCreateVotingSession_EmitsEvent() {
	s.SetupTest()
	s.seedEligibleValidators(1)
	s.seedVoteManager("zvote1admin")
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
			},
			msg: &types.MsgCastVote{
				VanNullifier:             bytes.Repeat([]byte{0xE1}, 32),
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
// SetVoteManager
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSetVoteManager_Bootstrap() {
	// First call when no vote manager exists — any validator can set it.
	s.SetupTest()
	val1 := testValAddr(1)
	mgr1 := testAccAddr(10)
	s.setupWithMockStaking(val1)

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: mgr1,
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Equal(mgr1, mgr.Address)
}

func (s *MsgServerTestSuite) TestSetVoteManager_VoteManagerCanChange() {
	s.SetupTest()
	s.setupWithMockStaking()

	currentMgr := testAccAddr(20)
	newMgr := testAccAddr(21)

	// Seed a vote manager.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: currentMgr}))

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    currentMgr,
		NewManager: newMgr,
	})
	s.Require().NoError(err)

	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Equal(newMgr, mgr.Address)
}

func (s *MsgServerTestSuite) TestSetVoteManager_ValidatorCanChange() {
	s.SetupTest()
	val1 := testValAddr(1)
	currentMgr := testAccAddr(30)
	newMgr := testAccAddr(31)
	s.setupWithMockStaking(val1)

	// Seed a vote manager that is NOT the validator.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: currentMgr}))

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: newMgr,
	})
	s.Require().NoError(err)

	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Equal(newMgr, mgr.Address)
}

func (s *MsgServerTestSuite) TestSetVoteManager_NonValidatorNonManagerRejected() {
	s.SetupTest()
	s.setupWithMockStaking() // no validators in the mock

	currentMgr := testAccAddr(40)
	newMgr := testAccAddr(41)

	// Seed a vote manager.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: currentMgr}))

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    "random_address",
		NewManager: newMgr,
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestSetVoteManager_EmptyNewManagerRejected() {
	s.SetupTest()
	val1 := testValAddr(1)
	s.setupWithMockStaking(val1)

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: "",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "new_manager cannot be empty")
}

func (s *MsgServerTestSuite) TestSetVoteManager_BootstrapNonValidatorRejected() {
	// No vote manager set, non-validator tries to set one.
	s.SetupTest()
	s.setupWithMockStaking() // no validators

	newMgr := testAccAddr(50)

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    "random_address",
		NewManager: newMgr,
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestSetVoteManager_InvalidAddressRejected() {
	s.SetupTest()
	val1 := testValAddr(1)
	s.setupWithMockStaking(val1)

	// Reject non-bech32 string.
	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: "not_a_valid_address",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not a valid account address")

	// Reject validator operator address (valoper).
	_, err = s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: testValAddr(2),
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not a valid account address")
}

func (s *MsgServerTestSuite) TestSetVoteManager_EmitsEvent() {
	s.SetupTest()
	val1 := testValAddr(1)
	mgr1 := testAccAddr(60)
	s.setupWithMockStaking(val1)

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: mgr1,
	})
	s.Require().NoError(err)

	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeSetVoteManager {
			found = true
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyVoteManager {
					s.Require().Equal(mgr1, attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeSetVoteManager)
}

// ---------------------------------------------------------------------------
// CreateVotingSession: VoteManager gating tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestCreateVotingSession_RejectedWithNoVoteManager() {
	s.SetupTest()
	s.seedEligibleValidators(1)

	msg := validSetupMsg()
	_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "no vote manager set")
}

func (s *MsgServerTestSuite) TestCreateVotingSession_RejectedWhenCreatorNotVoteManager() {
	s.SetupTest()
	s.seedEligibleValidators(1)
	s.seedVoteManager("the_real_manager")

	msg := validSetupMsg()
	msg.Creator = "not_the_manager"
	_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestCreateVotingSession_SucceedsWithVoteManager() {
	s.SetupTest()
	s.seedEligibleValidators(1)
	s.seedVoteManager("zvote1admin")

	msg := validSetupMsg()
	msg.Creator = "zvote1admin"
	resp, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)
	s.Require().NotEmpty(resp.VoteRoundId)
}

func (s *MsgServerTestSuite) TestCreateVotingSession_DescriptionPersisted() {
	s.SetupTest()
	s.seedEligibleValidators(1)
	s.seedVoteManager("zvote1admin")

	msg := validSetupMsg()
	msg.Creator = "zvote1admin"
	msg.Description = "Test round description"
	resp, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, resp.VoteRoundId)
	s.Require().NoError(err)
	s.Require().Equal("Test round description", round.Description)
}

// ---------------------------------------------------------------------------
// VoteManager CRUD tests (on KeeperTestSuite)
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestVoteManager_ReturnsNilWhenEmpty() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	state, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Nil(state, "should return nil when no vote manager exists")
}

func (s *KeeperTestSuite) TestVoteManager_RoundTrip() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: "zvote1manager"}))

	got, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().NotNil(got)
	s.Require().Equal("zvote1manager", got.Address)
}

func (s *KeeperTestSuite) TestVoteManager_Overwrite() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: "first"}))
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: "second"}))

	got, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Equal("second", got.Address)
}

// ---------------------------------------------------------------------------
// Genesis: VoteManager restoration (on KeeperTestSuite)
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestGenesis_VoteManagerRestored() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	genesis := &types.GenesisState{
		VoteManager: "zvote1genesis_manager",
	}

	s.Require().NoError(s.keeper.InitGenesis(kv, genesis))

	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().NotNil(mgr)
	s.Require().Equal("zvote1genesis_manager", mgr.Address)
}

func (s *KeeperTestSuite) TestGenesis_EmptyVoteManagerNotSet() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	genesis := &types.GenesisState{
		VoteManager: "",
	}

	s.Require().NoError(s.keeper.InitGenesis(kv, genesis))

	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Nil(mgr)
}
