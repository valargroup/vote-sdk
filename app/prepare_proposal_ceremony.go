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
	eaSkDir string,
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

// CeremonyAckPrepareProposalHandler returns a PrepareProposalInjector that
// checks whether a PENDING round's ceremony is in DEALT state and, if so,
// injects a MsgAckExecutiveAuthorityKey on behalf of the block proposer.
//
// The proposer decrypts their ECIES payload using the Pallas secret key loaded
// from pallasSkPath. If the key file is absent, the ceremony is not DEALT, or
// the proposer has already acked, injection is skipped gracefully.
//
// Verifies the share against Feldman commitments and writes it to
// <eaSkDir>/share.<hex(round_id)>.
func CeremonyAckPrepareProposalHandler(
	voteKeeper *votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	pallasSkPath string,
	eaSkDir string,
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

		// Find first PENDING round with ceremony in DEALT.
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
		if _, found := votekeeper.FindValidatorInRoundCeremony(round, proposerValAddr); !found {
			return txs
		}

		// Find the proposer's ECIES payload.
		var payload *types.DealerPayload
		for _, p := range round.CeremonyPayloads {
			if p.ValidatorAddress == proposerValAddr {
				payload = p
				break
			}
		}
		if payload == nil {
			logger.Error("PrepareProposal[ack]: no payload found for proposer",
				"proposer", proposerValAddr,
				"round", hex.EncodeToString(round.VoteRoundId))
			return txs
		}

		// Reconstruct the ECIES envelope and decrypt.
		ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to unmarshal ephemeral_pk",
				"proposer", proposerValAddr, "err", err)
			return txs
		}
		env := &ecies.Envelope{
			Ephemeral:  ephPk.Point,
			Ciphertext: payload.Ciphertext,
		}

		secretBytes, err := ecies.Decrypt(pallasSk.Scalar, env)
		if err != nil {
			logger.Error("PrepareProposal[ack]: ECIES decryption failed",
				"proposer", proposerValAddr, "err", err)
			return txs
		}

		recoveredSk, err := elgamal.UnmarshalSecretKey(secretBytes)
		if err != nil {
			zeroBytes(secretBytes)
			logger.Error("PrepareProposal[ack]: failed to parse decrypted secret",
				"proposer", proposerValAddr, "err", err)
			return txs
		}
		defer zeroSecret(secretBytes, recoveredSk)

		G := elgamal.PallasGenerator()

		// Verify the decrypted share against Feldman commitments:
		//   share_i * G == EvalCommitmentPolynomial(commitments, shamirIndex)
		ceremonyVal, found := votekeeper.FindValidatorInRoundCeremony(round, proposerValAddr)
		if !found || ceremonyVal.ShamirIndex == 0 {
			logger.Error("PrepareProposal[ack]: proposer not found in ceremony validators",
				"proposer", proposerValAddr,
				"round", hex.EncodeToString(round.VoteRoundId))
			return txs
		}

		commitmentPts, err := deserializeFeldmanCommitments(round.FeldmanCommitments)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to deserialize Feldman commitments",
				"err", err, "round", hex.EncodeToString(round.VoteRoundId))
			return txs
		}

		ok, err := shamir.VerifyFeldmanShare(G, commitmentPts, int(ceremonyVal.ShamirIndex), recoveredSk.Scalar)
		if err != nil {
			logger.Error("PrepareProposal[ack]: Feldman share verification error",
				"err", err, "proposer", proposerValAddr,
				"shamir_index", ceremonyVal.ShamirIndex,
				"round", hex.EncodeToString(round.VoteRoundId))
			return txs
		}
		if !ok {
			logger.Error("PrepareProposal[ack]: share failed Feldman verification — dealer sent bad share",
				"proposer", proposerValAddr,
				"shamir_index", ceremonyVal.ShamirIndex,
				"round", hex.EncodeToString(round.VoteRoundId))
			return txs
		}
		var diskPath string
		if eaSkDir != "" {
			diskPath = sharePathForRound(eaSkDir, round.VoteRoundId)
		}

		// Compute ack_signature = SHA256(AckSigDomain || ea_pk || validator_address).
		h := sha256.New()
		h.Write([]byte(types.AckSigDomain))
		h.Write(round.EaPk)
		h.Write([]byte(proposerValAddr))
		ackSig := h.Sum(nil)

		// Build and encode the ack message.
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

		// Write the decrypted secret to disk for the tally injector.
		if diskPath != "" {
			if err := os.WriteFile(diskPath, secretBytes, 0600); err != nil {
				logger.Error("PrepareProposal[ack]: failed to write secret to disk",
					"path", diskPath, "err", err)
				// Continue — the ack injection itself is more important.
			} else {
				logger.Info("PrepareProposal[ack]: secret written to disk", "path", diskPath)
			}
		}

		logger.Info("PrepareProposal[ack]: injecting MsgAckExecutiveAuthorityKey",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(round.VoteRoundId))
		return append([][]byte{txBytes}, txs...)
	}
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

// eaSkDirFromPath derives a directory for per-round ea_sk files from the
// legacy ea_sk_path config value. If the path is empty, returns "".
func eaSkDirFromPath(eaSkPath string) string {
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
