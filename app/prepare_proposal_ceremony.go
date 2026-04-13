package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	"github.com/mikelodder7/curvey"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/crypto/ecies"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	votekeeper "github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// sharePathForRound returns the per-round Shamir share file path (threshold mode).
// In threshold mode each validator writes their scalar share here instead of the
// full ea_sk. The file stores 32 raw bytes (the Pallas Fq scalar).
//
//	<dir>/share.<hex(round_id)>
func sharePathForRound(dir string, roundID []byte) string {
	return filepath.Join(dir, "share."+hex.EncodeToString(roundID))
}

// thresholdForN delegates to the canonical keeper.ThresholdForN.
func thresholdForN(n int) (int, error) {
	return votekeeper.ThresholdForN(n)
}

// pallasSkLoader creates a sync.Once-guarded loader for the validator's
// Pallas secret key file. Shared by the deal and ack ceremony injectors.
func pallasSkLoader(pallasSkPath string, logger log.Logger, phase string) func() (*elgamal.SecretKey, error) {
	var (
		once sync.Once
		sk   *elgamal.SecretKey
		err  error
	)
	return func() (*elgamal.SecretKey, error) {
		once.Do(func() {
			if pallasSkPath == "" {
				logger.Warn(fmt.Sprintf("PrepareProposal: vote.pallas_sk_path is empty — auto-%s disabled", phase))
				err = os.ErrNotExist
				return
			}
			logger.Info(fmt.Sprintf("PrepareProposal: loading Pallas secret key for %s", phase), "path", pallasSkPath)
			raw, readErr := os.ReadFile(pallasSkPath)
			if readErr != nil {
				err = readErr
				logger.Error(fmt.Sprintf("PrepareProposal[%s]: failed to load Pallas secret key", phase),
					"path", pallasSkPath, "err", readErr)
				return
			}
			sk, err = elgamal.UnmarshalSecretKey(raw)
			if err != nil {
				logger.Error(fmt.Sprintf("PrepareProposal[%s]: failed to parse Pallas secret key", phase),
					"path", pallasSkPath, "err", err)
			}
		})
		return sk, err
	}
}

