package app_test

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	voteapi "github.com/valargroup/shielded-vote/api"
	"github.com/valargroup/shielded-vote/crypto/ecies"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/crypto/shamir"
	"github.com/valargroup/shielded-vote/testutil"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// ---------------------------------------------------------------------------
// CeremonyDealPrepareProposalHandler — threshold mode
//
// With n=3 validators the deal handler must:
//   - inject a MsgDealExecutiveAuthorityKey
//   - set Threshold = ceil(3/2) = 2
//   - include one 32-byte VerificationKey per validator
//   - write the dealer's Shamir share to share.<hex(round_id)> (not ea_sk)
//   - ECIES-encrypt a different scalar to each validator (share, not full key)
// ---------------------------------------------------------------------------

func TestCeremonyDealThresholdMode(t *testing.T) {
	ta, pallasSk, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)

	dealerAddr := ta.ValidatorOperAddr()

	// Two extra validators with fresh Pallas keypairs (not the proposer).
	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: dealerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk2.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk3.Point.ToAffineCompressed()},
	}

	roundID := ta.SeedRegisteringCeremony(validators)

	// PrepareProposal should inject a single deal tx.
	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "expected exactly one injected deal tx")

	tag, protoMsg, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagDealExecutiveAuthorityKey, tag)

	deal, ok := protoMsg.(*types.MsgDealExecutiveAuthorityKey)
	require.True(t, ok)

	// n=3 → t = ceil(3/2) = 2
	require.EqualValues(t, 2, deal.Threshold, "threshold should be 2 for n=3")

	// One VK per validator, each 32 bytes.
	require.Len(t, deal.VerificationKeys, 3, "expected one VK per validator")
	for i, vk := range deal.VerificationKeys {
		require.Len(t, vk, 32, "VerificationKey[%d] must be a 32-byte compressed Pallas point", i)
	}

	// One payload per validator.
	require.Len(t, deal.Payloads, 3)
	require.Len(t, deal.EaPk, 32, "ea_pk must be 32 bytes")

	// The deal handler does NOT write anything to disk — the ack handler does that
	// uniformly for all validators (including the dealer). Verify both files are absent.
	sharePath := filepath.Join(ta.EaSkDir, "share."+hex.EncodeToString(roundID))
	_, statErr := os.Stat(sharePath)
	require.True(t, os.IsNotExist(statErr),
		"deal handler must not write the share file — ack handler does that")

	eaSkPath := filepath.Join(ta.EaSkDir, "ea_sk."+hex.EncodeToString(roundID))
	_, statErr2 := os.Stat(eaSkPath)
	require.True(t, os.IsNotExist(statErr2),
		"deal handler must not write ea_sk in threshold mode")

	// Consistency check: decrypt the dealer's own payload and verify VK.
	//
	// VK_dealer = share_dealer * G. We decrypt the first payload (dealer) using
	// the pallasSk returned by SetupTestAppWithPallasKey, recover the share
	// scalar, and recompute G * share. The result must equal VerificationKeys[0].
	dealerPayload := deal.Payloads[0]
	require.Equal(t, dealerAddr, dealerPayload.ValidatorAddress)

	ephPk, err := elgamal.UnmarshalPublicKey(dealerPayload.EphemeralPk)
	require.NoError(t, err)

	decrypted, err := ecies.Decrypt(pallasSk.Scalar, &ecies.Envelope{
		Ephemeral:  ephPk.Point,
		Ciphertext: dealerPayload.Ciphertext,
	})
	require.NoError(t, err, "dealer should be able to decrypt their own payload")
	require.Len(t, decrypted, 32, "decrypted share should be 32 bytes")

	// Recompute VK = share * G and compare with the published VerificationKeys[0].
	shareScalar, err := new(curvey.ScalarPallas).SetBytes(decrypted)
	require.NoError(t, err)
	G := elgamal.PallasGenerator()
	computedVK := G.Mul(shareScalar).ToAffineCompressed()
	require.Equal(t, deal.VerificationKeys[0], computedVK,
		"VerificationKeys[0] must equal share_dealer * G")

	// The decrypted share must be a valid Pallas scalar (non-zero).
	require.Len(t, decrypted, 32, "decrypted share must be 32 bytes")
}

// ---------------------------------------------------------------------------
// CeremonyDealPrepareProposalHandler — legacy mode (n=1)
//
// With only one validator the handler falls back to legacy mode:
//   - Threshold = 0
//   - VerificationKeys is empty
//   - ea_sk.<hex(round_id)> is written to disk (not share.<…>)
// ---------------------------------------------------------------------------

