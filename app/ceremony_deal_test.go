package app_test

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"cosmossdk.io/log"

	"github.com/valargroup/vote-sdk/app"
	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/crypto/ecies"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// CeremonyDealPrepareProposalHandler — no pallas_sk_path configured
//
// Without a Pallas secret key the deal handler must skip injection silently.
// ---------------------------------------------------------------------------

func TestCeremonyDealSkipsWhenNoPallasKey(t *testing.T) {
	// SetupTestApp does NOT configure pallas_sk_path.
	ta := testutil.SetupTestApp(t)

	dealerAddr := ta.ValidatorOperAddr()
	_, pk := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: dealerAddr, PallasPk: pk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk.Point.ToAffineCompressed()},
	}
	ta.SeedRegisteringCeremony(validators)

	resp := ta.CallPrepareProposal()
	require.Empty(t, resp.Txs, "no deal tx should be injected without a pallas key")
}

// ---------------------------------------------------------------------------
// CeremonyDealPrepareProposalHandler — proposer not in ceremony validators
//
// If the proposer's operator address is absent from ceremony_validators, the
// deal handler must skip injection without error.
// ---------------------------------------------------------------------------

func TestCeremonyDealSkipsWhenProposerNotInValidators(t *testing.T) {
	ta, _, _, _, _ := testutil.SetupTestAppWithPallasKey(t)

	// Seed a round where the ceremony validators do NOT include the genesis proposer.
	_, pkA := elgamal.KeyGen(rand.Reader)
	_, pkB := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: "sv1stranger1xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pkA.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1stranger2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pkB.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1stranger3xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pkB.Point.ToAffineCompressed()},
	}
	ta.SeedRegisteringCeremony(validators)

	resp := ta.CallPrepareProposal()
	require.Empty(t, resp.Txs, "no deal tx should be injected when proposer is not a ceremony validator")
}

// ---------------------------------------------------------------------------
// Share scalar zeroing — defence-in-depth
//
// After the deal handler builds payloads and verification keys, the share
// scalars (fresh f(i) values returned by shamir.Split) must be zeroed to
// prevent a heap-dump adversary from recovering all n shares and
// reconstructing ea_sk. This test exercises the zeroing mechanism directly:
// split, zero shares, assert all Values are the zero scalar.
// ---------------------------------------------------------------------------

func TestShareScalarsZeroedAfterDeal(t *testing.T) {
	secret := new(curvey.ScalarPallas).Random(rand.Reader)
	shares, coeffs, err := shamir.Split(secret, 2, 3)
	require.NoError(t, err)

	zeroBytes := new(curvey.ScalarPallas).Zero().Bytes()

	// Shares and coefficients must be non-zero before scrubbing.
	for i, s := range shares {
		require.NotEqual(t, zeroBytes, s.Value.Bytes(),
			"share[%d] must be non-zero before zeroing", i)
	}
	for i, c := range coeffs {
		require.NotEqual(t, zeroBytes, c.Bytes(),
			"coeffs[%d] must be non-zero before zeroing", i)
	}

	// curvey.Scalar.Zero() is a factory that returns a *new* zero scalar —
	// it does NOT mutate the receiver. Verify this is indeed the case so the
	// test is meaningful: if the library ever fixes this, the assertion below
	// would need updating.
	probe := new(curvey.ScalarPallas).Random(rand.Reader)
	probeBytes := probe.Bytes()
	_ = probe.Zero() // returns new scalar, should NOT touch probe
	require.Equal(t, probeBytes, probe.Bytes(),
		"Scalar.Zero() must not mutate receiver (confirms the bug this test guards against)")

	// In-place zeroing via Field4.SetZero() — mirrors the zeroScalar helper
	// used in the deal handler.
	inPlaceZero := func(s curvey.Scalar) {
		if ps, ok := s.(*curvey.ScalarPallas); ok && ps != nil && ps.Value != nil {
			ps.Value.SetZero()
		}
	}

	for i := range shares {
		if shares[i].Value != nil {
			inPlaceZero(shares[i].Value)
		}
	}
	for _, c := range coeffs {
		if c != nil {
			inPlaceZero(c)
		}
	}

	// Every share value must now be the zero scalar.
	for i, s := range shares {
		require.Equal(t, zeroBytes, s.Value.Bytes(),
			"share[%d].Value must be zero after scrubbing", i)
	}
	for i, c := range coeffs {
		require.Equal(t, zeroBytes, c.Bytes(),
			"coeffs[%d] must be zero after scrubbing", i)
	}
}

// ---------------------------------------------------------------------------
// CeremonyAckPrepareProposalHandler — threshold mode
//
// The ack handler must verify share_i * G == VK_i (not ea_pk) and write
// the share to share.<round_id> (not ea_sk.<round_id>).
// ---------------------------------------------------------------------------

