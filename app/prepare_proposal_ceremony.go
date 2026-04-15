package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
// The proposer loads their persisted polynomial coefficients, computes their
// own partial share, decrypts and verifies shares from every other contributor
// against that contributor's individual Feldman commitments, sums everything
// into a combined share, and verifies the result against the round's combined
// commitments. The coefficients file is deleted after success.
//
// The final share is written to <ceremonyDir>/share.<hex(round_id)> and a
// MsgAckExecutiveAuthorityKey is injected.
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

		// Resolve the proposer validator address.
		proposerValAddr, err := resolveProposer(ctx, stakingKeeper, req.ProposerAddress)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to resolve proposer validator", "err", err)
			return txs
		}

		kvStore := voteKeeper.OpenKVStore(ctx)

		// Find the first pending round in DEALT status.
		round, err := voteKeeper.FindFirstPendingRound(kvStore, types.CeremonyStatus_CEREMONY_STATUS_DEALT)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to find dealt round", "err", err)
			return txs
		}
		if round == nil {
			return txs
		}

		// Check if the proposer has already acked.
		if _, found := votekeeper.FindAckInRoundCeremony(round, proposerValAddr); found {
			return txs
		}

		// Check if the proposer is a ceremony validator.
		ceremonyVal, found := votekeeper.FindValidatorInRoundCeremony(round, proposerValAddr)
		if !found || ceremonyVal.ShamirIndex == 0 {
			return txs
		}

		shamirIndex := int(ceremonyVal.ShamirIndex)
		G := elgamal.PallasGenerator()

		secretBytes, recoveredSk, skippedContributors, err := ackDKGRound(pallasSk, round, proposerValAddr, shamirIndex, G, ceremonyDir, logger)
		if err != nil {
			logger.Error("PrepareProposal[ack]: share recovery failed",
				"err", err, "proposer", proposerValAddr,
				"round", hex.EncodeToString(round.VoteRoundId))
			return txs
		}
		defer zeroSecret(secretBytes, recoveredSk)

		if len(skippedContributors) > 0 {
			logger.Warn("PrepareProposal[ack]: skipped bad contributors",
				"skipped", skippedContributors,
				"round", hex.EncodeToString(round.VoteRoundId))
		}

		if ceremonyDir != "" {
			diskPath := sharePathForRound(ceremonyDir, round.VoteRoundId)
			if err := os.WriteFile(diskPath, secretBytes, 0600); err != nil {
				logger.Error("PrepareProposal[ack]: failed to write secret to disk — skipping ack injection",
					"path", diskPath, "err", err)
				return txs
			}
			logger.Info("PrepareProposal[ack]: secret written to disk", "path", diskPath)
		}

		ackBinding := types.ComputeAckBinding(round.EaPk, proposerValAddr, skippedContributors)

		ackMsg := &types.MsgAckExecutiveAuthorityKey{
			Creator:             proposerValAddr,
			VoteRoundId:         round.VoteRoundId,
			AckSignature:        ackBinding,
			SkippedContributors: skippedContributors,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(ackMsg, voteapi.TagAckExecutiveAuthorityKey)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to encode ack tx", "err", err)
			return txs
		}

		logger.Info("PrepareProposal[ack]: injecting MsgAckExecutiveAuthorityKey",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(round.VoteRoundId))
		return append([][]byte{txBytes}, txs...)
	}
}

