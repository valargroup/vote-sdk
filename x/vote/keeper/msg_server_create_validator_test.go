package keeper_test

import (
	"bytes"

	"cosmossdk.io/math"
	"google.golang.org/protobuf/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// validStakingMsgBytes builds a valid MsgCreateValidator and marshals it to
// gogoproto binary format, the same encoding used in production.
func validStakingMsgBytes() ([]byte, string) {
	pk := ed25519.GenPrivKey().PubKey()
	valAddr := "zvotevaloper1testval"

	pkAny, err := codectypes.NewAnyWithValue(pk)
	if err != nil {
		panic(err)
	}

	msg := &stakingtypes.MsgCreateValidator{
		Description: stakingtypes.Description{
			Moniker: "test-validator",
		},
		Commission: stakingtypes.CommissionRates{
			Rate:          math.LegacyNewDecWithPrec(1, 1),
			MaxRate:       math.LegacyNewDecWithPrec(2, 1),
			MaxChangeRate: math.LegacyNewDecWithPrec(1, 2),
		},
		MinSelfDelegation: math.NewInt(1),
		ValidatorAddress:  valAddr,
		Pubkey:            pkAny,
		Value:             sdk.NewInt64Coin("uzvote", 1000000),
	}

	bz, err := msg.Marshal()
	if err != nil {
		panic(err)
	}
	return bz, valAddr
}

// verifyStakingMsgRoundTrip verifies that the staking message bytes can be
// unmarshaled back and the pubkey can be unpacked.
func (s *MsgServerTestSuite) verifyStakingMsgRoundTrip(bz []byte) {
	msg := &stakingtypes.MsgCreateValidator{}
	s.Require().NoError(msg.Unmarshal(bz))
	s.Require().NotNil(msg.Pubkey, "pubkey should be set")

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	s.Require().NoError(msg.UnpackInterfaces(registry))
	s.Require().NotNil(msg.Pubkey.GetCachedValue(), "cached pubkey should be set after unpack")
}

// ===========================================================================
// CreateValidatorWithPallasKey tests
// ===========================================================================

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_InvalidPallasPk() {
	stakingMsgBytes, _ := validStakingMsgBytes()

	tests := []struct {
		name        string
		pallasPk    []byte
		errContains string
	}{
		{
			name:        "wrong size (16 bytes)",
			pallasPk:    bytes.Repeat([]byte{0x01}, 16),
			errContains: "invalid pallas point",
		},
		{
			name:        "wrong size (64 bytes)",
			pallasPk:    bytes.Repeat([]byte{0x01}, 64),
			errContains: "invalid pallas point",
		},
		{
			name:        "identity point (all zeros)",
			pallasPk:    make([]byte, 32),
			errContains: "invalid pallas point",
		},
		{
			name:        "off-curve point",
			pallasPk:    bytes.Repeat([]byte{0xFF}, 32),
			errContains: "invalid pallas point",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			_, err := s.msgServer.CreateValidatorWithPallasKey(s.ctx, &types.MsgCreateValidatorWithPallasKey{
				StakingMsg: stakingMsgBytes,
				PallasPk:   tc.pallasPk,
			})
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_NilStakingKeeper() {
	// The default test setup creates a keeper with nil stakingKeeper.
	// A valid message should pass all validation (decode, unpack, pallas_pk)
	// but fail at the staking call because nil can't be cast to
	// *stakingkeeper.Keeper.
	s.SetupTest()

	stakingMsgBytes, _ := validStakingMsgBytes()
	pallasPk := testPallasPK()

	// Verify our test staking message bytes round-trip correctly.
	s.verifyStakingMsgRoundTrip(stakingMsgBytes)

	_, err := s.msgServer.CreateValidatorWithPallasKey(s.ctx, &types.MsgCreateValidatorWithPallasKey{
		StakingMsg: stakingMsgBytes,
		PallasPk:   pallasPk,
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "staking keeper is not *stakingkeeper.Keeper")
}

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_StakingMsgDecode() {
	// Verify that the handler correctly decodes the embedded staking message
	// and unpacks the Any-wrapped pubkey. If decode or unpack failed, the
	// error message would contain "failed to decode" or "failed to unpack",
	// not "staking keeper".
	s.SetupTest()

	stakingMsgBytes, _ := validStakingMsgBytes()
	pallasPk := testPallasPK()

	_, err := s.msgServer.CreateValidatorWithPallasKey(s.ctx, &types.MsgCreateValidatorWithPallasKey{
		StakingMsg: stakingMsgBytes,
		PallasPk:   pallasPk,
	})

	// Should fail at staking call, NOT at decode/unpack.
	s.Require().Error(err)
	s.Require().NotContains(err.Error(), "failed to decode staking_msg")
	s.Require().NotContains(err.Error(), "failed to unpack staking_msg pubkey")
	s.Require().Contains(err.Error(), "staking keeper is not *stakingkeeper.Keeper")
}

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_StakingMsgValidatorAddress() {
	// Verify the staking message serialization format is compatible with
	// what the handler expects, and the validator address can be extracted.
	s.SetupTest()

	stakingMsgBytes, valAddr := validStakingMsgBytes()

	stakingMsg := &stakingtypes.MsgCreateValidator{}
	s.Require().NoError(stakingMsg.Unmarshal(stakingMsgBytes))
	s.Require().Equal(valAddr, stakingMsg.ValidatorAddress)
	s.Require().NotNil(stakingMsg.Pubkey)
	s.Require().Equal("test-validator", stakingMsg.Description.Moniker)
}

// TestCreateValidatorWithPallasKey_ProtobufRoundTrip verifies that the
// protoc-generated MsgCreateValidatorWithPallasKey type correctly serializes
// and deserializes via standard protobuf.
func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_ProtobufRoundTrip() {
	s.SetupTest()

	stakingMsgBytes, _ := validStakingMsgBytes()
	pallasPk := testPallasPK()

	original := &types.MsgCreateValidatorWithPallasKey{
		StakingMsg: stakingMsgBytes,
		PallasPk:   pallasPk,
	}

	// Verify the type has ProtoReflect (protoc-generated).
	s.Require().NotNil(original.ProtoReflect(), "should have ProtoReflect (protoc-generated type)")

	// Marshal via protov2.
	bz, err := proto.Marshal(original)
	s.Require().NoError(err)
	s.Require().NotEmpty(bz)

	// Unmarshal back.
	decoded := &types.MsgCreateValidatorWithPallasKey{}
	s.Require().NoError(proto.Unmarshal(bz, decoded))

	s.Require().Equal(original.StakingMsg, decoded.StakingMsg)
	s.Require().Equal(original.PallasPk, decoded.PallasPk)
}

// TestCreateValidatorWithPallasKey_ProtoReflectFullName verifies the correct
// protobuf full name is registered for the type.
func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_ProtoReflectFullName() {
	msg := &types.MsgCreateValidatorWithPallasKey{}
	s.Require().Equal(
		"zvote.v1.MsgCreateValidatorWithPallasKey",
		string(msg.ProtoReflect().Descriptor().FullName()),
	)
}
