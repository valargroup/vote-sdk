package ante_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp"
	zallytest "github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/ante"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// testProposerConsAddr is the consensus address embedded in the test block header.
// The mock staking keeper maps this to testValidatorAddr().
var testProposerConsAddr = bytes.Repeat([]byte{0xAA}, 20)

// mockStakingKeeper is a test double for the keeper.StakingKeeper interface.
// It always returns a bonded validator for any address. GetValidatorByConsAddr
// maps testProposerConsAddr to testValidatorAddr() for proposer checks.
type mockStakingKeeper struct{}

func (mockStakingKeeper) GetValidator(_ context.Context, _ sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{Status: stakingtypes.Bonded}, nil
}

func (mockStakingKeeper) GetValidatorByConsAddr(_ context.Context, _ sdk.ConsAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{
		Status:          stakingtypes.Bonded,
		OperatorAddress: testValidatorAddr(),
	}, nil
}

func (mockStakingKeeper) Jail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

func (mockStakingKeeper) Unjail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

// errStakingKeeper always returns ErrNoValidatorFound.
type errStakingKeeper struct{}

func (errStakingKeeper) GetValidator(_ context.Context, _ sdk.ValAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
}

func (errStakingKeeper) GetValidatorByConsAddr(_ context.Context, _ sdk.ConsAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound
}

func (errStakingKeeper) Jail(_ context.Context, _ sdk.ConsAddress) error {
	return stakingtypes.ErrNoValidatorFound
}

func (errStakingKeeper) Unjail(_ context.Context, _ sdk.ConsAddress) error {
	return stakingtypes.ErrNoValidatorFound
}

// ---------------------------------------------------------------------------
// Failing mock verifiers (for negative test cases)
// ---------------------------------------------------------------------------

// errSigVerifier always fails RedPallas signature verification.
type errSigVerifier struct{}

func (errSigVerifier) Verify(_, _, _ []byte) error {
	return fmt.Errorf("mock: RedPallas signature verification failed")
}

// errZKPVerifier always fails ZKP verification for all proof types.
type errZKPVerifier struct{}

func (errZKPVerifier) VerifyDelegation(_ []byte, _ zkp.DelegationInputs) error {
	return fmt.Errorf("mock: delegation proof verification failed")
}

func (errZKPVerifier) VerifyVoteCommitment(_ []byte, _ zkp.VoteCommitmentInputs) error {
	return fmt.Errorf("mock: vote commitment proof verification failed")
}

func (errZKPVerifier) VerifyVoteShare(_ []byte, _ zkp.VoteShareInputs) error {
	return fmt.Errorf("mock: vote share proof verification failed")
}

// spyZKPVerifier captures the DelegationInputs passed to VerifyDelegation.
type spyZKPVerifier struct {
	capturedDelegationInputs *zkp.DelegationInputs
}

func (s *spyZKPVerifier) VerifyDelegation(_ []byte, inputs zkp.DelegationInputs) error {
	s.capturedDelegationInputs = &inputs
	return nil
}

func (s *spyZKPVerifier) VerifyVoteCommitment(_ []byte, _ zkp.VoteCommitmentInputs) error {
	return nil
}

func (s *spyZKPVerifier) VerifyVoteShare(_ []byte, _ zkp.VoteShareInputs) error {
	return nil
}

// ---------------------------------------------------------------------------
// Test constants and message constructors
// ---------------------------------------------------------------------------

var (
	testBlockTime  = time.Unix(1_000_000, 0).UTC()
	activeEndTime  = uint64(2_000_000) // well in the future relative to testBlockTime
	expiredEndTime = uint64(999_999)   // in the past relative to testBlockTime
	testRoundID    = bytes.Repeat([]byte{0x01}, 32)
)

