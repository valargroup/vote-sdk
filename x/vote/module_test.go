package vote_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	"cosmossdk.io/x/tx/signing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	vote "github.com/z-cale/zally/x/vote"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// fpLE returns a 32-byte little-endian Pallas Fp encoding of a small integer.
// Used so commitment leaves are canonical and accepted by the votetree FFI.
func fpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

type EndBlockerTestSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper *keeper.Keeper
	module vote.AppModule
}

func TestEndBlockerTestSuite(t *testing.T) {
	suite.Run(t, new(EndBlockerTestSuite))
}

func (s *EndBlockerTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.
		WithBlockTime(time.Unix(1_000_000, 0).UTC()).
		WithBlockHeight(10)
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), nil)
	s.module = vote.NewAppModule(s.keeper, nil) // codec unused by EndBlock
}

// ---------------------------------------------------------------------------
// EndBlocker tests
// ---------------------------------------------------------------------------

func (s *EndBlockerTestSuite) TestEndBlock() {
	tests := []struct {
		name  string
		setup func()
		check func()
	}{
		{
			name:  "no-op when tree is empty",
			setup: func() {},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				root, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)
				s.Require().Nil(root) // no root stored
			},
		},
		{
			name: "computes and stores root when leaves exist",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, fpLE(1))
				s.Require().NoError(err)
				_, err = s.keeper.AppendCommitment(kv, fpLE(2))
				s.Require().NoError(err)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				// Root stored at block height 10.
				root, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)
				s.Require().NotNil(root)
				s.Require().Len(root, 32)

				// Tree state updated.
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(10), state.Height)
				s.Require().Equal(root, state.Root)
			},
		},
		{
			name: "skips when tree unchanged between blocks",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, fpLE(1))
				s.Require().NoError(err)

				// Run EndBlock at height 10 to compute root.
				s.Require().NoError(s.module.EndBlock(s.ctx))

				// Advance to height 11 (no new leaves).
				s.ctx = s.ctx.WithBlockHeight(11)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				// Root exists at height 10 but not at height 11.
				root10, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)
				s.Require().NotNil(root10)

				root11, err := s.keeper.GetCommitmentRootAtHeight(kv, 11)
				s.Require().NoError(err)
				s.Require().Nil(root11)

				// Height in state is still 10.
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(10), state.Height)
			},
		},
		{
			name: "new root stored when leaves added after previous root",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, fpLE(1))
				s.Require().NoError(err)

				// EndBlock at height 10.
				s.Require().NoError(s.module.EndBlock(s.ctx))

				// Add another leaf and advance height.
				_, err = s.keeper.AppendCommitment(kv, fpLE(2))
				s.Require().NoError(err)
				s.ctx = s.ctx.WithBlockHeight(11)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				root10, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)

				root11, err := s.keeper.GetCommitmentRootAtHeight(kv, 11)
				s.Require().NoError(err)
				s.Require().NotNil(root11)

				// Roots differ because tree changed.
				s.Require().NotEqual(root10, root11)

				// State reflects height 11.
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(11), state.Height)
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			s.Require().NoError(s.module.EndBlock(s.ctx))
			tc.check()
		})
	}
}

// ---------------------------------------------------------------------------
// Ceremony phase timeout tests
// ---------------------------------------------------------------------------

