package keeper_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"

	"cosmossdk.io/math"
	"github.com/mikelodder7/curvey"
	"google.golang.org/protobuf/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testPallasPK generates a random valid compressed Pallas public key (32 bytes).
func testPallasPK() []byte {
	_, pk := elgamal.KeyGen(rand.Reader)
	return pk.Point.ToAffineCompressed()
}

func (s *MsgServerTestSuite) ackSignature(roundID []byte, validator string) []byte {
	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)

	h := sha256.New()
	h.Write([]byte(types.AckSigDomain))
	h.Write(round.EaPk)
	h.Write([]byte(validator))
	return h.Sum(nil)
}

var testValoperAddr = svtest.TestValAddr

// registerValidators is a test helper that registers N validators and returns
// the stored valoper addresses and their Pallas public keys.
// It sends account addresses as msg.Creator; the keeper converts them to valoper
// before storing, so the returned addrs are in valoper format and can be used
// directly in DealerPayloads and AckExecutiveAuthorityKey.Creator.
func (s *MsgServerTestSuite) registerValidators(n int) (addrs []string, pks [][]byte) {
	for i := 0; i < n; i++ {
		seed := byte(i + 1)
		pk := testPallasPK()
		_, err := s.msgServer.RegisterPallasKey(s.ctx, &types.MsgRegisterPallasKey{
			Creator:  testAccAddr(seed),
			PallasPk: pk,
		})
		s.Require().NoError(err)
		addrs = append(addrs, testValoperAddr(seed)) // valoper form stored in state
		pks = append(pks, pk)
	}
	return
}

// createPendingRound creates a PENDING VoteRound with the given ceremony
// validators directly in the store, bypassing CreateVotingSession (which
// requires a staking keeper). Returns the round ID.
func (s *MsgServerTestSuite) createPendingRound(validators []*types.ValidatorPallasKey) []byte {
	roundID := make([]byte, 32)
	rand.Read(roundID)
	kv := s.keeper.OpenKVStore(s.ctx)
	round := &types.VoteRound{
		VoteRoundId:        roundID,
		VoteEndTime:        2_000_000,
		Creator:            "sv1creator",
		Status:             types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: validators,
		NullifierImtRoot:   bytes.Repeat([]byte{0x03}, 32),
		NcRoot:             bytes.Repeat([]byte{0x04}, 32),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "A", Description: "A", Options: svtest.DefaultOptions()},
		},
	}
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))
	return roundID
}

// createPendingRoundWithValidators registers n validators in the global registry
// and creates a PENDING round with them as ceremony validators.
// Returns (roundID, valoper addresses, pallas PKs).
func (s *MsgServerTestSuite) createPendingRoundWithValidators(n int) (roundID []byte, addrs []string, pks [][]byte) {
	addrs, pks = s.registerValidators(n)
	validators := make([]*types.ValidatorPallasKey, n)
	for i := range addrs {
		validators[i] = &types.ValidatorPallasKey{
			ValidatorAddress: addrs[i],
			PallasPk:         pks[i],
		}
	}
	roundID = s.createPendingRound(validators)
	return
}

// dealPendingRound creates a PENDING round with n validators, completes DKG via
// n MsgContributeDKG calls, and returns (roundID, validator addrs). The round is
// left in DEALT status.
func (s *MsgServerTestSuite) dealPendingRound(n int) (roundID []byte, addrs []string) {
	roundID, addrs, _ = s.createPendingRoundWithValidators(n)
	threshold := (n + 1) / 2
	if n == 1 {
		threshold = 1
	} else if threshold < 2 {
		threshold = 2
	}
	for i := 0; i < n; i++ {
		s.setBlockProposer(addrs[i])
		var payloads []*types.DealerPayload
		if n > 1 {
			payloads = makeDKGPayloads(addrs, addrs[i])
		}
		_, err := s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
			Creator:            addrs[i],
			VoteRoundId:        roundID,
			FeldmanCommitments: makeDKGCommitments(threshold),
			Payloads:           payloads,
		})
		s.Require().NoError(err)
	}
	return
}