func newValidMsgCreateVotingSession() *types.MsgCreateVotingSession {
	return &types.MsgCreateVotingSession{
		Creator:           "zvote1testcreator",
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

func newValidMsgDelegateVote() *types.MsgDelegateVote {
	msg := &types.MsgDelegateVote{
		Rk:                  bytes.Repeat([]byte{0xAA}, 32),
		SpendAuthSig:        bytes.Repeat([]byte{0xBB}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0xCC}, 32),
		CmxNew:              bytes.Repeat([]byte{0xDD}, 32),
		EncMemo:             bytes.Repeat([]byte{0xEE}, 64),
		VanCmx:              bytes.Repeat([]byte{0xFF}, 32),
		GovNullifiers: [][]byte{
			bytes.Repeat([]byte{0x11}, 32),
			bytes.Repeat([]byte{0x12}, 32),
		},
		Proof:       bytes.Repeat([]byte{0x22}, 192),
		VoteRoundId: testRoundID,
		Sighash:     bytes.Repeat([]byte{0x99}, 32), // any 32 bytes; chain only checks length + sig
	}
	return msg
}

func newValidMsgCastVote() *types.MsgCastVote {
	msg := &types.MsgCastVote{
		VanNullifier:             bytes.Repeat([]byte{0x33}, 32),
		RVpkX:                    bytes.Repeat([]byte{0x3a}, 32),
		RVpkY:                    bytes.Repeat([]byte{0x3b}, 32),
		VoteAuthorityNoteNew:     bytes.Repeat([]byte{0x44}, 32),
		VoteCommitment:           bytes.Repeat([]byte{0x55}, 32),
		ProposalId:               1,
		Proof:                    bytes.Repeat([]byte{0x66}, 192),
		VoteRoundId:              testRoundID,
		VoteCommTreeAnchorHeight: 10,
		VoteAuthSig:              bytes.Repeat([]byte{0xBB}, 64),
		RVpk:                     bytes.Repeat([]byte{0x3c}, 32),
		Sighash:                  make([]byte, 32), // overwritten below
	}
	msg.Sighash = types.ComputeCastVoteSighash(msg)
	return msg
}

func newValidMsgRevealShare() *types.MsgRevealShare {
	return &types.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0x77}, 32),
		EncShare:                 bytes.Repeat([]byte{0x88}, 64),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    bytes.Repeat([]byte{0x88}, 192),
		VoteRoundId:              testRoundID,
		VoteCommTreeAnchorHeight: 10,
	}
}

// ---------------------------------------------------------------------------
// Opts constructors
// ---------------------------------------------------------------------------

func mockOpts() ante.ValidateOpts {
	return ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: zkp.NewMockVerifier(),
	}
}

func recheckOpts() ante.ValidateOpts {
	// RecheckTx with failing verifiers — crypto checks should be skipped.
	return ante.ValidateOpts{
		IsRecheck:   true,
		SigVerifier: errSigVerifier{},
		ZKPVerifier: errZKPVerifier{},
	}
}

func failSigOpts() ante.ValidateOpts {
	return ante.ValidateOpts{
		SigVerifier: errSigVerifier{},
		ZKPVerifier: zkp.NewMockVerifier(),
	}
}

func failZKPOpts() ante.ValidateOpts {
	return ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: errZKPVerifier{},
	}
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

// ValidateTestSuite provides an in-memory KV store, keeper, and SDK context
// for testing the ante validation pipeline.
//
// Modelled after the Osmosis concentrated-liquidity test pattern:
// each test case calls SetupTest() for a clean slate.
type ValidateTestSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper *keeper.Keeper
}

func TestValidateTestSuite(t *testing.T) {
	suite.Run(t, new(ValidateTestSuite))
}

// SetupTest creates a fresh in-memory KV store, vote keeper, and SDK context
// with a deterministic block time before each test case.
func (s *ValidateTestSuite) SetupTest() {
	// Configure bech32 prefixes for validator address parsing.
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForValidator("zvotevaloper", "zvotevaloperpub")
	cfg.SetBech32PrefixForAccount("zvote", "zvotepub")

	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.WithBlockTime(testBlockTime).WithBlockHeader(cmtproto.Header{
		Time:            testBlockTime,
		ProposerAddress: testProposerConsAddr,
	})
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), mockStakingKeeper{})
}

// ---------------------------------------------------------------------------
// Suite helpers
// ---------------------------------------------------------------------------

// setupActiveRound stores a vote round that is still active (endTime > block time)
// and a commitment tree root at height 10 so CastVote/RevealShare validation can resolve the anchor.
func (s *ValidateTestSuite) setupActiveRound() {
	s.setupRound(testRoundID, activeEndTime)
	s.setupCommitmentRootAtHeight(10)
}

// setupExpiredRound stores a vote round that has already expired.
func (s *ValidateTestSuite) setupExpiredRound() {
	s.setupRound(testRoundID, expiredEndTime)
}

// setupRound stores a VoteRound with the given ID, end time, and ACTIVE status.
func (s *ValidateTestSuite) setupRound(roundID []byte, endTime uint64) {
	s.setupRoundWithStatus(roundID, endTime, types.SessionStatus_SESSION_STATUS_ACTIVE)
}