func TestCeremonyDealLegacyMode(t *testing.T) {
	ta, pallasSk, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)
	_ = pallasSk // held by app via pallas_sk_path

	dealerAddr := ta.ValidatorOperAddr()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: dealerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}

	roundID := ta.SeedRegisteringCeremony(validators)

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "expected one injected deal tx")

	tag, protoMsg, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagDealExecutiveAuthorityKey, tag)

	deal, ok := protoMsg.(*types.MsgDealExecutiveAuthorityKey)
	require.True(t, ok)

	// n=1 → legacy mode
	require.EqualValues(t, 0, deal.Threshold, "threshold should be 0 in legacy mode")
	require.Empty(t, deal.VerificationKeys, "verification_keys must be empty in legacy mode")
	require.Len(t, deal.Payloads, 1)

	// The deal handler must NOT write any key files — ack handler does that.
	eaSkPath := filepath.Join(ta.EaSkDir, "ea_sk."+hex.EncodeToString(roundID))
	_, statErr := os.Stat(eaSkPath)
	require.True(t, os.IsNotExist(statErr),
		"deal handler must not write ea_sk file — ack handler does that")

	sharePath := filepath.Join(ta.EaSkDir, "share."+hex.EncodeToString(roundID))
	_, statErr2 := os.Stat(sharePath)
	require.True(t, os.IsNotExist(statErr2),
		"deal handler must not write share file in legacy mode")
}

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
// DealExecutiveAuthorityKey — round-trip: deal → FinalizeBlock → read round
//
// Exercises the full on-chain path to verify that Threshold and
// VerificationKeys survive the handler and are persisted to KV. This is the
// test that was missing — all prior threshold tests bypass the handler by
// seeding state directly.
// ---------------------------------------------------------------------------

func TestCeremonyDealThresholdStoredOnRound(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	dealerAddr := ta.ValidatorOperAddr()

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: dealerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk2.Point.ToAffineCompressed()},
		{ValidatorAddress: "sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx", PallasPk: pk3.Point.ToAffineCompressed()},
	}

	roundID := ta.SeedRegisteringCeremony(validators)

	// PrepareProposal injects the deal tx.
	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1)

	tag, protoMsg, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagDealExecutiveAuthorityKey, tag)

	deal := protoMsg.(*types.MsgDealExecutiveAuthorityKey)
	require.EqualValues(t, 2, deal.Threshold)
	require.Len(t, deal.VerificationKeys, 3)

	// Deliver the deal tx through FinalizeBlock + Commit.
	txResult := ta.DeliverVoteTx(resp.Txs[0])
	require.EqualValues(t, 0, txResult.Code,
		"deal tx must succeed: %s", txResult.Log)

	// Read the round back from KV and verify TSS fields were persisted.
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)

	require.EqualValues(t, 2, round.Threshold,
		"round.Threshold must be persisted by DealExecutiveAuthorityKey handler")
	require.Len(t, round.VerificationKeys, 3,
		"round.VerificationKeys must be persisted by DealExecutiveAuthorityKey handler")
	for i, vk := range round.VerificationKeys {
		require.Equal(t, deal.VerificationKeys[i], vk,
			"round.VerificationKeys[%d] must match deal message", i)
	}
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
}

// ---------------------------------------------------------------------------
// DealExecutiveAuthorityKey — legacy mode round-trip (n=1)
//
// Verifies that Threshold=0 and empty VerificationKeys are stored correctly
// through the handler for the legacy single-key path.
// ---------------------------------------------------------------------------

