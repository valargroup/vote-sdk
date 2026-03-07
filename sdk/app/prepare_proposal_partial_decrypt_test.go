package app_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	voteapi "github.com/valargroup/shielded-vote/api"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/testutil"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// pdRound is a deterministic round ID used across partial-decrypt tests.
var pdRound = bytes.Repeat([]byte{0xCC}, 32)

// sampleProposals returns two proposals each with two vote options.
func sampleProposals() []*types.Proposal {
	opts := func() []*types.VoteOption {
		return []*types.VoteOption{
			{Index: 0, Label: "Yes"},
			{Index: 1, Label: "No"},
		}
	}
	return []*types.Proposal{
		{Id: 1, Title: "Proposal 1", Options: opts()},
		{Id: 2, Title: "Proposal 2", Options: opts()},
	}
}

// seedTallyingRoundWithAccumulators seeds a TALLYING threshold round and
// populates one real ElGamal ciphertext per (proposal, decision) pair.
// Returns a map of (proposalID<<32|decision) → ciphertext bytes so callers
// can verify expected D_i values.
func seedTallyingRoundWithAccumulators(
	t *testing.T,
	ta *testutil.TestApp,
	roundID []byte,
	threshold uint32,
	validators []*types.ValidatorPallasKey,
	vks [][]byte,
	eaPk *elgamal.PublicKey,
) map[uint64][]byte {
	t.Helper()

	proposals := sampleProposals()
	ta.SeedTallyingRoundThreshold(roundID, threshold, proposals, validators, vks)

	accumulators := make(map[uint64][]byte)
	for _, prop := range proposals {
		for _, opt := range prop.Options {
			ct, err := elgamal.Encrypt(eaPk, 42, rand.Reader)
			require.NoError(t, err)
			ctBytes, err := elgamal.MarshalCiphertext(ct)
			require.NoError(t, err)

			ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
			kvStore := ta.VoteKeeper().OpenKVStore(ctx)
			require.NoError(t, ta.VoteKeeper().AddToTally(kvStore, roundID, prop.Id, opt.Index, ctBytes))

			accumulators[uint64(prop.Id)<<32|uint64(opt.Index)] = ctBytes
		}
	}
	ta.NextBlock()
	return accumulators
}

// ---------------------------------------------------------------------------
// Happy path — injector emits one entry per accumulator with correct D_i
// ---------------------------------------------------------------------------

func TestPartialDecryptInjector_HappyPath(t *testing.T) {
	ta, _, pallasPk, _, eaPk := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()
	share, _ := elgamal.KeyGen(rand.Reader)
	shareBytes, err := elgamal.MarshalSecretKey(share)
	require.NoError(t, err)

	G := elgamal.PallasGenerator()
	vk := G.Mul(share.Scalar).ToAffineCompressed()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	accumulators := seedTallyingRoundWithAccumulators(t, ta, pdRound, 1, validators, [][]byte{vk}, eaPk)
	ta.WriteShareForRound(pdRound, shareBytes)

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "expected exactly one injected partial decryption tx")

	tag, protoMsg, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagSubmitPartialDecryption, tag)

	msg, ok := protoMsg.(*types.MsgSubmitPartialDecryption)
	require.True(t, ok)

	require.Equal(t, proposerAddr, msg.Creator)
	require.Equal(t, pdRound, msg.VoteRoundId)
	require.Equal(t, hex.EncodeToString(pdRound), hex.EncodeToString(msg.VoteRoundId),
		"injected tx must carry the correct round_id")
	require.EqualValues(t, 1, msg.ValidatorIndex, "proposer is at validator_index 1")
	require.Len(t, msg.Entries, len(accumulators),
		"one entry per non-empty accumulator (2 proposals × 2 decisions = 4)")

	for _, entry := range msg.Entries {
		require.Len(t, entry.PartialDecrypt, 32, "partial_decrypt must be 32 bytes")
		require.Empty(t, entry.DleqProof, "dleq_proof must be empty in Step 1")

		accKey := uint64(entry.ProposalId)<<32 | uint64(entry.VoteDecision)
		ctBytes, exists := accumulators[accKey]
		require.True(t, exists, "no accumulator for (proposal=%d, decision=%d)",
			entry.ProposalId, entry.VoteDecision)

		ct, err := elgamal.UnmarshalCiphertext(ctBytes)
		require.NoError(t, err)
		require.Equal(t, ct.C1.Mul(share.Scalar).ToAffineCompressed(), entry.PartialDecrypt,
			"D_i mismatch for (proposal=%d, decision=%d)", entry.ProposalId, entry.VoteDecision)
	}
}

// ---------------------------------------------------------------------------
// Duplicate injection — second PrepareProposal after block committed must skip
// ---------------------------------------------------------------------------

func TestPartialDecryptInjector_SkipsDuplicate(t *testing.T) {
	ta, _, pallasPk, _, eaPk := testutil.SetupTestAppWithPallasKey(t)

	proposerAddr := ta.ValidatorOperAddr()
	share, _ := elgamal.KeyGen(rand.Reader)
	G := elgamal.PallasGenerator()
	vk := G.Mul(share.Scalar).ToAffineCompressed()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	seedTallyingRoundWithAccumulators(t, ta, pdRound, 1, validators, [][]byte{vk}, eaPk)

	shareBytes, _ := elgamal.MarshalSecretKey(share)
	ta.WriteShareForRound(pdRound, shareBytes)

	require.Len(t, ta.CallPrepareProposal().Txs, 1, "first PrepareProposal should inject")

	// Commit the block so the partial decryption is on-chain.
	ta.NextBlockWithPrepareProposal()

	require.Empty(t, ta.CallPrepareProposal().Txs,
		"second PrepareProposal must not re-inject after on-chain submission")
}

