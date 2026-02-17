package keeper_test

import (
	"bytes"

	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

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
		Dealer:     "val1",
		DealHeight: 100,
		AckTimeout: 300,
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
	s.Require().Equal(uint64(100), got.DealHeight)
	s.Require().Equal(uint64(300), got.AckTimeout)
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
	state.DealHeight = 50
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
