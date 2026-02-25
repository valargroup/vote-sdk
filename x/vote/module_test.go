package vote_test

import (
	"bytes"
	"context"
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
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
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
	keeper keeper.Keeper
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
			VoteRoundId: roundID,
			Status:      types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:        make([]byte, 32),
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
// Ceremony miss jailing integration test
// ---------------------------------------------------------------------------

// jailTrackingStakingKeeper is a mock that tracks Jail calls.
type jailTrackingStakingKeeper struct {
	jailedConsAddrs []sdk.ConsAddress
	validators      map[string]stakingtypes.Validator
}

func (jk *jailTrackingStakingKeeper) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	v, ok := jk.validators[addr.String()]
	if !ok {
		return stakingtypes.Validator{}, fmt.Errorf("validator not found: %s", addr)
	}
	return v, nil
}

func (jk *jailTrackingStakingKeeper) GetValidatorByConsAddr(_ context.Context, _ sdk.ConsAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, fmt.Errorf("not implemented")
}

func (jk *jailTrackingStakingKeeper) Jail(_ context.Context, consAddr sdk.ConsAddress) error {
	jk.jailedConsAddrs = append(jk.jailedConsAddrs, consAddr)
	return nil
}

// setupJailTest creates a keeper with a jail-tracking staking mock and a
// validator with a real consensus pubkey so JailValidator → GetConsAddr succeeds.
func (s *EndBlockerTestSuite) setupJailTest() (sdk.Context, keeper.Keeper, vote.AppModule, *jailTrackingStakingKeeper, sdk.ValAddress) {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test_jail")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)
	ctx := testCtx.Ctx.
		WithBlockTime(time.Unix(1_000_000, 0).UTC()).
		WithBlockHeight(10)
	storeService := runtime.NewKVStoreService(key)

	jailKeeper := &jailTrackingStakingKeeper{
		validators: make(map[string]stakingtypes.Validator),
	}

	valAddr := sdk.ValAddress([]byte{0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})

	// Create validator with a real ed25519 consensus pubkey so GetConsAddr succeeds.
	privKey := ed25519.GenPrivKey()
	pubKey := privKey.PubKey()
	pkAny, err := codectypes.NewAnyWithValue(pubKey)
	s.Require().NoError(err)

	val := stakingtypes.Validator{
		OperatorAddress: valAddr.String(),
		ConsensusPubkey: pkAny,
	}
	jailKeeper.validators[valAddr.String()] = val

	k := keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), jailKeeper)
	m := vote.NewAppModule(k, nil)

	return ctx, k, m, jailKeeper, valAddr
}

// seedDealtRoundForValidator creates a DEALT round with a single non-acking
// validator that has already timed out (deadline == block_time).
func (s *EndBlockerTestSuite) seedDealtRoundForValidator(k keeper.Keeper, ctx sdk.Context, valAddr sdk.ValAddress, roundID []byte) {
	kvStore := k.OpenKVStore(ctx)
	round := &types.VoteRound{
		VoteRoundId:    roundID,
		Status:         types.SessionStatus_SESSION_STATUS_PENDING,
		EaPk:           make([]byte, 32),
		CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
		CeremonyValidators: []*types.ValidatorPallasKey{
			{ValidatorAddress: valAddr.String(), PallasPk: make([]byte, 32)},
		},
		CeremonyDealer:       valAddr.String(),
		CeremonyPhaseStart:   999_400,
		CeremonyPhaseTimeout: 600,
	}
	s.Require().NoError(k.SetVoteRound(kvStore, round))
}

func (s *EndBlockerTestSuite) TestEndBlock_CeremonyMissJailing() {
	ctx, k, m, jailKeeper, valAddr := s.setupJailTest()
	kvStore := k.OpenKVStore(ctx)
	roundID := bytes.Repeat([]byte{0xEE}, 32)

	// Pre-seed miss counter to 2 (one below jail threshold).
	_, err := k.IncrementCeremonyMiss(kvStore, valAddr.String())
	s.Require().NoError(err)
	_, err = k.IncrementCeremonyMiss(kvStore, valAddr.String())
	s.Require().NoError(err)

	// Create a DEALT round with a single non-acking validator, already timed out.
	s.seedDealtRoundForValidator(k, ctx, valAddr, roundID)

	// Run EndBlocker.
	s.Require().NoError(m.EndBlock(ctx))

	// Miss counter should be 3 now.
	missCount, err := k.GetCeremonyMissCount(kvStore, valAddr.String())
	s.Require().NoError(err)
	s.Require().Equal(uint64(3), missCount)

	// Validator should have been jailed via the staking keeper mock.
	s.Require().Len(jailKeeper.jailedConsAddrs, 1)
}