// ackDKGRound computes the proposer's combined Shamir share from DKG
// contributions. For each contributor (including self):
//
//   - Own contribution: evaluates the persisted polynomial at shamirIndex
//   - Other contributors: decrypts their ECIES payload and verifies against
//     that contributor's individual Feldman commitments
//
// Contributors whose shares fail decryption or Feldman verification are added
// to a skip set rather than causing a hard failure. The combined share is
// computed from only the non-skipped contributors, and verified against
// locally recomputed combined Feldman commitments (excluding the skipped set).
//
// Returns (shareBytes, secretKey, skippedContributors, error). The caller
// must include the sorted skip set in the ack message so the chain can
// determine a canonical skip set by majority vote.
func ackDKGRound(
	pallasSk *elgamal.SecretKey,
	round *types.VoteRound,
	proposerValAddr string,
	shamirIndex int,
	G curvey.Point,
	ceremonyDir string,
	logger log.Logger,
) ([]byte, *elgamal.SecretKey, []string, error) {
	t := int(round.Threshold)

	coeffs, err := loadCoeffs(coeffsPathForRound(ceremonyDir, round.VoteRoundId), t)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load coefficients: %w", err)
	}
	defer func() {
		for _, c := range coeffs {
			if c != nil {
				zeroScalar(c)
			}
		}
	}()

	// Evaluate the proposer's own polynomial at their shamir index.
	ownPartial := shamir.EvalPolynomial(coeffs, shamirIndex)
	combinedShare := ownPartial

	var skipped []string

	for _, contrib := range round.DkgContributions {
		// Skip the proposer's own contribution.
		if contrib.ValidatorAddress == proposerValAddr {
			continue
		}

		// Decrypt and verify the share.
		shareScalar, skipReason := decryptAndVerifyShare(
			pallasSk, contrib, proposerValAddr, shamirIndex, G,
		)
		if skipReason != "" {
			logger.Warn("PrepareProposal[ack]: skipping bad contributor",
				"contributor", contrib.ValidatorAddress,
				"reason", skipReason)
			skipped = append(skipped, contrib.ValidatorAddress)
			continue
		}

		combinedShare = combinedShare.Add(shareScalar)
		zeroScalar(shareScalar)
	}

	sort.Strings(skipped)

	// Recompute combined commitments locally, excluding skipped contributors.
	skipSet := make(map[string]bool, len(skipped))
	for _, s := range skipped {
		skipSet[s] = true
	}
	var keptCommitments [][]curvey.Point
	for _, contrib := range round.DkgContributions {
		if skipSet[contrib.ValidatorAddress] {
			continue
		}
		pts, err := deserializeFeldmanCommitments(contrib.FeldmanCommitments)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("recompute commitments: contributor %s: %w",
				contrib.ValidatorAddress, err)
		}
		keptCommitments = append(keptCommitments, pts)
	}
	if len(keptCommitments) < t {
		return nil, nil, nil, fmt.Errorf("remaining contributors (%d) below threshold (%d) — cannot form combined share",
			len(keptCommitments), t)
	}

	recomputed, err := shamir.CombineCommitments(keptCommitments)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("recompute combined commitments: %w", err)
	}

	ok, err := shamir.VerifyFeldmanShare(G, recomputed, shamirIndex, combinedShare)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("combined Feldman verification error: %w", err)
	}
	if !ok {
		return nil, nil, nil, fmt.Errorf("combined share failed Feldman verification")
	}

	zeroAndDeleteCoeffsFile(ceremonyDir, round.VoteRoundId, logger)

	secretBytes := combinedShare.Bytes()
	return secretBytes, &elgamal.SecretKey{Scalar: combinedShare}, skipped, nil
}

// decryptAndVerifyShare decrypts a single contributor's ECIES payload and
// verifies it against that contributor's Feldman commitments. Returns the
// share scalar on success, or ("", skipReason) on any failure.
func decryptAndVerifyShare(
	pallasSk *elgamal.SecretKey,
	contrib *types.DKGContribution,
	proposerValAddr string,
	shamirIndex int,
	G curvey.Point,
) (curvey.Scalar, string) {
	var payload *types.DealerPayload
	for _, p := range contrib.Payloads {
		if p.ValidatorAddress == proposerValAddr {
			payload = p
			break
		}
	}
	if payload == nil {
		return nil, fmt.Sprintf("no payload for %s", proposerValAddr)
	}

	// Decrypt the share.
	ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
	if err != nil {
		return nil, fmt.Sprintf("invalid ephemeral_pk: %v", err)
	}

	shareBytes, err := ecies.Decrypt(pallasSk.Scalar, &ecies.Envelope{
		Ephemeral:  ephPk.Point,
		Ciphertext: payload.Ciphertext,
	})
	if err != nil {
		return nil, fmt.Sprintf("ECIES decryption failed: %v", err)
	}

	shareSk, err := elgamal.UnmarshalSecretKey(shareBytes)
	zeroBytes(shareBytes)
	if err != nil {
		return nil, fmt.Sprintf("invalid share scalar: %v", err)
	}

	contribCommitments, err := deserializeFeldmanCommitments(contrib.FeldmanCommitments)
	if err != nil {
		zeroScalar(shareSk.Scalar)
		return nil, fmt.Sprintf("invalid commitments: %v", err)
	}

	// Verify the share against the contributor's Feldman commitments.
	ok, err := shamir.VerifyFeldmanShare(G, contribCommitments, shamirIndex, shareSk.Scalar)
	if err != nil {
		zeroScalar(shareSk.Scalar)
		return nil, fmt.Sprintf("Feldman verification error: %v", err)
	}
	if !ok {
		zeroScalar(shareSk.Scalar)
		return nil, "share failed Feldman verification"
	}

	return shareSk.Scalar, ""
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