// CeremonyDealPrepareProposalHandler returns a PrepareProposalInjector that
// checks whether a PENDING round needs a deal and, if so, generates a fresh
// ea_sk, and injects a MsgDealExecutiveAuthorityKey.
//
// ea_sk is Shamir-split into (t, n) shares with t = ceil(n/2) (min 1).
// Each validator receives ECIES(share_i, pk_i). Feldman polynomial commitments
// C_j = a_j*G are included so validators can verify their share on ack.
// The dealer's share is written to disk by the ack handler (when the dealer is
// next the block proposer after DEALT is set), not here.
//
// The proposer must be in the round's CeremonyValidators to deal.
func CeremonyDealPrepareProposalHandler(
	voteKeeper *votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	pallasSkPath string,
	ceremonyDir string,
	logger log.Logger,
) PrepareProposalInjector {
	loadPallasSk := pallasSkLoader(pallasSkPath, logger, "deal")

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		// Verify we have a Pallas key. The deal handler needs the Pallas SK
		// only to confirm this node is configured as a validator. The actual
		// ECIES encryption uses each validator's public key from the registry.
		// We load pallasSk to confirm we ARE a valid ceremony participant
		// (but don't need it for encryption).
		if _, err := loadPallasSk(); err != nil {
			return txs
		}

		proposerValAddr, err := resolveProposer(ctx, stakingKeeper, req.ProposerAddress)
		if err != nil {
			return txs
		}

		kvStore := voteKeeper.OpenKVStore(ctx)

		// Find first PENDING round with ceremony in REGISTERING.
		round, err := voteKeeper.FindFirstPendingRound(kvStore, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING)
		if err != nil {
			logger.Error("PrepareProposal[deal]: failed to find pending round", "err", err)
			return txs
		}
		if round == nil {
			return txs
		}

		// Check proposer is in the round's ceremony validators.
		if _, found := votekeeper.FindValidatorInRoundCeremony(round, proposerValAddr); !found {
			return txs
		}

		// Generate fresh ea_sk.
		eaSk, eaPk := elgamal.KeyGen(rand.Reader)
		// Zero the secret scalar as soon as we leave this scope so the full key
		// does not linger in GC-managed memory after shares/encryptions are built.
		defer zeroScalar(eaSk.Scalar)
		eaPkBytes := eaPk.Point.ToAffineCompressed()
		G := elgamal.PallasGenerator()

		n := len(round.CeremonyValidators)
		t, err := thresholdForN(n)
		if err != nil {
			logger.Error("PrepareProposal[deal]: threshold computation failed", "err", err)
			return txs
		}

		// Split ea_sk into (t, n) Shamir shares, ECIES-encrypt share_i to
		// validator_i, and compute Feldman polynomial commitments.
		shares, coeffs, err := shamir.Split(eaSk.Scalar, t, n)
		if err != nil {
			logger.Error("PrepareProposal[deal]: shamir split failed", "err", err)
			return txs
		}

		commitmentPts, err := shamir.FeldmanCommit(G, coeffs)
		if err != nil {
			logger.Error("PrepareProposal[deal]: Feldman commit failed", "err", err)
			return txs
		}
		feldmanCommitments := make([][]byte, len(commitmentPts))
		for j, c := range commitmentPts {
			feldmanCommitments[j] = c.ToAffineCompressed()
		}

		defer func() {
			for _, c := range coeffs {
				if c != nil {
					zeroScalar(c)
				}
			}
		}()
		defer func() {
			for i := range shares {
				if shares[i].Value != nil {
					zeroScalar(shares[i].Value)
				}
			}
		}()

		// ECIES-encrypt each share to the corresponding ceremony validator.
		payloads := make([]*types.DealerPayload, n)
		for i, v := range round.CeremonyValidators {
			recipientPk, err := elgamal.UnmarshalPublicKey(v.PallasPk)
			if err != nil {
				logger.Error("PrepareProposal[deal]: invalid Pallas PK for validator",
					"validator", v.ValidatorAddress, "err", err)
				return txs
			}

			env, err := ecies.Encrypt(G, recipientPk.Point, shares[i].Value.Bytes(), rand.Reader)
			if err != nil {
				logger.Error("PrepareProposal[deal]: ECIES encryption failed",
					"validator", v.ValidatorAddress, "err", err)
				return txs
			}
			payloads[i] = &types.DealerPayload{
				ValidatorAddress: v.ValidatorAddress,
				EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
				Ciphertext:       env.Ciphertext,
			}
		}

		// Build deal message.
		dealMsg := &types.MsgDealExecutiveAuthorityKey{
			Creator:            proposerValAddr,
			VoteRoundId:        round.VoteRoundId,
			EaPk:               eaPkBytes,
			Payloads:           payloads,
			Threshold:          uint32(t),
			FeldmanCommitments: feldmanCommitments,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(dealMsg, voteapi.TagDealExecutiveAuthorityKey)
		if err != nil {
			logger.Error("PrepareProposal[deal]: failed to encode deal tx", "err", err)
			return txs
		}

		// The dealer does NOT write their share to disk here. The ack handler
		// handles all validators uniformly: when the dealer is next the block proposer
		// after DEALT status is set, it decrypts its own payload and writes share.<round_id>
		// just like any other validator.

		logger.Info("PrepareProposal[deal]: injecting MsgDealExecutiveAuthorityKey",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(round.VoteRoundId),
			"validators", n,
			"threshold", t)
		return append([][]byte{txBytes}, txs...)
	}
}

// coeffsPathForRound returns the file path for DKG polynomial coefficients.
// The coefficients are persisted so the ack handler (Phase 5) can compute the
// contributor's own partial share f_i(shamirIndex) without re-deriving the
// polynomial. The file stores t concatenated 32-byte Pallas scalars.
//
//	<dir>/coeffs.<hex(round_id)>
func coeffsPathForRound(dir string, roundID []byte) string {
	return filepath.Join(dir, "coeffs."+hex.EncodeToString(roundID))
}