// ---------------------------------------------------------------------------
// Multi-round miss accumulation test
// ---------------------------------------------------------------------------

func (s *EndBlockerTestSuite) TestEndBlock_CeremonyMissAccumulatesAcrossRounds() {
	ctx, k, m, jailKeeper, valAddr := s.setupJailTest()

	// Helper: create a new DEALT round with a unique ID that has already timed out,
	// run EndBlocker to trigger the timeout, then reset the round to REGISTERING
	// to simulate the next cycle. Each call returns the updated context for store reads.
	timeoutRound := func(roundSeed byte) {
		roundID := bytes.Repeat([]byte{roundSeed}, 32)
		s.seedDealtRoundForValidator(k, ctx, valAddr, roundID)
		s.Require().NoError(m.EndBlock(ctx))
	}

	s.Run("3 consecutive misses → jailed", func() {
		// Round 1: miss=1
		timeoutRound(0xA1)
		kvStore := k.OpenKVStore(ctx)
		missCount, err := k.GetCeremonyMissCount(kvStore, valAddr.String())
		s.Require().NoError(err)
		s.Require().Equal(uint64(1), missCount)
		s.Require().Len(jailKeeper.jailedConsAddrs, 0, "not jailed after 1 miss")

		// Round 2: miss=2
		timeoutRound(0xA2)
		missCount, err = k.GetCeremonyMissCount(kvStore, valAddr.String())
		s.Require().NoError(err)
		s.Require().Equal(uint64(2), missCount)
		s.Require().Len(jailKeeper.jailedConsAddrs, 0, "not jailed after 2 misses")

		// Round 3: miss=3 → jailed
		timeoutRound(0xA3)
		missCount, err = k.GetCeremonyMissCount(kvStore, valAddr.String())
		s.Require().NoError(err)
		s.Require().Equal(uint64(3), missCount)
		s.Require().Len(jailKeeper.jailedConsAddrs, 1, "jailed after 3 misses")
	})

	s.Run("ack resets counter so validator is not jailed", func() {
		// Fresh setup to avoid state from previous sub-test.
		ctx2, k2, m2, jailKeeper2, valAddr2 := s.setupJailTest()

		// Round 1: miss=1
		round1ID := bytes.Repeat([]byte{0xB1}, 32)
		s.seedDealtRoundForValidator(k2, ctx2, valAddr2, round1ID)
		s.Require().NoError(m2.EndBlock(ctx2))

		kvStore2 := k2.OpenKVStore(ctx2)
		missCount, err := k2.GetCeremonyMissCount(kvStore2, valAddr2.String())
		s.Require().NoError(err)
		s.Require().Equal(uint64(1), missCount)

		// Simulate ack in round 2 by resetting miss counter (mimics what
		// AckExecutiveAuthorityKey handler does on successful ack).
		s.Require().NoError(k2.ResetCeremonyMiss(kvStore2, valAddr2.String()))
		missCount, err = k2.GetCeremonyMissCount(kvStore2, valAddr2.String())
		s.Require().NoError(err)
		s.Require().Equal(uint64(0), missCount)

		// Round 3: miss=1 (not 2, because the ack reset it)
		round3ID := bytes.Repeat([]byte{0xB3}, 32)
		s.seedDealtRoundForValidator(k2, ctx2, valAddr2, round3ID)
		s.Require().NoError(m2.EndBlock(ctx2))

		missCount, err = k2.GetCeremonyMissCount(kvStore2, valAddr2.String())
		s.Require().NoError(err)
		s.Require().Equal(uint64(1), missCount)
		s.Require().Len(jailKeeper2.jailedConsAddrs, 0, "should not be jailed — ack reset the counter")
	})
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