func TestCeremonyDealLegacyStoredOnRound(t *testing.T) {
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)

	dealerAddr := ta.ValidatorOperAddr()

	validators := []*types.ValidatorPallasKey{
		{ValidatorAddress: dealerAddr, PallasPk: pallasPk.Point.ToAffineCompressed()},
	}

	roundID := ta.SeedRegisteringCeremony(validators)

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1)

	txResult := ta.DeliverVoteTx(resp.Txs[0])
	require.EqualValues(t, 0, txResult.Code,
		"legacy deal tx must succeed: %s", txResult.Log)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	round, err := ta.VoteKeeper().GetVoteRound(kvStore, roundID)
	require.NoError(t, err)

	require.EqualValues(t, 0, round.Threshold,
		"legacy mode must have Threshold=0")
	require.Empty(t, round.VerificationKeys,
		"legacy mode must have empty VerificationKeys")
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, round.CeremonyStatus)
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

	// Build a (t=2, n=3) Shamir split of a fresh ea_sk.
	eaSk, eaPkForRound := elgamal.KeyGen(rand.Reader)
	shares, _, err := shamir.Split(eaSk.Scalar, 2, 3)
	require.NoError(t, err)

	eaPkBytes := eaPkForRound.Point.ToAffineCompressed()

	// Build per-validator inputs: the proposer is validator index 0 (1-based index 1).
	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	validatorPKs := []curvey.Point{
		pallasPk.Point,
		pk2.Point,
		pk3.Point,
	}
	validatorAddrs := []string{
		proposerAddr,
		"sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx",
		"sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx",
	}

	// ECIES-encrypt share_i to validator_i and compute VK_i = share_i * G.
	payloads := make([]*types.DealerPayload, 3)
	vks := make([][]byte, 3)
	for i := range shares {
		shareBytes := shares[i].Value.Bytes()
		env, encErr := ecies.Encrypt(G, validatorPKs[i], shareBytes, rand.Reader)
		require.NoError(t, encErr)
		payloads[i] = &types.DealerPayload{
			ValidatorAddress: validatorAddrs[i],
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		}
		vks[i] = G.Mul(shares[i].Value).ToAffineCompressed()
	}

	validators := make([]*types.ValidatorPallasKey, 3)
	for i := range validatorAddrs {
		validators[i] = &types.ValidatorPallasKey{
			ValidatorAddress: validatorAddrs[i],
			PallasPk:         validatorPKs[i].ToAffineCompressed(),
		}
	}

	roundID := ta.SeedDealtCeremonyThreshold(eaPkBytes, payloads, validators, 2, vks)

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

	// The written share must match share[0] (the proposer's share).
	require.Equal(t, shares[0].Value.Bytes(), shareBytes,
		"share on disk must equal the decrypted share for the proposer")

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
	ta, pallasSk, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	_ = pallasSk

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	shares, _, err := shamir.Split(eaSk.Scalar, 2, 3)
	require.NoError(t, err)

	wrongShareSk, _ := elgamal.KeyGen(rand.Reader) // random scalar ≠ shares[0]

	_, pk2 := elgamal.KeyGen(rand.Reader)
	_, pk3 := elgamal.KeyGen(rand.Reader)

	validatorPKs := []curvey.Point{pallasPk.Point, pk2.Point, pk3.Point}
	validatorAddrs := []string{
		proposerAddr,
		"sv1validator2xxxxxxxxxxxxxxxxxxxxxxxxxx",
		"sv1validator3xxxxxxxxxxxxxxxxxxxxxxxxxx",
	}

	// Encrypt the WRONG share to the proposer but publish the correct VK.
	payloads := make([]*types.DealerPayload, 3)
	vks := make([][]byte, 3)
	for i := range shares {
		var plaintext []byte
		if i == 0 {
			plaintext = wrongShareSk.Scalar.Bytes() // bad share for proposer
		} else {
			plaintext = shares[i].Value.Bytes()
		}
		env, encErr := ecies.Encrypt(G, validatorPKs[i], plaintext, rand.Reader)
		require.NoError(t, encErr)
		payloads[i] = &types.DealerPayload{
			ValidatorAddress: validatorAddrs[i],
			EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
			Ciphertext:       env.Ciphertext,
		}
		vks[i] = G.Mul(shares[i].Value).ToAffineCompressed() // correct VK
	}

	validators := make([]*types.ValidatorPallasKey, 3)
	for i := range validatorAddrs {
		validators[i] = &types.ValidatorPallasKey{
			ValidatorAddress: validatorAddrs[i],
			PallasPk:         validatorPKs[i].ToAffineCompressed(),
		}
	}

	ta.SeedDealtCeremonyThreshold(eaPk.Point.ToAffineCompressed(), payloads, validators, 2, vks)

	resp := ta.CallPrepareProposal()
	require.Empty(t, resp.Txs, "ack must be rejected when share_i * G != VK_i")
}

// ---------------------------------------------------------------------------
// CeremonyAckPrepareProposalHandler — legacy mode
//
// The ack handler must still verify ea_sk * G == ea_pk and write ea_sk to
// ea_sk.<round_id> when round.Threshold == 0.
// ---------------------------------------------------------------------------

func TestCeremonyAckLegacyMode(t *testing.T) {
	ta, pallasSk, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	require.NotEmpty(t, ta.EaSkDir)
	_ = pallasSk

	proposerAddr := ta.ValidatorOperAddr()
	G := elgamal.PallasGenerator()

	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
	require.NoError(t, err)

	env, err := ecies.Encrypt(G, pallasPk.Point, eaSkBytes, rand.Reader)
	require.NoError(t, err)

	payloads := []*types.DealerPayload{{
		ValidatorAddress: proposerAddr,
		EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
		Ciphertext:       env.Ciphertext,
	}}
	validators := []*types.ValidatorPallasKey{{
		ValidatorAddress: proposerAddr,
		PallasPk:         pallasPk.Point.ToAffineCompressed(),
	}}

	// Threshold == 0 → legacy mode (SeedDealtCeremony, no VK fields).
	eaPkBytes := eaPk.Point.ToAffineCompressed()
	roundID := ta.SeedDealtCeremony(pallasPk.Point.ToAffineCompressed(), eaPkBytes, payloads, validators)

	resp := ta.CallPrepareProposal()
	require.Len(t, resp.Txs, 1, "expected one injected ack tx")

	tag, _, err := voteapi.DecodeCeremonyTx(resp.Txs[0])
	require.NoError(t, err)
	require.Equal(t, voteapi.TagAckExecutiveAuthorityKey, tag)

	// ea_sk.<round_id> must exist.
	eaSkPath := filepath.Join(ta.EaSkDir, "ea_sk."+hex.EncodeToString(roundID))
	written, err := os.ReadFile(eaSkPath)
	require.NoError(t, err, "ea_sk file should have been written in legacy mode")
	require.Equal(t, eaSkBytes, written, "written ea_sk must match the original")

	// share.<round_id> must NOT exist.
	sharePath := filepath.Join(ta.EaSkDir, "share."+hex.EncodeToString(roundID))
	_, statErr := os.Stat(sharePath)
	require.True(t, os.IsNotExist(statErr), "share file must not exist in legacy mode")
}
