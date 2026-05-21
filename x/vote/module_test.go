package vote_test

import (
	"bytes"
	"context"
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

	svtest "github.com/valargroup/vote-sdk/testutil"
	vote "github.com/valargroup/vote-sdk/x/vote"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

var fpLE = svtest.FpLE

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
	s.keeper = keeper.NewKeeper(storeService, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	s.module = vote.NewAppModule(s.keeper, nil) // codec unused by EndBlock
}

type moduleMockStakingKeeper struct {
	validators     map[string]stakingtypes.Validator
	consToOperator map[string]string
}

func newModuleMockStakingKeeper(valAddrs ...string) *moduleMockStakingKeeper {
	mk := &moduleMockStakingKeeper{
		validators:     make(map[string]stakingtypes.Validator, len(valAddrs)),
		consToOperator: make(map[string]string, len(valAddrs)),
	}
	for _, addr := range valAddrs {
		validator, err := stakingtypes.NewValidator(addr, ed25519.GenPrivKey().PubKey(), stakingtypes.Description{Moniker: addr})
		if err != nil {
			panic(err)
		}
		validator.Status = stakingtypes.Bonded
		mk.validators[addr] = validator

		consAddr, err := validator.GetConsAddr()
		if err != nil {
			panic(err)
		}
		mk.consToOperator[sdk.ConsAddress(consAddr).String()] = addr
	}
	return mk
}

func (mk *moduleMockStakingKeeper) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	validator, ok := mk.validators[addr.String()]
	if !ok {
		return stakingtypes.Validator{}, fmt.Errorf("validator %s not found", addr)
	}
	return validator, nil
}

func (mk *moduleMockStakingKeeper) GetValidatorByConsAddr(_ context.Context, consAddr sdk.ConsAddress) (stakingtypes.Validator, error) {
	operAddr, ok := mk.consToOperator[consAddr.String()]
	if !ok {
		return stakingtypes.Validator{}, fmt.Errorf("validator with consensus address %s not found", consAddr)
	}
	return mk.validators[operAddr], nil
}

func (mk *moduleMockStakingKeeper) Jail(_ context.Context, consAddr sdk.ConsAddress) error {
	operAddr, ok := mk.consToOperator[consAddr.String()]
	if !ok {
		return fmt.Errorf("validator with consensus address %s not found", consAddr)
	}
	validator := mk.validators[operAddr]
	validator.Jailed = true
	mk.validators[operAddr] = validator
	return nil
}

func (mk *moduleMockStakingKeeper) Unjail(_ context.Context, consAddr sdk.ConsAddress) error {
	operAddr, ok := mk.consToOperator[consAddr.String()]
	if !ok {
		return fmt.Errorf("validator with consensus address %s not found", consAddr)
	}
	validator := mk.validators[operAddr]
	validator.Jailed = false
	mk.validators[operAddr] = validator
	return nil
}

type moduleMockSlashingKeeper struct {
	jailDuration time.Duration
	jailedUntil  map[string]time.Time
	jailCalls    []sdk.ConsAddress
}

func newModuleMockSlashingKeeper(jailDuration time.Duration) *moduleMockSlashingKeeper {
	return &moduleMockSlashingKeeper{
		jailDuration: jailDuration,
		jailedUntil:  make(map[string]time.Time),
	}
}

func (mk *moduleMockSlashingKeeper) Jail(_ context.Context, consAddr sdk.ConsAddress) error {
	mk.jailCalls = append(mk.jailCalls, consAddr)
	return nil
}

func (mk *moduleMockSlashingKeeper) JailUntil(_ context.Context, consAddr sdk.ConsAddress, jailTime time.Time) error {
	mk.jailedUntil[consAddr.String()] = jailTime
	return nil
}

func (mk *moduleMockSlashingKeeper) DowntimeJailDuration(context.Context) (time.Duration, error) {
	return mk.jailDuration, nil
}