// writeCoeffs serializes polynomial coefficients as concatenated 32-byte
// Pallas scalars and writes them to path with mode 0600.
func writeCoeffs(path string, coeffs []curvey.Scalar) error {
	buf := make([]byte, 0, len(coeffs)*32)
	for _, c := range coeffs {
		buf = append(buf, c.Bytes()...)
	}
	return os.WriteFile(path, buf, 0600)
}

// loadCoeffs reads polynomial coefficients persisted by the DKG contribution
// handler. Returns expectedT scalars parsed from the t*32 byte file. The raw
// bytes are zeroed after parsing.
func loadCoeffs(path string, expectedT int) ([]curvey.Scalar, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != expectedT*32 {
		zeroBytes(raw)
		return nil, fmt.Errorf("expected %d bytes (%d coefficients), got %d", expectedT*32, expectedT, len(raw))
	}
	coeffs := make([]curvey.Scalar, expectedT)
	for i := 0; i < expectedT; i++ {
		s, err := new(curvey.ScalarPallas).SetBytes(raw[i*32 : (i+1)*32])
		if err != nil {
			zeroBytes(raw)
			return nil, fmt.Errorf("coefficient %d: %w", i, err)
		}
		coeffs[i] = s
	}
	zeroBytes(raw)
	return coeffs, nil
}

// zeroAndDeleteCoeffsFile overwrites the DKG coefficients file with zeros and
// removes it. Called after the ack handler has computed the combined share and
// no longer needs the polynomial.
func zeroAndDeleteCoeffsFile(dir string, roundID []byte, logger log.Logger) {
	if dir == "" {
		return
	}
	path := coeffsPathForRound(dir, roundID)
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("coeffs cleanup: failed to stat", "path", path, "err", err)
		}
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		logger.Warn("coeffs cleanup: failed to open for zeroing", "path", path, "err", err)
		return
	}
	zeros := make([]byte, info.Size())
	_, _ = f.Write(zeros)
	_ = f.Sync()
	f.Close()
	if err := os.Remove(path); err != nil {
		logger.Warn("coeffs cleanup: failed to remove", "path", path, "err", err)
	}
}

