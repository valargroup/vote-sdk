package keeper_test

import (
	"bytes"
	"crypto/rand"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/ecies"
	"github.com/z-cale/zally/crypto/elgamal"
	zallytest "github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// testPallasPK generates a random valid compressed Pallas public key (32 bytes).
func testPallasPK() []byte {
	_, pk := elgamal.KeyGen(rand.Reader)
	return pk.Point.ToAffineCompressed()
}

// testValoperAddr returns the valoper address corresponding to testAccAddr(seed).
// This is what RegisterPallasKey stores after converting account → valoper.
func testValoperAddr(seed byte) string {
	addr := make([]byte, 20)
	addr[0] = seed
	return sdk.ValAddress(addr).String()
}

// ===========================================================================
// MsgRegisterPallasKey handler tests (Step 4)
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
// Per-round ceremony helper tests
// ===========================================================================

func (s *KeeperTestSuite) TestOneThirdAcked() {
	tests := []struct {
		name   string
		round  *types.VoteRound
		expect bool
	}{
		{
			name: "all acked (3/3)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"}, {ValidatorAddress: "val3"},
				},
				CeremonyAcks: []*types.AckEntry{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"}, {ValidatorAddress: "val3"},
				},
			},
			expect: true,
		},
		{
			name: "exactly 1/3 (1 of 3)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"}, {ValidatorAddress: "val3"},
				},
				CeremonyAcks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
				},
			},
			expect: true,
		},
		{
			name: "below 1/3 (1 of 4)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"}, {ValidatorAddress: "val4"},
				},
				CeremonyAcks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
				},
			},
			expect: false,
		},
		{
			name: "2 of 4 (50% >= 33%)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"}, {ValidatorAddress: "val4"},
				},
				CeremonyAcks: []*types.AckEntry{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"},
				},
			},
			expect: true,
		},
		{
			name: "no acks",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"},
				},
			},
			expect: false,
		},
		{
			name:   "no validators",
			round:  &types.VoteRound{},
			expect: false,
		},
		{
			name: "single validator acked (1/1 >= 1/3)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{{ValidatorAddress: "val1"}},
				CeremonyAcks:       []*types.AckEntry{{ValidatorAddress: "val1"}},
			},
			expect: true,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.Require().Equal(tc.expect, keeper.OneThirdAcked(tc.round))
		})
	}
}

func (s *KeeperTestSuite) TestFindValidatorInRoundCeremony() {
	round := &types.VoteRound{
		CeremonyValidators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val_alpha"}, {ValidatorAddress: "val_beta"}, {ValidatorAddress: "val_gamma"},
		},
	}

	tests := []struct {
		name      string
		valAddr   string
		wantIndex int
		wantFound bool
	}{
		{"first", "val_alpha", 0, true},
		{"middle", "val_beta", 1, true},
		{"last", "val_gamma", 2, true},
		{"unknown", "val_delta", -1, false},
		{"empty", "", -1, false},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			idx, found := keeper.FindValidatorInRoundCeremony(round, tc.valAddr)
			s.Require().Equal(tc.wantFound, found)
			s.Require().Equal(tc.wantIndex, idx)
		})
	}
}

func (s *KeeperTestSuite) TestFindAckInRoundCeremony() {
	round := &types.VoteRound{
		CeremonyAcks: []*types.AckEntry{
			{ValidatorAddress: "val_alpha", AckHeight: 10},
			{ValidatorAddress: "val_beta", AckHeight: 11},
		},
	}

	idx, found := keeper.FindAckInRoundCeremony(round, "val_alpha")
	s.Require().True(found)
	s.Require().Equal(0, idx)

	idx, found = keeper.FindAckInRoundCeremony(round, "val_beta")
	s.Require().True(found)
	s.Require().Equal(1, idx)

	idx, found = keeper.FindAckInRoundCeremony(round, "val_gamma")
	s.Require().False(found)
	s.Require().Equal(-1, idx)
}

func (s *KeeperTestSuite) TestStripNonAckersFromRound() {
	round := &types.VoteRound{
		CeremonyValidators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val1", PallasPk: []byte{0x01}},
			{ValidatorAddress: "val2", PallasPk: []byte{0x02}},
			{ValidatorAddress: "val3", PallasPk: []byte{0x03}},
		},
		CeremonyPayloads: []*types.DealerPayload{
			{ValidatorAddress: "val1", Ciphertext: []byte{0x10}},
			{ValidatorAddress: "val2", Ciphertext: []byte{0x20}},
			{ValidatorAddress: "val3", Ciphertext: []byte{0x30}},
		},
		CeremonyAcks: []*types.AckEntry{
			{ValidatorAddress: "val1"},
			{ValidatorAddress: "val3"},
		},
	}

	keeper.StripNonAckersFromRound(round)

	s.Require().Len(round.CeremonyValidators, 2)
	s.Require().Equal("val1", round.CeremonyValidators[0].ValidatorAddress)
	s.Require().Equal("val3", round.CeremonyValidators[1].ValidatorAddress)

	s.Require().Len(round.CeremonyPayloads, 2)
	s.Require().Equal("val1", round.CeremonyPayloads[0].ValidatorAddress)
	s.Require().Equal("val3", round.CeremonyPayloads[1].ValidatorAddress)

	s.Require().Len(round.CeremonyAcks, 2)
}