// setupRoundWithStatus stores a VoteRound with explicit status.
func (s *ValidateTestSuite) setupRoundWithStatus(roundID []byte, endTime uint64, status types.SessionStatus) {
	kvStore := s.keeper.OpenKVStore(s.ctx)
	round := &types.VoteRound{
		VoteRoundId:       roundID,
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
		VoteEndTime:       endTime,
		NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
		NcRoot:            bytes.Repeat([]byte{0x04}, 32),
		Creator:           "zvote1testcreator",
		Status:            status,
		EaPk:              bytes.Repeat([]byte{0x05}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
			{Id: 2, Title: "Proposal B", Description: "Second", Options: zallytest.DefaultOptions()},
		},
	}
	err := s.keeper.SetVoteRound(kvStore, round)
	s.Require().NoError(err)
}

// setupTallyingRound stores a VoteRound in TALLYING status with an expired end time.
func (s *ValidateTestSuite) setupTallyingRound() {
	s.setupRoundWithStatus(testRoundID, expiredEndTime, types.SessionStatus_SESSION_STATUS_TALLYING)
}

// setupCommitmentRootAtHeight stores a commitment tree root at the given height
// so that CastVote/RevealShare messages with that anchor height pass the anchor check.
func (s *ValidateTestSuite) setupCommitmentRootAtHeight(height uint64) {
	kvStore := s.keeper.OpenKVStore(s.ctx)
	root := bytes.Repeat([]byte{0xCC}, 32)
	err := s.keeper.SetCommitmentRootAtHeight(kvStore, height, root)
	s.Require().NoError(err)
}

// recordNullifier marks a nullifier as already spent in the KV store,
// using the given type and round scoping.
func (s *ValidateTestSuite) recordNullifier(nfType types.NullifierType, roundID, nullifier []byte) {
	kvStore := s.keeper.OpenKVStore(s.ctx)
	err := s.keeper.SetNullifier(kvStore, nfType, roundID, nullifier)
	s.Require().NoError(err)
}

// seedCommitmentRoot stores a dummy commitment tree root at the given height.
// MsgRevealShare tests need this because verifyRevealShare looks up the root.
func (s *ValidateTestSuite) seedCommitmentRoot(height uint64) {
	kvStore := s.keeper.OpenKVStore(s.ctx)
	root := bytes.Repeat([]byte{0xAB}, 32)
	err := s.keeper.SetCommitmentRootAtHeight(kvStore, height, root)
	s.Require().NoError(err)
}

// ---------------------------------------------------------------------------
// Tests: MsgCreateVotingSession
// ---------------------------------------------------------------------------