// ===========================================================================
// MsgRegisterPallasKey handler tests
// ===========================================================================

func (s *MsgServerTestSuite) TestRegisterPallasKey_HappyPath() {
	s.SetupTest()

	pks := []struct {
		creator    string // account address sent as msg.Creator
		storedAddr string // valoper address stored in global registry after conversion
		pk         []byte
	}{
		{testAccAddr(1), testValoperAddr(1), testPallasPK()},
		{testAccAddr(2), testValoperAddr(2), testPallasPK()},
		{testAccAddr(3), testValoperAddr(3), testPallasPK()},
	}

	for i, tc := range pks {
		_, err := s.msgServer.RegisterPallasKey(s.ctx, &types.MsgRegisterPallasKey{
			Creator:  tc.creator,
			PallasPk: tc.pk,
		})
		s.Require().NoError(err, "registration %d", i)

		// Verify entry in global Pallas PK registry.
		kv := s.keeper.OpenKVStore(s.ctx)
		vpk, err := s.keeper.GetPallasKey(kv, tc.storedAddr)
		s.Require().NoError(err)
		s.Require().NotNil(vpk)
		s.Require().Equal(tc.storedAddr, vpk.ValidatorAddress)
		s.Require().Equal(tc.pk, vpk.PallasPk)
	}

	// Verify event was emitted for each registration.
	var eventCount int
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeRegisterPallasKey {
			eventCount++
		}
	}
	s.Require().Equal(len(pks), eventCount, "expected one event per registration")
}

func (s *MsgServerTestSuite) TestRegisterPallasKey_Rejects() {
	tests := []struct {
		name        string
		setup       func() // optional: pre-seed ceremony state
		msg         *types.MsgRegisterPallasKey
		errContains string
	}{
		{
			name: "wrong size (16 bytes)",
			msg: &types.MsgRegisterPallasKey{
				Creator:  testAccAddr(1),
				PallasPk: bytes.Repeat([]byte{0x01}, 16),
			},
			errContains: "invalid pallas point",
		},
		{
			name: "wrong size (64 bytes)",
			msg: &types.MsgRegisterPallasKey{
				Creator:  testAccAddr(1),
				PallasPk: bytes.Repeat([]byte{0x01}, 64),
			},
			errContains: "invalid pallas point",
		},
		{
			name: "identity point (all zeros)",
			msg: &types.MsgRegisterPallasKey{
				Creator:  testAccAddr(1),
				PallasPk: make([]byte, 32),
			},
			errContains: "invalid pallas point",
		},
		{
			name: "off-curve point",
			msg: &types.MsgRegisterPallasKey{
				Creator:  testAccAddr(1),
				PallasPk: bytes.Repeat([]byte{0xFF}, 32),
			},
			errContains: "invalid pallas point",
		},
		{
			name: "invalid creator address",
			msg: &types.MsgRegisterPallasKey{
				Creator:  "not-a-bech32-address",
				PallasPk: testPallasPK(),
			},
			errContains: "invalid creator address",
		},
		{
			name: "duplicate validator address",
			setup: func() {
				_, err := s.msgServer.RegisterPallasKey(s.ctx, &types.MsgRegisterPallasKey{
					Creator:  testAccAddr(1),
					PallasPk: testPallasPK(),
				})
				s.Require().NoError(err)
			},
			msg: &types.MsgRegisterPallasKey{
				Creator:  testAccAddr(1), // same account → same valoper → duplicate
				PallasPk: testPallasPK(),
			},
			errContains: "already registered",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			_, err := s.msgServer.RegisterPallasKey(s.ctx, tc.msg)
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}

// TestRegisterPallasKey_GlobalRegistry verifies that registration goes to the
// global Pallas PK registry and is independent of any ceremony state.
func (s *MsgServerTestSuite) TestRegisterPallasKey_GlobalRegistry() {
	s.SetupTest()

	pk := testPallasPK()
	_, err := s.msgServer.RegisterPallasKey(s.ctx, &types.MsgRegisterPallasKey{
		Creator:  testAccAddr(1),
		PallasPk: pk,
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	vpk, err := s.keeper.GetPallasKey(kv, testValoperAddr(1))
	s.Require().NoError(err)
	s.Require().NotNil(vpk)
	s.Require().Equal(testValoperAddr(1), vpk.ValidatorAddress)
	s.Require().Equal(pk, vpk.PallasPk)
}

// ===========================================================================
// MsgAckExecutiveAuthorityKey handler tests
// ===========================================================================

func (s *MsgServerTestSuite) TestAckExecutiveAuthorityKey_HappyPath() {
	s.SetupTest()
	roundID, addrs := s.dealPendingRound(3)

	s.setBlockProposer(addrs[0])
	_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[0]),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)

	s.setBlockProposer(addrs[1])
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[1],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[1]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)

	s.setBlockProposer(addrs[2])
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[2],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[2]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
	s.Require().Len(round.CeremonyValidators, 3)
	s.Require().Len(round.CeremonyAcks, 3)
	s.Require().Equal(addrs[0], round.CeremonyAcks[0].ValidatorAddress)
	s.Require().Equal(uint64(s.ctx.BlockHeight()), round.CeremonyAcks[0].AckHeight)

	var ackEvents int
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeAckExecutiveAuthorityKey {
			ackEvents++
		}
	}
	s.Require().Equal(3, ackEvents)
}

