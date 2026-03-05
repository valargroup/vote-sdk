package keeper_test

import (
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ===========================================================================
// Per-round ceremony helper tests (pure functions on KeeperTestSuite)
// ===========================================================================

func (s *KeeperTestSuite) TestHalfAcked() {
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
			name: "exactly 1/2 (2 of 4)",
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
			name: "exactly ceil(n/2) (2 of 3)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"}, {ValidatorAddress: "val3"},
				},
				CeremonyAcks: []*types.AckEntry{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"},
				},
			},
			expect: true,
		},
		{
			name: "below 1/2 (1 of 3)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{
					{ValidatorAddress: "val1"}, {ValidatorAddress: "val2"}, {ValidatorAddress: "val3"},
				},
				CeremonyAcks: []*types.AckEntry{
					{ValidatorAddress: "val1"},
				},
			},
			expect: false,
		},
		{
			name: "below 1/2 (1 of 4)",
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
			name: "single validator acked (1/1)",
			round: &types.VoteRound{
				CeremonyValidators: []*types.ValidatorPallasKey{{ValidatorAddress: "val1"}},
				CeremonyAcks:       []*types.AckEntry{{ValidatorAddress: "val1"}},
			},
			expect: true,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.Require().Equal(tc.expect, keeper.HalfAcked(tc.round))
		})
	}
}

func (s *KeeperTestSuite) TestFindValidatorInRoundCeremony() {
	round := &types.VoteRound{
		CeremonyValidators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val_alpha", ShamirIndex: 1},
			{ValidatorAddress: "val_beta", ShamirIndex: 2},
			{ValidatorAddress: "val_gamma", ShamirIndex: 3},
		},
	}

	tests := []struct {
		name            string
		valAddr         string
		wantShamirIndex uint32
		wantFound       bool
	}{
		{"first", "val_alpha", 1, true},
		{"middle", "val_beta", 2, true},
		{"last", "val_gamma", 3, true},
		{"unknown", "val_delta", 0, false},
		{"empty", "", 0, false},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			v, found := keeper.FindValidatorInRoundCeremony(round, tc.valAddr)
			s.Require().Equal(tc.wantFound, found)
			if found {
				s.Require().Equal(tc.wantShamirIndex, v.ShamirIndex)
			} else {
				s.Require().Nil(v)
			}
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
			{ValidatorAddress: "val1", PallasPk: []byte{0x01}, ShamirIndex: 1},
			{ValidatorAddress: "val2", PallasPk: []byte{0x02}, ShamirIndex: 2},
			{ValidatorAddress: "val3", PallasPk: []byte{0x03}, ShamirIndex: 3},
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
	// ShamirIndex must be preserved through stripping so Lagrange interpolation
	// uses the correct original x-coordinate (val3's share is f(3), not f(2)).
	s.Require().Equal(uint32(1), round.CeremonyValidators[0].ShamirIndex)
	s.Require().Equal(uint32(3), round.CeremonyValidators[1].ShamirIndex)

	s.Require().Len(round.CeremonyPayloads, 2)
	s.Require().Equal("val1", round.CeremonyPayloads[0].ValidatorAddress)
	s.Require().Equal("val3", round.CeremonyPayloads[1].ValidatorAddress)

	s.Require().Len(round.CeremonyAcks, 2)
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
