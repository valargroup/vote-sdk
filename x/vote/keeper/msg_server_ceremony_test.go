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

	"github.com/valargroup/vote-sdk/crypto/ecies"
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

// dealPendingRound creates a PENDING round with n validators (n >= 2), deals,
// and returns (roundID, validator addrs). The round is left in DEALT status.
func (s *MsgServerTestSuite) dealPendingRound(n int) (roundID []byte, addrs []string) {
	roundID, addrs, _ = s.createPendingRoundWithValidators(n)
	s.setBlockProposer(addrs[0])
	t := (n + 1) / 2
	if t < 2 {
		t = 2
	}
	msg := &types.MsgDealExecutiveAuthorityKey{
		Creator:            addrs[0],
		VoteRoundId:        roundID,
		EaPk:               testPallasPK(),
		Payloads:           makePayloads(addrs),
		Threshold:          uint32(t),
		FeldmanCommitments: make([][]byte, t),
	}
	for i := range msg.FeldmanCommitments {
		msg.FeldmanCommitments[i] = testPallasPK()
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
	commitments := make([][]byte, 2) // threshold = 2, so 2 Feldman commitments
	for i := range commitments {
		commitments[i] = testPallasPK()
	}

	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:            addrs[0],
		VoteRoundId:        roundID,
		EaPk:               eaPk,
		Payloads:           payloads,
		Threshold:          2,
		FeldmanCommitments: commitments,
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
	s.Require().Len(round.FeldmanCommitments, 2)
	for i, c := range round.FeldmanCommitments {
		s.Require().Equal(commitments[i], c)
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
			name: "threshold < 1 rejected",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(2)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:            "dealer1",
					VoteRoundId:        roundID,
					EaPk:               testPallasPK(),
					Payloads:           makePayloads(addrs),
					Threshold:          0,
					FeldmanCommitments: [][]byte{testPallasPK(), testPallasPK()},
				}
			},
			errContains: "invalid threshold",
		},
		{
			name: "n>=2: wrong number of Feldman commitments",
			setup: func() ([]byte, []string) {
				roundID, addrs, _ := s.createPendingRoundWithValidators(3)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:            "dealer1",
					VoteRoundId:        roundID,
					EaPk:               testPallasPK(),
					Payloads:           makePayloads(addrs),
					Threshold:          2,
					FeldmanCommitments: [][]byte{testPallasPK()},
				}
			},
			errContains: "invalid threshold",
		},
		{
			name: "n>=2: invalid point in Feldman commitments",
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
					FeldmanCommitments: [][]byte{
						testPallasPK(),
						bytes.Repeat([]byte{0xFF}, 32),
					},
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
	shares, coeffs, err := shamir.Split(eaSk.Scalar, threshold, numValidators)
	s.Require().NoError(err)

	commitmentPts, err := shamir.FeldmanCommit(G, coeffs)
	s.Require().NoError(err)
	feldmanCommitments := make([][]byte, len(commitmentPts))
	for j, c := range commitmentPts {
		feldmanCommitments[j] = c.ToAffineCompressed()
	}

	payloads := make([]*types.DealerPayload, numValidators)
	for i, v := range validators {
		shareBytes := shares[i].Value.Bytes()
		env, err := ecies.Encrypt(G, v.pk.Point, shareBytes, rand.Reader)
		s.Require().NoError(err, "ECIES encrypt for validator %d", i)

		payloads[i] = &types.DealerPayload{
			ValidatorAddress: addrs[i],
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		}
	}

	_, err = s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:            addrs[0],
		VoteRoundId:        roundID,
		EaPk:               eaPkBytes,
		Payloads:           payloads,
		Threshold:          threshold,
		FeldmanCommitments: feldmanCommitments,
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
	s.Require().EqualValues(threshold, round.Threshold)
	s.Require().Len(round.FeldmanCommitments, threshold)

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
	}

	for j, c := range round.FeldmanCommitments {
		s.Require().Equal(feldmanCommitments[j], c,
			"stored Feldman commitment[%d] must match computed commitment", j)
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

// ===========================================================================
// F-15: Threshold-1 Ceremony Downgrade — Regression Tests
//
// These tests verify that the handler rejects deals with a threshold that
// does not match the deterministic ThresholdForN(n) policy. Before the fix,
// the handler only checked msg.Threshold < 1, allowing any value >= 1.
// ===========================================================================

// TestDealExecutiveAuthorityKey_Threshold1RejectedWithMultipleValidators
// verifies that the handler rejects Threshold=1 with n=3 validators
// (expected threshold for n=3 is ThresholdForN(3) = 2).
func (s *MsgServerTestSuite) TestDealExecutiveAuthorityKey_Threshold1RejectedWithMultipleValidators() {
	s.SetupTest()

	roundID, addrs, _ := s.createPendingRoundWithValidators(3)
	s.setBlockProposer(addrs[0])

	vks := make([][]byte, 3)
	for i := range vks {
		vks[i] = testPallasPK()
	}

	msg := &types.MsgDealExecutiveAuthorityKey{
		Creator:          addrs[0],
		VoteRoundId:      roundID,
		EaPk:             testPallasPK(),
		Payloads:         makePayloads(addrs),
		Threshold:        1,
		FeldmanCommitments: vks,
	}

	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, msg)
	s.Require().Error(err, "Threshold=1 with n=3 must be rejected")
	s.Require().Contains(err.Error(), "invalid threshold")
}