func TestCeremonyAckThresholdMode(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	addrs := []string{proposerAddr, "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx"}
	pks := []curvey.Point{pallasPk.Point, pk2.Point, pk3.Point}

	contributions, combinedSerialized, eaPk, allShares, allCoeffs, combinedPts :=
		buildDKGDealtRoundState(t, addrs, pks)

	roundID := seedDKGDealtRound(t, ta, addrs, pks, contributions, combinedSerialized, eaPk, allCoeffs[0])

	// PrepareProposal should inject one ack tx.
	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "expected one injected ack tx")

	tag, _, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagAckExecutiveAuthorityKey, tag)

	// share.<round_id> must now exist on disk.
	sharePath := filepath.Join(ta.EaSkDir, "share."+hex.EncodeToString(roundID))
	shareBytes, err := os.ReadFile(sharePath)
	require.NoError(t, err, "share file should have been written by ack handler")
	require.Len(t, shareBytes, 32)

	// The written share must equal the sum of all contributors' shares for
	// the proposer (validator index 0, Shamir 1-based index 1).
	expectedCombined := allShares[0][0].Value
	for i := 1; i < len(allShares); i++ {
		expectedCombined = expectedCombined.Add(allShares[i][0].Value)
	}
	require.Equal(t, expectedCombined.Bytes(), shareBytes,
		"share on disk must equal the combined share for the proposer")

	shareScalar, err := new(curvey.ScalarPallas).SetBytes(shareBytes)
	require.NoError(t, err)
	ok, err := shamir.VerifyFeldmanShare(G, combinedPts, 1, shareScalar)
	require.NoError(t, err)
	require.True(t, ok, "combined share must verify against combined Feldman commitments")

	// ea_sk.<round_id> must NOT exist.
	eaSkPath := filepath.Join(ta.EaSkDir, "ea_sk."+hex.EncodeToString(roundID))
	_, statErr := os.Stat(eaSkPath)
	require.True(t, os.IsNotExist(statErr), "ea_sk file must not be written in threshold mode")
}

// ---------------------------------------------------------------------------
// CeremonyAckPrepareProposalHandler — threshold mode, bad share
//
// If the dealer sends a share inconsistent with the published VK, the ack
// handler must reject it silently (no ack tx injected).
// ---------------------------------------------------------------------------

func TestCeremonyAckThresholdRejectsBadShare(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	addrs := []string{proposerAddr, "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx"}
	pks := []curvey.Point{pallasPk.Point, pk2.Point, pk3.Point}

	contributions, combinedSerialized, eaPk, _, allCoeffs, _ :=
		buildDKGDealtRoundState(t, addrs, pks)

	// Tamper with contributor 1's payload for the proposer: encrypt a random
	// (wrong) share instead of the correct f_1(1).
	wrongScalar := new(curvey.ScalarPallas).Random(rand.Reader)
	env, err := ecies.Encrypt(G, pallasPk.Point, wrongScalar.Bytes(), rand.Reader)
	require.NoError(t, err)

	for _, p := range contributions[1].Payloads {
		if p.ValidatorAddress == proposerAddr {
			p.EphemeralPk = env.Ephemeral.ToAffineCompressed()
			p.Ciphertext = env.Ciphertext
			break
		}
	}

	seedDKGDealtRound(t, ta, addrs, pks, contributions, combinedSerialized, eaPk, allCoeffs[0])

	resp := ta.CallPrepareProposal()
	require.Empty(t, resp.Txs, "ack must be rejected when share fails Feldman verification")
}

// ---------------------------------------------------------------------------
// ProcessProposal: MsgContributeDKG validation
//
// Verifies that ProcessProposal accepts a well-formed MsgContributeDKG when
// the round is REGISTERING and rejects it when the round is not REGISTERING.
// ---------------------------------------------------------------------------

func TestProcessProposalAcceptsDKGContribution(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	valAddr := ta.ValidatorOperAddr()

	validators := []*types.ValidatorPallasKey{{ValidatorAddress: valAddr}}
	roundID := ta.SeedRegisteringCeremony(validators)

	msg := &types.MsgContributeDKG{
		Creator:     valAddr,
		VoteRoundId: roundID,
	}
	txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagContributeDKG)
	require.NoError(t, err)

	resp := ta.CallProcessProposal([][]byte{txBytes})
	require.Equal(t, abci.ResponseProcessProposal_ACCEPT, resp.Status,
		"ProcessProposal should ACCEPT valid DKG contribution in REGISTERING state")
}

func TestProcessProposalRejectsDKGContributionWhenDealt(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	valAddr := ta.ValidatorOperAddr()

	validators := []*types.ValidatorPallasKey{{ValidatorAddress: valAddr}}
	eaPkBytes := make([]byte, 32)
	roundID := ta.SeedDealtCeremony(make([]byte, 32), eaPkBytes, validators)

	msg := &types.MsgContributeDKG{
		Creator:     valAddr,
		VoteRoundId: roundID,
	}
	txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagContributeDKG)
	require.NoError(t, err)

	resp := ta.CallProcessProposal([][]byte{txBytes})
	require.Equal(t, abci.ResponseProcessProposal_REJECT, resp.Status,
		"ProcessProposal should REJECT DKG contribution when ceremony is DEALT")
}

// ---------------------------------------------------------------------------
// CeremonyDKGContributionPrepareProposalHandler
//
// The DKG contribution handler is not wired into ComposedPrepareProposalHandler
// until Phase 6, so these tests instantiate and invoke it directly.
// ---------------------------------------------------------------------------

