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

// ---------------------------------------------------------------------------
// CeremonyState CRUD
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestGetCeremonyState_ReturnsNilWhenEmpty() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	state, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Nil(state, "should return nil when no ceremony exists")
}

func (s *KeeperTestSuite) TestCeremonyState_RoundTrip() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	original := &types.CeremonyState{
		Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		Validators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val1", PallasPk: bytes.Repeat([]byte{0x01}, 32)},
			{ValidatorAddress: "val2", PallasPk: bytes.Repeat([]byte{0x02}, 32)},
		},
		Dealer:       "val1",
		PhaseStart:   100,
		PhaseTimeout: 300,
	}

	s.Require().NoError(s.keeper.SetCeremonyState(kv, original))

	got, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().NotNil(got)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, got.Status)
	s.Require().Len(got.Validators, 2)
	s.Require().Equal("val1", got.Validators[0].ValidatorAddress)
	s.Require().Equal("val2", got.Validators[1].ValidatorAddress)
	s.Require().Equal(bytes.Repeat([]byte{0x01}, 32), got.Validators[0].PallasPk)
	s.Require().Equal("val1", got.Dealer)
	s.Require().Equal(uint64(100), got.PhaseStart)
	s.Require().Equal(uint64(300), got.PhaseTimeout)
}

func (s *KeeperTestSuite) TestCeremonyState_Overwrite() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	first := &types.CeremonyState{
		Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
	}
	s.Require().NoError(s.keeper.SetCeremonyState(kv, first))

	second := &types.CeremonyState{
		Status: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
		EaPk:   bytes.Repeat([]byte{0xAA}, 32),
		Dealer: "dealer1",
	}
	s.Require().NoError(s.keeper.SetCeremonyState(kv, second))

	got, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, got.Status)
	s.Require().Equal(bytes.Repeat([]byte{0xAA}, 32), got.EaPk)
	s.Require().Equal("dealer1", got.Dealer)
}

func (s *KeeperTestSuite) TestCeremonyState_FullLifecycle() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	state := &types.CeremonyState{
		Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
		Validators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val1", PallasPk: bytes.Repeat([]byte{0x01}, 32)},
			{ValidatorAddress: "val2", PallasPk: bytes.Repeat([]byte{0x02}, 32)},
		},
	}
	s.Require().NoError(s.keeper.SetCeremonyState(kv, state))

	// Transition to DEALT.
	state.Status = types.CeremonyStatus_CEREMONY_STATUS_DEALT
	state.EaPk = bytes.Repeat([]byte{0xEA}, 32)
	state.Dealer = "val1"
	state.PhaseStart = 50
	state.Payloads = []*types.DealerPayload{
		{ValidatorAddress: "val1", EphemeralPk: bytes.Repeat([]byte{0x10}, 32), Ciphertext: bytes.Repeat([]byte{0x11}, 48)},
		{ValidatorAddress: "val2", EphemeralPk: bytes.Repeat([]byte{0x20}, 32), Ciphertext: bytes.Repeat([]byte{0x21}, 48)},
	}
	s.Require().NoError(s.keeper.SetCeremonyState(kv, state))

	got, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, got.Status)
	s.Require().Len(got.Payloads, 2)
	s.Require().Equal(bytes.Repeat([]byte{0xEA}, 32), got.EaPk)

	// Transition to CONFIRMED.
	state.Status = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
	state.Acks = []*types.AckEntry{
		{ValidatorAddress: "val1", AckHeight: 51},
		{ValidatorAddress: "val2", AckHeight: 52},
	}
	s.Require().NoError(s.keeper.SetCeremonyState(kv, state))

	got, err = s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, got.Status)
	s.Require().Len(got.Acks, 2)
}

// ---------------------------------------------------------------------------
// FindValidatorInCeremony
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestFindValidatorInCeremony() {
	state := &types.CeremonyState{
		Validators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val_alpha", PallasPk: bytes.Repeat([]byte{0x01}, 32)},
			{ValidatorAddress: "val_beta", PallasPk: bytes.Repeat([]byte{0x02}, 32)},
			{ValidatorAddress: "val_gamma", PallasPk: bytes.Repeat([]byte{0x03}, 32)},
		},
	}

	tests := []struct {
		name       string
		valAddr    string
		wantIndex  int
		wantFound  bool
	}{
		{"first validator", "val_alpha", 0, true},
		{"middle validator", "val_beta", 1, true},
		{"last validator", "val_gamma", 2, true},
		{"unknown validator", "val_delta", -1, false},
		{"empty address", "", -1, false},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			idx, found := keeper.FindValidatorInCeremony(state, tc.valAddr)
			s.Require().Equal(tc.wantFound, found)
			s.Require().Equal(tc.wantIndex, idx)
		})
	}
}