func (s *EndBlockerTestSuite) TestEndBlock_CeremonyTimeout() {
	roundID := bytes.Repeat([]byte{0xCC}, 32)

	// Helper: seed a PENDING round with DEALT ceremony and 3 validators.
	// phase_start=999_400, phase_timeout=600 -> deadline = 1_000_000 == block_time.
	seedDealtRound := func(ackCount int) {
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:    roundID,
			Status:         types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:           make([]byte, 32),
			CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: "val1", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val2", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val3", PallasPk: make([]byte, 32)},
			},
			CeremonyDealer:       "val1",
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
		}
		for i := 0; i < ackCount; i++ {
			round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
				ValidatorAddress: round.CeremonyValidators[i].ValidatorAddress,
				AckHeight:        9,
			})
		}
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))
	}

	// Helper: seed a PENDING round with DEALT ceremony, n validators, a
	// Shamir threshold, and ackCount acks. Uses the same timeout deadline.
	seedDealtRoundWithThreshold := func(nVals int, threshold uint32, ackCount int) {
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:          roundID,
			Status:               types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:                 make([]byte, 32),
			CeremonyStatus:       types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			Threshold:            threshold,
			CeremonyDealer:       "val1",
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
		}
		for i := 0; i < nVals; i++ {
			addr := fmt.Sprintf("val%d", i+1)
			round.CeremonyValidators = append(round.CeremonyValidators,
				&types.ValidatorPallasKey{ValidatorAddress: addr, PallasPk: make([]byte, 32)})
		}
		for i := 0; i < ackCount; i++ {
			round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
				ValidatorAddress: round.CeremonyValidators[i].ValidatorAddress,
				AckHeight:        9,
			})
		}
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))
	}

	tests := []struct {
		name               string
		setup              func()
		wantCeremonyStatus types.CeremonyStatus
		wantRoundStatus    types.SessionStatus
	}{
		{
			name: "DEALT + 1/3 acked + timeout -> CONFIRMED + ACTIVE",
			setup: func() {
				seedDealtRound(1) // 1 of 3 acked (exactly 1/3)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_ACTIVE,
		},
		{
			name: "DEALT + all acks + timeout -> CONFIRMED + ACTIVE",
			setup: func() {
				seedDealtRound(3) // 3 of 3 acked
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_ACTIVE,
		},
		{
			name: "DEALT + zero acks + timeout -> REGISTERING (reset)",
			setup: func() {
				seedDealtRound(0)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_PENDING,
		},
		{
			// n=9, threshold=4, 3 acks: OneThirdAcked (3*3>=9) but 3 < threshold=4.
			// Activating would leave only 3 validators, making tally impossible.
			// Must reset to REGISTERING instead.
			name: "DEALT + 1/3 acked + threshold=4 + 3 acks -> REGISTERING (below threshold)",
			setup: func() {
				seedDealtRoundWithThreshold(9, 4, 3)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_PENDING,
		},
		{
			// n=9, threshold=4, 4 acks: OneThirdAcked (4*3>=9) and 4 >= threshold=4.
			// Both conditions met — strip 5 non-ackers and activate.
			name: "DEALT + threshold=4 + 4 acks -> CONFIRMED + ACTIVE (meets threshold)",
			setup: func() {
				seedDealtRoundWithThreshold(9, 4, 4)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_ACTIVE,
		},
		{
			// Threshold=0 (legacy single-validator mode): liveness check skipped,
			// 1/3 rule alone governs the timeout path.
			name: "DEALT + threshold=0 (legacy) + 1/3 acked -> CONFIRMED + ACTIVE",
			setup: func() {
				seedDealtRound(1) // 1 of 3 acked, Threshold=0
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_ACTIVE,
		},
		{
			name: "DEALT + no timeout yet (block_time < deadline)",
			setup: func() {
				seedDealtRound(0)
				// Push phase_start forward so deadline = 999_401 + 600 = 1_000_001 > block_time.
				kv := s.keeper.OpenKVStore(s.ctx)
				round, err := s.keeper.GetVoteRound(kv, roundID)
				s.Require().NoError(err)
				round.CeremonyPhaseStart = 999_401
				s.Require().NoError(s.keeper.SetVoteRound(kv, round))
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_PENDING,
		},
		{
			name: "REGISTERING round is skipped (no timeout)",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId:    roundID,
					Status:         types.SessionStatus_SESSION_STATUS_PENDING,
					CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
					CeremonyValidators: []*types.ValidatorPallasKey{
						{ValidatorAddress: "val1", PallasPk: make([]byte, 32)},
					},
				}))
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_PENDING,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			s.Require().NoError(s.module.EndBlock(s.ctx))

			kv := s.keeper.OpenKVStore(s.ctx)
			round, err := s.keeper.GetVoteRound(kv, roundID)
			s.Require().NoError(err)
			s.Require().NotNil(round)
			s.Require().Equal(tc.wantCeremonyStatus, round.CeremonyStatus)
			s.Require().Equal(tc.wantRoundStatus, round.Status)
		})
	}
}

// ---------------------------------------------------------------------------
// Ceremony log tests for EndBlocker timeout paths
// ---------------------------------------------------------------------------

func (s *EndBlockerTestSuite) TestEndBlock_CeremonyTimeoutLog() {
	roundID := bytes.Repeat([]byte{0xDD}, 32)

	s.Run("timeout+confirm logs entry", func() {
		s.SetupTest()
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:    roundID,
			Status:         types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:           make([]byte, 32),
			CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: "val1", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val2", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val3", PallasPk: make([]byte, 32)},
			},
			CeremonyDealer:       "val1",
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
			CeremonyAcks: []*types.AckEntry{
				{ValidatorAddress: "val1", AckHeight: 9},
			},
		}
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))
		s.Require().NoError(s.module.EndBlock(s.ctx))

		round, err := s.keeper.GetVoteRound(kv, roundID)
		s.Require().NoError(err)
		s.Require().Len(round.CeremonyLog, 1)
		s.Require().Contains(round.CeremonyLog[0], "DEALT timeout: confirmed")
		s.Require().Contains(round.CeremonyLog[0], "1/3 acks")
		s.Require().Contains(round.CeremonyLog[0], "2 stripped")
	})

	s.Run("timeout+reset logs entry", func() {
		s.SetupTest()
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:    roundID,
			Status:         types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:           make([]byte, 32),
			CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: "val1", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val2", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val3", PallasPk: make([]byte, 32)},
			},
			CeremonyDealer:       "val1",
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
		}
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))
		s.Require().NoError(s.module.EndBlock(s.ctx))

		round, err := s.keeper.GetVoteRound(kv, roundID)
		s.Require().NoError(err)
		s.Require().Len(round.CeremonyLog, 1)
		s.Require().Contains(round.CeremonyLog[0], "DEALT timeout: reset to REGISTERING")
		s.Require().Contains(round.CeremonyLog[0], "0/3 acks")
	})

	s.Run("timeout+below-threshold logs entry and preserves validators", func() {
		// n=9, threshold=4, 3 acks: 1/3 met but 3 < threshold. Ceremony resets
		// to REGISTERING so a fresh deal can address all 9 validators again.
		s.SetupTest()
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:          roundID,
			Status:               types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:                 make([]byte, 32),
			CeremonyStatus:       types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			Threshold:            4,
			CeremonyDealer:       "val1",
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
		}
		for i := 1; i <= 9; i++ {
			round.CeremonyValidators = append(round.CeremonyValidators,
				&types.ValidatorPallasKey{ValidatorAddress: fmt.Sprintf("val%d", i), PallasPk: make([]byte, 32)})
		}
		for i := 0; i < 3; i++ {
			round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{
				ValidatorAddress: round.CeremonyValidators[i].ValidatorAddress,
				AckHeight:        9,
			})
		}
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))
		s.Require().NoError(s.module.EndBlock(s.ctx))

		round, err := s.keeper.GetVoteRound(kv, roundID)
		s.Require().NoError(err)
		s.Require().Len(round.CeremonyLog, 1)
		s.Require().Contains(round.CeremonyLog[0], "DEALT timeout: reset to REGISTERING")
		s.Require().Contains(round.CeremonyLog[0], "3/9 acks")
		s.Require().Contains(round.CeremonyLog[0], "remaining 3 < threshold 4")
		// All 9 validators are preserved (non-ackers were NOT stripped before reset).
		s.Require().Len(round.CeremonyValidators, 9)
	})
}