// callDKGContributionHandler creates a CeremonyDKGContributionPrepareProposalHandler
// and invokes it with the TestApp's keepers. Returns the resulting tx list.
func callDKGContributionHandler(t *testing.T, ta *testutil.TestApp) [][]byte {
	t.Helper()

	pallasSkPath := filepath.Join(ta.EaSkDir, "pallas.sk")
	handler := app.CeremonyDKGContributionPrepareProposalHandler(
		ta.VoteKeeper(),
		ta.StakingKeeper,
		pallasSkPath,
		ta.EaSkDir,
		log.NewNopLogger(),
	)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height + 1})
	req := &abci.RequestPrepareProposal{
		Height:          ta.Height + 1,
		ProposerAddress: ta.ProposerAddress,
	}
	return handler(ctx, req, nil)
}

// TestDKGContributionInjection verifies the happy path: the handler generates
// a valid MsgContributeDKG with correct Feldman commitments, n-1 ECIES
// payloads (excluding self), and persists coefficients to disk.
func TestDKGContributionInjection(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()

	sk2, pk2 := elgamal.KeyGen(rand.Reader)
	sk3, pk3 := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk2.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk3.Point.ToAffineCompressed()},
	}

	roundID := ta.SeedRegisteringCeremony(validators)

	txs := callDKGContributionHandler(t, ta)
	require.Len(t, txs, 1, "expected exactly one injected DKG contribution tx")

	tag, protoMsg, err := voteapi.DecodeCeremonyTx(txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagContributeDKG, tag)

	contrib, ok := protoMsg.(*types.MsgContributeDKG)
	require.True(t, ok)
	require.Equal(t, proposerAddr, contrib.Creator)
	require.Equal(t, roundID, contrib.VoteRoundId)

	// n=3 → t = ceil(3/2) = 2
	require.Len(t, contrib.FeldmanCommitments, 2, "expected t=2 Feldman commitments")
	for j, c := range contrib.FeldmanCommitments {
		require.Len(t, c, 32, "FeldmanCommitment[%d] must be 32-byte compressed Pallas point", j)
	}

	// n-1 = 2 payloads (excludes self).
	require.Len(t, contrib.Payloads, 2, "expected n-1=2 payloads (excludes self)")
	for _, p := range contrib.Payloads {
		require.NotEqual(t, proposerAddr, p.ValidatorAddress,
			"payload must not include contributor's own address")
	}

	// Coefficients file must exist on disk with t*32 bytes.
	coeffsPath := filepath.Join(ta.EaSkDir, "coeffs."+hex.EncodeToString(roundID))
	coeffsBytes, err := os.ReadFile(coeffsPath)
	require.NoError(t, err, "coefficients file must exist after injection")
	require.Len(t, coeffsBytes, 2*32, "coefficients file must contain t*32 bytes (t=2)")

	// Each ECIES envelope must be decryptable by the intended recipient,
	// and the decrypted share must verify against the published Feldman commitments.
	G := elgamal.PallasGenerator()
	commitmentPts := make([]curvey.Point, len(contrib.FeldmanCommitments))
	for j, c := range contrib.FeldmanCommitments {
		pt, err := elgamal.DecompressPallasPoint(c)
		require.NoError(t, err)
		commitmentPts[j] = pt
	}

	recipientSKs := map[string]curvey.Scalar{
		"sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx": sk2.Scalar,
		"sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx": sk3.Scalar,
	}
	shamirIndices := map[string]int{
		"sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx": 2,
		"sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx": 3,
	}

	for _, p := range contrib.Payloads {
		recipientSk := recipientSKs[p.ValidatorAddress]
		require.NotNil(t, recipientSk, "unexpected payload for %s", p.ValidatorAddress)

		ephPk, err := elgamal.UnmarshalPublicKey(p.EphemeralPk)
		require.NoError(t, err)

		decrypted, err := ecies.Decrypt(recipientSk, &ecies.Envelope{
			Ephemeral:  ephPk.Point,
			Ciphertext: p.Ciphertext,
		})
		require.NoError(t, err, "ECIES decryption must succeed for %s", p.ValidatorAddress)
		require.Len(t, decrypted, 32, "decrypted share must be 32 bytes")

		shareScalar, err := new(curvey.ScalarPallas).SetBytes(decrypted)
		require.NoError(t, err)

		shamirIdx := shamirIndices[p.ValidatorAddress]
		ok, err = shamir.VerifyFeldmanShare(G, commitmentPts, shamirIdx, shareScalar)
		require.NoError(t, err)
		require.True(t, ok, "share for %s must verify against Feldman commitments", p.ValidatorAddress)
	}
}