func (s *KeeperTestSuite) TestFindValidatorInCeremony_EmptyList() {
	state := &types.CeremonyState{}
	idx, found := keeper.FindValidatorInCeremony(state, "val1")
	s.Require().False(found)
	s.Require().Equal(-1, idx)
}

// ---------------------------------------------------------------------------
// FindAckForValidator
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestFindAckForValidator() {
	state := &types.CeremonyState{
		Acks: []*types.AckEntry{
			{ValidatorAddress: "val_alpha", AckHeight: 10},
			{ValidatorAddress: "val_beta", AckHeight: 11},
		},
	}

	tests := []struct {
		name       string
		valAddr    string
		wantIndex  int
		wantFound  bool
	}{
		{"found first", "val_alpha", 0, true},
		{"found second", "val_beta", 1, true},
		{"not found", "val_gamma", -1, false},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			idx, found := keeper.FindAckForValidator(state, tc.valAddr)
			s.Require().Equal(tc.wantFound, found)
			s.Require().Equal(tc.wantIndex, idx)
		})
	}
}

// ---------------------------------------------------------------------------
// AllValidatorsAcked
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestAllValidatorsAcked() {
	tests := []struct {
		name   string
		state  *types.CeremonyState
		expect bool
	}{
		{
			name: "all acked",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
			},
			expect: true,
		},
		{
			name: "partial acks (2 of 3)",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val3"},
				},
			},
			expect: false,
		},
		{
			name: "no acks",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
				},
				Acks: nil,
			},
			expect: false,
		},
		{
			name: "no validators (edge case)",
			state: &types.CeremonyState{
				Validators: nil,
				Acks:       nil,
			},
			expect: false,
		},
		{
			name: "single validator acked",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
				},
			},
			expect: true,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.Require().Equal(tc.expect, keeper.AllValidatorsAcked(tc.state))
		})
	}
}

// ---------------------------------------------------------------------------
// TwoThirdsAcked
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestTwoThirdsAcked() {
	tests := []struct {
		name   string
		state  *types.CeremonyState
		expect bool
	}{
		{
			name: "all acked (3/3)",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
			},
			expect: true,
		},
		{
			name: "exactly 2/3 (2 of 3)",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
				},
			},
			expect: true,
		},
		{
			name: "below 2/3 (1 of 3)",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
				},
			},
			expect: false,
		},
		{
			name: "no acks",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
				},
				Acks: nil,
			},
			expect: false,
		},
		{
			name:   "no validators",
			state:  &types.CeremonyState{},
			expect: false,
		},
		{
			name: "single validator acked (1/1 >= 2/3)",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
				},
			},
			expect: true,
		},
		{
			name: "3 of 4 (75% >= 66.7%)",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
					{ValidatorAddress: "val4"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
			},
			expect: true,
		},
		{
			name: "2 of 4 (50% < 66.7%)",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
					{ValidatorAddress: "val4"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
				},
			},
			expect: false,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.Require().Equal(tc.expect, keeper.TwoThirdsAcked(tc.state))
		})
	}
}

// ---------------------------------------------------------------------------
// NonAckingValidators
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestNonAckingValidators() {
	tests := []struct {
		name   string
		state  *types.CeremonyState
		expect []string
	}{
		{
			name: "all acked - none missing",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
				},
			},
			expect: nil,
		},
		{
			name: "one missing",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
					{ValidatorAddress: "val3"},
				},
				Acks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val3"},
				},
			},
			expect: []string{"val2"},
		},
		{
			name: "none acked",
			state: &types.CeremonyState{
				Validators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"},
					{ValidatorAddress: "val2"},
				},
			},
			expect: []string{"val1", "val2"},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			got := keeper.NonAckingValidators(tc.state)
			s.Require().Equal(tc.expect, got)
		})
	}
}