func (s *EndBlockerTestSuite) setupCeremonyJailing(valAddrs ...string) *moduleMockSlashingKeeper {
	s.keeper.SetStakingKeeper(newModuleMockStakingKeeper(valAddrs...))
	slashing := newModuleMockSlashingKeeper(5 * time.Minute)
	s.keeper.SetSlashingKeeper(slashing)
	return slashing
}

// ---------------------------------------------------------------------------
// EndBlocker tests
// ---------------------------------------------------------------------------

// seedActiveRound stores a vote round with ACTIVE status and a future VoteEndTime.
func (s *EndBlockerTestSuite) seedActiveRound(roundID []byte) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, svtest.ActiveRoundFixture(roundID)))
}

func (s *EndBlockerTestSuite) TestEndBlock() {
	roundID := bytes.Repeat([]byte{0xAA}, 32)

	tests := []struct {
		name  string
		setup func()
		check func()
	}{
		{
			name:  "no-op when tree is empty",
			setup: func() { s.seedActiveRound(roundID) },
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				root, err := s.keeper.GetCommitmentRootAtHeight(kv, roundID, 10)
				s.Require().NoError(err)
				s.Require().Nil(root) // no root stored
			},
		},
		{
			name: "computes and stores root when leaves exist",
			setup: func() {
				s.seedActiveRound(roundID)
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, roundID, fpLE(1))
				s.Require().NoError(err)
				_, err = s.keeper.AppendCommitment(kv, roundID, fpLE(2))
				s.Require().NoError(err)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				// Root stored at block height 10.
				root, err := s.keeper.GetCommitmentRootAtHeight(kv, roundID, 10)
				s.Require().NoError(err)
				s.Require().NotNil(root)
				s.Require().Len(root, 32)

				// Tree state updated.
				state, err := s.keeper.GetCommitmentTreeState(kv, roundID)
				s.Require().NoError(err)
				s.Require().Equal(uint64(10), state.Height)
				s.Require().Equal(root, state.Root)
			},
		},
		{
			name: "skips when tree unchanged between blocks",
			setup: func() {
				s.seedActiveRound(roundID)
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, roundID, fpLE(1))
				s.Require().NoError(err)

				// Run EndBlock at height 10 to compute root.
				s.Require().NoError(s.module.EndBlock(s.ctx))

				// Advance to height 11 (no new leaves).
				s.ctx = s.ctx.WithBlockHeight(11)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				// Root exists at height 10 but not at height 11.
				root10, err := s.keeper.GetCommitmentRootAtHeight(kv, roundID, 10)
				s.Require().NoError(err)
				s.Require().NotNil(root10)

				root11, err := s.keeper.GetCommitmentRootAtHeight(kv, roundID, 11)
				s.Require().NoError(err)
				s.Require().Nil(root11)

				// Height in state is still 10.
				state, err := s.keeper.GetCommitmentTreeState(kv, roundID)
				s.Require().NoError(err)
				s.Require().Equal(uint64(10), state.Height)
			},
		},
		{
			name: "new root stored when leaves added after previous root",
			setup: func() {
				s.seedActiveRound(roundID)
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, roundID, fpLE(1))
				s.Require().NoError(err)

				// EndBlock at height 10.
				s.Require().NoError(s.module.EndBlock(s.ctx))

				// Add another leaf and advance height.
				_, err = s.keeper.AppendCommitment(kv, roundID, fpLE(2))
				s.Require().NoError(err)
				s.ctx = s.ctx.WithBlockHeight(11)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				root10, err := s.keeper.GetCommitmentRootAtHeight(kv, roundID, 10)
				s.Require().NoError(err)

				root11, err := s.keeper.GetCommitmentRootAtHeight(kv, roundID, 11)
				s.Require().NoError(err)
				s.Require().NotNil(root11)

				// Roots differ because tree changed.
				s.Require().NotEqual(root10, root11)

				// State reflects height 11.
				state, err := s.keeper.GetCommitmentTreeState(kv, roundID)
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

func (s *EndBlockerTestSuite) TestCeremonyMissingContributorSelection() {
	addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3)}

	tests := []struct {
		name          string
		validators    []*types.ValidatorPallasKey
		contributions []*types.DKGContribution
		want          []string
	}{
		{
			name: "no missing contributors",
			validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
			},
			contributions: []*types.DKGContribution{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
			},
			want: []string{},
		},
		{
			name: "one missing contributor",
			validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
				{ValidatorAddress: addrs[2]},
			},
			contributions: []*types.DKGContribution{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[2]},
			},
			want: []string{addrs[1]},
		},
		{
			name: "all contributors missing",
			validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
				{ValidatorAddress: addrs[2]},
			},
			want: []string{addrs[0], addrs[1], addrs[2]},
		},
		{
			name: "duplicate contributions still count once",
			validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
				{ValidatorAddress: addrs[2]},
			},
			contributions: []*types.DKGContribution{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[2]},
			},
			want: []string{addrs[1]},
		},
		{
			name: "nil contributions are ignored",
			validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
			},
			contributions: []*types.DKGContribution{
				nil,
				{ValidatorAddress: addrs[0]},
			},
			want: []string{addrs[1]},
		},
		{
			name: "duplicate validators are returned once",
			validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
				{ValidatorAddress: addrs[1]},
				{ValidatorAddress: addrs[2]},
			},
			contributions: []*types.DKGContribution{
				{ValidatorAddress: addrs[0]},
			},
			want: []string{addrs[1], addrs[2]},
		},
		{
			name: "missing contributors preserve snapshot order",
			validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[2]},
				{ValidatorAddress: addrs[0]},
				{ValidatorAddress: addrs[1]},
			},
			contributions: []*types.DKGContribution{
				{ValidatorAddress: addrs[0]},
			},
			want: []string{addrs[2], addrs[1]},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			round := &types.VoteRound{
				CeremonyValidators: tc.validators,
				DkgContributions:   tc.contributions,
			}
			s.Require().Equal(tc.want, keeper.MissingCeremonyContributors(round))
		})
	}
}