// TestDKGContributionCoeffsRoundTrip verifies that persisted coefficients can
// be parsed back into scalars whose Feldman commitments match the published ones.
func TestDKGContributionCoeffsRoundTrip(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	proposerAddr := ta.ValidatorOperAddr()

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk2.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk3.Point.ToAffineCompressed()},
	}

	roundID := ta.SeedRegisteringCeremony(validators)

	txs := callDKGContributionHandler(t, ta)
	require.Len(t, txs, 1)

	_, protoMsg, err := voteapi.DecodeCeremonyTx(txs[0])
	require.NoError(t, err)
	contrib := protoMsg.(*types.MsgContributeDKG)

	coeffsPath := filepath.Join(ta.EaSkDir, "coeffs."+hex.EncodeToString(roundID))
	raw, err := os.ReadFile(coeffsPath)
	require.NoError(t, err)

	tVal := len(contrib.FeldmanCommitments)
	require.Len(t, raw, tVal*32)

	G := elgamal.PallasGenerator()
	coeffs := make([]curvey.Scalar, tVal)
	for i := 0; i < tVal; i++ {
		s, err := new(curvey.ScalarPallas).SetBytes(raw[i*32 : (i+1)*32])
		require.NoError(t, err)
		coeffs[i] = s
	}

	recomputedPts, err := shamir.FeldmanCommit(G, coeffs)
	require.NoError(t, err)

	for j, pt := range recomputedPts {
		require.Equal(t, contrib.FeldmanCommitments[j], pt.ToAffineCompressed(),
			"recomputed commitment[%d] must match published", j)
	}
}

func TestDKGContributionSkipsWhenAlreadyContributed(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	proposerAddr := ta.ValidatorOperAddr()

	_, pk2 := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk2.Point.ToAffineCompressed()},
	}

	roundID := ta.SeedRegisteringCeremony(validators)

	// Pre-populate a contribution from the proposer so the handler skips.
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)
	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)
	round.DkgContributions = append(round.DkgContributions, &types.DKGContribution{
		ValidatorAddress: proposerAddr,
	})
	err = ta.VoteKeeper().SetVoteRound(kvStore, round)
	require.NoError(t, err)
	ta.NextBlock()

	txs := callDKGContributionHandler(t, ta)
	require.Empty(t, txs, "no DKG contribution should be injected when already contributed")
}

func TestDKGContributionSkipsWhenNotCeremonyValidator(t *testing.T) {
	ta, _, _, _, _ := testutil.SetupTestAppWithPallasKey(t)

	_, pkA := elgamal.KeyGen(rand.Reader)
	_, pkB := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: "sv1stranger1xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pkA.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1stranger2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pkB.Point.ToAffineCompressed()},
	}
	ta.SeedRegisteringCeremony(validators)

	txs := callDKGContributionHandler(t, ta)
	require.Empty(t, txs, "no DKG contribution when proposer is not a ceremony validator")
}

func TestDKGContributionSkipsWhenNoPallasKey(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	valAddr := ta.ValidatorOperAddr()

	_, pk := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: valAddr, PallasPk: pk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk.Point.ToAffineCompressed()},
	}
	ta.SeedRegisteringCeremony(validators)

	handler := app.CeremonyDKGContributionPrepareProposalHandler(
		ta.VoteKeeper(),
		ta.StakingKeeper,
		"",
		t.TempDir(),
		log.NewNopLogger(),
	)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height + 1})
	req := &abci.RequestPrepareProposal{
		Height:          ta.Height + 1,
		ProposerAddress: ta.ProposerAddress,
	}
	txs := handler(ctx, req, nil)
	require.Empty(t, txs, "no DKG contribution should be injected without a Pallas key")
}

// ---------------------------------------------------------------------------
// CeremonyAckPrepareProposalHandler — DKG path
//
// When round.DkgContributions is populated, the ack handler takes the DKG
// path: loads coefficients, decrypts shares from other contributors, verifies
// per-contributor Feldman, sums into combined share, verifies against combined
// commitments, writes share to disk, and deletes the coefficients file.
// ---------------------------------------------------------------------------

// buildDKGDealtRoundState generates the full crypto state for a DKG ceremony
// with n validators and returns the round, contributions, combined commitments,
// and per-validator polynomial data needed for testing the ack handler.
func buildDKGDealtRoundState(
	t *testing.T,
	validatorAddrs []string,
	validatorPKs []curvey.Point,
) (
	contributions []*types.DKGContribution,
	combinedSerialized [][]byte,
	eaPk []byte,
	allShares [][]shamir.Share,
	allCoeffs [][]curvey.Scalar,
	combinedPts []curvey.Point,
) {
	t.Helper()

	G := elgamal.PallasGenerator()
	n := len(validatorAddrs)
	tVal := (n + 1) / 2 // ceil(n/2)

	allShares = make([][]shamir.Share, n)
	allCoeffs = make([][]curvey.Scalar, n)
	allCommitmentPts := make([][]curvey.Point, n)

	for i := 0; i < n; i++ {
		secret := new(curvey.ScalarPallas).Random(rand.Reader)
		shares, coeffs, err := shamir.Split(secret, tVal, n)
		require.NoError(t, err)
		allShares[i] = shares
		allCoeffs[i] = coeffs

		commitPts, err := shamir.FeldmanCommit(G, coeffs)
		require.NoError(t, err)
		allCommitmentPts[i] = commitPts
	}

	contributions = make([]*types.DKGContribution, n)
	for i := 0; i < n; i++ {
		commitments := make([][]byte, tVal)
		for j, pt := range allCommitmentPts[i] {
			commitments[j] = pt.ToAffineCompressed()
		}

		payloads := make([]*types.DealerPayload, 0, n-1)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			env, err := ecies.Encrypt(G, validatorPKs[j], allShares[i][j].Value.Bytes(), rand.Reader)
			require.NoError(t, err)
			payloads = append(payloads, &types.DealerPayload{
				ValidatorAddress: validatorAddrs[j],
				EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
				Ciphertext:       env.Ciphertext,
			})
		}

		contributions[i] = &types.DKGContribution{
			ValidatorAddress:   validatorAddrs[i],
			FeldmanCommitments: commitments,
			Payloads:           payloads,
		}
	}

	combinedPts, err := shamir.CombineCommitments(allCommitmentPts)
	require.NoError(t, err)

	combinedSerialized = make([][]byte, len(combinedPts))
	for j, c := range combinedPts {
		combinedSerialized[j] = c.ToAffineCompressed()
	}
	eaPk = combinedSerialized[0]
	return
}

