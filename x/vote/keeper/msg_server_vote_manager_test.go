package keeper_test

import (
	"context"
	"fmt"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

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
	validators map[string]stakingtypes.Validator
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
	return stakingtypes.Validator{}, fmt.Errorf("not implemented in mock")
}

func (mk *mockStakingKeeper) Jail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

func (mk *mockStakingKeeper) Unjail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

// ---------------------------------------------------------------------------
// VoteManager CRUD tests
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
// MsgSetVoteManager handler tests
// ---------------------------------------------------------------------------

// setupWithMockStaking replaces the keeper's staking keeper with a mock that
// recognizes the given addresses as validators.
func (s *MsgServerTestSuite) setupWithMockStaking(valAddrs ...string) {
	s.setupWithMockStakingKeeper(newMockStakingKeeper(valAddrs...))
}

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
// Genesis: VoteManager restoration
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

// ---------------------------------------------------------------------------
// MsgUnjailValidator handler tests
// ---------------------------------------------------------------------------

// unjailTrackingStakingKeeper tracks Jail/Unjail calls for unjail tests.
type unjailTrackingStakingKeeper struct {
	validators      map[string]stakingtypes.Validator
	unjailedAddrs   []sdk.ConsAddress
}

func newUnjailTrackingKeeper() *unjailTrackingStakingKeeper {
	return &unjailTrackingStakingKeeper{
		validators: make(map[string]stakingtypes.Validator),
	}
}

func (uk *unjailTrackingStakingKeeper) addValidator(val stakingtypes.Validator) {
	uk.validators[val.OperatorAddress] = val
}

func (uk *unjailTrackingStakingKeeper) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	v, ok := uk.validators[addr.String()]
	if !ok {
		return stakingtypes.Validator{}, fmt.Errorf("validator %s not found", addr)
	}
	return v, nil
}

func (uk *unjailTrackingStakingKeeper) GetValidatorByConsAddr(_ context.Context, _ sdk.ConsAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, fmt.Errorf("not implemented")
}

func (uk *unjailTrackingStakingKeeper) Jail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

func (uk *unjailTrackingStakingKeeper) Unjail(_ context.Context, consAddr sdk.ConsAddress) error {
	uk.unjailedAddrs = append(uk.unjailedAddrs, consAddr)
	return nil
}

func (s *MsgServerTestSuite) TestUnjailValidator_Success() {
	s.SetupTest()

	// Create a mock with a jailed validator and a bonded sender.
	uk := newUnjailTrackingKeeper()

	senderAddr := testValAddr(1)
	targetAddr := testValAddr(2)

	// Sender is bonded (not jailed).
	uk.addValidator(stakingtypes.Validator{
		OperatorAddress: senderAddr,
		Status:          stakingtypes.Bonded,
		Jailed:          false,
	})

	// Target is jailed, with a real consensus pubkey for GetConsAddr.
	targetPk := ed25519.GenPrivKey().PubKey()
	targetPkAny, err := codectypes.NewAnyWithValue(targetPk)
	s.Require().NoError(err)
	uk.addValidator(stakingtypes.Validator{
		OperatorAddress: targetAddr,
		ConsensusPubkey: targetPkAny,
		Status:          stakingtypes.Bonded,
		Jailed:          true,
	})

	s.setupWithMockStakingKeeper(uk)

	// Pre-seed some ceremony misses for the jailed validator.
	kv := s.keeper.OpenKVStore(s.ctx)
	_, err = s.keeper.IncrementCeremonyMiss(kv, targetAddr)
	s.Require().NoError(err)
	_, err = s.keeper.IncrementCeremonyMiss(kv, targetAddr)
	s.Require().NoError(err)

	_, err = s.msgServer.UnjailValidator(s.ctx, &types.MsgUnjailValidator{
		Creator:          senderAddr,
		ValidatorAddress: targetAddr,
	})
	s.Require().NoError(err)

	// Unjail was called on the staking keeper.
	s.Require().Len(uk.unjailedAddrs, 1)

	// Ceremony miss counter was reset.
	missCount, err := s.keeper.GetCeremonyMissCount(kv, targetAddr)
	s.Require().NoError(err)
	s.Require().Equal(uint64(0), missCount)

	// Event emitted.
	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeUnjailValidator {
			found = true
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeUnjailValidator)
}

func (s *MsgServerTestSuite) TestUnjailValidator_NonValidatorRejected() {
	s.SetupTest()
	s.setupWithMockStaking() // no validators

	_, err := s.msgServer.UnjailValidator(s.ctx, &types.MsgUnjailValidator{
		Creator:          testValAddr(1),
		ValidatorAddress: testValAddr(2),
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not a bonded validator")
}

func (s *MsgServerTestSuite) TestUnjailValidator_NotJailedRejected() {
	s.SetupTest()

	uk := newUnjailTrackingKeeper()
	senderAddr := testValAddr(1)
	targetAddr := testValAddr(2)

	uk.addValidator(stakingtypes.Validator{
		OperatorAddress: senderAddr,
		Status:          stakingtypes.Bonded,
	})
	// Target is NOT jailed.
	targetPk := ed25519.GenPrivKey().PubKey()
	targetPkAny, err := codectypes.NewAnyWithValue(targetPk)
	s.Require().NoError(err)
	uk.addValidator(stakingtypes.Validator{
		OperatorAddress: targetAddr,
		ConsensusPubkey: targetPkAny,
		Status:          stakingtypes.Bonded,
		Jailed:          false,
	})

	s.setupWithMockStakingKeeper(uk)

	_, err = s.msgServer.UnjailValidator(s.ctx, &types.MsgUnjailValidator{
		Creator:          senderAddr,
		ValidatorAddress: targetAddr,
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not jailed")
}

func (s *MsgServerTestSuite) TestUnjailValidator_EmptyAddressRejected() {
	s.SetupTest()
	senderAddr := testValAddr(1)
	s.setupWithMockStaking(senderAddr)

	_, err := s.msgServer.UnjailValidator(s.ctx, &types.MsgUnjailValidator{
		Creator:          senderAddr,
		ValidatorAddress: "",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "validator_address cannot be empty")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// seedVoteManager sets the vote manager address in the KV store for tests.
func (s *MsgServerTestSuite) seedVoteManager(addr string) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: addr}))
}

// setupWithMockStakingKeeper replaces the keeper's staking keeper with the
// given mock and rebuilds the msgServer so it uses the updated keeper.
func (s *MsgServerTestSuite) setupWithMockStakingKeeper(sk keeper.StakingKeeper) {
	s.keeper.SetStakingKeeper(sk)
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)
}