// ===========================================================================
// MsgDealExecutiveAuthorityKey handler tests
// ===========================================================================

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
		Creator:            "zvote1creator",
		Status:             types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:     types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		CeremonyValidators: validators,
		NullifierImtRoot:   bytes.Repeat([]byte{0x03}, 32),
		NcRoot:             bytes.Repeat([]byte{0x04}, 32),
		VkZkp1:             bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:             bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:             bytes.Repeat([]byte{0x08}, 64),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "A", Description: "A", Options: zallytest.DefaultOptions()},
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

func (s *MsgServerTestSuite) TestDealExecutiveAuthorityKey_HappyPath() {
	s.SetupTest()

	roundID, addrs, _ := s.createPendingRoundWithValidators(3)
	eaPk := testPallasPK()
	payloads := makePayloads(addrs)

	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:     "dealer1",
		VoteRoundId: roundID,
		EaPk:        eaPk,
		Payloads:    payloads,
	})
	s.Require().NoError(err)

	// Verify round's ceremony transitioned to DEALT with all fields set.
	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
	s.Require().Equal(eaPk, round.EaPk)
	s.Require().Equal("dealer1", round.CeremonyDealer)
	s.Require().Equal(uint64(s.ctx.BlockTime().Unix()), round.CeremonyPhaseStart)
	s.Require().Equal(types.DefaultDealTimeout, round.CeremonyPhaseTimeout)
	s.Require().Len(round.CeremonyPayloads, 3)
	for i, p := range round.CeremonyPayloads {
		s.Require().Equal(addrs[i], p.ValidatorAddress)
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
				// Force ceremony to DEALT.
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
					EaPk:        bytes.Repeat([]byte{0xFF}, 32), // off-curve
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
				payloads[1].ValidatorAddress = addrs[0] // duplicate
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
				payloads[0].EphemeralPk = make([]byte, 32) // identity point
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:     "dealer1",
					VoteRoundId: roundID,
					EaPk:        testPallasPK(),
					Payloads:    payloads,
				}
			},
			errContains: "invalid pallas point",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			roundID, addrs := tc.setup()
			_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, tc.msg(roundID, addrs))
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}

// ===========================================================================
// MsgAckExecutiveAuthorityKey handler tests (Step 6)
// ===========================================================================

// dealPendingRound creates a PENDING round with n validators, deals, and
// returns (roundID, validator addrs). The round is left in DEALT status.
func (s *MsgServerTestSuite) dealPendingRound(n int) (roundID []byte, addrs []string) {
	roundID, addrs, _ = s.createPendingRoundWithValidators(n)
	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:     "dealer",
		VoteRoundId: roundID,
		EaPk:        testPallasPK(),
		Payloads:    makePayloads(addrs),
	})
	s.Require().NoError(err)
	return
}

func (s *MsgServerTestSuite) TestAckExecutiveAuthorityKey_HappyPath() {
	s.SetupTest()
	roundID, addrs := s.dealPendingRound(3)

	// First ack: with 3 validators, 1/3 is already met (1*3 >= 3).
	// So the first ack triggers CONFIRMED + ACTIVE.
	_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: bytes.Repeat([]byte{0xAC}, 64),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
	// StripNonAckers keeps only the one who acked.
	s.Require().Len(round.CeremonyAcks, 1)
	s.Require().Equal(addrs[0], round.CeremonyAcks[0].ValidatorAddress)
	s.Require().Equal(uint64(s.ctx.BlockHeight()), round.CeremonyAcks[0].AckHeight)

	// Verify event emission.
	var ackEvents int
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeAckExecutiveAuthorityKey {
			ackEvents++
		}
	}
	s.Require().Equal(1, ackEvents)
}

// TestAckExecutiveAuthorityKey_MultipleAcks tests a scenario where 1/3
// threshold requires more than 1 ack (4 validators, need 2).
func (s *MsgServerTestSuite) TestAckExecutiveAuthorityKey_MultipleAcks() {
	s.SetupTest()
	roundID, addrs := s.dealPendingRound(4)

	// First ack: 1 of 4 → 1*3=3 < 4, stays DEALT.
	_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: bytes.Repeat([]byte{0xAC}, 64),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)

	// Second ack: 2 of 4 → 2*3=6 >= 4, triggers CONFIRMED + ACTIVE.
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[1],
		VoteRoundId:  roundID,
		AckSignature: bytes.Repeat([]byte{0xAC}, 64),
	})
	s.Require().NoError(err)

	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
	// StripNonAckers: only addrs[0] and addrs[1] remain.
	s.Require().Len(round.CeremonyValidators, 2)
	s.Require().Len(round.CeremonyPayloads, 2)
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
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "ceremony is CEREMONY_STATUS_REGISTERING",
		},
		{
			name: "ceremony already CONFIRMED (round ACTIVE)",
			setup: func() ([]byte, []string) {
				roundID, addrs := s.dealPendingRound(1)
				// Force to CONFIRMED + ACTIVE.
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
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
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
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "validator not in ceremony",
		},
		{
			name: "duplicate ack",
			setup: func() ([]byte, []string) {
				// Use 4 validators so 1 ack doesn't trigger confirmation.
				roundID, addrs := s.dealPendingRound(4)
				_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					VoteRoundId:  roundID,
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				})
				s.Require().NoError(err)
				return roundID, addrs
			},
			msg: func(roundID []byte, addrs []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0], // same validator again
					VoteRoundId:  roundID,
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "already acknowledged",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			roundID, addrs := tc.setup()
			_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, tc.msg(roundID, addrs))
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}

