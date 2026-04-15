package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestComputeCanonicalSkipSet(t *testing.T) {
	tests := []struct {
		name     string
		acks     []*types.AckEntry
		expected []string
	}{
		{
			name:     "no acks",
			acks:     nil,
			expected: nil,
		},
		{
			name: "all empty skip sets",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1"},
				{ValidatorAddress: "val2"},
				{ValidatorAddress: "val3"},
			},
			expected: nil,
		},
		{
			name: "unanimous skip of one contributor",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val3", SkippedContributors: []string{"bad"}},
			},
			expected: []string{"bad"},
		},
		{
			name: "majority skip (2/3)",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val3"},
			},
			expected: []string{"bad"},
		},
		{
			name: "minority skip (1/3) — not canonical",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"maybe_bad"}},
				{ValidatorAddress: "val2"},
				{ValidatorAddress: "val3"},
			},
			expected: nil,
		},
		{
			name: "exactly half (2/4) — not majority (need strictly more than half)",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"suspect"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"suspect"}},
				{ValidatorAddress: "val3"},
				{ValidatorAddress: "val4"},
			},
			expected: nil,
		},
		{
			name: "3/5 skip — majority",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val3", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val4"},
				{ValidatorAddress: "val5"},
			},
			expected: []string{"bad"},
		},
		{
			name: "multiple bad contributors with different majorities",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad1", "bad2"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"bad1", "bad2"}},
				{ValidatorAddress: "val3", SkippedContributors: []string{"bad1"}},
				{ValidatorAddress: "val4"},
				{ValidatorAddress: "val5"},
			},
			// bad1: 3/5 → canonical; bad2: 2/5 → not canonical
			expected: []string{"bad1"},
		},
		{
			name: "canonical result is sorted",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"alpha", "zed"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"alpha", "zed"}},
				{ValidatorAddress: "val3", SkippedContributors: []string{"alpha", "zed"}},
			},
			expected: []string{"alpha", "zed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := keeper.ComputeCanonicalSkipSet(tc.acks)
			if tc.expected == nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, tc.expected, result)
			}
		})
	}
}

func TestFilterCompatibleAcks(t *testing.T) {
	tests := []struct {
		name      string
		acks      []*types.AckEntry
		canonical []string
		wantCount int
	}{
		{
			name: "all empty skip sets match nil canonical",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1"},
				{ValidatorAddress: "val2"},
			},
			canonical: nil,
			wantCount: 2,
		},
		{
			name: "matching skip sets pass",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val3"},
			},
			canonical: []string{"bad"},
			wantCount: 2,
		},
		{
			name: "superset skip set excluded",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"bad", "extra"}},
			},
			canonical: []string{"bad"},
			wantCount: 1,
		},
		{
			name: "no compatible acks",
			acks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"a"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"b"}},
			},
			canonical: []string{"c"},
			wantCount: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := keeper.FilterCompatibleAcks(tc.acks, tc.canonical)
			require.Len(t, result, tc.wantCount)
		})
	}
}