func (s *ValidateTestSuite) TestValidateVoteTx_CreateVotingSession() {
	tests := []struct {
		name        string
		msg         func() types.VoteMessage
		opts        ante.ValidateOpts
		setup       func()
		expectErr   bool
		errContains string
	}{
		{
			name: "valid create voting session passes basic validation",
			msg:  func() types.VoteMessage { return newValidMsgCreateVotingSession() },
			opts: mockOpts(),
		},
		{
			name: "valid create voting session on recheck also passes",
			msg:  func() types.VoteMessage { return newValidMsgCreateVotingSession() },
			opts: recheckOpts(),
		},
		{
			name: "invalid: empty creator",
			msg: func() types.VoteMessage {
				m := newValidMsgCreateVotingSession()
				m.Creator = ""
				return m
			},
			opts:        mockOpts(),
			expectErr:   true,
			errContains: "creator",
		},
		{
			name: "invalid: zero snapshot_height",
			msg: func() types.VoteMessage {
				m := newValidMsgCreateVotingSession()
				m.SnapshotHeight = 0
				return m
			},
			opts:        mockOpts(),
			expectErr:   true,
			errContains: "snapshot_height",
		},
		{
			name: "invalid: empty proposals_hash",
			msg: func() types.VoteMessage {
				m := newValidMsgCreateVotingSession()
				m.ProposalsHash = nil
				return m
			},
			opts:        mockOpts(),
			expectErr:   true,
			errContains: "proposals_hash",
		},
		{
			name: "invalid: zero vote_end_time",
			msg: func() types.VoteMessage {
				m := newValidMsgCreateVotingSession()
				m.VoteEndTime = 0
				return m
			},
			opts:        mockOpts(),
			expectErr:   true,
			errContains: "vote_end_time",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := ante.ValidateVoteTx(s.ctx, tc.msg(), s.keeper, tc.opts)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: MsgDelegateVote
// ---------------------------------------------------------------------------

func (s *ValidateTestSuite) TestValidateVoteTx_DelegateVote() {
	tests := []struct {
		name        string
		msg         func() types.VoteMessage
		opts        ante.ValidateOpts
		setup       func()
		expectErr   bool
		errContains string
	}{
		{
			name:  "valid delegation with active round and mock verifiers",
			msg:   func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:  mockOpts(),
			setup: func() { s.setupActiveRound() },
		},
		{
			name: "valid: non-canonical 32-byte sighash accepted when signature verifies",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.Sighash = bytes.Repeat([]byte{0x99}, 32)
				return m
			},
			opts:  mockOpts(),
			setup: func() { s.setupActiveRound() },
		},
		// --- ValidateBasic failures ---
		{
			name: "invalid: rk wrong length (not 32 bytes)",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.Rk = bytes.Repeat([]byte{0xAA}, 16) // 16 instead of 32
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "rk must be 32 bytes",
		},
		{
			name: "invalid: empty spend_auth_sig",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.SpendAuthSig = nil
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "spend_auth_sig",
		},
		{
			name: "invalid: empty gov_nullifiers",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.GovNullifiers = nil
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "gov_nullifiers",
		},
		{
			name: "invalid: too many gov_nullifiers (>5)",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.GovNullifiers = [][]byte{
					bytes.Repeat([]byte{0x01}, 32),
					bytes.Repeat([]byte{0x02}, 32),
					bytes.Repeat([]byte{0x03}, 32),
					bytes.Repeat([]byte{0x04}, 32),
					bytes.Repeat([]byte{0x05}, 32),
					bytes.Repeat([]byte{0x06}, 32), // 6th one
				}
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "gov_nullifiers cannot exceed 5",
		},
		{
			name: "invalid: empty proof",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.Proof = nil
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "proof",
		},
		{
			name: "invalid: sighash wrong length",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.Sighash = bytes.Repeat([]byte{0x99}, 31) // 31 bytes, must be 32
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "sighash must be 32 bytes",
		},
		// --- Round state failures ---
		{
			name:        "round not found",
			msg:         func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:        mockOpts(),
			setup:       func() { /* no round created */ },
			expectErr:   true,
			errContains: "vote round not found",
		},
		{
			name:        "round expired",
			msg:         func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:        mockOpts(),
			setup:       func() { s.setupExpiredRound() },
			expectErr:   true,
			errContains: "vote round is not active",
		},
		{
			name:        "tallying round rejected for delegation",
			msg:         func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:        mockOpts(),
			setup:       func() { s.setupTallyingRound() },
			expectErr:   true,
			errContains: "vote round is not active",
		},
		// --- Nullifier uniqueness failures ---
		{
			name: "duplicate gov nullifier (first of two)",
			msg:  func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts: mockOpts(),
			setup: func() {
				s.setupActiveRound()
				// Record the first gov nullifier as already spent.
				s.recordNullifier(types.NullifierTypeGov, testRoundID, bytes.Repeat([]byte{0x11}, 32))
			},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
		{
			name: "duplicate gov nullifier (second of two)",
			msg:  func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts: mockOpts(),
			setup: func() {
				s.setupActiveRound()
				// Record the second gov nullifier as already spent.
				s.recordNullifier(types.NullifierTypeGov, testRoundID, bytes.Repeat([]byte{0x12}, 32))
			},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
		// --- Signature verification failure ---
		{
			name:        "signature verification fails",
			msg:         func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:        failSigOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "invalid RedPallas signature",
		},
		// --- ZKP verification failure ---
		{
			name:        "ZKP delegation proof fails",
			msg:         func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:        failZKPOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "invalid zero-knowledge proof",
		},
		// --- RecheckTx behavior ---
		{
			name:  "recheck: skips sig and ZKP, passes with active round and fresh nullifiers",
			msg:   func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:  recheckOpts(),
			setup: func() { s.setupActiveRound() },
		},
		{
			name: "recheck: still catches duplicate nullifier",
			msg:  func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts: recheckOpts(),
			setup: func() {
				s.setupActiveRound()
				s.recordNullifier(types.NullifierTypeGov, testRoundID, bytes.Repeat([]byte{0x11}, 32))
			},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
		{
			name:        "recheck: still catches expired round",
			msg:         func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts:        recheckOpts(),
			setup:       func() { s.setupExpiredRound() },
			expectErr:   true,
			errContains: "vote round is not active",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := ante.ValidateVoteTx(s.ctx, tc.msg(), s.keeper, tc.opts)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: MsgCastVote
// ---------------------------------------------------------------------------

func (s *ValidateTestSuite) TestValidateVoteTx_CastVote() {
	tests := []struct {
		name        string
		msg         func() types.VoteMessage
		opts        ante.ValidateOpts
		setup       func()
		expectErr   bool
		errContains string
	}{
		{
			name:  "valid cast vote with active round and mock verifiers",
			msg:   func() types.VoteMessage { return newValidMsgCastVote() },
			opts:  mockOpts(),
			setup: func() { s.setupActiveRound() },
		},
		// --- ValidateBasic failures ---
		{
			name: "invalid: empty van_nullifier",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.VanNullifier = nil
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "van_nullifier",
		},
		{
			name: "invalid: empty vote_commitment",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.VoteCommitment = nil
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "vote_commitment",
		},
		{
			name: "invalid: zero anchor height",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.VoteCommTreeAnchorHeight = 0
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "vote_comm_tree_anchor_height",
		},
		{
			name: "invalid: empty vote_auth_sig",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.VoteAuthSig = nil
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "vote_auth_sig",
		},
		{
			name: "invalid: sighash wrong length",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.Sighash = bytes.Repeat([]byte{0x01}, 16) // 16 instead of 32
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "sighash must be 32 bytes",
		},
		{
			name: "invalid: r_vpk wrong length",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.RVpk = bytes.Repeat([]byte{0x01}, 16) // 16 instead of 32
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "r_vpk must be 32 bytes",
		},
		{
			name: "invalid: sighash does not match message",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.Sighash = bytes.Repeat([]byte{0x99}, 32) // wrong; must equal ComputeCastVoteSighash(m)
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "sighash does not match",
		},
		// --- Signature verification failure ---
		{
			name:        "signature verification fails for cast vote",
			msg:         func() types.VoteMessage { return newValidMsgCastVote() },
			opts:        failSigOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "invalid RedPallas signature",
		},
		// --- Round state failures ---
		{
			name:        "round not found",
			msg:         func() types.VoteMessage { return newValidMsgCastVote() },
			opts:        mockOpts(),
			setup:       func() { /* no round */ },
			expectErr:   true,
			errContains: "vote round not found",
		},
		{
			name:        "round expired",
			msg:         func() types.VoteMessage { return newValidMsgCastVote() },
			opts:        mockOpts(),
			setup:       func() { s.setupExpiredRound() },
			expectErr:   true,
			errContains: "vote round is not active",
		},
		{
			name:        "tallying round rejected for cast vote",
			msg:         func() types.VoteMessage { return newValidMsgCastVote() },
			opts:        mockOpts(),
			setup:       func() { s.setupTallyingRound() },
			expectErr:   true,
			errContains: "vote round is not active",
		},
		// --- Nullifier uniqueness failure ---
		{
			name: "duplicate van nullifier",
			msg:  func() types.VoteMessage { return newValidMsgCastVote() },
			opts: mockOpts(),
			setup: func() {
				s.setupActiveRound()
				s.recordNullifier(types.NullifierTypeVoteAuthorityNote, testRoundID, bytes.Repeat([]byte{0x33}, 32))
			},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
		// --- ZKP verification failure ---
		{
			name:        "ZKP cast vote proof fails",
			msg:         func() types.VoteMessage { return newValidMsgCastVote() },
			opts:        failZKPOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "invalid zero-knowledge proof",
		},
		// --- RecheckTx behavior ---
		{
			name:  "recheck: skips sig and ZKP, passes with active round and fresh nullifier",
			msg:   func() types.VoteMessage { return newValidMsgCastVote() },
			opts:  recheckOpts(),
			setup: func() { s.setupActiveRound() },
		},
		{
			name: "recheck: still catches duplicate van nullifier",
			msg:  func() types.VoteMessage { return newValidMsgCastVote() },
			opts: recheckOpts(),
			setup: func() {
				s.setupActiveRound()
				s.recordNullifier(types.NullifierTypeVoteAuthorityNote, testRoundID, bytes.Repeat([]byte{0x33}, 32))
			},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := ante.ValidateVoteTx(s.ctx, tc.msg(), s.keeper, tc.opts)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: MsgRevealShare
// ---------------------------------------------------------------------------

func (s *ValidateTestSuite) TestValidateVoteTx_RevealShare() {
	tests := []struct {
		name        string
		msg         func() types.VoteMessage
		opts        ante.ValidateOpts
		setup       func()
		expectErr   bool
		errContains string
	}{
		{
			name: "valid reveal share with active round and mock verifiers",
			msg:  func() types.VoteMessage { return newValidMsgRevealShare() },
			opts: mockOpts(),
			setup: func() {
				s.setupActiveRound()
				s.seedCommitmentRoot(10) // anchor height used by newValidMsgRevealShare
			},
		},
		// --- ValidateBasic failures ---
		{
			name: "invalid: empty share_nullifier",
			msg: func() types.VoteMessage {
				m := newValidMsgRevealShare()
				m.ShareNullifier = nil
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "share_nullifier",
		},
		{
			name: "invalid: wrong enc_share length",
			msg: func() types.VoteMessage {
				m := newValidMsgRevealShare()
				m.EncShare = bytes.Repeat([]byte{0x88}, 32) // 32 instead of 64
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "enc_share must be 64 bytes",
		},
		{
			name: "invalid: zero anchor height",
			msg: func() types.VoteMessage {
				m := newValidMsgRevealShare()
				m.VoteCommTreeAnchorHeight = 0
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "vote_comm_tree_anchor_height",
		},
		// --- Round state failures ---
		{
			name:        "round not found",
			msg:         func() types.VoteMessage { return newValidMsgRevealShare() },
			opts:        mockOpts(),
			setup:       func() { /* no round */ },
			expectErr:   true,
			errContains: "vote round not found",
		},
		{
			name: "expired ACTIVE round accepted for shares",
			msg:  func() types.VoteMessage { return newValidMsgRevealShare() },
			opts: mockOpts(),
			setup: func() {
				s.setupExpiredRound()
				s.seedCommitmentRoot(10) // anchor height used by newValidMsgRevealShare
			},
		},
		{
			name: "tallying round accepted for shares",
			msg:  func() types.VoteMessage { return newValidMsgRevealShare() },
			opts: mockOpts(),
			setup: func() {
				s.setupTallyingRound()
				s.seedCommitmentRoot(10) // anchor height used by newValidMsgRevealShare
			},
		},
		{
			name: "finalized round rejected for shares",
			msg:  func() types.VoteMessage { return newValidMsgRevealShare() },
			opts: mockOpts(),
			setup: func() {
				s.setupRoundWithStatus(testRoundID, expiredEndTime, types.SessionStatus_SESSION_STATUS_FINALIZED)
			},
			expectErr:   true,
			errContains: "vote round is not active",
		},
		// --- Nullifier uniqueness failure ---
		{
			name: "duplicate share nullifier",
			msg:  func() types.VoteMessage { return newValidMsgRevealShare() },
			opts: mockOpts(),
			setup: func() {
				s.setupActiveRound()
				s.recordNullifier(types.NullifierTypeShare, testRoundID, bytes.Repeat([]byte{0x77}, 32))
			},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
		// --- ZKP verification failure ---
		{
			name: "ZKP reveal share proof fails",
			msg:  func() types.VoteMessage { return newValidMsgRevealShare() },
			opts: failZKPOpts(),
			setup: func() {
				s.setupActiveRound()
				s.seedCommitmentRoot(10)
			},
			expectErr:   true,
			errContains: "invalid zero-knowledge proof",
		},
		// --- RecheckTx behavior ---
		{
			name:  "recheck: skips ZKP, passes with active round and fresh nullifier",
			msg:   func() types.VoteMessage { return newValidMsgRevealShare() },
			opts:  recheckOpts(),
			setup: func() { s.setupActiveRound() },
		},
		{
			name: "recheck: still catches duplicate share nullifier",
			msg:  func() types.VoteMessage { return newValidMsgRevealShare() },
			opts: recheckOpts(),
			setup: func() {
				s.setupActiveRound()
				s.recordNullifier(types.NullifierTypeShare, testRoundID, bytes.Repeat([]byte{0x77}, 32))
			},
			expectErr:   true,
			errContains: "nullifier already spent",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := ante.ValidateVoteTx(s.ctx, tc.msg(), s.keeper, tc.opts)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: MsgSubmitTally
// ---------------------------------------------------------------------------

// testValidatorAddr returns a valid zvotevaloper bech32 address for test use.
// Must be called after bech32 config is set in SetupTest.
func testValidatorAddr() string {
	return sdk.ValAddress(bytes.Repeat([]byte{0x01}, 20)).String()
}

func newValidMsgSubmitTally() *types.MsgSubmitTally {
	return &types.MsgSubmitTally{
		VoteRoundId: testRoundID,
		Creator:     testValidatorAddr(),
		Entries: []*types.TallyEntry{
			{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
		},
	}
}

func (s *ValidateTestSuite) TestValidateVoteTx_SubmitTally() {
	// checkTxCtx returns a context with IsCheckTx=true for testing mempool rejection.
	checkTxCtx := func() sdk.Context {
		return s.ctx.WithIsCheckTx(true)
	}
	// recheckTxCtx returns a context with IsReCheckTx=true.
	recheckTxCtx := func() sdk.Context {
		return s.ctx.WithIsReCheckTx(true)
	}

	tests := []struct {
		name        string
		msg         func() types.VoteMessage
		opts        ante.ValidateOpts
		setup       func()
		ctxFn       func() sdk.Context // optional: override context (e.g. CheckTx)
		expectErr   bool
		errContains string
	}{
		{
			name:  "valid submit tally with tallying round (FinalizeBlock context)",
			msg:   func() types.VoteMessage { return newValidMsgSubmitTally() },
			opts:  mockOpts(),
			setup: func() { s.setupTallyingRound() },
		},
		// --- ValidateBasic failures ---
		{
			name: "invalid: empty vote_round_id",
			msg: func() types.VoteMessage {
				m := newValidMsgSubmitTally()
				m.VoteRoundId = nil
				return m
			},
			opts:        mockOpts(),
			expectErr:   true,
			errContains: "vote_round_id",
		},
		{
			name: "invalid: empty creator",
			msg: func() types.VoteMessage {
				m := newValidMsgSubmitTally()
				m.Creator = ""
				return m
			},
			opts:        mockOpts(),
			expectErr:   true,
			errContains: "creator",
		},
		// --- Round state failures ---
		{
			name:        "round not found",
			msg:         func() types.VoteMessage { return newValidMsgSubmitTally() },
			opts:        mockOpts(),
			setup:       func() { /* no round */ },
			expectErr:   true,
			errContains: "vote round not found",
		},
		{
			name:        "active round rejected (must be TALLYING)",
			msg:         func() types.VoteMessage { return newValidMsgSubmitTally() },
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "not in tallying state",
		},
		{
			name: "finalized round rejected",
			msg:  func() types.VoteMessage { return newValidMsgSubmitTally() },
			opts: mockOpts(),
			setup: func() {
				s.setupRoundWithStatus(testRoundID, expiredEndTime, types.SessionStatus_SESSION_STATUS_FINALIZED)
			},
			expectErr:   true,
			errContains: "not in tallying state",
		},
		// --- Proposer check ---
		{
			name: "creator mismatch with block proposer rejected",
			msg: func() types.VoteMessage {
				m := newValidMsgSubmitTally()
				// Use a valid valoper address that is NOT the proposer.
				m.Creator = sdk.ValAddress(bytes.Repeat([]byte{0xFF}, 20)).String()
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupTallyingRound() },
			expectErr:   true,
			errContains: "does not match block proposer",
		},
		// --- CheckTx rejection ---
		{
			name:        "rejected in CheckTx (mempool submission not allowed)",
			msg:         func() types.VoteMessage { return newValidMsgSubmitTally() },
			opts:        mockOpts(),
			setup:       func() { s.setupTallyingRound() },
			ctxFn:       checkTxCtx,
			expectErr:   true,
			errContains: "cannot be submitted via mempool",
		},
		// --- RecheckTx behavior ---
		{
			name:        "recheck: also rejected (mempool re-validation)",
			msg:         func() types.VoteMessage { return newValidMsgSubmitTally() },
			opts:        recheckOpts(),
			setup:       func() { s.setupTallyingRound() },
			ctxFn:       recheckTxCtx,
			expectErr:   true,
			errContains: "cannot be submitted via mempool",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			ctx := s.ctx
			if tc.ctxFn != nil {
				ctx = tc.ctxFn()
			}
			err := ante.ValidateVoteTx(ctx, tc.msg(), s.keeper, tc.opts)
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Validation ordering
// ---------------------------------------------------------------------------

// TestValidateVoteTx_ValidationOrder verifies that the pipeline checks run in
// the correct order: ValidateBasic → round active → nullifiers → sig → ZKP.
// A message that fails an earlier check must not reach later checks.
func (s *ValidateTestSuite) TestValidateVoteTx_ValidationOrder() {
	tests := []struct {
		name        string
		msg         func() types.VoteMessage
		opts        ante.ValidateOpts
		setup       func()
		errContains string
	}{
		{
			name: "ValidateBasic fires before round check (bad field, no round)",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.Rk = nil // fails ValidateBasic
				return m
			},
			opts:  mockOpts(),
			setup: func() { /* no round — but ValidateBasic should fire first */ },
			// Should get basic validation error, not round-not-found.
			errContains: "rk must be 32 bytes",
		},
		{
			name: "round check fires before nullifier check (expired round, duplicate nullifier)",
			msg:  func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts: mockOpts(),
			setup: func() {
				s.setupExpiredRound()
				s.recordNullifier(types.NullifierTypeGov, testRoundID, bytes.Repeat([]byte{0x11}, 32))
			},
			// Should get round-not-active, not duplicate-nullifier.
			errContains: "vote round is not active",
		},
		{
			name: "nullifier check fires before sig check (duplicate nullifier, failing sig)",
			msg:  func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts: failSigOpts(),
			setup: func() {
				s.setupActiveRound()
				s.recordNullifier(types.NullifierTypeGov, testRoundID, bytes.Repeat([]byte{0x11}, 32))
			},
			// Should get duplicate-nullifier, not invalid-signature.
			errContains: "nullifier already spent",
		},
		{
			name: "sig check fires before ZKP check (failing sig, failing ZKP)",
			msg:  func() types.VoteMessage { return newValidMsgDelegateVote() },
			opts: ante.ValidateOpts{
				SigVerifier: errSigVerifier{},
				ZKPVerifier: errZKPVerifier{},
			},
			setup: func() { s.setupActiveRound() },
			// Should get invalid-signature, not invalid-proof.
			errContains: "invalid RedPallas signature",
		},
		// CastVote ordering: sighash check fires before sig check.
		{
			name: "CastVote: sighash mismatch fires before sig check",
			msg: func() types.VoteMessage {
				m := newValidMsgCastVote()
				m.Sighash = bytes.Repeat([]byte{0x99}, 32) // wrong sighash
				return m
			},
			opts:  failSigOpts(),
			setup: func() { s.setupActiveRound() },
			// Should get sighash-mismatch, not invalid-signature.
			errContains: "sighash does not match",
		},
		// CastVote ordering: sig check fires before ZKP check.
		{
			name: "CastVote: sig check fires before ZKP check",
			msg:  func() types.VoteMessage { return newValidMsgCastVote() },
			opts: ante.ValidateOpts{
				SigVerifier: errSigVerifier{},
				ZKPVerifier: errZKPVerifier{},
			},
			setup: func() { s.setupActiveRound() },
			// Should get invalid-signature, not invalid-proof.
			errContains: "invalid RedPallas signature",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			err := ante.ValidateVoteTx(s.ctx, tc.msg(), s.keeper, tc.opts)
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: ZKP public inputs wiring (spy verifier)
// ---------------------------------------------------------------------------

// TestVerifyDelegation_SessionDerivedInputs verifies that the ZKP verifier
// receives nc_root and nullifier_imt_root from the stored session state.
func (s *ValidateTestSuite) TestVerifyDelegation_SessionDerivedInputs() {
	s.SetupTest()
	s.setupActiveRound() // stores round with NcRoot and NullifierImtRoot

	spy := &spyZKPVerifier{}
	opts := ante.ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: spy,
	}

	msg := newValidMsgDelegateVote()
	err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
	s.Require().NoError(err)

	// Verify the spy captured the inputs.
	s.Require().NotNil(spy.capturedDelegationInputs, "spy should have captured delegation inputs")
	s.Require().Equal(bytes.Repeat([]byte{0x04}, 32), spy.capturedDelegationInputs.NcRoot,
		"NcRoot should match stored round value")
	s.Require().Equal(bytes.Repeat([]byte{0x03}, 32), spy.capturedDelegationInputs.NullifierImtRoot,
		"NullifierImtRoot should match stored round value")
}