// CeremonyDKGContributionPrepareProposalHandler returns a PrepareProposalInjector
// that generates and injects a MsgContributeDKG when the proposer is a ceremony
// validator in a REGISTERING round that has not yet received their contribution.
//
// Each validator independently:
//  1. Generates a random secret s_i and splits it into (t, n) Shamir shares
//  2. Computes Feldman commitments C_{i,j} = a_{i,j} * G for the polynomial
//  3. ECIES-encrypts share_{i,k} to validator k's Pallas public key (for k ≠ i)
//  4. Persists polynomial coefficients to disk for the ack handler
//  5. Injects MsgContributeDKG containing commitments and n-1 encrypted payloads
//
// The proposer's own share is not included in the payloads — the ack handler
// recomputes it from the persisted coefficients as f_i(shamirIndex).
func CeremonyDKGContributionPrepareProposalHandler(
	voteKeeper *votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	pallasSkPath string,
	ceremonyDir string,
	logger log.Logger,
) PrepareProposalInjector {
	loadPallasSk := pallasSkLoader(pallasSkPath, logger, "dkg-contribute")

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		if _, err := loadPallasSk(); err != nil {
			return txs
		}

		proposerValAddr, err := resolveProposer(ctx, stakingKeeper, req.ProposerAddress)
		if err != nil {
			return txs
		}

		kvStore := voteKeeper.OpenKVStore(ctx)

		round, err := voteKeeper.FindFirstPendingRound(kvStore, types.CeremonyStatus_CEREMONY_STATUS_REGISTERING)
		if err != nil {
			logger.Error("PrepareProposal[dkg-contribute]: failed to find pending round", "err", err)
			return txs
		}
		if round == nil {
			return txs
		}

		if _, found := votekeeper.FindValidatorInRoundCeremony(round, proposerValAddr); !found {
			return txs
		}

		if _, found := votekeeper.FindContributionInRound(round, proposerValAddr); found {
			return txs
		}

		G := elgamal.PallasGenerator()
		n := len(round.CeremonyValidators)
		t, err := thresholdForN(n)
		if err != nil {
			logger.Error("PrepareProposal[dkg-contribute]: threshold computation failed", "err", err)
			return txs
		}

		secret := new(curvey.ScalarPallas).Random(rand.Reader)
		defer zeroScalar(secret)

		shares, coeffs, err := shamir.Split(secret, t, n)
		if err != nil {
			logger.Error("PrepareProposal[dkg-contribute]: shamir split failed", "err", err)
			return txs
		}
		defer func() {
			for _, c := range coeffs {
				if c != nil {
					zeroScalar(c)
				}
			}
		}()
		defer func() {
			for i := range shares {
				if shares[i].Value != nil {
					zeroScalar(shares[i].Value)
				}
			}
		}()

		commitmentPts, err := shamir.FeldmanCommit(G, coeffs)
		if err != nil {
			logger.Error("PrepareProposal[dkg-contribute]: Feldman commit failed", "err", err)
			return txs
		}
		feldmanCommitments := make([][]byte, len(commitmentPts))
		for j, c := range commitmentPts {
			feldmanCommitments[j] = c.ToAffineCompressed()
		}

		if ceremonyDir != "" {
			cp := coeffsPathForRound(ceremonyDir, round.VoteRoundId)
			if err := writeCoeffs(cp, coeffs); err != nil {
				logger.Error("PrepareProposal[dkg-contribute]: failed to write coefficients",
					"path", cp, "err", err)
				return txs
			}
		}

		payloads := make([]*types.DealerPayload, 0, n-1)
		for i, v := range round.CeremonyValidators {
			if v.ValidatorAddress == proposerValAddr {
				continue
			}
			recipientPk, err := elgamal.UnmarshalPublicKey(v.PallasPk)
			if err != nil {
				logger.Error("PrepareProposal[dkg-contribute]: invalid Pallas PK",
					"validator", v.ValidatorAddress, "err", err)
				return txs
			}
			env, err := ecies.Encrypt(G, recipientPk.Point, shares[i].Value.Bytes(), rand.Reader)
			if err != nil {
				logger.Error("PrepareProposal[dkg-contribute]: ECIES encryption failed",
					"validator", v.ValidatorAddress, "err", err)
				return txs
			}
			payloads = append(payloads, &types.DealerPayload{
				ValidatorAddress: v.ValidatorAddress,
				EphemeralPk:      env.Ephemeral.ToAffineCompressed(),
				Ciphertext:       env.Ciphertext,
			})
		}

		msg := &types.MsgContributeDKG{
			Creator:            proposerValAddr,
			VoteRoundId:        round.VoteRoundId,
			FeldmanCommitments: feldmanCommitments,
			Payloads:           payloads,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagContributeDKG)
		if err != nil {
			logger.Error("PrepareProposal[dkg-contribute]: failed to encode contribution tx", "err", err)
			return txs
		}

		logger.Info("PrepareProposal[dkg-contribute]: injecting MsgContributeDKG",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(round.VoteRoundId),
			"validators", n,
			"threshold", t)
		return append([][]byte{txBytes}, txs...)
	}
}