// ===========================================================================
// Full ceremony integration test with real ECIES (Step 10)
// ===========================================================================

func (s *MsgServerTestSuite) TestFullCeremonyWithECIES() {
	s.SetupTest()
	G := elgamal.PallasGenerator()
	const numValidators = 3

	// 1. Generate 3 validator keypairs (sk_i, pk_i).
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

	// 2. Create a PENDING round with these validators.
	roundID := s.createPendingRound(ceremonyVals)

	// 3. Generate ea_sk, ea_pk.
	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	s.Require().NoError(err)
	eaPkBytes := eaPk.Point.ToAffineCompressed()

	// 4. For each validator, encrypt ea_sk to pk_i using ECIES.
	payloads := make([]*types.DealerPayload, numValidators)
	for i, v := range validators {
		env, err := ecies.Encrypt(G, v.pk.Point, eaSkBytes, rand.Reader)
		s.Require().NoError(err, "ECIES encrypt for validator %d", i)

		payloads[i] = &types.DealerPayload{
			ValidatorAddress: addrs[i],
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		}
	}

	// 5. Submit MsgDealExecutiveAuthorityKey with VoteRoundId.
	_, err = s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:     "dealer",
		VoteRoundId: roundID,
		EaPk:        eaPkBytes,
		Payloads:    payloads,
	})
	s.Require().NoError(err)

	// Verify DEALT status on the round.
	kv := s.keeper.OpenKVStore(s.ctx)
	round, err := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_PENDING, round.Status)

	// 6. For each validator: decrypt and verify they can recover ea_sk.
	for i, v := range validators {
		payload := round.CeremonyPayloads[i]
		s.Require().Equal(addrs[i], payload.ValidatorAddress)

		// Reconstruct the ECIES envelope from on-chain bytes.
		ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
		s.Require().NoError(err, "unmarshal ephemeral_pk for validator %d", i)

		env := &ecies.Envelope{
			Ephemeral:  ephPk.Point,
			Ciphertext: payload.Ciphertext,
		}

		// Decrypt using sk_i.
		decryptedEaSk, err := ecies.Decrypt(v.sk.Scalar, env)
		s.Require().NoError(err, "ECIES decrypt for validator %d", i)

		// Verify decrypted bytes == ea_sk bytes.
		s.Require().Equal(eaSkBytes, decryptedEaSk,
			"decrypted ea_sk mismatch for validator %d", i)

		// Verify ea_sk * G == ea_pk.
		recoveredSk, err := elgamal.UnmarshalSecretKey(decryptedEaSk)
		s.Require().NoError(err)
		recoveredPk := G.Mul(recoveredSk.Scalar)
		s.Require().Equal(eaPkBytes, recoveredPk.ToAffineCompressed(),
			"recovered ea_pk mismatch for validator %d", i)
	}

	// 7. Submit ack from first validator — with 3 validators and 1/3
	// threshold, one ack is enough to confirm.
	_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[0],
		VoteRoundId:  roundID,
		AckSignature: bytes.Repeat([]byte{0xAC}, 64),
	})
	s.Require().NoError(err)

	// 8. Verify round ceremony is CONFIRMED and round is ACTIVE.
	round, err = s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, round.CeremonyStatus)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_ACTIVE, round.Status)
	s.Require().Equal(eaPkBytes, round.EaPk)
}

// ===========================================================================
// ValidateAckSubmitter mempool-blocking tests
// ===========================================================================

func (s *KeeperTestSuite) TestValidateAckSubmitter_BlocksCheckTx() {
	s.SetupTest()

	// CheckTx context: should reject.
	checkCtx := s.ctx.WithIsCheckTx(true)
	err := s.keeper.ValidateAckSubmitter(checkCtx)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "cannot be submitted via mempool")
}

func (s *KeeperTestSuite) TestValidateAckSubmitter_BlocksReCheckTx() {
	s.SetupTest()

	// ReCheckTx context: should reject.
	recheckCtx := s.ctx.WithIsReCheckTx(true)
	err := s.keeper.ValidateAckSubmitter(recheckCtx)
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "cannot be submitted via mempool")
}

func (s *KeeperTestSuite) TestValidateAckSubmitter_AllowsFinalizeBlock() {
	s.SetupTest()

	// FinalizeBlock context (neither CheckTx nor ReCheckTx): should allow.
	err := s.keeper.ValidateAckSubmitter(s.ctx)
	s.Require().NoError(err)
}