// TestDealExecutiveAuthorityKey_ThresholdDowngradeVariants verifies that
// all invalid (n, threshold) combinations are rejected by the handler.
func (s *MsgServerTestSuite) TestDealExecutiveAuthorityKey_ThresholdDowngradeVariants() {
	tests := []struct {
		name      string
		n         int
		threshold uint32
	}{
		{"n=2 t=1", 2, 1},
		{"n=3 t=1", 3, 1},
		{"n=5 t=1", 5, 1},
		{"n=10 t=1", 10, 1},
		{"n=3 t=100 (above n)", 3, 100},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			roundID, addrs, _ := s.createPendingRoundWithValidators(tc.n)
			s.setBlockProposer(addrs[0])
			vks := make([][]byte, tc.n)
			for i := range vks {
				vks[i] = testPallasPK()
			}
			msg := &types.MsgDealExecutiveAuthorityKey{
				Creator:          addrs[0],
				VoteRoundId:      roundID,
				EaPk:             testPallasPK(),
				Payloads:         makePayloads(addrs),
				Threshold:        tc.threshold,
				FeldmanCommitments: vks,
			}
			_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, msg)
			s.Require().Error(err, "Threshold=%d with n=%d must be rejected", tc.threshold, tc.n)
			s.Require().Contains(err.Error(), "invalid threshold")
		})
	}
}

// TestThresholdDowngrade_FullAttackChainRejected verifies that the full
// F-15 attack chain is blocked at the deal step. The attacker constructs
// a cryptographically valid deal with t=1 (degree-0 Shamir polynomial),
// but the handler rejects it because the threshold doesn't match
// ThresholdForN(3) = 2.
func (s *MsgServerTestSuite) TestThresholdDowngrade_FullAttackChainRejected() {
	s.SetupTest()

	const numValidators = 3
	G := elgamal.PallasGenerator()

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

	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	shares, _, err := shamir.Split(eaSk.Scalar, 1, numValidators)
	s.Require().NoError(err)

	// Verify all shares equal ea_sk (degree-0 polynomial property).
	for i, share := range shares {
		s.Require().Equal(0, share.Value.Cmp(eaSk.Scalar),
			"share[%d] should equal ea_sk with t=1", i)
	}

	payloads := make([]*types.DealerPayload, numValidators)
	vks := make([][]byte, numValidators)
	for i, v := range validators {
		shareBytes := shares[i].Value.Bytes()
		env, err := ecies.Encrypt(G, v.pk.Point, shareBytes, rand.Reader)
		s.Require().NoError(err)
		payloads[i] = &types.DealerPayload{
			ValidatorAddress: addrs[i],
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		}
		vks[i] = G.Mul(shares[i].Value).ToAffineCompressed()
	}

	// The deal is cryptographically consistent (VKs match shares, ECIES valid),
	// but the handler must reject it because threshold != ThresholdForN(3).
	s.setBlockProposer(addrs[0])
	_, err = s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:          addrs[0],
		VoteRoundId:      roundID,
		EaPk:             eaPk.Point.ToAffineCompressed(),
		Payloads:         payloads,
		Threshold:        1,
		FeldmanCommitments: vks,
	})
	s.Require().Error(err, "malicious deal with Threshold=1 must be rejected")
	s.Require().Contains(err.Error(), "invalid threshold")

	// Round must remain in REGISTERING (deal was not applied).
	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus)
}