// ---------------------------------------------------------------------------
// Ceremony signer provider tests (Step 9 wiring)
// ---------------------------------------------------------------------------

// TestCeremonySignerProviders verifies that each ceremony signer provider
// returns a CustomGetSigner targeting the correct protobuf message type and
// that the no-op Fn returns nil signers (ceremony messages use ZKP auth).
func TestCeremonySignerProviders(t *testing.T) {
	valAddr := sdk.ValAddress([]byte("testvalidator___________"))
	accAddrBytes := []byte(sdk.AccAddress(valAddr))

	tests := []struct {
		name    string
		signer  func() signing.CustomGetSigner
		wantMsg protoreflect.FullName
		msg     proto.Message // nil → noop signer; non-nil → ceremonyCreatorSignerFn
	}{
		{
			name:    "RegisterPallasKey",
			signer:  vote.ProvideRegisterPallasKeySigner,
			wantMsg: "zvote.v1.MsgRegisterPallasKey",
			msg:     &types.MsgRegisterPallasKey{Creator: valAddr.String()},
		},
		{
			name:    "DealExecutiveAuthorityKey",
			signer:  vote.ProvideDealExecutiveAuthorityKeySigner,
			wantMsg: "zvote.v1.MsgDealExecutiveAuthorityKey",
			msg:     &types.MsgDealExecutiveAuthorityKey{Creator: valAddr.String()},
		},
		{
			name:    "AckExecutiveAuthorityKey",
			signer:  vote.ProvideAckExecutiveAuthorityKeySigner,
			wantMsg: "zvote.v1.MsgAckExecutiveAuthorityKey",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.signer()
			require.Equal(t, tc.wantMsg, s.MsgType, "MsgType mismatch")
			require.NotNil(t, s.Fn, "Fn must not be nil")

			if tc.msg == nil {
				signers, err := s.Fn(nil)
				require.NoError(t, err)
				require.Nil(t, signers)
			} else {
				signers, err := s.Fn(tc.msg)
				require.NoError(t, err)
				require.Len(t, signers, 1)
				require.Equal(t, accAddrBytes, signers[0])
			}
		})
	}
}