// ---------------------------------------------------------------------------
// StripNonAckers
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestStripNonAckers() {
	state := &types.CeremonyState{
		Validators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val1", PallasPk: []byte{0x01}},
			{ValidatorAddress: "val2", PallasPk: []byte{0x02}},
			{ValidatorAddress: "val3", PallasPk: []byte{0x03}},
		},
		Payloads: []*types.DealerPayload{
			{ValidatorAddress: "val1", Ciphertext: []byte{0x10}},
			{ValidatorAddress: "val2", Ciphertext: []byte{0x20}},
			{ValidatorAddress: "val3", Ciphertext: []byte{0x30}},
		},
		Acks: []*types.AckEntry{
			{ValidatorAddress: "val1"},
			{ValidatorAddress: "val3"},
		},
	}

	keeper.StripNonAckers(state)

	// Only val1 and val3 should remain.
	s.Require().Len(state.Validators, 2)
	s.Require().Equal("val1", state.Validators[0].ValidatorAddress)
	s.Require().Equal("val3", state.Validators[1].ValidatorAddress)

	s.Require().Len(state.Payloads, 2)
	s.Require().Equal("val1", state.Payloads[0].ValidatorAddress)
	s.Require().Equal("val3", state.Payloads[1].ValidatorAddress)

	// Acks unchanged.
	s.Require().Len(state.Acks, 2)
}

func (s *KeeperTestSuite) TestStripNonAckers_AllAcked() {
	state := &types.CeremonyState{
		Validators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val1"},
			{ValidatorAddress: "val2"},
		},
		Payloads: []*types.DealerPayload{
			{ValidatorAddress: "val1"},
			{ValidatorAddress: "val2"},
		},
		Acks: []*types.AckEntry{
			{ValidatorAddress: "val1"},
			{ValidatorAddress: "val2"},
		},
	}

	keeper.StripNonAckers(state)

	// All remain.
	s.Require().Len(state.Validators, 2)
	s.Require().Len(state.Payloads, 2)
}

// ===========================================================================
// MsgRegisterPallasKey handler tests (Step 4)
// ===========================================================================

func (s *MsgServerTestSuite) TestRegisterPallasKey_HappyPath() {
	s.SetupTest()

	pks := []struct {
		creator    string // account address sent as msg.Creator
		storedAddr string // valoper address stored in ceremony state after conversion
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

		kv := s.keeper.OpenKVStore(s.ctx)
		state, err := s.keeper.GetCeremonyState(kv)
		s.Require().NoError(err)
		s.Require().NotNil(state)
		s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status)
		s.Require().Len(state.Validators, i+1)
		s.Require().Equal(tc.storedAddr, state.Validators[i].ValidatorAddress)
		s.Require().Equal(tc.pk, state.Validators[i].PallasPk)
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
		{
			name: "wrong ceremony status (DEALT)",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetCeremonyState(kv, &types.CeremonyState{
					Status: types.CeremonyStatus_CEREMONY_STATUS_DEALT,
					Validators: []*types.ValidatorPallasKey{
						{ValidatorAddress: testValoperAddr(1), PallasPk: testPallasPK()},
					},
				}))
			},
			msg: &types.MsgRegisterPallasKey{
				Creator:  testAccAddr(2),
				PallasPk: testPallasPK(),
			},
			errContains: "operation invalid for current ceremony status",
		},
		{
			name: "wrong ceremony status (CONFIRMED)",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetCeremonyState(kv, &types.CeremonyState{
					Status: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
				}))
			},
			msg: &types.MsgRegisterPallasKey{
				Creator:  testAccAddr(1),
				PallasPk: testPallasPK(),
			},
			errContains: "operation invalid for current ceremony status",
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

// TestRegisterPallasKey_NoPhaseTimeout verifies that the REGISTERING phase
// does not set a phase timeout (it persists until deal or reinit).
func (s *MsgServerTestSuite) TestRegisterPallasKey_NoPhaseTimeout() {
	s.SetupTest()

	_, err := s.msgServer.RegisterPallasKey(s.ctx, &types.MsgRegisterPallasKey{
		Creator:  testAccAddr(1),
		PallasPk: testPallasPK(),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	state, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(uint64(0), state.PhaseTimeout, "REGISTERING should have no timeout")
}

// TestRegisterPallasKey_AfterReset verifies that registration works after the
// ceremony was reset to REGISTERING (e.g., after a DEALT timeout or reinit).
func (s *MsgServerTestSuite) TestRegisterPallasKey_AfterReset() {
	s.SetupTest()

	// Seed empty REGISTERING state (simulating post-reset).
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetCeremonyState(kv, &types.CeremonyState{
		Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
	}))

	// Registration should succeed.
	pk := testPallasPK()
	_, err := s.msgServer.RegisterPallasKey(s.ctx, &types.MsgRegisterPallasKey{
		Creator:  testAccAddr(1),
		PallasPk: pk,
	})
	s.Require().NoError(err)

	state, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, state.Status)
	s.Require().Len(state.Validators, 1)
	s.Require().Equal(testValoperAddr(1), state.Validators[0].ValidatorAddress)
	s.Require().Equal(pk, state.Validators[0].PallasPk)
	s.Require().Equal(uint64(0), state.PhaseTimeout, "REGISTERING should have no timeout")
}