func TestValidateSkippedContributors(t *testing.T) {
	round := &types.VoteRound{
		Threshold: 3, // ceil(5/2)
		CeremonyValidators: []*types.ValidatorPallasKey{
			{ValidatorAddress: "val1"},
			{ValidatorAddress: "val2"},
			{ValidatorAddress: "val3"},
			{ValidatorAddress: "val4"},
			{ValidatorAddress: "val5"},
		},
		DkgContributions: []*types.DKGContribution{
			{ValidatorAddress: "val1"},
			{ValidatorAddress: "val2"},
			{ValidatorAddress: "val3"},
			{ValidatorAddress: "val4"},
			{ValidatorAddress: "val5"},
		},
	}

	tests := []struct {
		name    string
		acker   string
		skipped []string
		wantErr string
	}{
		{
			name:    "empty skip set",
			acker:   "val1",
			skipped: nil,
		},
		{
			name:    "valid single skip",
			acker:   "val1",
			skipped: []string{"val2"},
		},
		{
			name:    "valid at max skip (n-threshold = 2)",
			acker:   "val1",
			skipped: []string{"val2", "val3"},
		},
		{
			name:    "exceeds max skip (3 > n-threshold=2)",
			acker:   "val1",
			skipped: []string{"val2", "val3", "val4"},
			wantErr: "exceeds maximum",
		},
		{
			name:    "unsorted",
			acker:   "val1",
			skipped: []string{"val3", "val2"},
			wantErr: "must be sorted",
		},
		{
			name:    "duplicate",
			acker:   "val1",
			skipped: []string{"val2", "val2"},
			wantErr: "duplicate",
		},
		{
			name:    "self included",
			acker:   "val1",
			skipped: []string{"val1"},
			wantErr: "must not include self",
		},
		{
			name:    "unknown contributor",
			acker:   "val1",
			skipped: []string{"unknown"},
			wantErr: "non-contributor",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := keeper.ValidateSkippedContributors(round, tc.acker, tc.skipped)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStripRoundToCompatible(t *testing.T) {
	t.Run("removes non-compatible and skipped contributors", func(t *testing.T) {
		round := &types.VoteRound{
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: "val1", ShamirIndex: 1},
				{ValidatorAddress: "val2", ShamirIndex: 2},
				{ValidatorAddress: "val3", ShamirIndex: 3},
				{ValidatorAddress: "bad", ShamirIndex: 4},
				{ValidatorAddress: "offline", ShamirIndex: 5},
			},
			DkgContributions: []*types.DKGContribution{
				{ValidatorAddress: "val1"},
				{ValidatorAddress: "val2"},
				{ValidatorAddress: "val3"},
				{ValidatorAddress: "bad"},
				{ValidatorAddress: "offline"},
			},
			CeremonyAcks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val2", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "val3", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "bad"},
			},
		}

		compatible := []*types.AckEntry{
			{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
			{ValidatorAddress: "val2", SkippedContributors: []string{"bad"}},
			{ValidatorAddress: "val3", SkippedContributors: []string{"bad"}},
		}
		skipSet := []string{"bad"}

		keeper.StripRoundToCompatible(round, compatible, skipSet)

		require.Len(t, round.CeremonyValidators, 3)
		require.Len(t, round.DkgContributions, 3)
		require.Len(t, round.CeremonyAcks, 3)

		for _, v := range round.CeremonyValidators {
			require.NotEqual(t, "bad", v.ValidatorAddress)
			require.NotEqual(t, "offline", v.ValidatorAddress)
		}
	})

	t.Run("skipped contributor removed even if they acked with matching skip set", func(t *testing.T) {
		round := &types.VoteRound{
			CeremonyValidators: []*types.ValidatorPallasKey{
				{ValidatorAddress: "val1", ShamirIndex: 1},
				{ValidatorAddress: "bad", ShamirIndex: 2},
			},
			DkgContributions: []*types.DKGContribution{
				{ValidatorAddress: "val1"},
				{ValidatorAddress: "bad"},
			},
			CeremonyAcks: []*types.AckEntry{
				{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
				{ValidatorAddress: "bad", SkippedContributors: []string{"bad"}},
			},
		}

		compatible := []*types.AckEntry{
			{ValidatorAddress: "val1", SkippedContributors: []string{"bad"}},
			{ValidatorAddress: "bad", SkippedContributors: []string{"bad"}},
		}
		skipSet := []string{"bad"}

		keeper.StripRoundToCompatible(round, compatible, skipSet)

		require.Len(t, round.CeremonyValidators, 1)
		require.Equal(t, "val1", round.CeremonyValidators[0].ValidatorAddress)
	})
}

func TestAckBindingBindsSkipSet(t *testing.T) {
	eaPk := make([]byte, 32)

	sig1 := types.ComputeAckBinding(eaPk, "val1", nil)
	sig2 := types.ComputeAckBinding(eaPk, "val1", []string{"val2"})
	sig3 := types.ComputeAckBinding(eaPk, "val1", []string{"val3"})

	require.NotEqual(t, sig1, sig2,
		"ack bindings with different skip sets must differ")
	require.NotEqual(t, sig2, sig3,
		"ack bindings with different skip sets must differ")
}