func (s *EndBlockerTestSuite) TestJailCeremonyNonContributors() {
	addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3)}

	tests := []struct {
		name          string
		snapshot      []string
		contributed   []string
		alreadyJailed []string
		wantJailed    []string
	}{
		{
			name:        "no missing contributors",
			snapshot:    []string{addrs[0], addrs[1], addrs[2]},
			contributed: []string{addrs[0], addrs[1], addrs[2]},
			wantJailed:  []string{},
		},
		{
			name:        "jails only missing contributor",
			snapshot:    []string{addrs[0], addrs[1], addrs[2]},
			contributed: []string{addrs[0], addrs[2]},
			wantJailed:  []string{addrs[1]},
		},
		{
			name:        "skips already jailed missing contributor",
			snapshot:    []string{addrs[0], addrs[1], addrs[2]},
			contributed: []string{addrs[0], addrs[2]},
			alreadyJailed: []string{
				addrs[1],
			},
			wantJailed: []string{},
		},
		{
			name:        "duplicate snapshot entries are jailed once",
			snapshot:    []string{addrs[0], addrs[1], addrs[1], addrs[2]},
			contributed: []string{addrs[0]},
			wantJailed:  []string{addrs[1], addrs[2]},
		},
		{
			name:       "all missing contributors",
			snapshot:   []string{addrs[2], addrs[0], addrs[1]},
			wantJailed: []string{addrs[2], addrs[0], addrs[1]},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			staking := newModuleMockStakingKeeper(addrs...)
			for _, addr := range tc.alreadyJailed {
				validator := staking.validators[addr]
				validator.Jailed = true
				staking.validators[addr] = validator
			}
			slashing := newModuleMockSlashingKeeper(5 * time.Minute)
			s.keeper.SetStakingKeeper(staking)
			s.keeper.SetSlashingKeeper(slashing)

			round := &types.VoteRound{
				VoteRoundId: bytes.Repeat([]byte{0xCB}, 32),
			}
			for _, addr := range tc.snapshot {
				round.CeremonyValidators = append(round.CeremonyValidators,
					&types.ValidatorPallasKey{ValidatorAddress: addr})
			}
			for _, addr := range tc.contributed {
				round.DkgContributions = append(round.DkgContributions,
					&types.DKGContribution{ValidatorAddress: addr})
			}

			results, err := s.keeper.JailCeremonyNonContributors(s.ctx, round)
			s.Require().NoError(err)

			gotJailed := make([]string, 0, len(results))
			for _, result := range results {
				gotJailed = append(gotJailed, result.ValidatorAddress)
				s.Require().Equal(s.ctx.BlockTime().Add(5*time.Minute), result.JailedUntil)
				s.Require().Equal(result.JailedUntil, slashing.jailedUntil[result.ConsAddress.String()])
			}
			s.Require().Equal(tc.wantJailed, gotJailed)
			s.Require().Len(slashing.jailCalls, len(tc.wantJailed))
		})
	}
}