// ===========================================================================
// MsgDealExecutiveAuthorityKey handler tests (Step 5)
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

func (s *MsgServerTestSuite) TestDealExecutiveAuthorityKey_HappyPath() {
	s.SetupTest()

	addrs, _ := s.registerValidators(3)
	eaPk := testPallasPK()
	payloads := makePayloads(addrs)

	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:  "dealer1",
		EaPk:     eaPk,
		Payloads: payloads,
	})
	s.Require().NoError(err)

	// Verify state transitioned to DEALT with all fields set.
	kv := s.keeper.OpenKVStore(s.ctx)
	state, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, state.Status)
	s.Require().Equal(eaPk, state.EaPk)
	s.Require().Equal("dealer1", state.Dealer)
	s.Require().Equal(uint64(s.ctx.BlockTime().Unix()), state.PhaseStart)
	s.Require().Equal(types.DefaultDealTimeout, state.PhaseTimeout)
	s.Require().Len(state.Payloads, 3)
	for i, p := range state.Payloads {
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
		setup       func() (addrs []string) // register validators, return addrs
		msg         func(addrs []string) *types.MsgDealExecutiveAuthorityKey
		errContains string
	}{
		{
			name: "no ceremony exists",
			setup: func() []string {
				return nil
			},
			msg: func(_ []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     testPallasPK(),
					Payloads: []*types.DealerPayload{},
				}
			},
			errContains: "no ceremony exists",
		},
		{
			name: "ceremony already DEALT",
			setup: func() []string {
				addrs, _ := s.registerValidators(2)
				// Force ceremony to DEALT.
				kv := s.keeper.OpenKVStore(s.ctx)
				state, _ := s.keeper.GetCeremonyState(kv)
				state.Status = types.CeremonyStatus_CEREMONY_STATUS_DEALT
				s.Require().NoError(s.keeper.SetCeremonyState(kv, state))
				return addrs
			},
			msg: func(addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     testPallasPK(),
					Payloads: makePayloads(addrs),
				}
			},
			errContains: "operation invalid for current ceremony status",
		},
		{
			name: "no validators registered",
			setup: func() []string {
				// Seed empty REGISTERING ceremony with no validators.
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetCeremonyState(kv, &types.CeremonyState{
					Status: types.CeremonyStatus_CEREMONY_STATUS_REGISTERING,
				}))
				return nil
			},
			msg: func(_ []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     testPallasPK(),
					Payloads: []*types.DealerPayload{},
				}
			},
			errContains: "no validators registered",
		},
		{
			name: "invalid ea_pk",
			setup: func() []string {
				addrs, _ := s.registerValidators(2)
				return addrs
			},
			msg: func(addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     bytes.Repeat([]byte{0xFF}, 32), // off-curve
					Payloads: makePayloads(addrs),
				}
			},
			errContains: "invalid pallas point",
		},
		{
			name: "payload count mismatch (too few)",
			setup: func() []string {
				addrs, _ := s.registerValidators(3)
				return addrs
			},
			msg: func(addrs []string) *types.MsgDealExecutiveAuthorityKey {
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     testPallasPK(),
					Payloads: makePayloads(addrs[:2]), // only 2 of 3
				}
			},
			errContains: "payload count does not match",
		},
		{
			name: "payload references unknown validator",
			setup: func() []string {
				addrs, _ := s.registerValidators(2)
				return addrs
			},
			msg: func(addrs []string) *types.MsgDealExecutiveAuthorityKey {
				payloads := makePayloads(addrs)
				payloads[1].ValidatorAddress = "unknown_val"
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     testPallasPK(),
					Payloads: payloads,
				}
			},
			errContains: "unknown validator",
		},
		{
			name: "duplicate validator in payloads",
			setup: func() []string {
				addrs, _ := s.registerValidators(2)
				return addrs
			},
			msg: func(addrs []string) *types.MsgDealExecutiveAuthorityKey {
				payloads := makePayloads(addrs)
				payloads[1].ValidatorAddress = addrs[0] // duplicate
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     testPallasPK(),
					Payloads: payloads,
				}
			},
			errContains: "duplicate payload",
		},
		{
			name: "invalid ephemeral_pk in payload",
			setup: func() []string {
				addrs, _ := s.registerValidators(2)
				return addrs
			},
			msg: func(addrs []string) *types.MsgDealExecutiveAuthorityKey {
				payloads := makePayloads(addrs)
				payloads[0].EphemeralPk = make([]byte, 32) // identity point
				return &types.MsgDealExecutiveAuthorityKey{
					Creator:  "dealer1",
					EaPk:     testPallasPK(),
					Payloads: payloads,
				}
			},
			errContains: "invalid pallas point",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			addrs := tc.setup()
			_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, tc.msg(addrs))
			s.Require().Error(err)
			s.Require().Contains(err.Error(), tc.errContains)
		})
	}
}