func (s *MsgServerTestSuite) TestAckExecutiveAuthorityKey_PartialAcks() {
	s.SetupTest()
	roundID, addrs := s.dealPendingRound(4)

	s.setBlockProposer(addrs[0])
	_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[0]),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)

	s.setBlockProposer(addrs[1])
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[1],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[1]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
	s.Require().Len(round.CeremonyValidators, 4)
	s.Require().Len(round.CeremonyAcks, 2)
}

func (s *MsgServerTestSuite) TestAckExecutiveAuthorityKey_Rejects() {
	tests := []struct {
		name        string
		setup       func() (roundID []byte, addrs []string)
		msg         func(roundID []byte, addrs []string) *types.MsgAckExecutiveAuthorityKey
		errContains string
	}{
		{
			name: "round not found",
			setup: func() ([]byte, []string) {
				return bytes.Repeat([]byte{0xDE}, 32), nil
			},
			msg: func(roundID []byte, _ []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      "val1",
					VoteRoundId:  roundID,
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "vote round not found",
		},
		{
			name: "ceremony still REGISTERING",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					VoteRoundId:  roundID,
					AckSignature: s.ackSignature(roundID, addrs[0]),
				}
			},
			errContains: "ceremony is CEREMONY_STATUS_REGISTERING",
		},
		{
			name: "ceremony already CONFIRMED (round ACTIVE)",
			setup: func() ([]byte, []string) {
				roundID, addrs := s.dealPendingRound(2)
				kv := s.keeper.OpenKVStore(s.ctx)
				round, _ := s.keeper.GetVoteRound(kv, roundID)
				round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
				round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
				s.Require().NoError(s.keeper.SetVoteRound(kv, round))
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					VoteRoundId:  roundID,
					AckSignature: s.ackSignature(roundID, addrs[0]),
				}
			},
			errContains: "round is SESSION_STATUS_ACTIVE",
		},
		{
			name: "non-registered validator",
			setup: func() ([]byte, []string) {
				return s.dealPendingRound(2)
			},
			msg: func(roundID []byte, _ []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      "outsider",
					VoteRoundId:  roundID,
					AckSignature: s.ackSignature(roundID, "outsider"),
				}
			},
			errContains: "validator not in ceremony",
		},
		{
			name: "duplicate ack",
			setup: func() ([]byte, []string) {
				roundID, addrs := s.dealPendingRound(4)
				s.setBlockProposer(addrs[0])
				_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					VoteRoundId:  roundID,
					AckSignature: s.ackSignature(roundID, addrs[0]),
				})
				s.Require().NoError(err)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					VoteRoundId:  roundID,
					AckSignature: s.ackSignature(roundID, addrs[0]),
				}
			},
			errContains: "already acknowledged",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			roundID, addrs := tc.setup()
			msg := tc.msg(roundID, addrs)
			s.setBlockProposer(msg.Creator)
			_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, msg)
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}