func (s *EndBlockerTestSuite) TestEndBlock_CeremonyTimeout() {
	roundID := bytes.Repeat([]byte{0xCC}, 32)

	// Helper: seed a PENDING round with DEALT ceremony and 3 validators.
	// phase_start=999_400, phase_timeout=600 -> deadline = 1_000_000 == block_time.
	seedDealtRound := func(ackCount int) {
		addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3)}
		s.setupCeremonyJailing(addrs...)
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:    roundID,
			Status:         types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:           make([]byte, 32),
			CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0], PallasPk: make([]byte, 32)},
				{ValidatorAddress: addrs[1], PallasPk: make([]byte, 32)},
				{ValidatorAddress: addrs[2], PallasPk: make([]byte, 32)},
			},
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
		addrs := make([]string, nVals)
		for i := 0; i < nVals; i++ {
			addrs[i] = svtest.TestValAddr(byte(i + 1))
		}
		s.setupCeremonyJailing(addrs...)
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:          roundID,
			Status:               types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:                 make([]byte, 32),
			CeremonyStatus:       types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			Threshold:            threshold,
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
		}
		for i := 0; i < nVals; i++ {
			round.CeremonyValidators = append(round.CeremonyValidators,
				&types.ValidatorPallasKey{ValidatorAddress: addrs[i], PallasPk: make([]byte, 32)})
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
			// n=3 requires all 3 acks under RequiredCeremonyQuorumForN.
			name: "DEALT + 2/3 acked + timeout -> CEREMONY_FAILED",
			setup: func() {
				seedDealtRound(2)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED,
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
			name: "DEALT + zero acks + timeout -> CEREMONY_FAILED",
			setup: func() {
				seedDealtRound(0)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED,
		},
		{
			// n=3: 1 ack is below the required quorum of 3.
			name: "DEALT + below quorum (1/3 acks) + timeout -> CEREMONY_FAILED",
			setup: func() {
				seedDealtRound(1) // 1 of 3 acked
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED,
		},
		{
			// n=5, threshold=3, required quorum=4. Exactly threshold-sized
			// ack sets are rejected so one surviving withholder cannot break tally.
			name: "DEALT + exactly threshold (3/5 acks) -> CEREMONY_FAILED",
			setup: func() {
				seedDealtRoundWithThreshold(5, 3, 3)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED,
		},
		{
			// n=5, threshold=3, required quorum=4. One non-acker is stripped.
			name: "DEALT + required quorum (4/5 acks) -> CONFIRMED + ACTIVE",
			setup: func() {
				seedDealtRoundWithThreshold(5, 3, 4)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_ACTIVE,
		},
		{
			// n=9, threshold=5, required quorum=7. Exactly threshold-sized
			// ack sets no longer activate on timeout.
			name: "DEALT + exactly threshold (5/9 acks) -> CEREMONY_FAILED",
			setup: func() {
				seedDealtRoundWithThreshold(9, 5, 5)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED,
		},
		{
			// n=9, threshold=5, required quorum=7. Two non-ackers are stripped.
			name: "DEALT + required quorum (7/9 acks) -> CONFIRMED + ACTIVE",
			setup: func() {
				seedDealtRoundWithThreshold(9, 5, 7)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_ACTIVE,
		},
		{
			// n=9 requires 7 acks regardless of the published tally threshold.
			name: "DEALT + below required quorum (5/9 acks) + threshold=6 -> CEREMONY_FAILED",
			setup: func() {
				seedDealtRoundWithThreshold(9, 6, 5)
			},
			wantCeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			wantRoundStatus:    types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED,
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

func (s *EndBlockerTestSuite) TestEndBlock_RegisteringTimeoutMarksCeremonyFailedAndPreservesSnapshot() {
	roundID := bytes.Repeat([]byte{0xCE}, 32)
	addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3)}
	slashing := s.setupCeremonyJailing(addrs...)
	kv := s.keeper.OpenKVStore(s.ctx)

	round := &types.VoteRound{
		VoteRoundId:    roundID,
		Status:         types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: []*types.ValidatorPallasKey{
			{ValidatorAddress: addrs[0], PallasPk: make([]byte, 32), ShamirIndex: 1},
			{ValidatorAddress: addrs[1], PallasPk: make([]byte, 32), ShamirIndex: 2},
			{ValidatorAddress: addrs[2], PallasPk: make([]byte, 32), ShamirIndex: 3},
		},
		CeremonyPhaseStart:   999_400,
		CeremonyPhaseTimeout: 600,
		DkgContributions: []*types.DKGContribution{
			{ValidatorAddress: addrs[0], FeldmanCommitments: [][]byte{{0x01}}},
			{ValidatorAddress: addrs[2], FeldmanCommitments: [][]byte{{0x03}}},
		},
	}
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))

	s.Require().NoError(s.module.EndBlock(s.ctx))

	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyValidators, 3)
	s.Require().Equal(addrs[0], round.CeremonyValidators[0].ValidatorAddress)
	s.Require().Equal(uint32(1), round.CeremonyValidators[0].ShamirIndex)
	s.Require().Equal(addrs[1], round.CeremonyValidators[1].ValidatorAddress)
	s.Require().Equal(uint32(2), round.CeremonyValidators[1].ShamirIndex)
	s.Require().Equal(addrs[2], round.CeremonyValidators[2].ValidatorAddress)
	s.Require().Equal(uint32(3), round.CeremonyValidators[2].ShamirIndex)
	s.Require().Len(round.DkgContributions, 2)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED, round.Status)
	s.Require().Contains(round.CeremonyLog[0], "REGISTERING timeout: ceremony failed (2/3 contributions)")
	s.Require().Contains(round.CeremonyLog[1], "REGISTERING timeout: jailed 1 non-contributors")
	s.Require().Len(slashing.jailCalls, 1)
	s.Require().Equal(s.ctx.BlockTime().Add(5*time.Minute), slashing.jailedUntil[slashing.jailCalls[0].String()])
}

// ---------------------------------------------------------------------------
// Ceremony log tests for EndBlocker timeout paths
// ---------------------------------------------------------------------------

func (s *EndBlockerTestSuite) TestEndBlock_CeremonyTimeoutLog() {
	roundID := bytes.Repeat([]byte{0xDD}, 32)

	s.Run("timeout+confirm logs entry", func() {
		// n=4 requires 3 acks. One non-acker is stripped.
		s.SetupTest()
		addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3), svtest.TestValAddr(4)}
		slashing := s.setupCeremonyJailing(addrs...)
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:    roundID,
			Status:         types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:           make([]byte, 32),
			CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0], PallasPk: make([]byte, 32)},
				{ValidatorAddress: addrs[1], PallasPk: make([]byte, 32)},
				{ValidatorAddress: addrs[2], PallasPk: make([]byte, 32)},
				{ValidatorAddress: addrs[3], PallasPk: make([]byte, 32)},
			},
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
			CeremonyAcks: []*types.AckEntry{
				{ValidatorAddress: addrs[0], AckHeight: 9},
				{ValidatorAddress: addrs[1], AckHeight: 9},
				{ValidatorAddress: addrs[2], AckHeight: 9},
			},
		}
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))
		s.Require().NoError(s.module.EndBlock(s.ctx))

		round, err := s.keeper.GetVoteRound(kv, roundID)
		s.Require().NoError(err)
		s.Require().Len(round.CeremonyLog, 1)
		s.Require().Contains(round.CeremonyLog[0], "DEALT timeout: confirmed")
		s.Require().Contains(round.CeremonyLog[0], "3/4 acks")
		s.Require().Contains(round.CeremonyLog[0], "required 3")
		s.Require().Contains(round.CeremonyLog[0], "1 stripped")
		s.Require().Empty(slashing.jailCalls)
	})

	s.Run("timeout+finalize logs entry", func() {
		s.SetupTest()
		addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3)}
		slashing := s.setupCeremonyJailing(addrs...)
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:    roundID,
			Status:         types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:           make([]byte, 32),
			CeremonyStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: addrs[0], PallasPk: make([]byte, 32)},
				{ValidatorAddress: addrs[1], PallasPk: make([]byte, 32)},
				{ValidatorAddress: addrs[2], PallasPk: make([]byte, 32)},
			},
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
		}
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))
		s.Require().NoError(s.module.EndBlock(s.ctx))

		round, err := s.keeper.GetVoteRound(kv, roundID)
		s.Require().NoError(err)
		s.Require().Len(round.CeremonyLog, 1)
		s.Require().Contains(round.CeremonyLog[0], "DEALT timeout: ceremony failed")
		s.Require().Contains(round.CeremonyLog[0], "0/3 acks")
		s.Require().Empty(slashing.jailCalls)
		s.Require().Equal(types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED, round.Status)
	})

	s.Run("timeout+below-threshold logs entry and preserves validators", func() {
		// n=9 requires 7 acks. Five acks leave a threshold-sized survivor set,
		// so the failed pending round is preserved for audit.
		s.SetupTest()
		addrs := make([]string, 9)
		for i := range addrs {
			addrs[i] = svtest.TestValAddr(byte(i + 1))
		}
		slashing := s.setupCeremonyJailing(addrs...)
		kv := s.keeper.OpenKVStore(s.ctx)
		round := &types.VoteRound{
			VoteRoundId:          roundID,
			Status:               types.SessionStatus_SESSION_STATUS_PENDING,
			EaPk:                 make([]byte, 32),
			CeremonyStatus:       types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			Threshold:            6,
			CeremonyPhaseStart:   999_400,
			CeremonyPhaseTimeout: 600,
		}
		for i := 1; i <= 9; i++ {
			round.CeremonyValidators = append(round.CeremonyValidators,
				&types.ValidatorPallasKey{ValidatorAddress: addrs[i-1], PallasPk: make([]byte, 32)})
		}
		for i := 0; i < 5; i++ {
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
		s.Require().Contains(round.CeremonyLog[0], "DEALT timeout: ceremony failed")
		s.Require().Contains(round.CeremonyLog[0], "5/9 acks")
		s.Require().Contains(round.CeremonyLog[0], "required 7")
		s.Require().Empty(slashing.jailCalls)
		s.Require().Len(round.CeremonyValidators, 9)
		s.Require().Equal(types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED, round.Status)
	})
}