// ===========================================================================
// MsgAckExecutiveAuthorityKey handler tests (Step 6)
// ===========================================================================

// dealCeremony registers n validators and deals, leaving the ceremony in DEALT.
// Returns the validator addresses used.
func (s *MsgServerTestSuite) dealCeremony(n int) []string {
	addrs, _ := s.registerValidators(n)
	_, err := s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:  "dealer",
		EaPk:     testPallasPK(),
		Payloads: makePayloads(addrs),
	})
	s.Require().NoError(err)
	return addrs
}

func (s *MsgServerTestSuite) TestAckExecutiveAuthorityKey_HappyPath() {
	s.SetupTest()
	addrs := s.dealCeremony(3)

	// First two acks: status stays DEALT.
	for _, addr := range addrs[:2] {
		_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
			Creator:      addr,
			AckSignature: bytes.Repeat([]byte{0xAC}, 64),
		})
		s.Require().NoError(err)

		kv := s.keeper.OpenKVStore(s.ctx)
		state, err := s.keeper.GetCeremonyState(kv)
		s.Require().NoError(err)
		s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, state.Status,
			"should remain DEALT after %d of %d acks", len(state.Acks), len(state.Validators))
	}

	// Third (final) ack: triggers CONFIRMED.
	_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
		Creator:      addrs[2],
		AckSignature: bytes.Repeat([]byte{0xAC}, 64),
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	state, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status)
	s.Require().Len(state.Acks, 3)
	for i, ack := range state.Acks {
		s.Require().Equal(addrs[i], ack.ValidatorAddress)
		s.Require().Equal(uint64(s.ctx.BlockHeight()), ack.AckHeight)
	}

	// Verify event emission: one per ack.
	var ackEvents int
	for _, e := range s.ctx.EventManager().Events() {
		if e.Type == types.EventTypeAckExecutiveAuthorityKey {
			ackEvents++
		}
	}
	s.Require().Equal(3, ackEvents, "expected one event per ack")
}

