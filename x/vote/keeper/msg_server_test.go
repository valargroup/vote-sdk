package keeper_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/vote-sdk/ffi/roundid"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
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
	s.keeper = keeper.NewKeeper(storeService, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// setupActiveRound creates a vote round in the store with an end time in the future and ACTIVE status.
func (s *MsgServerTestSuite) setupActiveRound(roundID []byte) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, svtest.ActiveRoundFixture(roundID)))
}

// setupRootAtHeight stores a commitment tree root at the given height for a round.
func (s *MsgServerTestSuite) setupRootAtHeight(roundID []byte, height uint64) {
	kv := s.keeper.OpenKVStore(s.ctx)
	root := bytes.Repeat([]byte{0xCC}, 32)
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(kv, roundID, height, root))
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
	return svtest.ValidCreateVotingSessionWithEndTime(time.Unix(2_000_000, 0))
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

var (
	testValAddr = svtest.TestValAddr
	testAccAddr = svtest.TestAccAddr
)

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

// seedAdmins installs an admin set with the given addresses in the KV store.
func (s *MsgServerTestSuite) seedAdmins(addrs ...string) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetAdmins(kv, &types.AdminSet{Addresses: addrs}))
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
// Mock bank keeper
// ---------------------------------------------------------------------------

// mockBankKeeper implements keeper.BankKeeper for tests.
type mockBankKeeper struct {
	balances  map[string]sdk.Coin // addr -> balance
	sendCalls []sendCall          // recorded SendCoins calls
}

type sendCall struct {
	From, To sdk.AccAddress
	Amt      sdk.Coins
}

func newMockBankKeeper() *mockBankKeeper {
	return &mockBankKeeper{balances: make(map[string]sdk.Coin)}
}

func (mk *mockBankKeeper) GetBalance(_ context.Context, addr sdk.AccAddress, denom string) sdk.Coin {
	if c, ok := mk.balances[addr.String()]; ok && c.Denom == denom {
		return c
	}
	return sdk.NewCoin(denom, sdkmath.ZeroInt())
}

func (mk *mockBankKeeper) SendCoins(_ context.Context, from, to sdk.AccAddress, amt sdk.Coins) error {
	mk.sendCalls = append(mk.sendCalls, sendCall{From: from, To: to, Amt: amt})
	return nil
}