// ===========================================================================
// Ceremony log tests
// ===========================================================================

func (s *MsgServerTestSuite) TestCeremonyLog_DKGAndAck() {
	s.SetupTest()
	roundID, addrs := s.dealPendingRound(3)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 3, "expected 3 DKG log lines before acks")
	s.Require().Contains(round.CeremonyLog[0], "DKG contribution from")
	s.Require().Contains(round.CeremonyLog[1], "DKG contribution from")
	s.Require().Contains(round.CeremonyLog[2], "DKG complete")
	s.Require().Contains(round.CeremonyLog[2], "ea_pk=")

	s.setBlockProposer(addrs[0])
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[0]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 4, "expected DKG logs + first ack")
	s.Require().Contains(round.CeremonyLog[3], "ack from")
	s.Require().Contains(round.CeremonyLog[3], "1/3 acked")

	for _, addr := range addrs[1:] {
		s.setBlockProposer(addr)
		_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
			Creator:      addr,
			VoteRoundId:  roundID,
			AckSignature: s.ackSignature(roundID, addr),
		})
		s.Require().NoError(err)
	}

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 7, "DKG(3) + 3 acks + confirm")
	s.Require().Contains(round.CeremonyLog[5], "3/3 acked")
	s.Require().Contains(round.CeremonyLog[6], "ceremony confirmed")
	s.Require().Contains(round.CeremonyLog[6], "round ACTIVE")
}

func (s *MsgServerTestSuite) TestCeremonyLog_PartialAcksNoConfirm() {
	s.SetupTest()
	roundID, addrs := s.dealPendingRound(4)

	s.setBlockProposer(addrs[0])
	_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[0]),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 5, "DKG(4) + first ack")
	s.Require().Contains(round.CeremonyLog[4], "1/4 acked")

	s.setBlockProposer(addrs[1])
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[1],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[1]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 6)
	s.Require().Contains(round.CeremonyLog[5], "2/4 acked")
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
}

// ===========================================================================
// CreateValidatorWithPallasKey tests
// ===========================================================================