// CeremonyAckPrepareProposalHandler returns a PrepareProposalInjector that
// checks whether a PENDING round's ceremony is in DEALT state and, if so,
// injects a MsgAckExecutiveAuthorityKey on behalf of the block proposer.
//
// Two paths are supported:
//
//   - Dealer path (legacy): a single dealer distributed ECIES-encrypted shares
//     via CeremonyPayloads. The proposer decrypts their payload and verifies
//     against the round's combined Feldman commitments.
//
//   - DKG path: each validator contributed independently via DkgContributions.
//     The proposer loads their persisted polynomial coefficients, computes their
//     own partial share, decrypts and verifies shares from every other contributor
//     against that contributor's individual Feldman commitments, sums everything
//     into a combined share, and verifies the result against the round's combined
//     commitments. The coefficients file is deleted after success.
//
// Both paths write the final share to <eaSkDir>/share.<hex(round_id)> and
// inject the same MsgAckExecutiveAuthorityKey.
func CeremonyAckPrepareProposalHandler(
	voteKeeper *votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	pallasSkPath string,
	ceremonyDir string,
	logger log.Logger,
) PrepareProposalInjector {
	loadPallasSk := pallasSkLoader(pallasSkPath, logger, "ack")

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		pallasSk, err := loadPallasSk()
		if err != nil {
			return txs
		}

		proposerValAddr, err := resolveProposer(ctx, stakingKeeper, req.ProposerAddress)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to resolve proposer validator", "err", err)
			return txs
		}

		kvStore := voteKeeper.OpenKVStore(ctx)

		round, err := voteKeeper.FindFirstPendingRound(kvStore, types.CeremonyStatus_CEREMONY_STATUS_DEALT)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to find dealt round", "err", err)
			return txs
		}
		if round == nil {
			return txs
		}

		if _, found := votekeeper.FindAckInRoundCeremony(round, proposerValAddr); found {
			return txs
		}

		ceremonyVal, found := votekeeper.FindValidatorInRoundCeremony(round, proposerValAddr)
		if !found || ceremonyVal.ShamirIndex == 0 {
			return txs
		}

		shamirIndex := int(ceremonyVal.ShamirIndex)
		G := elgamal.PallasGenerator()

		var secretBytes []byte
		var recoveredSk *elgamal.SecretKey

		if len(round.DkgContributions) > 0 {
			secretBytes, recoveredSk, err = ackDKGRound(pallasSk, round, proposerValAddr, shamirIndex, G, eaSkDir, logger)
		} else {
			secretBytes, recoveredSk, err = ackDealerRound(pallasSk, round, proposerValAddr, shamirIndex, G)
		}
		if err != nil {
			logger.Error("PrepareProposal[ack]: share recovery failed",
				"err", err, "proposer", proposerValAddr,
				"round", hex.EncodeToString(round.VoteRoundId),
				"dkg", len(round.DkgContributions) > 0)
			return txs
		}
		defer zeroSecret(secretBytes, recoveredSk)

		var diskPath string
		if ceremonyDir != "" {
			diskPath = sharePathForRound(ceremonyDir, round.VoteRoundId)
		}

		h := sha256.New()
		h.Write([]byte(types.AckSigDomain))
		h.Write(round.EaPk)
		h.Write([]byte(proposerValAddr))
		ackSig := h.Sum(nil)

		ackMsg := &types.MsgAckExecutiveAuthorityKey{
			Creator:      proposerValAddr,
			VoteRoundId:  round.VoteRoundId,
			AckSignature: ackSig,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(ackMsg, voteapi.TagAckExecutiveAuthorityKey)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to encode ack tx", "err", err)
			return txs
		}

		if diskPath != "" {
			if err := os.WriteFile(diskPath, secretBytes, 0600); err != nil {
				logger.Error("PrepareProposal[ack]: failed to write secret to disk",
					"path", diskPath, "err", err)
			} else {
				logger.Info("PrepareProposal[ack]: secret written to disk", "path", diskPath)
			}
		}

		logger.Info("PrepareProposal[ack]: injecting MsgAckExecutiveAuthorityKey",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(round.VoteRoundId),
			"dkg", len(round.DkgContributions) > 0)
		return append([][]byte{txBytes}, txs...)
	}
}

