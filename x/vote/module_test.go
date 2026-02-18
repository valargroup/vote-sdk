package vote_test

import (
	"encoding/binary"
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
	// Helper: seed a DEALT ceremony with 3 validators.
	// phase_start=999_400, phase_timeout=600 -> deadline = 1_000_000 == block_time.
	seedDealtCeremony := func(ackCount int) {
		kv := s.keeper.OpenKVStore(s.ctx)
		state := &types.CeremonyState{
			Status: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
			EaPk:   make([]byte, 32),
			Validators: []*types.ValidatorPallasKey{
				{ValidatorAddress: "val1", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val2", PallasPk: make([]byte, 32)},
				{ValidatorAddress: "val3", PallasPk: make([]byte, 32)},
			},
			Dealer:       "val1",
			PhaseStart:   999_400,
			PhaseTimeout: 600,
		}
		for i := 0; i < ackCount; i++ {
			state.Acks = append(state.Acks, &types.AckEntry{
				ValidatorAddress: state.Validators[i].ValidatorAddress,
				AckHeight:        9,
			})
		}
		s.Require().NoError(s.keeper.SetCeremonyState(kv, state))
	}

	tests := []struct {
		name       string
		setup      func()
		wantStatus types.CeremonyStatus
	}{
		{
			name: "DEALT + partial acks + timeout -> REGISTERING",
			setup: func() {
				seedDealtCeremony(1) // 1 of 3 acked
			},
			wantStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		},
		{
			name: "DEALT + all acks + timeout -> CONFIRMED (>= 2/3 acked)",
			setup: func() {
				seedDealtCeremony(3) // 3 of 3 acked -> confirms, no non-ackers to strip
			},
			wantStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
		},
		{
			name: "DEALT + zero acks + timeout -> REGISTERING",
			setup: func() {
				seedDealtCeremony(0)
			},
			wantStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		},
		{
			name: "DEALT + no timeout yet (block_time < deadline)",
			setup: func() {
				seedDealtCeremony(0)
				// Push phase_start forward so deadline = 999_401 + 600 = 1_000_001 > block_time.
				kv := s.keeper.OpenKVStore(s.ctx)
				state, err := s.keeper.GetCeremonyState(kv)
				s.Require().NoError(err)
				state.PhaseStart = 999_401
				s.Require().NoError(s.keeper.SetCeremonyState(kv, state))
			},
			wantStatus: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
		},
		{
			name: "DEALT + exact deadline + 2/3 acked -> CONFIRMED",
			setup: func() {
				seedDealtCeremony(2) // 2 of 3 acked (>= 2/3) -> confirms, strips val3
				// phase_start=999_400 + phase_timeout=600 = 1_000_000 == block_time
			},
			wantStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
		},
		{
			name: "skip when ceremony is REGISTERING (no timeout)",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetCeremonyState(kv, &types.CeremonyState{
					Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
					Validators: []*types.ValidatorPallasKey{
						{ValidatorAddress: "val1", PallasPk: make([]byte, 32)},
					},
				}))
			},
			wantStatus: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		},
		{
			name: "skip when ceremony is already CONFIRMED",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetCeremonyState(kv, &types.CeremonyState{
					Status: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
				}))
			},
			wantStatus: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			s.Require().NoError(s.module.EndBlock(s.ctx))

			kv := s.keeper.OpenKVStore(s.ctx)
			state, err := s.keeper.GetCeremonyState(kv)
			s.Require().NoError(err)
			s.Require().NotNil(state)
			s.Require().Equal(tc.wantStatus, state.Status)
		})
	}
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