// setupWithMockBankKeeper replaces the keeper's bank keeper with the given mock.
func (s *MsgServerTestSuite) setupWithMockBankKeeper(bk keeper.BankKeeper) {
	s.keeper.SetBankKeeper(bk)
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
				s.seedAdmins(svtest.DefaultAdminAddress)
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
				s.seedEligibleValidators(2)
				s.seedAdmins(svtest.DefaultAdminAddress)
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
				s.seedEligibleValidators(2)
				s.seedAdmins(svtest.DefaultAdminAddress)
			},
			msg: &types.MsgCreateVotingSession{
				Creator:           svtest.DefaultAdminAddress,
				SnapshotHeight:    999,
				SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
				ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
				VoteEndTime:       2_000_000,
				NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
				NcRoot:            bytes.Repeat([]byte{0x04}, 32),
				Proposals: []*types.Proposal{
					{Id: 1, Title: "Proposal A", Description: "First", Options: svtest.DefaultOptions()},
					{Id: 2, Title: "Proposal B", Description: "Second", Options: svtest.DefaultOptions()},
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
				s.seedAdmins(svtest.DefaultAdminAddress)
				s.setupWithMockStaking()
			},
			msg:         msg,
			expectErr:   true,
			errContains: "at least 1 validators",
		},
		{
			name: "rejected: 1 validator below min_ceremony_validators=2",
			setup: func() {
				s.seedEligibleValidators(1)
				s.seedAdmins(svtest.DefaultAdminAddress)
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetMinCeremonyValidators(kv, 2))
			},
			msg:         msg,
			expectErr:   true,
			errContains: "at least 2 validators",
		},
		{
			name: "happy path: 1 validator with default min_ceremony_validators=1",
			setup: func() {
				s.seedEligibleValidators(1)
				s.seedAdmins(svtest.DefaultAdminAddress)
			},
			msg: msg,
			checkResp: func(resp *types.MsgCreateVotingSessionResponse) {
				s.Require().Equal(expectedID, resp.VoteRoundId)

				kv := s.keeper.OpenKVStore(s.ctx)
				round, err := s.keeper.GetVoteRound(kv, expectedID)
				s.Require().NoError(err)
				s.Require().Len(round.CeremonyValidators, 1)
			},
		},
		{
			name: "happy path: exactly 2 validators",
			setup: func() {
				s.seedEligibleValidators(2)
				s.seedAdmins(svtest.DefaultAdminAddress)
			},
			msg: msg,
			checkResp: func(resp *types.MsgCreateVotingSessionResponse) {
				s.Require().Equal(expectedID, resp.VoteRoundId)

				kv := s.keeper.OpenKVStore(s.ctx)
				round, err := s.keeper.GetVoteRound(kv, expectedID)
				s.Require().NoError(err)
				s.Require().Len(round.CeremonyValidators, 2)
			},
		},
		{
			name: "rejected: another PENDING round already exists",
			setup: func() {
				s.seedEligibleValidators(2)
				s.seedAdmins(svtest.DefaultAdminAddress)
				// Create a different round first to put it in PENDING.
				_, err := s.msgServer.CreateVotingSession(s.ctx, &types.MsgCreateVotingSession{
					Creator:           svtest.DefaultAdminAddress,
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
	s.seedEligibleValidators(2)
	s.seedAdmins(svtest.DefaultAdminAddress)
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
	s.seedEligibleValidators(2)
	s.seedAdmins(svtest.DefaultAdminAddress)
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
				state, err := s.keeper.GetCommitmentTreeState(kv, roundID)
				s.Require().NoError(err)
				s.Require().Equal(uint64(1), state.NextIndex)

				// Verify the single leaf is van_cmx.
				leaf0, err := kv.Get(types.CommitmentLeafKey(roundID, 0))
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
				s.setupRootAtHeight(roundID, 10)
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

				state, err := s.keeper.GetCommitmentTreeState(kv, roundID)
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
				s.setupRootAtHeight(roundID, 10)
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
				s.setupRootAtHeight(roundID, 10)
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
// UpdateAdmins (any-of-N atomic replace; no balance transfer)
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestUpdateAdmins_AnyAdminCanUpdate() {
	adminA := testAccAddr(20)
	adminB := testAccAddr(21)
	replacement := testAccAddr(22)

	// Run once per member of the initial {A, B} set to prove any-of-N.
	for _, caller := range []string{adminA, adminB} {
		s.Run("caller="+caller, func() {
			s.SetupTest()
			s.seedAdmins(adminA, adminB)

			_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
				Creator:   caller,
				NewAdmins: []string{caller, replacement},
			})
			s.Require().NoError(err)

			kv := s.keeper.OpenKVStore(s.ctx)
			set, err := s.keeper.GetAdmins(kv)
			s.Require().NoError(err)
			s.Require().Equal([]string{caller, replacement}, set.Addresses)
		})
	}
}

func (s *MsgServerTestSuite) TestUpdateAdmins_NonAdminRejected() {
	s.SetupTest()

	adminA := testAccAddr(40)
	s.seedAdmins(adminA)

	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   testAccAddr(99), // not in the set
		NewAdmins: []string{testAccAddr(41)},
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestUpdateAdmins_ValidatorRejected() {
	s.SetupTest()
	// Register the same underlying 20-byte address as a bonded validator so
	// MsgUpdateAdmins's handler can't mistake "is a validator" for admin
	// authority. Creator is the account (sv1...) form of the same bytes.
	s.setupWithMockStaking(testValAddr(1))

	s.seedAdmins(testAccAddr(30))

	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   testAccAddr(1),
		NewAdmins: []string{testAccAddr(31)},
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestUpdateAdmins_NoAdminsRejected() {
	s.SetupTest()

	// No admin set installed — UpdateAdmins should fail with ErrNoAdmins.
	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   testAccAddr(1),
		NewAdmins: []string{testAccAddr(2)},
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "no admin set configured")
}

func (s *MsgServerTestSuite) TestUpdateAdmins_EmptySetRejected() {
	s.SetupTest()
	adminA := testAccAddr(10)
	s.seedAdmins(adminA)

	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: nil,
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "admin set must be non-empty")
}

func (s *MsgServerTestSuite) TestUpdateAdmins_DuplicatesRejected() {
	s.SetupTest()
	adminA := testAccAddr(10)
	s.seedAdmins(adminA)

	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: []string{adminA, adminA},
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "admin address appears more than once")
}

func (s *MsgServerTestSuite) TestUpdateAdmins_InvalidBech32Rejected() {
	s.SetupTest()
	adminA := testAccAddr(10)
	s.seedAdmins(adminA)

	// Non-bech32 string.
	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: []string{"not_a_valid_address"},
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not a valid bech32 address")

	// Validator operator address (valoper) — different bech32 HRP, rejected.
	_, err = s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: []string{testValAddr(2)},
	})
	s.Require().Error(err)
}