// seedDKGDealtRound writes a DEALT round with DKG contributions to the store
// and persists the proposer's polynomial coefficients to disk.
func seedDKGDealtRound(
	t *testing.T,
	ta *testutil.TestApp,
	validatorAddrs []string,
	validatorPKs []curvey.Point,
	contributions []*types.DKGContribution,
	combinedSerialized [][]byte,
	eaPk []byte,
	proposerCoeffs []curvey.Scalar,
) []byte {
	t.Helper()

	n := len(validatorAddrs)
	tVal := (n + 1) / 2

	validators := make([]*types.ValidatorPallasKey, n)
	for i := range validatorAddrs {
		validators[i] = &types.ValidatorPallasKey{
			ValidatorAddress: validatorAddrs[i],
			PallasPk:         validatorPKs[i].ToAffineCompressed(),
			ShamirIndex:      uint32(i + 1),
		}
	}

	roundID := make([]byte, 32)
	copy(roundID, []byte("dkg-ack-test"))

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	round := &types.VoteRound{
		VoteRoundId:          roundID,
		Status:               types.SessionStatus_SESSION_STATUS_PENDING,
		EaPk:                 eaPk,
		CeremonyStatus:       types.CeremonyStatus_CEREMONY_STATUS_DEALT,
		CeremonyValidators:   validators,
		DkgContributions:     contributions,
		CeremonyPhaseStart:   uint64(ta.Time.Unix()),
		CeremonyPhaseTimeout: types.DefaultDealTimeout,
		Threshold:            uint32(tVal),
		FeldmanCommitments:   combinedSerialized,
	}
	err := ta.VoteKeeper().SetVoteRound(kvStore, round)
	require.NoError(t, err)
	ta.NextBlock()

	// Persist proposer's coefficients to disk.
	coeffsPath := filepath.Join(ta.EaSkDir, "coeffs."+hex.EncodeToString(roundID))
	buf := make([]byte, 0, tVal*32)
	for _, c := range proposerCoeffs {
		buf = append(buf, c.Bytes()...)
	}
	require.NoError(t, os.WriteFile(coeffsPath, buf, 0600))

	return roundID
}

func TestDKGAckHappyPath(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	addrs := []string{proposerAddr, "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx"}
	pks := []curvey.Point{pallasPk.Point, pk2.Point, pk3.Point}

	contributions, combinedSerialized, eaPk, allShares, allCoeffs, combinedPts :=
		buildDKGDealtRoundState(t, addrs, pks)

	roundID := seedDKGDealtRound(t, ta, addrs, pks, contributions, combinedSerialized, eaPk, allCoeffs[0])

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "expected one injected ack tx")

	tag, _, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagAckExecutiveAuthorityKey, tag)

	// Share file must exist with 32 bytes.
	sharePath := filepath.Join(ta.EaSkDir, "share."+hex.EncodeToString(roundID))
	shareBytes, err := os.ReadFile(sharePath)
	require.NoError(t, err)
	require.Len(t, shareBytes, 32)

	// Combined share must verify against combined Feldman commitments.
	shareScalar, err := new(curvey.ScalarPallas).SetBytes(shareBytes)
	require.NoError(t, err)
	ok, err := shamir.VerifyFeldmanShare(G, combinedPts, 1, shareScalar)
	require.NoError(t, err)
	require.True(t, ok, "combined share must verify against combined Feldman commitments")

	// Combined share must equal sum of all contributors' shares at index 0 (1-based index 1).
	expectedCombined := allShares[0][0].Value
	for i := 1; i < 3; i++ {
		expectedCombined = expectedCombined.Add(allShares[i][0].Value)
	}
	require.Equal(t, expectedCombined.Bytes(), shareBytes,
		"combined share must equal sum of all contributors' shares at this index")

	// Coefficients file must be deleted.
	coeffsPath := filepath.Join(ta.EaSkDir, "coeffs."+hex.EncodeToString(roundID))
	_, statErr := os.Stat(coeffsPath)
	require.True(t, os.IsNotExist(statErr), "coefficients file must be deleted after ack")
}