// ackDealerRound recovers the proposer's share from the single-dealer ceremony
// payloads. Decrypts the ECIES envelope addressed to this validator and verifies
// the share against the round's Feldman commitments.
func ackDealerRound(
	pallasSk *elgamal.SecretKey,
	round *types.VoteRound,
	proposerValAddr string,
	shamirIndex int,
	G curvey.Point,
) ([]byte, *elgamal.SecretKey, error) {
	var payload *types.DealerPayload
	for _, p := range round.CeremonyPayloads {
		if p.ValidatorAddress == proposerValAddr {
			payload = p
			break
		}
	}
	if payload == nil {
		return nil, nil, fmt.Errorf("no dealer payload for %s", proposerValAddr)
	}

	ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid ephemeral_pk: %w", err)
	}
	secretBytes, err := ecies.Decrypt(pallasSk.Scalar, &ecies.Envelope{
		Ephemeral:  ephPk.Point,
		Ciphertext: payload.Ciphertext,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ECIES decryption failed: %w", err)
	}

	recoveredSk, err := elgamal.UnmarshalSecretKey(secretBytes)
	if err != nil {
		zeroBytes(secretBytes)
		return nil, nil, fmt.Errorf("invalid share scalar: %w", err)
	}

	commitmentPts, err := deserializeFeldmanCommitments(round.FeldmanCommitments)
	if err != nil {
		zeroSecret(secretBytes, recoveredSk)
		return nil, nil, fmt.Errorf("invalid Feldman commitments: %w", err)
	}

	ok, err := shamir.VerifyFeldmanShare(G, commitmentPts, shamirIndex, recoveredSk.Scalar)
	if err != nil {
		zeroSecret(secretBytes, recoveredSk)
		return nil, nil, fmt.Errorf("Feldman verification error: %w", err)
	}
	if !ok {
		zeroSecret(secretBytes, recoveredSk)
		return nil, nil, fmt.Errorf("share failed Feldman verification — dealer sent bad share")
	}

	return secretBytes, recoveredSk, nil
}

// ackDKGRound computes the proposer's combined Shamir share from DKG
// contributions. For each contributor (including self):
//
//   - Own contribution: evaluates the persisted polynomial at shamirIndex
//   - Other contributors: decrypts their ECIES payload and verifies against
//     that contributor's individual Feldman commitments
//
// The partial shares are summed into combined_share, which is verified against
// the round's combined Feldman commitments. On success the coefficients file is
// securely deleted and the combined share bytes are returned.
func ackDKGRound(
	pallasSk *elgamal.SecretKey,
	round *types.VoteRound,
	proposerValAddr string,
	shamirIndex int,
	G curvey.Point,
	eaSkDir string,
	logger log.Logger,
) ([]byte, *elgamal.SecretKey, error) {
	t := int(round.Threshold)

	coeffs, err := loadCoeffs(coeffsPathForRound(eaSkDir, round.VoteRoundId), t)
	if err != nil {
		return nil, nil, fmt.Errorf("load coefficients: %w", err)
	}
	defer func() {
		for _, c := range coeffs {
			if c != nil {
				zeroScalar(c)
			}
		}
	}()

	ownPartial := shamir.EvalPolynomial(coeffs, shamirIndex)
	combinedShare := ownPartial

	for _, contrib := range round.DkgContributions {
		if contrib.ValidatorAddress == proposerValAddr {
			continue
		}

		var payload *types.DealerPayload
		for _, p := range contrib.Payloads {
			if p.ValidatorAddress == proposerValAddr {
				payload = p
				break
			}
		}
		if payload == nil {
			return nil, nil, fmt.Errorf("contributor %s: no payload for %s",
				contrib.ValidatorAddress, proposerValAddr)
		}

		ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
		if err != nil {
			return nil, nil, fmt.Errorf("contributor %s: invalid ephemeral_pk: %w",
				contrib.ValidatorAddress, err)
		}
		shareBytes, err := ecies.Decrypt(pallasSk.Scalar, &ecies.Envelope{
			Ephemeral:  ephPk.Point,
			Ciphertext: payload.Ciphertext,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("contributor %s: ECIES decryption failed: %w",
				contrib.ValidatorAddress, err)
		}

		shareSk, err := elgamal.UnmarshalSecretKey(shareBytes)
		zeroBytes(shareBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("contributor %s: invalid share scalar: %w",
				contrib.ValidatorAddress, err)
		}

		contribCommitments, err := deserializeFeldmanCommitments(contrib.FeldmanCommitments)
		if err != nil {
			zeroScalar(shareSk.Scalar)
			return nil, nil, fmt.Errorf("contributor %s: invalid commitments: %w",
				contrib.ValidatorAddress, err)
		}

		ok, err := shamir.VerifyFeldmanShare(G, contribCommitments, shamirIndex, shareSk.Scalar)
		if err != nil {
			zeroScalar(shareSk.Scalar)
			return nil, nil, fmt.Errorf("contributor %s: Feldman verification error: %w",
				contrib.ValidatorAddress, err)
		}
		if !ok {
			zeroScalar(shareSk.Scalar)
			return nil, nil, fmt.Errorf("contributor %s: share failed Feldman verification",
				contrib.ValidatorAddress)
		}

		combinedShare = combinedShare.Add(shareSk.Scalar)
		zeroScalar(shareSk.Scalar)
	}

	combinedCommitments, err := deserializeFeldmanCommitments(round.FeldmanCommitments)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid combined commitments: %w", err)
	}
	ok, err := shamir.VerifyFeldmanShare(G, combinedCommitments, shamirIndex, combinedShare)
	if err != nil {
		return nil, nil, fmt.Errorf("combined Feldman verification error: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("combined share failed Feldman verification")
	}

	zeroAndDeleteCoeffsFile(eaSkDir, round.VoteRoundId, logger)

	secretBytes := combinedShare.Bytes()
	return secretBytes, &elgamal.SecretKey{Scalar: combinedShare}, nil
}

// deserializeFeldmanCommitments converts serialized Feldman commitments (32-byte
// compressed Pallas points) into curvey.Point values for use with VerifyFeldmanShare
// and EvalCommitmentPolynomial.
func deserializeFeldmanCommitments(raw [][]byte) ([]curvey.Point, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty feldman commitments")
	}
	pts := make([]curvey.Point, len(raw))
	for i, b := range raw {
		pt, err := elgamal.DecompressPallasPoint(b)
		if err != nil {
			return nil, fmt.Errorf("feldman commitment %d: %w", i, err)
		}
		pts[i] = pt
	}
	return pts, nil
}