// validStakingMsgBytes builds a valid MsgCreateValidator and marshals it to
// gogoproto binary format, the same encoding used in production.
func validStakingMsgBytes() ([]byte, string) {
	pk := ed25519.GenPrivKey().PubKey()
	valAddr := "svvaloper1testval"

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
		Value:             sdk.NewInt64Coin("usvote", 1000000),
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

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_InvalidPallasPk() {
	stakingMsgBytes, _ := validStakingMsgBytes()

	tests := []struct {
		name        string
		pallasPk    []byte
		errContains string
	}{
		{"wrong size (16 bytes)", bytes.Repeat([]byte{0x01}, 16), "invalid pallas point"},
		{"wrong size (64 bytes)", bytes.Repeat([]byte{0x01}, 64), "invalid pallas point"},
		{"identity point (all zeros)", make([]byte, 32), "invalid pallas point"},
		{"off-curve point", bytes.Repeat([]byte{0xFF}, 32), "invalid pallas point"},
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
	s.SetupTest()
	stakingMsgBytes, _ := validStakingMsgBytes()
	pallasPk := testPallasPK()
	s.verifyStakingMsgRoundTrip(stakingMsgBytes)

	_, err := s.msgServer.CreateValidatorWithPallasKey(s.ctx, &types.MsgCreateValidatorWithPallasKey{
		StakingMsg: stakingMsgBytes,
		PallasPk:   pallasPk,
	})
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "staking keeper is not *stakingkeeper.Keeper")
}

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_StakingMsgDecode() {
	s.SetupTest()
	stakingMsgBytes, _ := validStakingMsgBytes()
	pallasPk := testPallasPK()

	_, err := s.msgServer.CreateValidatorWithPallasKey(s.ctx, &types.MsgCreateValidatorWithPallasKey{
		StakingMsg: stakingMsgBytes,
		PallasPk:   pallasPk,
	})

	s.Require().Error(err)
	s.Require().NotContains(err.Error(), "failed to decode staking_msg")
	s.Require().NotContains(err.Error(), "failed to unpack staking_msg pubkey")
	s.Require().Contains(err.Error(), "staking keeper is not *stakingkeeper.Keeper")
}

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_StakingMsgValidatorAddress() {
	s.SetupTest()
	stakingMsgBytes, valAddr := validStakingMsgBytes()

	stakingMsg := &stakingtypes.MsgCreateValidator{}
	s.Require().NoError(stakingMsg.Unmarshal(stakingMsgBytes))
	s.Require().Equal(valAddr, stakingMsg.ValidatorAddress)
	s.Require().NotNil(stakingMsg.Pubkey)
	s.Require().Equal("test-validator", stakingMsg.Description.Moniker)
}

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_ProtobufRoundTrip() {
	s.SetupTest()
	stakingMsgBytes, _ := validStakingMsgBytes()
	pallasPk := testPallasPK()

	original := &types.MsgCreateValidatorWithPallasKey{
		StakingMsg: stakingMsgBytes,
		PallasPk:   pallasPk,
	}

	s.Require().NotNil(original.ProtoReflect(), "should have ProtoReflect (protoc-generated type)")

	bz, err := proto.Marshal(original)
	s.Require().NoError(err)
	s.Require().NotEmpty(bz)

	decoded := &types.MsgCreateValidatorWithPallasKey{}
	s.Require().NoError(proto.Unmarshal(bz, decoded))

	s.Require().Equal(original.StakingMsg, decoded.StakingMsg)
	s.Require().Equal(original.PallasPk, decoded.PallasPk)
}

func (s *MsgServerTestSuite) TestCreateValidatorWithPallasKey_ProtoReflectFullName() {
	msg := &types.MsgCreateValidatorWithPallasKey{}
	s.Require().Equal(
		"svote.v1.MsgCreateValidatorWithPallasKey",
		string(msg.ProtoReflect().Descriptor().FullName()),
	)
}

// ===========================================================================
// MsgContributeDKG handler tests
// ===========================================================================

// makeDKGPayloads builds valid DealerPayloads for all addresses except excludeAddr.
func makeDKGPayloads(allAddrs []string, excludeAddr string) []*types.DealerPayload {
	var payloads []*types.DealerPayload
	for i, addr := range allAddrs {
		if addr == excludeAddr {
			continue
		}
		payloads = append(payloads, &types.DealerPayload{
			ValidatorAddress: addr,
			EphemeralPk:      testPallasPK(),
			Ciphertext:       bytes.Repeat([]byte{byte(i + 1)}, 48),
		})
	}
	return payloads
}

// makeDKGCommitments generates t valid Pallas points to use as Feldman commitments.
func makeDKGCommitments(t int) [][]byte {
	c := make([][]byte, t)
	for i := range c {
		c[i] = testPallasPK()
	}
	return c
}

func (s *MsgServerTestSuite) TestContributeDKG_HappyPath_SingleValidator() {
	s.SetupTest()

	roundID, addrs, _ := s.createPendingRoundWithValidators(1)
	s.setBlockProposer(addrs[0])

	_, err := s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
		Creator:            addrs[0],
		VoteRoundId:        roundID,
		FeldmanCommitments: makeDKGCommitments(1),
		Payloads:           nil,
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
	s.Require().Len(round.DkgContributions, 1)
	s.Require().EqualValues(1, round.Threshold)
	s.Require().Len(round.FeldmanCommitments, 1)
	s.Require().NotEmpty(round.EaPk)
	s.Require().EqualValues(1, round.CeremonyValidators[0].ShamirIndex)
	s.Require().Equal(uint64(s.ctx.BlockTime().Unix()), round.CeremonyPhaseStart)
}

func (s *MsgServerTestSuite) TestContributeDKG_PartialAccumulation() {
	s.SetupTest()

	roundID, addrs, _ := s.createPendingRoundWithValidators(3)

	// First contribution: stays REGISTERING.
	s.setBlockProposer(addrs[0])
	_, err := s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
		Creator:            addrs[0],
		VoteRoundId:        roundID,
		FeldmanCommitments: makeDKGCommitments(2),
		Payloads:           makeDKGPayloads(addrs, addrs[0]),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus)
	s.Require().Len(round.DkgContributions, 1)
	s.Require().Empty(round.EaPk, "ea_pk must not be set before final contribution")
	s.Require().Empty(round.FeldmanCommitments, "combined commitments must not be set yet")

	// Second contribution: still REGISTERING (need 3).
	s.setBlockProposer(addrs[1])
	_, err = s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
		Creator:            addrs[1],
		VoteRoundId:        roundID,
		FeldmanCommitments: makeDKGCommitments(2),
		Payloads:           makeDKGPayloads(addrs, addrs[1]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus)
	s.Require().Len(round.DkgContributions, 2)

	// Third contribution: transitions to DEALT.
	s.setBlockProposer(addrs[2])
	_, err = s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
		Creator:            addrs[2],
		VoteRoundId:        roundID,
		FeldmanCommitments: makeDKGCommitments(2),
		Payloads:           makeDKGPayloads(addrs, addrs[2]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Len(round.DkgContributions, 3)
	s.Require().NotEmpty(round.EaPk)
	s.Require().Len(round.FeldmanCommitments, 2)
	s.Require().EqualValues(2, round.Threshold)

	for i, v := range round.CeremonyValidators {
		s.Require().EqualValues(i+1, v.ShamirIndex, "ShamirIndex for validator %d", i)
	}
}

func (s *MsgServerTestSuite) TestContributeDKG_FinalComputesCorrectCombinedCommitments() {
	s.SetupTest()

	G := elgamal.PallasGenerator()
	const numValidators = 3
	const threshold = 2

	addrs := make([]string, numValidators)
	ceremonyVals := make([]*types.ValidatorPallasKey, numValidators)
	for i := range addrs {
		addrs[i] = testValoperAddr(byte(i + 1))
		ceremonyVals[i] = &types.ValidatorPallasKey{
			ValidatorAddress: addrs[i],
			PallasPk:         testPallasPK(),
		}
	}
	roundID := s.createPendingRound(ceremonyVals)

	allCoeffs := make([][]shamir.Share, numValidators)
	allCommitmentPts := make([][]curvey.Point, numValidators)
	allFeldmanBytes := make([][][]byte, numValidators)
	secrets := make([]curvey.Scalar, numValidators)

	for i := 0; i < numValidators; i++ {
		sk, _ := elgamal.KeyGen(rand.Reader)
		secrets[i] = sk.Scalar
		shares, coeffs, err := shamir.Split(sk.Scalar, threshold, numValidators)
		s.Require().NoError(err)
		_ = shares
		allCoeffs[i] = nil

		commitPts, err := shamir.FeldmanCommit(G, coeffs)
		s.Require().NoError(err)
		allCommitmentPts[i] = commitPts

		feldmanBytes := make([][]byte, threshold)
		for j, c := range commitPts {
			feldmanBytes[j] = c.ToAffineCompressed()
		}
		allFeldmanBytes[i] = feldmanBytes
	}

	for i, addr := range addrs {
		s.setBlockProposer(addr)
		_, err := s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
			Creator:            addr,
			VoteRoundId:        roundID,
			FeldmanCommitments: allFeldmanBytes[i],
			Payloads:           makeDKGPayloads(addrs, addr),
		})
		s.Require().NoError(err)
	}

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)

	expectedCombined, err := shamir.CombineCommitments(allCommitmentPts)
	s.Require().NoError(err)

	for j, c := range expectedCombined {
		s.Require().Equal(c.ToAffineCompressed(), round.FeldmanCommitments[j],
			"combined Feldman commitment[%d] must match", j)
	}

	expectedEaPk := expectedCombined[0].ToAffineCompressed()
	s.Require().Equal(expectedEaPk, round.EaPk, "ea_pk must equal combined[0]")

	var secretSum curvey.Scalar
	for _, sec := range secrets {
		if secretSum == nil {
			secretSum = sec
		} else {
			secretSum = secretSum.Add(sec)
		}
	}
	expectedPK := G.Mul(secretSum).ToAffineCompressed()
	s.Require().Equal(expectedPK, round.EaPk, "ea_pk must equal (sum of secrets)*G")
}