func TestDKGAckRejectsBadShare(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	addrs := []string{proposerAddr, "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx"}
	pks := []curvey.Point{pallasPk.Point, pk2.Point, pk3.Point}

	contributions, combinedSerialized, eaPk, _, allCoeffs, _ :=
		buildDKGDealtRoundState(t, addrs, pks)

	// Tamper with contributor 1's payload for the proposer: encrypt a random
	// (wrong) share instead of the correct f_1(1).
	wrongScalar := new(curvey.ScalarPallas).Random(rand.Reader)
	env, err := ecies.Encrypt(G, pallasPk.Point, wrongScalar.Bytes(), rand.Reader)
	require.NoError(t, err)

	for _, p := range contributions[1].Payloads {
		if p.ValidatorAddress == proposerAddr {
			p.EphemeralPk = env.Ephemeral.ToAffineCompressed()
			p.Ciphertext = env.Ciphertext
			break
		}
	}

	seedDKGDealtRound(t, ta, addrs, pks, contributions, combinedSerialized, eaPk, allCoeffs[0])

	resp := ta.CallPrepareProposal()
	require.Empty(t, resp.Txs, "ack must not be injected when a contributor's share fails Feldman verification")
}

func TestDKGAckSingleValidator(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	addrs := []string{proposerAddr}
	pks := []curvey.Point{pallasPk.Point}

	contributions, combinedSerialized, eaPk, allShares, allCoeffs, combinedPts :=
		buildDKGDealtRoundState(t, addrs, pks)

	roundID := seedDKGDealtRound(t, ta, addrs, pks, contributions, combinedSerialized, eaPk, allCoeffs[0])

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "expected one injected ack tx")

	tag, _, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagAckExecutiveAuthorityKey, tag)

	sharePath := filepath.Join(ta.EaSkDir, "share."+hex.EncodeToString(roundID))
	shareBytes, err := os.ReadFile(sharePath)
	require.NoError(t, err)

	// n=1: combined share is just own partial f_0(1).
	shareScalar, err := new(curvey.ScalarPallas).SetBytes(shareBytes)
	require.NoError(t, err)
	ok, err := shamir.VerifyFeldmanShare(G, combinedPts, 1, shareScalar)
	require.NoError(t, err)
	require.True(t, ok)

	require.Equal(t, allShares[0][0].Value.Bytes(), shareBytes,
		"n=1: combined share must equal the single contributor's share")
}

// ---------------------------------------------------------------------------
// DKG contribution through the full PrepareProposal pipeline
//
// After Phase 6, ComposedPrepareProposalHandler wires the DKG contribution
// handler instead of the deal handler. This test verifies that calling
// CallPrepareProposal on a REGISTERING round injects a MsgContributeDKG.
// ---------------------------------------------------------------------------

func TestDKGContributionThroughPipeline(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	proposerAddr := ta.ValidatorOperAddr()

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk2.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk3.Point.ToAffineCompressed()},
	}
	roundID := ta.SeedRegisteringCeremony(validators)

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "pipeline should inject exactly one DKG contribution tx")

	tag, protoMsg, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagContributeDKG, tag,
		"injected tx must be a DKG contribution, not a deal")

	contrib, ok := protoMsg.(*types.MsgContributeDKG)
	require.True(t, ok)
	require.Equal(t, proposerAddr, contrib.Creator)
	require.Equal(t, roundID, contrib.VoteRoundId)
	require.Len(t, contrib.FeldmanCommitments, 2, "t=ceil(3/2)=2 commitments")
	require.Len(t, contrib.Payloads, 2, "n-1=2 ECIES payloads (self excluded)")
}

// ---------------------------------------------------------------------------
// EndBlocker clears DkgContributions on ceremony timeout
//
// Seeds a DEALT round with DkgContributions populated, then advances past the
// ceremony phase timeout so the EndBlocker fires. Verifies that the round
// resets to REGISTERING with DkgContributions = nil.
// ---------------------------------------------------------------------------

func TestEndBlockerClearsDKGContributionsOnTimeout(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	proposerAddr := ta.ValidatorOperAddr()
	_, pk2 := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: proposerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk2.Point.ToAffineCompressed()},
	}

	roundID := make([]byte, 32)
	roundID[0] = 0xEB

	phaseStart := uint64(ta.Time.Unix())
	phaseTimeout := uint64(30 * 60)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	round := &types.VoteRound{
		VoteRoundId:          roundID,
		Status:               types.SessionStatus_SESSION_STATUS_PENDING,
		CeremonyStatus:       types.CeremonyStatus_CEREMONY_STATUS_DEALT,
		CeremonyValidators:   validators,
		CeremonyPhaseStart:   phaseStart,
		CeremonyPhaseTimeout: phaseTimeout,
		EaPk:                 make([]byte, 32),
		Threshold:            1,
		FeldmanCommitments:   [][]byte{make([]byte, 32)},
		DkgContributions: []*types.DKGContribution{
			{ValidatorAddress: proposerAddr, FeldmanCommitments: [][]byte{{0x01}}},
			{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", FeldmanCommitments: [][]byte{{0x02}}},
		},
		VoteEndTime:      uint64(ta.Time.Add(24 * time.Hour).Unix()),
		Proposals:        testutil.SampleProposals(),
		NullifierImtRoot: make([]byte, 32),
		NcRoot:           make([]byte, 32),
	}
	require.NoError(t, ta.VoteKeeper().SetVoteRound(kvStore, round))
	ta.NextBlock()

	// Advance past the ceremony phase timeout to trigger the EndBlocker reset.
	timeoutTime := time.Unix(int64(phaseStart+phaseTimeout)+1, 0)
	ta.NextBlockAtTime(timeoutTime)

	ctx = ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore = ta.VoteKeeper().OpenKVStore(ctx)
	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)

	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING, round.CeremonyStatus,
		"ceremony should reset to REGISTERING after timeout")
	require.Nil(t, round.DkgContributions,
		"DkgContributions must be cleared on timeout reset")
	require.Nil(t, round.CeremonyAcks,
		"CeremonyAcks must be cleared on timeout reset")
}