// zeroAndDeleteShareFile overwrites the share file with zeros and removes it.
// Errors are logged but not fatal — the security-critical part is the overwrite.
func zeroAndDeleteShareFile(dir string, roundID []byte, logger log.Logger) {
	if dir == "" {
		return
	}
	path := sharePathForRound(dir, roundID)
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("share cleanup: failed to open for zeroing", "path", path, "err", err)
		}
		return
	}
	var zeros [32]byte
	_, _ = f.Write(zeros[:])
	_ = f.Sync()
	f.Close()
	if err := os.Remove(path); err != nil {
		logger.Warn("share cleanup: failed to remove", "path", path, "err", err)
	} else {
		logger.Info("share cleanup: zeroed and deleted share file", "path", path)
	}
}

// zeroScalar overwrites a Pallas scalar's internal limbs in place.
// curvey.Scalar.Zero() returns a *new* zero scalar without mutating the
// receiver, so we type-assert to ScalarPallas and call Field4.SetZero()
// which actually zeroes the memory backing the value.
func zeroScalar(s curvey.Scalar) {
	if ps, ok := s.(*curvey.ScalarPallas); ok && ps != nil && ps.Value != nil {
		ps.Value.SetZero()
	}
}

// zeroBytes overwrites a byte slice with zeros.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// zeroSecret zeroes both the raw secret bytes and the parsed scalar.
func zeroSecret(raw []byte, sk *elgamal.SecretKey) {
	zeroBytes(raw)
	if sk != nil {
		zeroScalar(sk.Scalar)
	}
}

// bytesEqual compares two byte slices for equality.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ceremonyDirFromPath derives the ceremony data directory (for per-round
// shares and coefficients) from the legacy ea_sk_path config value.
// If the path is empty, returns "".
func ceremonyDirFromPath(eaSkPath string) string {
	if eaSkPath == "" {
		return ""
	}
	return filepath.Dir(eaSkPath)
}

// loadShareForRound reads the per-round Shamir share file written by the ack
// handler and returns the scalar as an elgamal.SecretKey (same 32-byte format).
// Returns a non-nil error if the file doesn't exist.
func loadShareForRound(dir string, roundID []byte) (*elgamal.SecretKey, error) {
	if dir == "" {
		return nil, fmt.Errorf("share dir is empty")
	}
	path := sharePathForRound(dir, roundID)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sk, err := elgamal.UnmarshalSecretKey(raw)
	zeroBytes(raw)
	return sk, err
}
