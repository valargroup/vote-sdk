package keeper_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"

	"cosmossdk.io/math"
	"google.golang.org/protobuf/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/shielded-vote/crypto/ecies"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/crypto/shamir"
	svtest "github.com/valargroup/shielded-vote/testutil"
	"github.com/valargroup/shielded-vote/x/vote/types"
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

// makePayloads builds valid DealerPayloads for the given validator addresses.
func makePayloads(addrs []string) []*types.DealerPayload {
	payloads := make([]*types.DealerPayload, len(addrs))
	for i, addr := range addrs {
		payloads[i] = &types.DealerPayload{
			ValidatorAddress: addr,
			EphemeralPk:      testPallasPK(),
			Ciphertext:       bytes.Repeat([]byte{byte(i + 1)}, 48),
		}
	}
	return payloads
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
		VkZkp1:             bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:             bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:             bytes.Repeat([]byte{0x08}, 64),
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

// dealPendingRound creates a PENDING round with n validators, deals, and
// returns (roundID, validator addrs). The round is left in DEALT status.
// Threshold mode is used automatically when n >= 2.
func (s *MsgServerTestSuite) dealPendingRound(n int) (roundID []byte, addrs []string) {
	roundID, addrs, _ = s.createPendingRoundWithValidators(n)
	s.setBlockProposer(addrs[0])
	msg := &types.MsgDealExecutiveAuthorityKey{
		Creator:     addrs[0],
		VoteRoundId: roundID,
		EaPk:        testPallasPK(),
		Payloads:    makePayloads(addrs),
	}
	if n >= 2 {
		t := (n + 1) / 2 // ceil(n/2) — matches thresholdForN
		if t < 2 {
			t = 2
		}
		msg.Threshold = uint32(t)
		msg.VerificationKeys = make([][]byte, n)
		for i := range msg.VerificationKeys {
			msg.VerificationKeys[i] = testPallasPK()
		}
	}
	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, msg)
	s.Require().NoError(err)
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
// MsgDealExecutiveAuthorityKey handler tests
// ===========================================================================

func (s *MsgServerTestSuite) TestDealExecutiveAuthorityKey_HappyPath() {
	s.SetupTest()

	roundID, addrs, _ := s.createPendingRoundWithValidators(3)
	s.setBlockProposer(addrs[0])
	eaPk := testPallasPK()
	payloads := makePayloads(addrs)
	vks := make([][]byte, 3)
	for i := range vks {
		vks[i] = testPallasPK()
	}

	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:          addrs[0],
		VoteRoundId:      roundID,
		EaPk:             eaPk,
		Payloads:         payloads,
		Threshold:        2,
		VerificationKeys: vks,
	})
	s.Require().NoError(err)

	// Verify round's ceremony transitioned to DEALT with all fields set.
	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
	s.Require().Equal(eaPk, round.EaPk)
	s.Require().Equal(addrs[0], round.CeremonyDealer)
	s.Require().Equal(uint64(s.ctx.BlockTime().Unix()), round.CeremonyPhaseStart)
	s.Require().Equal(types.DefaultDealTimeout, round.CeremonyPhaseTimeout)
	s.Require().Len(round.CeremonyPayloads, 3)
	for i, p := range round.CeremonyPayloads {
		s.Require().Equal(addrs[i], p.ValidatorAddress)
	}
	s.Require().EqualValues(2, round.Threshold)
	s.Require().Len(round.VerificationKeys, 3)
	for i, vk := range round.VerificationKeys {
		s.Require().Equal(vks[i], vk)
	}

	// Verify event emission.
	var found bool
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeDealExecutiveAuthorityKey {
			found = true
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyEAPK {
					s.Require().NotEmpty(attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeDealExecutiveAuthorityKey)
}

func (s *MsgServerTestSuite) TestDealExecutiveAuthorityKey_Rejects() {
	tests := []struct {
		name        string
		setup       func() (roundID []byte, addrs []string)
		msg         func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey
		errContains string
	}{
		{
			name: "round not found",
			setup: func() ([]byte, []string) {
				return bytes.Repeat([]byte{0xDE}, 32), nil
			},
			msg: func(roundID []byte, _ []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    []*types.DealerPayload{},
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
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    makePayloads(addrs),
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
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    makePayloads(addrs),
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
			msg: func(roundID []byte, _ []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    []*types.DealerPayload{},
				}
			},
			errContains: "no validators in round ceremony",
		},
		{
			name: "invalid ea_pk",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        bytes.Repeat([]byte{0xFF}, 32),
					Payloads:    makePayloads(addrs),
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
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    makePayloads(addrs[:2]),
				}
			},
			errContains: "payload count does not match",
		},
		{
			name: "payload references unknown validator",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				payloads := makePayloads(addrs)
				payloads[1].ValidatorAddress = "unknown_val"
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    payloads,
				}
			},
			errContains: "unknown validator",
		},
		{
			name: "duplicate validator in payloads",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				payloads := makePayloads(addrs)
				payloads[1].ValidatorAddress = addrs[0]
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    payloads,
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
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				payloads := makePayloads(addrs)
				payloads[0].EphemeralPk = make([]byte, 32)
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    payloads,
				}
			},
			errContains: "invalid pallas point",
		},
		{
			name: "n>=2: threshold < 2",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:          "dealer1",
					VoteRoundId:      roundID,
					EaPk:             testPallasPK(),
					Payloads:         makePayloads(addrs),
					Threshold:        1,
					VerificationKeys: [][]byte{testPallasPK(), testPallasPK()},
				}
			},
			errContains: "invalid threshold",
		},
		{
			name: "n>=2: wrong number of verification keys",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(3)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:          "dealer1",
					VoteRoundId:      roundID,
					EaPk:             testPallasPK(),
					Payloads:         makePayloads(addrs),
					Threshold:        2,
					VerificationKeys: [][]byte{testPallasPK()},
				}
			},
			errContains: "invalid threshold",
		},
		{
			name: "n>=2: invalid point in verification keys",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    makePayloads(addrs),
					Threshold:   2,
					VerificationKeys: [][]byte{
						testPallasPK(),
						bytes.Repeat([]byte{0xFF}, 32),
					},
				}
			},
			errContains: "invalid pallas point",
		},
		{
			name: "n==1: threshold must be 0",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(1)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    makePayloads(addrs),
					Threshold:   1,
				}
			},
			errContains: "invalid threshold",
		},
		{
			name: "n==1: verification_keys must be empty",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(1)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:          "dealer1",
					VoteRoundId:      roundID,
					EaPk:             testPallasPK(),
					Payloads:         makePayloads(addrs),
					Threshold:        0,
					VerificationKeys: [][]byte{testPallasPK()},
				}
			},
			errContains: "invalid threshold",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			roundID, addrs := tc.setup()
			msg := tc.msg(roundID, addrs)
			if len(addrs) > 0 {
				msg.Creator = addrs[0]
			}
			s.setBlockProposer(msg.Creator)
			_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, msg)
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
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
				roundID, addrs := s.dealPendingRound(1)
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