// ---------------------------------------------------------------------------
// Tally phase timeout tests
// ---------------------------------------------------------------------------

func (s *EndBlockerTestSuite) TestEndBlock_TallyTimeout() {
	roundID := bytes.Repeat([]byte{0xEE}, 32)

	tests := []struct {
		name         string
		setup        func()
		wantStatus   types.SessionStatus
		wantTimedOut bool
	}{
		{
			name: "TALLYING past deadline -> FINALIZED with tally_timed_out=true",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				round := &types.VoteRound{
					VoteRoundId:       roundID,
					Status:            types.SessionStatus_SESSION_STATUS_TALLYING,
					EaPk:              make([]byte, 32),
					Proposals:         svtest.SampleProposals(),
					TallyPhaseStart:   999_400,
					TallyPhaseTimeout: 600, // deadline = 1_000_000 == block_time
				}
				s.Require().NoError(s.keeper.SetVoteRound(kv, round))
			},
			wantStatus:   types.SessionStatus_SESSION_STATUS_FINALIZED,
			wantTimedOut: true,
		},
		{
			name: "TALLYING before deadline -> stays TALLYING",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				round := &types.VoteRound{
					VoteRoundId:       roundID,
					Status:            types.SessionStatus_SESSION_STATUS_TALLYING,
					EaPk:              make([]byte, 32),
					Proposals:         svtest.SampleProposals(),
					TallyPhaseStart:   999_401,
					TallyPhaseTimeout: 600, // deadline = 1_000_001 > block_time
				}
				s.Require().NoError(s.keeper.SetVoteRound(kv, round))
			},
			wantStatus:   types.SessionStatus_SESSION_STATUS_TALLYING,
			wantTimedOut: false,
		},
		{
			name: "TALLYING with zero timeout -> no timeout (disabled)",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				round := &types.VoteRound{
					VoteRoundId:       roundID,
					Status:            types.SessionStatus_SESSION_STATUS_TALLYING,
					EaPk:              make([]byte, 32),
					Proposals:         svtest.SampleProposals(),
					TallyPhaseStart:   999_400,
					TallyPhaseTimeout: 0,
				}
				s.Require().NoError(s.keeper.SetVoteRound(kv, round))
			},
			wantStatus:   types.SessionStatus_SESSION_STATUS_TALLYING,
			wantTimedOut: false,
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
			s.Require().Equal(tc.wantStatus, round.Status)
			s.Require().Equal(tc.wantTimedOut, round.TallyTimedOut)
		})
	}
}