// ---------------------------------------------------------------------------
// Skip cases — table-driven
//
// All cases assert require.Empty(resp.Txs). They differ only in how the
// chain state is prepared before CallPrepareProposal is invoked.
// ---------------------------------------------------------------------------

// skipSetup receives a fully initialised TestApp, the proposer's operator
// address, and the pallas/ea keys returned by SetupTestAppWithPallasKey.
type skipSetup func(
	t *testing.T,
	ta *testutil.TestApp,
	proposerAddr string,
	pallasPk *elgamal.PublicKey,
	eaPk *elgamal.PublicKey,
)

func TestPartialDecryptInjector_Skips(t *testing.T) {
	cases := []struct {
		name  string
		setup skipSetup
	}{
		{
			name: "no TALLYING round",
			setup: func(t *testing.T, ta *testutil.TestApp, _ string, _ *elgamal.PublicKey, _ *elgamal.PublicKey) {
				// no round seeded; nothing to inject
			},
		},
		{
			name: "legacy round (threshold=0)",
			setup: func(t *testing.T, ta *testutil.TestApp, proposerAddr string, pallasPk *elgamal.PublicKey, eaPk *elgamal.PublicKey) {
				validators := []*types.ValidatorPallasKey{
					{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
				}
				// threshold=0 → partial decrypt injector must ignore this round
				ta.SeedTallyingRoundThreshold(pdRound, 0, sampleProposals(), validators, nil)

				ct, _ := elgamal.Encrypt(eaPk, 10, rand.Reader)
				ctBytes, _ := elgamal.MarshalCiphertext(ct)
				ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
				require.NoError(t, ta.VoteKeeper().AddToTally(ta.VoteKeeper().OpenKVStore(ctx), pdRound, 1, 0, ctBytes))
				ta.NextBlock()

				share, _ := elgamal.KeyGen(rand.Reader)
				shareBytes, _ := elgamal.MarshalSecretKey(share)
				ta.WriteShareForRound(pdRound, shareBytes)
			},
		},
		{
			name: "no share file on disk",
			setup: func(t *testing.T, ta *testutil.TestApp, proposerAddr string, pallasPk *elgamal.PublicKey, eaPk *elgamal.PublicKey) {
				share, _ := elgamal.KeyGen(rand.Reader)
				G := elgamal.PallasGenerator()
				vk := G.Mul(share.Scalar).ToAffineCompressed()
				validators := []*types.ValidatorPallasKey{
					{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
				}
				seedTallyingRoundWithAccumulators(t, ta, pdRound, 1, validators, [][]byte{vk}, eaPk)
				// intentionally do NOT call ta.WriteShareForRound
			},
		},
		{
			name: "proposer not in ceremony_validators",
			setup: func(t *testing.T, ta *testutil.TestApp, _ string, _ *elgamal.PublicKey, eaPk *elgamal.PublicKey) {
				// ceremony_validators contains a stranger; the genesis proposer is absent
				_, strangePk := elgamal.KeyGen(rand.Reader)
				share, _ := elgamal.KeyGen(rand.Reader)
				G := elgamal.PallasGenerator()
				vk := G.Mul(share.Scalar).ToAffineCompressed()
				validators := []*types.ValidatorPallasKey{{
					ValidatorAddress: "sv1stranger1xxxxxxxxxxxxxxxxxxxxxxxxxx",
					PallasPk:         strangePk.Point.ToAffineCompressed(),
				}}
				seedTallyingRoundWithAccumulators(t, ta, pdRound, 1, validators, [][]byte{vk}, eaPk)

				shareBytes, _ := elgamal.MarshalSecretKey(share)
				ta.WriteShareForRound(pdRound, shareBytes)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case gets a fresh chain to avoid state bleed.
			ta, _, pallasPk, _, eaPk := testutil.SetupTestAppWithPallasKey(t)
			require.NotEmpty(t, ta.EaSkDir)

			tc.setup(t, ta, ta.ValidatorOperAddr(), pallasPk, eaPk)

			require.Empty(t, ta.CallPrepareProposal().Txs,
				"injector should not fire for case %q", tc.name)
		})
	}
}

// TestPartialDecryptInjector_Skips_ZeroAccumulators verifies that the partial
// decrypt injector does NOT fire when there are no tally accumulators.
// The tally handler DOES fire (injecting an empty MsgSubmitTally to finalize
// the zero-vote round), so we verify the injected tx is a tally, not a partial.
func TestPartialDecryptInjector_Skips_ZeroAccumulators(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()

	share, _ := elgamal.KeyGen(rand.Reader)
	G := elgamal.PallasGenerator()
	vk := G.Mul(share.Scalar).ToAffineCompressed()
	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}
	ta.SeedTallyingRoundThreshold(pdRound, 1, sampleProposals(), validators, [][]byte{vk})

	shareBytes, _ := elgamal.MarshalSecretKey(share)
	ta.WriteShareForRound(pdRound, shareBytes)

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1,
		"tally handler should inject MsgSubmitTally for zero-vote round")

	require.Equal(t, byte(voteapi.TagSubmitTally), resp.Txs[0][0],
		"injected tx should be a tally, not a partial decryption")
}