func (s *MsgServerTestSuite) TestCeremonyLog_DealAndAck() {
	s.SetupTest()
	roundID, addrs := s.dealPendingRound(3)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 1, "expected 1 log entry after deal")
	s.Require().Contains(round.CeremonyLog[0], "deal from")
	s.Require().Contains(round.CeremonyLog[0], "ea_pk=")

	s.setBlockProposer(addrs[0])
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[0]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 2, "expected 2 log entries after deal+ack")
	s.Require().Contains(round.CeremonyLog[1], "ack from")
	s.Require().Contains(round.CeremonyLog[1], "1/3 acked")

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
	s.Require().Len(round.CeremonyLog, 5, "expected 5 log entries after deal+3acks+confirm")
	s.Require().Contains(round.CeremonyLog[3], "3/3 acked")
	s.Require().Contains(round.CeremonyLog[4], "ceremony confirmed")
	s.Require().Contains(round.CeremonyLog[4], "round ACTIVE")
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
	s.Require().Len(round.CeremonyLog, 2)
	s.Require().Contains(round.CeremonyLog[1], "1/4 acked")

	s.setBlockProposer(addrs[1])
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[1],
		VoteRoundId:  roundID,
		AckSignature: s.ackSignature(roundID, addrs[1]),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Len(round.CeremonyLog, 3)
	s.Require().Contains(round.CeremonyLog[2], "2/4 acked")
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
}

// ===========================================================================
// Full ceremony integration test with real ECIES
// ===========================================================================

func (s *MsgServerTestSuite) TestFullCeremonyWithECIES() {
	s.SetupTest()
	G := elgamal.PallasGenerator()
	const numValidators = 3

	type validatorKeys struct {
		sk *elgamal.SecretKey
		pk *elgamal.PublicKey
	}
	validators := make([]validatorKeys, numValidators)
	addrs := make([]string, numValidators)
	ceremonyVals := make([]*types.ValidatorPallasKey, numValidators)
	for i := range validators {
		sk, pk := elgamal.KeyGen(rand.Reader)
		validators[i] = validatorKeys{sk: sk, pk: pk}
		addrs[i] = testValoperAddr(byte(i + 1))
		ceremonyVals[i] = &types.ValidatorPallasKey{
			ValidatorAddress: addrs[i],
			PallasPk:         pk.Point.ToAffineCompressed(),
		}
	}

	roundID := s.createPendingRound(ceremonyVals)
	s.setBlockProposer(addrs[0])

	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	eaPkBytes := eaPk.Point.ToAffineCompressed()
	const threshold = 2
	shares, _, err := shamir.Split(eaSk.Scalar, threshold, numValidators)
	s.Require().NoError(err)

	payloads := make([]*types.DealerPayload, numValidators)
	vks := make([][]byte, numValidators)
	for i, v := range validators {
		shareBytes := shares[i].Value.Bytes()
		env, err := ecies.Encrypt(G, v.pk.Point, shareBytes, rand.Reader)
		s.Require().NoError(err, "ECIES encrypt for validator %d", i)

		payloads[i] = &types.DealerPayload{
			ValidatorAddress: addrs[i],
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		}
		vks[i] = G.Mul(shares[i].Value).ToAffineCompressed()
	}

	_, err = s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:          addrs[0],
		VoteRoundId:      roundID,
		EaPk:             eaPkBytes,
		Payloads:         payloads,
		Threshold:        threshold,
		VerificationKeys: vks,
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
	s.Require().EqualValues(threshold, round.Threshold)
	s.Require().Len(round.VerificationKeys, numValidators)

	for i, v := range validators {
		payload := round.CeremonyPayloads[i]
		s.Require().Equal(addrs[i], payload.ValidatorAddress)

		ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
		s.Require().NoError(err, "unmarshal ephemeral_pk for validator %d", i)

		env := &ecies.Envelope{
			Ephemeral:  ephPk.Point,
			Ciphertext: payload.Ciphertext,
		}

		decryptedShare, err := ecies.Decrypt(v.sk.Scalar, env)
		s.Require().NoError(err, "ECIES decrypt for validator %d", i)
		s.Require().Equal(shares[i].Value.Bytes(), decryptedShare,
			"decrypted share mismatch for validator %d", i)

		s.Require().Equal(vks[i], round.VerificationKeys[i],
			"stored VK[%d] must match computed VK", i)
	}

	for _, addr := range addrs {
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
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
	s.Require().Equal(eaPkBytes, round.EaPk)
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