func (s *MsgServerTestSuite) TestUpdateAdmins_DoesNotTouchBalances() {
	s.SetupTest()
	adminA := testAccAddr(20)
	adminB := testAccAddr(21)

	bk := newMockBankKeeper()
	bk.balances[adminA] = sdk.NewCoin(sdk.DefaultBondDenom, sdkmath.NewInt(1_000_000_000))
	s.setupWithMockBankKeeper(bk)
	s.seedAdmins(adminA)

	// Replace {A} with {B}; A's balance must remain untouched because under
	// the per-admin balance model UpdateAdmins never moves funds.
	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: []string{adminB},
	})
	s.Require().NoError(err)

	s.Require().Empty(bk.sendCalls, "UpdateAdmins must not call SendCoins")
	s.Require().Equal(sdkmath.NewInt(1_000_000_000), bk.balances[adminA].Amount)
}

func (s *MsgServerTestSuite) TestUpdateAdmins_CreatorRemovingSelfAllowed() {
	s.SetupTest()
	adminA := testAccAddr(50)
	adminB := testAccAddr(51)
	s.seedAdmins(adminA, adminB)

	// adminA calls UpdateAdmins and removes themselves — allowed as long as
	// the new set is non-empty.
	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: []string{adminB},
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	set, err := s.keeper.GetAdmins(kv)
	s.Require().NoError(err)
	s.Require().Equal([]string{adminB}, set.Addresses)

	// A subsequent call by adminA must now fail (they're no longer in the set).
	_, err = s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: []string{adminA},
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestUpdateAdmins_EmitsEvent() {
	s.SetupTest()
	adminA := testAccAddr(60)
	adminB := testAccAddr(61)
	s.seedAdmins(adminA)

	_, err := s.msgServer.UpdateAdmins(s.ctx, &types.MsgUpdateAdmins{
		Creator:   adminA,
		NewAdmins: []string{adminA, adminB},
	})
	s.Require().NoError(err)

	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeUpdateAdmins {
			found = true
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyAdmins {
					s.Require().Contains(attr.Value, adminA)
					s.Require().Contains(attr.Value, adminB)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeUpdateAdmins)
}

// ---------------------------------------------------------------------------
// CreateVotingSession: admin gating tests
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestCreateVotingSession_RejectedWithNoAdmins() {
	s.SetupTest()
	s.seedEligibleValidators(1)

	msg := validSetupMsg()
	_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "no admin set configured")
}

func (s *MsgServerTestSuite) TestCreateVotingSession_RejectedWhenCreatorNotAdmin() {
	s.SetupTest()
	s.seedEligibleValidators(1)
	s.seedAdmins(testAccAddr(80))

	msg := validSetupMsg()
	msg.Creator = testAccAddr(81) // not in the admin set
	_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestCreateVotingSession_SucceedsForEachAdmin() {
	adminA := testAccAddr(90)
	adminB := testAccAddr(91)
	adminC := testAccAddr(92)

	for _, admin := range []string{adminA, adminB, adminC} {
		s.Run("admin="+admin, func() {
			s.SetupTest()
			s.seedEligibleValidators(2)
			s.seedAdmins(adminA, adminB, adminC)

			msg := validSetupMsg()
			msg.Creator = admin
			resp, err := s.msgServer.CreateVotingSession(s.ctx, msg)
			s.Require().NoError(err)
			s.Require().NotEmpty(resp.VoteRoundId)
		})
	}
}

func (s *MsgServerTestSuite) TestCreateVotingSession_DescriptionPersisted() {
	s.SetupTest()
	s.seedEligibleValidators(2)
	admin := testAccAddr(95)
	s.seedAdmins(admin)

	msg := validSetupMsg()
	msg.Creator = admin
	msg.Description = "Test round description"
	resp, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, resp.VoteRoundId)
	s.Require().NoError(err)
	s.Require().Equal("Test round description", round.Description)
}