// ---------------------------------------------------------------------------
// ackDKGRound — unit-level error path coverage
//
// Calls AckDKGRound directly (not through PrepareProposal) with a lightweight
// golden state, then mutates one thing per table row to trigger each error.
// ---------------------------------------------------------------------------

// goldenAckState holds everything needed to call AckDKGRound with a valid
// 3-validator DKG round. Tests clone and mutate individual fields.
type goldenAckState struct {
	pallasSk      *elgamal.SecretKey
	round         *types.VoteRound
	proposerAddr  string
	shamirIndex   int
	G             curvey.Point
	ceremonyDir   string
	expectedShare curvey.Scalar
}

// buildGoldenAckState constructs a fully valid 3-validator DKG round state
// (keys, Shamir shares, ECIES envelopes, Feldman commitments, and a
// coefficients file on disk) suitable for calling AckDKGRound directly.
// Tests clone the returned round and apply targeted mutations to trigger
// individual error paths.
func buildGoldenAckState(t *testing.T) goldenAckState {
	t.Helper()

	G := elgamal.PallasGenerator()
	const n = 3
	tVal := 2 // ceil(3/2)

	addrs := []string{
		"sv1proposer0xxxxxxxxxxxxxxxxxxxxxxxxxx",
		"sv1validator1xxxxxxxxxxxxxxxxxxxxxxxxxx",
		"sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx",
	}

	sks := make([]*elgamal.SecretKey, n)
	pks := make([]*elgamal.PublicKey, n)
	for i := 0; i < n; i++ {
		sks[i], pks[i] = elgamal.KeyGen(rand.Reader)
	}

	allShares := make([][]shamir.Share, n)
	allCoeffs := make([][]curvey.Scalar, n)
	allCommitPts := make([][]curvey.Point, n)

	for i := 0; i < n; i++ {
		secret := new(curvey.ScalarPallas).Random(rand.Reader)
		shares, coeffs, err := shamir.Split(secret, tVal, n)
		require.NoError(t, err)
		allShares[i] = shares
		allCoeffs[i] = coeffs
		commitPts, err := shamir.FeldmanCommit(G, coeffs)
		require.NoError(t, err)
		allCommitPts[i] = commitPts
	}

	contributions := make([]*types.DKGContribution, n)
	for i := 0; i < n; i++ {
		commitments := make([][]byte, tVal)
		for j, pt := range allCommitPts[i] {
			commitments[j] = pt.ToAffineCompressed()
		}
		payloads := make([]*types.DealerPayload, 0, n-1)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			env, err := ecies.Encrypt(G, pks[j].Point, allShares[i][j].Value.Bytes(), rand.Reader)
			require.NoError(t, err)
			payloads = append(payloads, &types.DealerPayload{
				ValidatorAddress: addrs[j],
				EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
				Ciphertext:       env.Ciphertext,
			})
		}
		contributions[i] = &types.DKGContribution{
			ValidatorAddress:   addrs[i],
			FeldmanCommitments: commitments,
			Payloads:           payloads,
		}
	}

	combinedPts, err := shamir.CombineCommitments(allCommitPts)
	require.NoError(t, err)
	combinedSerialized := make([][]byte, len(combinedPts))
	for j, c := range combinedPts {
		combinedSerialized[j] = c.ToAffineCompressed()
	}

	dir := t.TempDir()
	roundID := []byte("ack-dkg-unit-test")
	coeffsPath := filepath.Join(dir, "coeffs."+hex.EncodeToString(roundID))
	buf := make([]byte, 0, tVal*32)
	for _, c := range allCoeffs[0] {
		buf = append(buf, c.Bytes()...)
	}
	require.NoError(t, os.WriteFile(coeffsPath, buf, 0600))

	expectedShare := allShares[0][0].Value
	for i := 1; i < n; i++ {
		expectedShare = expectedShare.Add(allShares[i][0].Value)
	}

	return goldenAckState{
		pallasSk:     sks[0],
		proposerAddr: addrs[0],
		shamirIndex:  1,
		G:            G,
		ceremonyDir:  dir,
		round: &types.VoteRound{
			VoteRoundId:        roundID,
			Threshold:          uint32(tVal),
			DkgContributions:   contributions,
			FeldmanCommitments: combinedSerialized,
		},
		expectedShare: expectedShare,
	}
}

func cloneRound(r *types.VoteRound) *types.VoteRound {
	return proto.Clone(r).(*types.VoteRound)
}

func proposerPayload(contrib *types.DKGContribution, addr string) *types.DealerPayload {
	for _, p := range contrib.Payloads {
		if p.ValidatorAddress == addr {
			return p
		}
	}
	return nil
}

