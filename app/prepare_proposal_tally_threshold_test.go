package app

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/crypto/shamir"
	votekeeper "github.com/valargroup/shielded-vote/x/vote/keeper"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// ---------------------------------------------------------------------------
// Per-test infrastructure
// ---------------------------------------------------------------------------

// newTallyThresholdTestKV creates a fresh in-memory keeper + open KV store for
// each table row.  Using a unique store key per test prevents cross-row bleed.
func newTallyThresholdTestKV(t *testing.T) (*votekeeper.Keeper, sdk.Context) {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey + "_tth_" + t.Name())
	tkey := storetypes.NewTransientStoreKey("transient_" + t.Name())
	ctx := sdktestutil.DefaultContextWithDB(t, key, tkey).Ctx
	svc := runtime.NewKVStoreService(key)
	k := votekeeper.NewKeeper(svc, "authority", log.NewNopLogger(), nil, nil)
	return k, ctx
}

// accumEntry describes one (proposal, decision, encrypted value) tuple.
type accumEntry struct {
	proposalID uint32
	decision   uint32
	value      uint64
}

// Two-proposal, two-option layout used by the multi-accumulator cases.
var multiProposals = []*types.Proposal{
	{Id: 1, Title: "P1", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
	{Id: 2, Title: "P2", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
}

// bsgsSmall is built once for the package; N=1000 supports values 0..999 and
// builds in <1 ms (m = ceil(√1000) = 32 baby steps).
var bsgsSmall = elgamal.NewBSGSTable(1000)

// ---------------------------------------------------------------------------
// Table
// ---------------------------------------------------------------------------

func TestDecryptRoundTalliesThreshold(t *testing.T) {
	const roundHex = "tally_threshold_round"
	roundID := bytes.Repeat([]byte{0x99}, 32)

	type testCase struct {
		name string
		// Crypto setup
		threshold   uint32
		nValidators int
		// Which 1-based validator indices submit partial decryptions.
		// nil = all validators submit.
		submitIdxs []int
		// Accumulators to populate in the tally store.
		accumulators []accumEntry
		// Proposals embedded in the VoteRound.
		proposals []*types.Proposal
		// Assertions
		wantErr     bool
		errContains string
		// If !wantErr, wantValues maps AccumulatorKey(proposalID,decision)→totalValue.
		wantValues map[uint64]uint64
	}
	_ = roundHex // suppress unused warning

	cases := []testCase{
		// --- happy paths ---
		{
			name:         "t=2 n=2 all submit single accumulator",
			threshold:    2,
			nValidators:  2,
			accumulators: []accumEntry{{proposalID: 1, decision: 0, value: 100}},
			proposals:    multiProposals,
			wantValues:   map[uint64]uint64{votekeeper.AccumulatorKey(1, 0): 100},
		},
		{
			name:         "t=2 n=3 all submit single accumulator",
			threshold:    2,
			nValidators:  3,
			accumulators: []accumEntry{{proposalID: 1, decision: 0, value: 42}},
			proposals:    multiProposals,
			wantValues:   map[uint64]uint64{votekeeper.AccumulatorKey(1, 0): 42},
		},
		{
			name:         "t=2 n=3 exactly threshold (first 2 validators) submit",
			threshold:    2,
			nValidators:  3,
			submitIdxs:   []int{1, 2},
			accumulators: []accumEntry{{proposalID: 1, decision: 0, value: 7}},
			proposals:    multiProposals,
			wantValues:   map[uint64]uint64{votekeeper.AccumulatorKey(1, 0): 7},
		},
		{
			name:         "t=2 n=3 different threshold subset (validators 2 and 3)",
			threshold:    2,
			nValidators:  3,
			submitIdxs:   []int{2, 3},
			accumulators: []accumEntry{{proposalID: 1, decision: 0, value: 55}},
			proposals:    multiProposals,
			wantValues:   map[uint64]uint64{votekeeper.AccumulatorKey(1, 0): 55},
		},
		{
			name:        "multiple accumulators (2 proposals × 2 decisions)",
			threshold:   2,
			nValidators: 2,
			accumulators: []accumEntry{
				{proposalID: 1, decision: 0, value: 10},
				{proposalID: 1, decision: 1, value: 20},
				{proposalID: 2, decision: 0, value: 30},
				{proposalID: 2, decision: 1, value: 40},
			},
			proposals: multiProposals,
			wantValues: map[uint64]uint64{
				votekeeper.AccumulatorKey(1, 0): 10,
				votekeeper.AccumulatorKey(1, 1): 20,
				votekeeper.AccumulatorKey(2, 0): 30,
				votekeeper.AccumulatorKey(2, 1): 40,
			},
		},
		{
			name:         "zero value accumulator",
			threshold:    2,
			nValidators:  2,
			accumulators: []accumEntry{{proposalID: 1, decision: 0, value: 0}},
			proposals:    multiProposals,
			wantValues:   map[uint64]uint64{votekeeper.AccumulatorKey(1, 0): 0},
		},
		{
			name:         "no accumulators (no votes cast) returns empty slice",
			threshold:    2,
			nValidators:  2,
			accumulators: nil,
			proposals:    multiProposals,
			wantValues:   map[uint64]uint64{},
		},
		{
			name:        "dleq_proof is nil on every entry (Step 1 behaviour)",
			threshold:   2,
			nValidators: 2,
			accumulators: []accumEntry{
				{proposalID: 1, decision: 0, value: 5},
				{proposalID: 1, decision: 1, value: 15},
			},
			proposals:  multiProposals,
			wantValues: map[uint64]uint64{votekeeper.AccumulatorKey(1, 0): 5, votekeeper.AccumulatorKey(1, 1): 15},
		},

		// --- error paths ---
		{
			name:         "accumulator exists but no partial decryptions stored",
			threshold:    2,
			nValidators:  2,
			submitIdxs:   []int{}, // nobody submits
			accumulators: []accumEntry{{proposalID: 1, decision: 0, value: 99}},
			proposals:    multiProposals,
			wantErr:      true,
			errContains:  "no partial decryptions stored",
		},
		{
			name:         "only 1 partial stored but threshold is 2",
			threshold:    2,
			nValidators:  3,
			submitIdxs:   []int{1}, // one validator, below t=2
			accumulators: []accumEntry{{proposalID: 1, decision: 0, value: 99}},
			proposals:    multiProposals,
			wantErr:      true,
			errContains:  "Lagrange combination failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx := newTallyThresholdTestKV(t)
			kvStore := k.OpenKVStore(ctx)

			// Generate ea_sk and split it.
			eaSk, eaPk := elgamal.KeyGen(rand.Reader)
			shares, _, err := shamir.Split(eaSk.Scalar, int(tc.threshold), tc.nValidators)
			require.NoError(t, err)

			// Populate tally accumulators.
			for _, acc := range tc.accumulators {
				ct, err := elgamal.Encrypt(eaPk, acc.value, rand.Reader)
				require.NoError(t, err)
				ctBytes, err := elgamal.MarshalCiphertext(ct)
				require.NoError(t, err)
				require.NoError(t, k.AddToTally(kvStore, roundID, acc.proposalID, acc.decision, ctBytes))
			}

			// Determine which validator indices submit.
			submitters := tc.submitIdxs
			if submitters == nil {
				submitters = make([]int, tc.nValidators)
				for i := range submitters {
					submitters[i] = i + 1
				}
			}

			// For each submitting validator, compute D_i for every accumulator
			// and store via SetPartialDecryptions.
			G := elgamal.PallasGenerator()
			for _, idx := range submitters {
				share := shares[idx-1] // shares are 0-indexed, idx is 1-based
				var entries []*types.PartialDecryptionEntry

				for _, acc := range tc.accumulators {
					ctBytes, err := k.GetTally(kvStore, roundID, acc.proposalID, acc.decision)
					require.NoError(t, err)
					ct, err := elgamal.UnmarshalCiphertext(ctBytes)
					require.NoError(t, err)

					Di := ct.C1.Mul(share.Value) // D_i = share_i * C1
					_ = G                        // G used indirectly via UnmarshalPoint verification

					entries = append(entries, &types.PartialDecryptionEntry{
						ProposalId:     acc.proposalID,
						VoteDecision:   acc.decision,
						PartialDecrypt: Di.ToAffineCompressed(),
					})
				}

				if len(entries) > 0 {
					require.NoError(t, k.SetPartialDecryptions(kvStore, roundID, uint32(idx), entries))
				}
			}

			// Build the VoteRound.
			round := &types.VoteRound{
				VoteRoundId: roundID,
				Threshold:   tc.threshold,
				Proposals:   tc.proposals,
			}

			// Exercise the function under test.
			got, err := decryptRoundTalliesThreshold(kvStore, k, round, bsgsSmall)

			if tc.wantErr {
				require.Error(t, err)
				if tc.errContains != "" {
					require.Contains(t, err.Error(), tc.errContains)
				}
				return
			}

			require.NoError(t, err)
			require.Len(t, got, len(tc.wantValues),
				"expected %d tally entries, got %d", len(tc.wantValues), len(got))

			// Verify each recovered value and that DecryptionProof is nil.
			for _, entry := range got {
				accKey := votekeeper.AccumulatorKey(entry.ProposalId, entry.VoteDecision)
				expected, ok := tc.wantValues[accKey]
				require.True(t, ok,
					"unexpected entry for (proposal=%d, decision=%d)", entry.ProposalId, entry.VoteDecision)
				require.Equal(t, expected, entry.TotalValue,
					"wrong total_value for (proposal=%d, decision=%d)", entry.ProposalId, entry.VoteDecision)
				require.Nil(t, entry.DecryptionProof,
					"DecryptionProof must be nil in Step 1 threshold mode")
			}
		})
	}
}

// TestRoundHasAccumulators verifies that roundHasAccumulators correctly
// distinguishes rounds with zero votes from rounds with at least one
// accumulator entry.
func TestRoundHasAccumulators(t *testing.T) {
	roundID := bytes.Repeat([]byte{0xAA}, 32)

	proposals := []*types.Proposal{
		{Id: 1, Title: "P1", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
		{Id: 2, Title: "P2", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
	}

	t.Run("no accumulators", func(t *testing.T) {
		k, ctx := newTallyThresholdTestKV(t)
		kvStore := k.OpenKVStore(ctx)

		round := &types.VoteRound{
			VoteRoundId: roundID,
			Proposals:   proposals,
			Threshold:   2,
		}
		require.NoError(t, k.SetVoteRound(kvStore, round))

		has, err := roundHasAccumulators(kvStore, k, round)
		require.NoError(t, err)
		require.False(t, has, "should be false when no votes were cast")
	})

	t.Run("one accumulator in first proposal", func(t *testing.T) {
		k, ctx := newTallyThresholdTestKV(t)
		kvStore := k.OpenKVStore(ctx)

		_, eaPk := elgamal.KeyGen(rand.Reader)
		ct, err := elgamal.Encrypt(eaPk, 42, rand.Reader)
		require.NoError(t, err)
		ctBytes, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		require.NoError(t, k.AddToTally(kvStore, roundID, 1, 0, ctBytes))

		round := &types.VoteRound{
			VoteRoundId: roundID,
			Proposals:   proposals,
			Threshold:   2,
		}
		require.NoError(t, k.SetVoteRound(kvStore, round))

		has, err := roundHasAccumulators(kvStore, k, round)
		require.NoError(t, err)
		require.True(t, has, "should be true when at least one vote was cast")
	})

	t.Run("accumulator only in second proposal", func(t *testing.T) {
		k, ctx := newTallyThresholdTestKV(t)
		kvStore := k.OpenKVStore(ctx)

		_, eaPk := elgamal.KeyGen(rand.Reader)
		ct, err := elgamal.Encrypt(eaPk, 7, rand.Reader)
		require.NoError(t, err)
		ctBytes, err := elgamal.MarshalCiphertext(ct)
		require.NoError(t, err)
		require.NoError(t, k.AddToTally(kvStore, roundID, 2, 1, ctBytes))

		round := &types.VoteRound{
			VoteRoundId: roundID,
			Proposals:   proposals,
			Threshold:   2,
		}
		require.NoError(t, k.SetVoteRound(kvStore, round))

		has, err := roundHasAccumulators(kvStore, k, round)
		require.NoError(t, err)
		require.True(t, has, "should detect accumulator even in later proposals")
	})
}