func (s *EndBlockerTestSuite) TestEndBlock_TallyTimeout_MultipleRounds() {
	roundA := bytes.Repeat([]byte{0xA1}, 32)
	roundB := bytes.Repeat([]byte{0xB2}, 32)

	s.Run("independent timeouts: one expires, other stays", func() {
		s.SetupTest()
		kv := s.keeper.OpenKVStore(s.ctx)

		// Round A: past deadline (should timeout).
		s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
			VoteRoundId:       roundA,
			Status:            types.SessionStatus_SESSION_STATUS_TALLYING,
			EaPk:              make([]byte, 32),
			Proposals:         svtest.SampleProposals(),
			TallyPhaseStart:   999_400,
			TallyPhaseTimeout: 600, // deadline = 1_000_000 == block_time
		}))

		// Round B: not yet expired (should stay TALLYING).
		s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
			VoteRoundId:       roundB,
			Status:            types.SessionStatus_SESSION_STATUS_TALLYING,
			EaPk:              make([]byte, 32),
			Proposals:         svtest.SampleProposals(),
			TallyPhaseStart:   999_500,
			TallyPhaseTimeout: 600, // deadline = 1_000_100 > block_time
		}))

		s.Require().NoError(s.module.EndBlock(s.ctx))

		rA, err := s.keeper.GetVoteRound(kv, roundA)
		s.Require().NoError(err)
		s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, rA.Status)
		s.Require().True(rA.TallyTimedOut)

		rB, err := s.keeper.GetVoteRound(kv, roundB)
		s.Require().NoError(err)
		s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, rB.Status)
		s.Require().False(rB.TallyTimedOut)
	})
}

