package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ===========================================================================
// ThresholdForN — table-driven unit tests
// ===========================================================================

func TestThresholdForN(t *testing.T) {
	tests := []struct {
		n       int
		want    int
		wantErr bool
	}{
		// Edge cases
		{n: -1, wantErr: true},
		{n: 0, wantErr: true},

		// n=1: solo validator, threshold must be 1.
		{n: 1, want: 1},

		// n=2: ceil(2/2)=1 but floor is 2 → clamped to 2.
		{n: 2, want: 2},

		// n=3: ceil(3/2)=2, satisfies >=2.
		{n: 3, want: 2},

		// n=4: (4+1)/2=2 (integer division).
		{n: 4, want: 2},

		// n=5: (5+1)/2=3.
		{n: 5, want: 3},

		// n=6: (6+1)/2=3.
		{n: 6, want: 3},

		// n=9: (9+1)/2=5.
		{n: 9, want: 5},

		// n=10: (10+1)/2=5.
		{n: 10, want: 5},

		// Large n.
		{n: 100, want: 50},
		{n: 101, want: 51},
	}

	for _, tc := range tests {
		got, err := keeper.ThresholdForN(tc.n)
		if tc.wantErr {
			require.Error(t, err, "n=%d should error", tc.n)
			continue
		}
		require.NoError(t, err, "n=%d", tc.n)
		require.Equal(t, tc.want, got, "n=%d", tc.n)
	}
}

func TestThresholdForN_Invariants(t *testing.T) {
	for n := 1; n <= 50; n++ {
		got, err := keeper.ThresholdForN(n)
		require.NoError(t, err, "n=%d", n)
		require.GreaterOrEqual(t, got, 1, "n=%d: t must be >= 1", n)
		require.LessOrEqual(t, got, n, "n=%d: t must not exceed n", n)
		if n >= 2 {
			require.GreaterOrEqual(t, got, 2, "n=%d: t must be >= 2 when n >= 2", n)
		}
	}
}

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
// ValidateProposerIsCreator tests
// ===========================================================================

func (s *KeeperTestSuite) TestValidateProposerIsCreator_BlocksCheckTx() {
	s.SetupTest()

	checkCtx := s.ctx.WithIsCheckTx(true)
	err := s.keeper.ValidateProposerIsCreator(checkCtx, "anyval", "MsgAckExecutiveAuthorityKey")
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "cannot be submitted via mempool")
}

func (s *KeeperTestSuite) TestValidateProposerIsCreator_BlocksReCheckTx() {
	s.SetupTest()

	recheckCtx := s.ctx.WithIsReCheckTx(true)
	err := s.keeper.ValidateProposerIsCreator(recheckCtx, "anyval", "MsgAckExecutiveAuthorityKey")
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "cannot be submitted via mempool")
}