func (s *MsgServerTestSuite) TestAckExecutiveAuthorityKey_Rejects() {
	tests := []struct {
		name        string
		setup       func() []string // returns validator addrs from deal
		msg         func(addrs []string) *types.MsgAckExecutiveAuthorityKey
		errContains string
	}{
		{
			name: "no ceremony exists",
			setup: func() []string {
				return nil
			},
			msg: func(_ []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      "val1",
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "no ceremony exists",
		},
		{
			name: "ceremony still REGISTERING",
			setup: func() []string {
				addrs, _ := s.registerValidators(2)
				return addrs
			},
			msg: func(addrs []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "operation invalid for current ceremony status",
		},
		{
			name: "ceremony already CONFIRMED",
			setup: func() []string {
				addrs := s.dealCeremony(1)
				// Force to CONFIRMED.
				kv := s.keeper.OpenKVStore(s.ctx)
				state, _ := s.keeper.GetCeremonyState(kv)
				state.Status = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
				s.Require().NoError(s.keeper.SetCeremonyState(kv, state))
				return addrs
			},
			msg: func(addrs []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "operation invalid for current ceremony status",
		},
		{
			name: "non-registered validator",
			setup: func() []string {
				return s.dealCeremony(2)
			},
			msg: func(_ []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      "outsider",
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "validator not in ceremony",
		},
		{
			name: "duplicate ack",
			setup: func() []string {
				addrs := s.dealCeremony(2)
				_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0],
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				})
				s.Require().NoError(err)
				return addrs
			},
			msg: func(addrs []string) *types.MsgAckExecutiveAuthorityKey {
				return &types.MsgAckExecutiveAuthorityKey{
					Creator:      addrs[0], // same validator again
					AckSignature: bytes.Repeat([]byte{0xAC}, 64),
				}
			},
			errContains: "already acknowledged",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			addrs := tc.setup()
			_, err := s.msgServer.AckExecutiveAuthorityKey(s.ctx, tc.msg(addrs))
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
	addrs := make([]string, numValidators) // valoper addresses stored in state
	for i := range validators {
		sk, pk := elgamal.KeyGen(rand.Reader)
		validators[i] = validatorKeys{sk: sk, pk: pk}
		addrs[i] = testValoperAddr(byte(i + 1))
	}

	// 2. Register all 3 pk_i via MsgRegisterPallasKey.
	for i, v := range validators {
		_, err := s.msgServer.RegisterPallasKey(s.ctx, &types.MsgRegisterPallasKey{
			Creator:  testAccAddr(byte(i + 1)),
			PallasPk: v.pk.Point.ToAffineCompressed(),
		})
		s.Require().NoError(err, "register validator %d", i)
	}

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

	// 5. Submit MsgDealExecutiveAuthorityKey.
	_, err = s.msgServer.DealExecutiveAuthorityKey(s.ctx, &types.MsgDealExecutiveAuthorityKey{
		Creator:  "dealer",
		EaPk:     eaPkBytes,
		Payloads: payloads,
	})
	s.Require().NoError(err)

	// Verify DEALT status.
	kv := s.keeper.OpenKVStore(s.ctx)
	state, err := s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_DEALT, state.Status)

	// 6. For each validator: decrypt, verify, ack.
	for i, v := range validators {
		// 6a. Grab their (E_i, ct_i) from ceremony state.
		payload := state.Payloads[i]
		s.Require().Equal(addrs[i], payload.ValidatorAddress)

		// Reconstruct the ECIES envelope from on-chain bytes.
		ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
		s.Require().NoError(err, "unmarshal ephemeral_pk for validator %d", i)

		env := &ecies.Envelope{
			Ephemeral:  ephPk.Point,
			Ciphertext: payload.Ciphertext,
		}

		// 6b. Decrypt using sk_i.
		decryptedEaSk, err := ecies.Decrypt(v.sk.Scalar, env)
		s.Require().NoError(err, "ECIES decrypt for validator %d", i)

		// 6c. Verify decrypted bytes == ea_sk bytes.
		s.Require().Equal(eaSkBytes, decryptedEaSk,
			"decrypted ea_sk mismatch for validator %d", i)

		// 6d. Verify ea_sk * G == ea_pk.
		recoveredSk, err := elgamal.UnmarshalSecretKey(decryptedEaSk)
		s.Require().NoError(err)
		recoveredPk := G.Mul(recoveredSk.Scalar)
		s.Require().Equal(eaPkBytes, recoveredPk.ToAffineCompressed(),
			"recovered ea_pk mismatch for validator %d", i)

		// 6e. Submit MsgAckExecutiveAuthorityKey.
		_, err = s.msgServer.AckExecutiveAuthorityKey(s.ctx, &types.MsgAckExecutiveAuthorityKey{
			Creator:      addrs[i],
			AckSignature: bytes.Repeat([]byte{0xAC}, 64),
		})
		s.Require().NoError(err, "ack for validator %d", i)
	}

	// 7. Verify ceremony is CONFIRMED.
	state, err = s.keeper.GetCeremonyState(kv)
	s.Require().NoError(err)
	s.Require().Equal(types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED, state.Status)
	s.Require().Len(state.Acks, numValidators)
	s.Require().Equal(eaPkBytes, state.EaPk)

	// 8. Create a voting session, verify round.EaPk == ea_pk.
	s.seedVoteManager("zvote1admin")
	msg := &types.MsgCreateVotingSession{
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
		VoteEndTime:       2_000_000,
		NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
		NcRoot:            bytes.Repeat([]byte{0x04}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
			{Id: 2, Title: "Proposal B", Description: "Second", Options: zallytest.DefaultOptions()},
		},
	}
	resp, err := s.msgServer.CreateVotingSession(s.ctx, msg)
	s.Require().NoError(err)
	s.Require().NotEmpty(resp.VoteRoundId)

	// Read the round back and verify ea_pk matches the ceremony's.
	round, err := s.keeper.GetVoteRound(kv, resp.VoteRoundId)
	s.Require().NoError(err)
	s.Require().Equal(eaPkBytes, round.EaPk,
		"round.EaPk should match the ceremony's confirmed ea_pk")
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