func (s *MsgServerTestSuite) TestContributeDKG_EmitsEvent() {
	s.SetupTest()

	roundID, addrs, _ := s.createPendingRoundWithValidators(1)
	s.setBlockProposer(addrs[0])

	_, err := s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
		Creator:            addrs[0],
		VoteRoundId:        roundID,
		FeldmanCommitments: makeDKGCommitments(1),
		Payloads:           nil,
	})
	s.Require().NoError(err)

	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeContributeDKG {
			found = true
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyValidatorAddress {
					s.Require().Equal(addrs[0], attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeContributeDKG)
}

func (s *MsgServerTestSuite) TestContributeDKG_CeremonyLog() {
	s.SetupTest()

	roundID, addrs, _ := s.createPendingRoundWithValidators(2)

	s.setBlockProposer(addrs[0])
	_, err := s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
		Creator:            addrs[0],
		VoteRoundId:        roundID,
		FeldmanCommitments: makeDKGCommitments(2),
		Payloads:           makeDKGPayloads(addrs, addrs[0]),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 1)
	s.Require().Contains(round.CeremonyLog[0], "DKG contribution from")
	s.Require().Contains(round.CeremonyLog[0], "1/2")

	s.setBlockProposer(addrs[1])
	_, err = s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
		Creator:            addrs[1],
		VoteRoundId:        roundID,
		FeldmanCommitments: makeDKGCommitments(2),
		Payloads:           makeDKGPayloads(addrs, addrs[1]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 2)
	s.Require().Contains(round.CeremonyLog[1], "DKG complete")
	s.Require().Contains(round.CeremonyLog[1], "ea_pk=")
}