func TestAckDKGRound(t *testing.T) {
	gs := buildGoldenAckState(t)
	G := gs.G
	logger := log.NewNopLogger()

	tests := []struct {
		name    string
		mutate  func(t *testing.T, round *types.VoteRound, dir string)
		wantErr string
	}{
		{
			name:   "happy path",
			mutate: nil,
		},
		{
			name: "missing coefficients file",
			mutate: func(t *testing.T, _ *types.VoteRound, dir string) {
				p := filepath.Join(dir, "coeffs."+hex.EncodeToString(gs.round.VoteRoundId))
				require.NoError(t, os.Remove(p))
			},
			wantErr: "load coefficients",
		},
		{
			name: "no payload for proposer",
			mutate: func(_ *testing.T, round *types.VoteRound, _ string) {
				c := round.DkgContributions[1]
				filtered := make([]*types.DealerPayload, 0, len(c.Payloads)-1)
				for _, p := range c.Payloads {
					if p.ValidatorAddress != gs.proposerAddr {
						filtered = append(filtered, p)
					}
				}
				c.Payloads = filtered
			},
			wantErr: "no payload for",
		},
		{
			name: "invalid ephemeral pk",
			mutate: func(_ *testing.T, round *types.VoteRound, _ string) {
				p := proposerPayload(round.DkgContributions[1], gs.proposerAddr)
				p.EphemeralPk = []byte{0xff}
			},
			wantErr: "invalid ephemeral_pk",
		},
		{
			name: "ECIES decrypt fails",
			mutate: func(t *testing.T, round *types.VoteRound, _ string) {
				_, wrongPk := elgamal.KeyGen(rand.Reader)
				env, err := ecies.Encrypt(G, wrongPk.Point, make([]byte, 32), rand.Reader)
				require.NoError(t, err)
				p := proposerPayload(round.DkgContributions[1], gs.proposerAddr)
				p.EphemeralPk = env.Ephemeral.ToAffineCompressed()
				p.Ciphertext = env.Ciphertext
			},
			wantErr: "ECIES decryption failed",
		},
		{
			name: "zero share scalar",
			mutate: func(t *testing.T, round *types.VoteRound, _ string) {
				proposerPk := G.Mul(gs.pallasSk.Scalar)
				env, err := ecies.Encrypt(G, proposerPk, make([]byte, 32), rand.Reader)
				require.NoError(t, err)
				p := proposerPayload(round.DkgContributions[1], gs.proposerAddr)
				p.EphemeralPk = env.Ephemeral.ToAffineCompressed()
				p.Ciphertext = env.Ciphertext
			},
			wantErr: "invalid share scalar",
		},
		{
			name: "invalid contributor commitments",
			mutate: func(_ *testing.T, round *types.VoteRound, _ string) {
				round.DkgContributions[1].FeldmanCommitments[0] = []byte{0xff}
			},
			wantErr: "invalid commitments",
		},
		{
			name: "contributor Feldman fails",
			mutate: func(t *testing.T, round *types.VoteRound, _ string) {
				wrongScalar := new(curvey.ScalarPallas).Random(rand.Reader)
				proposerPk := G.Mul(gs.pallasSk.Scalar)
				env, err := ecies.Encrypt(G, proposerPk, wrongScalar.Bytes(), rand.Reader)
				require.NoError(t, err)
				p := proposerPayload(round.DkgContributions[1], gs.proposerAddr)
				p.EphemeralPk = env.Ephemeral.ToAffineCompressed()
				p.Ciphertext = env.Ciphertext
			},
			wantErr: "share failed Feldman",
		},
		{
			name: "invalid combined commitments",
			mutate: func(_ *testing.T, round *types.VoteRound, _ string) {
				round.FeldmanCommitments[0] = []byte{0xff}
			},
			wantErr: "invalid combined commitments",
		},
		{
			name: "combined Feldman fails",
			mutate: func(t *testing.T, round *types.VoteRound, _ string) {
				secret := new(curvey.ScalarPallas).Random(rand.Reader)
				_, wrongCoeffs, err := shamir.Split(secret, int(round.Threshold), 3)
				require.NoError(t, err)
				wrongPts, err := shamir.FeldmanCommit(G, wrongCoeffs)
				require.NoError(t, err)
				for i, pt := range wrongPts {
					round.FeldmanCommitments[i] = pt.ToAffineCompressed()
				}
			},
			wantErr: "combined share failed Feldman",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			roundClone := cloneRound(gs.round)
			srcCoeffs := filepath.Join(gs.ceremonyDir, "coeffs."+hex.EncodeToString(gs.round.VoteRoundId))
			dstCoeffs := filepath.Join(dir, "coeffs."+hex.EncodeToString(gs.round.VoteRoundId))
			raw, err := os.ReadFile(srcCoeffs)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(dstCoeffs, raw, 0600))

			if tc.mutate != nil {
				tc.mutate(t, roundClone, dir)
			}

			secretBytes, sk, err := app.AckDKGRound(gs.pallasSk, roundClone, gs.proposerAddr, gs.shamirIndex, G, dir, logger)

			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Nil(t, secretBytes)
				require.Nil(t, sk)
				return
			}

			require.NoError(t, err)
			require.Len(t, secretBytes, 32)
			require.NotNil(t, sk)
			require.Equal(t, gs.expectedShare.Bytes(), secretBytes)
		})
	}
}