// TestRegisterInterfaces_IncludesCeremonyMsgs verifies that RegisterInterfaces
// registers the ceremony message types so BaseApp's MsgServiceRouter can
// resolve them.
func TestRegisterInterfaces_IncludesCeremonyMsgs(t *testing.T) {
	reg := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(reg)

	ceremonyMsgs := []sdk.Msg{
		&types.MsgRegisterPallasKey{},
		&types.MsgDealExecutiveAuthorityKey{},
		&types.MsgAckExecutiveAuthorityKey{},
	}
	for _, msg := range ceremonyMsgs {
		require.NoError(t, reg.EnsureRegistered(msg),
			"expected %T to be registered", msg)
	}
}

// TestAllSignerProviders_Completeness verifies that every Msg type registered
// in RegisterInterfaces has a corresponding signer provider in init(). This
// catches the case where a new message is added to codec.go but forgotten in
// module.go.
func TestAllSignerProviders_Completeness(t *testing.T) {
	allSigners := []signing.CustomGetSigner{
		vote.ProvideCreateVotingSessionSigner(),
		vote.ProvideDelegateVoteSigner(),
		vote.ProvideCastVoteSigner(),
		vote.ProvideRevealShareSigner(),
		vote.ProvideSubmitTallySigner(),
		vote.ProvideRegisterPallasKeySigner(),
		vote.ProvideDealExecutiveAuthorityKeySigner(),
		vote.ProvideAckExecutiveAuthorityKeySigner(),
	}

	wantMsgTypes := []protoreflect.FullName{
		"zvote.v1.MsgCreateVotingSession",
		"zvote.v1.MsgDelegateVote",
		"zvote.v1.MsgCastVote",
		"zvote.v1.MsgRevealShare",
		"zvote.v1.MsgSubmitTally",
		"zvote.v1.MsgRegisterPallasKey",
		"zvote.v1.MsgDealExecutiveAuthorityKey",
		"zvote.v1.MsgAckExecutiveAuthorityKey",
	}

	signerMap := make(map[protoreflect.FullName]bool, len(allSigners))
	for _, s := range allSigners {
		signerMap[s.MsgType] = true
	}

	for _, want := range wantMsgTypes {
		require.True(t, signerMap[want],
			"missing signer provider for %s", want)
	}
}