func (s *EndBlockerTestSuite) TestEndBlock_ActiveToTallyingSetsTimeoutFields() {
	roundID := bytes.Repeat([]byte{0xFF}, 32)

	s.Run("ACTIVE->TALLYING sets tally_phase_start and tally_phase_timeout", func() {
		s.SetupTest()
		kv := s.keeper.OpenKVStore(s.ctx)

		// Seed an ACTIVE round whose vote_end_time has passed.
		round := svtest.ActiveRoundFixture(roundID)
		round.VoteEndTime = 999_999 // < block_time (1_000_000)
		s.Require().NoError(s.keeper.SetVoteRound(kv, round))

		s.Require().NoError(s.module.EndBlock(s.ctx))

		got, err := s.keeper.GetVoteRound(kv, roundID)
		s.Require().NoError(err)
		s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, got.Status)
		s.Require().Equal(uint64(1_000_000), got.TallyPhaseStart)
		s.Require().Equal(types.DefaultTallyTimeout, got.TallyPhaseTimeout)
		s.Require().False(got.TallyTimedOut)
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
			wantMsg: "svote.v1.MsgRegisterPallasKey",
			msg:     &types.MsgRegisterPallasKey{Creator: valAddr.String()},
		},
		{
			name:    "ContributeDKG",
			signer:  vote.ProvideContributeDKGSigner,
			wantMsg: "svote.v1.MsgContributeDKG",
		},
		{
			name:    "AckExecutiveAuthorityKey",
			signer:  vote.ProvideAckExecutiveAuthorityKeySigner,
			wantMsg: "svote.v1.MsgAckExecutiveAuthorityKey",
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
		&types.MsgRotatePallasKey{},
		&types.MsgContributeDKG{},
		&types.MsgAckExecutiveAuthorityKey{},
	}
	for _, msg := range ceremonyMsgs {
		require.NoError(t, reg.EnsureRegistered(msg),
			"expected %T to be registered", msg)
	}
}

