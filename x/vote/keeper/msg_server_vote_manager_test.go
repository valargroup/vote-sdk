package keeper_test

import (
	"context"
	"fmt"

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

// mockStakingKeeper implements keeper.StakingKeeper for tests.
// validators maps bech32 operator address -> validator.
type mockStakingKeeper struct {
	validators map[string]stakingtypes.Validator
}

func newMockStakingKeeper(valAddrs ...string) *mockStakingKeeper {
	mk := &mockStakingKeeper{validators: make(map[string]stakingtypes.Validator)}
	for _, addr := range valAddrs {
		mk.validators[addr] = stakingtypes.Validator{OperatorAddress: addr}
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
	s.setupWithMockStaking(val1)

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: "zvote1manager",
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Equal("zvote1manager", mgr.Address)
}

func (s *MsgServerTestSuite) TestSetVoteManager_VoteManagerCanChange() {
	s.SetupTest()
	s.setupWithMockStaking()

	// Seed a vote manager.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: "current_manager"}))

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    "current_manager",
		NewManager: "new_manager",
	})
	s.Require().NoError(err)

	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Equal("new_manager", mgr.Address)
}

func (s *MsgServerTestSuite) TestSetVoteManager_ValidatorCanChange() {
	s.SetupTest()
	val1 := testValAddr(1)
	s.setupWithMockStaking(val1)

	// Seed a vote manager that is NOT the validator.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: "current_manager"}))

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: "yet_another_manager",
	})
	s.Require().NoError(err)

	mgr, err := s.keeper.GetVoteManager(kv)
	s.Require().NoError(err)
	s.Require().Equal("yet_another_manager", mgr.Address)
}

func (s *MsgServerTestSuite) TestSetVoteManager_NonValidatorNonManagerRejected() {
	s.SetupTest()
	s.setupWithMockStaking() // no validators in the mock

	// Seed a vote manager.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteManager(kv, &types.VoteManagerState{Address: "current_manager"}))

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    "random_address",
		NewManager: "whatever",
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

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    "random_address",
		NewManager: "whatever",
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestSetVoteManager_EmitsEvent() {
	s.SetupTest()
	val1 := testValAddr(1)
	s.setupWithMockStaking(val1)

	_, err := s.msgServer.SetVoteManager(s.ctx, &types.MsgSetVoteManager{
		Creator:    val1,
		NewManager: "zvote1manager",
	})
	s.Require().NoError(err)

	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeSetVoteManager {
			found = true
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyVoteManager {
					s.Require().Equal("zvote1manager", attr.Value)
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
	s.seedConfirmedCeremony()

	msg := validSetupMsg()
	_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "no vote manager set")
}

func (s *MsgServerTestSuite) TestCreateVotingSession_RejectedWhenCreatorNotVoteManager() {
	s.SetupTest()
	s.seedConfirmedCeremony()
	s.seedVoteManager("the_real_manager")

	msg := validSetupMsg()
	msg.Creator = "not_the_manager"
	_, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "not authorized")
}

func (s *MsgServerTestSuite) TestCreateVotingSession_SucceedsWithVoteManager() {
	s.SetupTest()
	s.seedConfirmedCeremony()
	s.seedVoteManager("zvote1admin")

	msg := validSetupMsg()
	msg.Creator = "zvote1admin"
	resp, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)
	s.Require().NotEmpty(resp.VoteRoundId)
}

func (s *MsgServerTestSuite) TestCreateVotingSession_DescriptionPersisted() {
	s.SetupTest()
	s.seedConfirmedCeremony()
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