func (s *MsgServerTestSuite) TestContributeDKG_Rejects() {
	tests := []struct {
		name        string
		setup       func() (roundID []byte, addrs []string)
		msg         func(roundID []byte, addrs []string) *types.MsgContributeDKG
		errContains string
	}{
		{
			name: "round not found",
			setup: func() ([]byte, []string) {
				return bytes.Repeat([]byte{0xDE}, 32), nil
			},
			msg: func(roundID []byte, _ []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:     "val1",
					VoteRoundId: roundID,
				}
			},
			errContains: "vote round not found",
		},
		{
			name: "ceremony already DEALT",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				kv := s.keeper.OpenKVStore(s.ctx)
				round, _ := s.keeper.GetVoteRound(kv, roundID)
				round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_DEALT
				s.Require().NoError(s.keeper.SetVoteRound(kv, round))
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:     addrs[0],
					VoteRoundId: roundID,
				}
			},
			errContains: "ceremony is CEREMONY_STATUS_DEALT",
		},
		{
			name: "round is ACTIVE (not PENDING)",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				kv := s.keeper.OpenKVStore(s.ctx)
				round, _ := s.keeper.GetVoteRound(kv, roundID)
				round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
				s.Require().NoError(s.keeper.SetVoteRound(kv, round))
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:     addrs[0],
					VoteRoundId: roundID,
				}
			},
			errContains: "round is SESSION_STATUS_ACTIVE",
		},
		{
			name: "no validators in round ceremony",
			setup: func() ([]byte, []string) {
				roundID := s.createPendingRound(nil)
				return roundID, nil
			},
			msg: func(roundID []byte, _ []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:     "val1",
					VoteRoundId: roundID,
				}
			},
			errContains: "no validators in round ceremony",
		},
		{
			name: "non-registered validator",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, _ []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:     "outsider",
					VoteRoundId: roundID,
				}
			},
			errContains: "is not a ceremony validator",
		},
		{
			name: "duplicate contribution",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(3)
				s.setBlockProposer(addrs[0])
				_, err := s.msgServer.ContributeDKG(s.ctx, &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(2),
					Payloads:           makeDKGPayloads(addrs, addrs[0]),
				})
				s.Require().NoError(err)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(2),
					Payloads:           makeDKGPayloads(addrs, addrs[0]),
				}
			},
			errContains: "already contributed",
		},
		{
			name: "wrong Feldman commitment count",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(3)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(1),
					Payloads:           makeDKGPayloads(addrs, addrs[0]),
				}
			},
			errContains: "expected 2 Feldman commitments, got 1",
		},
		{
			name: "invalid Feldman commitment point",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				commitments := makeDKGCommitments(2)
				commitments[1] = bytes.Repeat([]byte{0xFF}, 32)
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: commitments,
					Payloads:           makeDKGPayloads(addrs, addrs[0]),
				}
			},
			errContains: "invalid pallas point",
		},
		{
			name: "payload count mismatch (too few)",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(3)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(2),
					Payloads:           makeDKGPayloads(addrs, addrs[0])[:1],
				}
			},
			errContains: "got 1 payloads, expected 2",
		},
		{
			name: "payload includes contributor's own address",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				payloads := []*types.DealerPayload{{
					ValidatorAddress: addrs[0],
					EphemeralPk:      testPallasPK(),
					Ciphertext:       bytes.Repeat([]byte{0x01}, 48),
				}}
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(2),
					Payloads:           payloads,
				}
			},
			errContains: "must not include contributor's own address",
		},
		{
			name: "payload references unknown validator",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				payloads := []*types.DealerPayload{{
					ValidatorAddress: "unknown_val",
					EphemeralPk:      testPallasPK(),
					Ciphertext:       bytes.Repeat([]byte{0x01}, 48),
				}}
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(2),
					Payloads:           payloads,
				}
			},
			errContains: "unknown validator",
		},
		{
			name: "duplicate payload for same validator",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(3)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				payloads := []*types.DealerPayload{
					{ValidatorAddress: addrs[1], EphemeralPk: testPallasPK(), Ciphertext: bytes.Repeat([]byte{0x01}, 48)},
					{ValidatorAddress: addrs[1], EphemeralPk: testPallasPK(), Ciphertext: bytes.Repeat([]byte{0x02}, 48)},
				}
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(2),
					Payloads:           payloads,
				}
			},
			errContains: "duplicate payload",
		},
		{
			name: "invalid ephemeral_pk in payload",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgContributeDKG {
				payloads := []*types.DealerPayload{{
					ValidatorAddress: addrs[1],
					EphemeralPk:      make([]byte, 32),
					Ciphertext:       bytes.Repeat([]byte{0x01}, 48),
				}}
				return &types.MsgContributeDKG{
					Creator:            addrs[0],
					VoteRoundId:        roundID,
					FeldmanCommitments: makeDKGCommitments(2),
					Payloads:           payloads,
				}
			},
			errContains: "invalid pallas point",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			roundID, addrs := tc.setup()
			msg := tc.msg(roundID, addrs)
			if len(addrs) > 0 && msg.Creator == "" {
				msg.Creator = addrs[0]
			}
			s.setBlockProposer(msg.Creator)
			_, err := s.msgServer.ContributeDKG(s.ctx, msg)
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}
