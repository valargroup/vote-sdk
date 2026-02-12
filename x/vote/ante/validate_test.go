package ante_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp"
	"github.com/z-cale/zally/x/vote/ante"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

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

// ---------------------------------------------------------------------------
// Test constants and message constructors
// ---------------------------------------------------------------------------

var (
	testBlockTime  = time.Unix(1_000_000, 0).UTC()
	activeEndTime  = uint64(2_000_000) // well in the future relative to testBlockTime
	expiredEndTime = uint64(999_999)   // in the past relative to testBlockTime
	testRoundID = bytes.Repeat([]byte{0x01}, 32)
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
	}
}

func newValidMsgDelegateVote() *types.MsgDelegateVote {
	return &types.MsgDelegateVote{
		Rk:                  bytes.Repeat([]byte{0xAA}, 32),
		SpendAuthSig:        bytes.Repeat([]byte{0xBB}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0xCC}, 32),
		CmxNew:              bytes.Repeat([]byte{0xDD}, 32),
		EncMemo:             bytes.Repeat([]byte{0xEE}, 64),
		GovComm:             bytes.Repeat([]byte{0xFF}, 32),
		GovNullifiers: [][]byte{
			bytes.Repeat([]byte{0x11}, 32),
			bytes.Repeat([]byte{0x12}, 32),
		},
		Proof:       bytes.Repeat([]byte{0x22}, 192),
		VoteRoundId: testRoundID,
		Sighash:     bytes.Repeat([]byte{0x99}, 32),
	}
}

func newValidMsgCastVote() *types.MsgCastVote {
	return &types.MsgCastVote{
		VanNullifier:             bytes.Repeat([]byte{0x33}, 32),
		VoteAuthorityNoteNew:     bytes.Repeat([]byte{0x44}, 32),
		VoteCommitment:           bytes.Repeat([]byte{0x55}, 32),
		ProposalId:               1,
		Proof:                    bytes.Repeat([]byte{0x66}, 192),
		VoteRoundId:              testRoundID,
		VoteCommTreeAnchorHeight: 10,
	}
}

func newValidMsgRevealShare() *types.MsgRevealShare {
	return &types.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0x77}, 32),
		VoteAmount:               1000,
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
	keeper keeper.Keeper
}

func TestValidateTestSuite(t *testing.T) {
	suite.Run(t, new(ValidateTestSuite))
}

// SetupTest creates a fresh in-memory KV store, vote keeper, and SDK context
// with a deterministic block time before each test case.
func (s *ValidateTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.WithBlockTime(testBlockTime)
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger())
}

// ---------------------------------------------------------------------------
// Suite helpers
// ---------------------------------------------------------------------------

// setupActiveRound stores a vote round that is still active (endTime > block time).
func (s *ValidateTestSuite) setupActiveRound() {
	s.setupRound(testRoundID, activeEndTime)
}

// setupExpiredRound stores a vote round that has already expired.
func (s *ValidateTestSuite) setupExpiredRound() {
	s.setupRound(testRoundID, expiredEndTime)
}

// setupRound stores a VoteRound with the given ID and end time.
func (s *ValidateTestSuite) setupRound(roundID []byte, endTime uint64) {
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
	}
	err := s.keeper.SetVoteRound(kvStore, round)
	s.Require().NoError(err)
}

// recordNullifier marks a nullifier as already spent in the KV store,
// using the given type and round scoping.
func (s *ValidateTestSuite) recordNullifier(nfType types.NullifierType, roundID, nullifier []byte) {
	kvStore := s.keeper.OpenKVStore(s.ctx)
	err := s.keeper.SetNullifier(kvStore, nfType, roundID, nullifier)
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
			name: "valid create voting session passes all checks",
			msg:  func() types.VoteMessage { return newValidMsgCreateVotingSession() },
			opts: mockOpts(),
		},
		{
			name: "valid create voting session on recheck also passes (no expensive checks needed)",
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
			name: "invalid: too many gov_nullifiers (>4)",
			msg: func() types.VoteMessage {
				m := newValidMsgDelegateVote()
				m.GovNullifiers = [][]byte{
					bytes.Repeat([]byte{0x01}, 32),
					bytes.Repeat([]byte{0x02}, 32),
					bytes.Repeat([]byte{0x03}, 32),
					bytes.Repeat([]byte{0x04}, 32),
					bytes.Repeat([]byte{0x05}, 32), // 5th one
				}
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "gov_nullifiers cannot exceed 4",
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
			name:  "recheck: skips ZKP, passes with active round and fresh nullifier",
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
			name:  "valid reveal share with active round and mock verifiers",
			msg:   func() types.VoteMessage { return newValidMsgRevealShare() },
			opts:  mockOpts(),
			setup: func() { s.setupActiveRound() },
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
			name: "invalid: zero vote_amount",
			msg: func() types.VoteMessage {
				m := newValidMsgRevealShare()
				m.VoteAmount = 0
				return m
			},
			opts:        mockOpts(),
			setup:       func() { s.setupActiveRound() },
			expectErr:   true,
			errContains: "vote_amount",
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
			name:        "round expired",
			msg:         func() types.VoteMessage { return newValidMsgRevealShare() },
			opts:        mockOpts(),
			setup:       func() { s.setupExpiredRound() },
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
			name:        "ZKP reveal share proof fails",
			msg:         func() types.VoteMessage { return newValidMsgRevealShare() },
			opts:        failZKPOpts(),
			setup:       func() { s.setupActiveRound() },
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