func TestMsgServiceSurface_RemovesCoordinatorPayloadRPCs(t *testing.T) {
	msgService := types.File_svote_v1_tx_proto.Services().ByName("Msg")
	require.NotNil(t, msgService)

	removedMethods := []protoreflect.Name{
		"CreateVotingSession",
		"UpdateVoteManagers",
		"AuthorizedSend",
		"ScheduleUpgrade",
		"CancelUpgrade",
		"SetEndorser",
	}
	for _, method := range removedMethods {
		require.Nil(t, msgService.Methods().ByName(method), "coordinator payload %s must not be a public Msg RPC", method)
	}

	requiredMethods := []protoreflect.Name{
		"ProposeCoordinatorAction",
		"ApproveCoordinatorAction",
	}
	for _, method := range requiredMethods {
		require.NotNil(t, msgService.Methods().ByName(method), "coordinator action RPC %s must remain public", method)
	}
}

// TestCustomSignerProviders_Registered verifies the custom signer providers
// still target the public Msg service messages that need custom signing.
func TestCustomSignerProviders_Registered(t *testing.T) {
	allSigners := []signing.CustomGetSigner{
		vote.ProvideDelegateVoteSigner(),
		vote.ProvideCastVoteSigner(),
		vote.ProvideRevealShareSigner(),
		vote.ProvideSubmitTallySigner(),
		vote.ProvideSubmitPartialDecryptionSigner(),
		vote.ProvideRegisterPallasKeySigner(),
		vote.ProvideRotatePallasKeySigner(),
		vote.ProvideContributeDKGSigner(),
		vote.ProvideAckExecutiveAuthorityKeySigner(),
		vote.ProvideCreateValidatorWithPallasKeySigner(),
	}

	wantMsgTypes := []protoreflect.FullName{
		"svote.v1.MsgDelegateVote",
		"svote.v1.MsgCastVote",
		"svote.v1.MsgRevealShare",
		"svote.v1.MsgSubmitTally",
		"svote.v1.MsgSubmitPartialDecryption",
		"svote.v1.MsgRegisterPallasKey",
		"svote.v1.MsgRotatePallasKey",
		"svote.v1.MsgContributeDKG",
		"svote.v1.MsgAckExecutiveAuthorityKey",
		"svote.v1.MsgCreateValidatorWithPallasKey",
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
