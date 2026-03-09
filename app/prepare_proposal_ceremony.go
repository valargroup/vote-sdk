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

	voteapi "github.com/valargroup/shielded-vote/api"
	"github.com/valargroup/shielded-vote/crypto/ecies"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/crypto/shamir"
	votekeeper "github.com/valargroup/shielded-vote/x/vote/keeper"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// eaSkPathForRound returns the per-round ea_sk file path (legacy single-key mode):
//
//	<dir>/ea_sk.<hex(round_id)>
func eaSkPathForRound(dir string, roundID []byte) string {
	return filepath.Join(dir, "ea_sk."+hex.EncodeToString(roundID))
}

// sharePathForRound returns the per-round Shamir share file path (threshold mode).
// In threshold mode each validator writes their scalar share here instead of the
// full ea_sk. The file stores 32 raw bytes (the Pallas Fq scalar).
//
//	<dir>/share.<hex(round_id)>
func sharePathForRound(dir string, roundID []byte) string {
	return filepath.Join(dir, "share."+hex.EncodeToString(roundID))
}

// thresholdForN computes the default threshold t = ceil(n/2).
// This matches the ack requirement (HalfAcked) so that the set of validators
// that survives ceremony stripping is always large enough to reconstruct the
// EA key during tally.
// Returns 0 when n < 2 (threshold splitting is not meaningful for fewer than
// two validators; callers should fall back to legacy single-key mode).
func thresholdForN(n int) int {
	if n < 2 {
		return 0
	}
	t := (n + 1) / 2 // ceil(n/2)
	if t < 2 {
		t = 2 // Shamir requires t >= 2; applies only when n=2 gives ceil(2/2)=1
	}
	return t
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
// Threshold mode (n >= 2): ea_sk is Shamir-split into (t, n) shares with
// t = ceil(n/2). Each validator receives ECIES(share_i, pk_i). VK_i = share_i*G
// and the threshold value are included in the deal message so validators can verify
// their share on ack. The dealer's share is written to disk by the ack handler
// (when the dealer is next the block proposer after DEALT is set), not here.
//
// Legacy mode (n < 2): ea_sk is ECIES-encrypted to every validator unchanged.
// ea_sk is likewise written to disk by the ack handler, not here.
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
		t := thresholdForN(n)

		// Threshold mode: split ea_sk into (t, n) Shamir shares, ECIES-encrypt
		// share_i to validator_i, and compute VK_i = share_i * G.
		// Legacy mode (t == 0): encrypt the full ea_sk to every validator.
		var (
			shares           []shamir.Share
			verificationKeys [][]byte
		)
			if t > 0 {
			var coeffs []curvey.Scalar
			shares, coeffs, err = shamir.Split(eaSk.Scalar, t, n)
			if err != nil {
				logger.Error("PrepareProposal[deal]: shamir split failed", "err", err)
				return txs
			}
			// Coefficients are secret material — zero them after use.
			defer func() {
				for _, c := range coeffs {
					if c != nil {
						zeroScalar(c)
					}
				}
			}()
			// Share values (each f(i)) are equally secret — zero them once payloads
			// and verification keys have been built so they don't linger on the heap.
			defer func() {
				for i := range shares {
					if shares[i].Value != nil {
						zeroScalar(shares[i].Value)
					}
				}
			}()

			verificationKeys = make([][]byte, n)
			for i := range shares {
				verificationKeys[i] = G.Mul(shares[i].Value).ToAffineCompressed()
			}
		}

		// ECIES-encrypt the payload (share or full ea_sk) to each ceremony validator.
		payloads := make([]*types.DealerPayload, n)
		for i, v := range round.CeremonyValidators {
			recipientPk, err := elgamal.UnmarshalPublicKey(v.PallasPk)
			if err != nil {
				logger.Error("PrepareProposal[deal]: invalid Pallas PK for validator",
					"validator", v.ValidatorAddress, "err", err)
				return txs
			}

			var plaintext []byte
			if t > 0 {
				plaintext = shares[i].Value.Bytes()
			} else {
				eaSkBytes, marshalErr := elgamal.MarshalSecretKey(eaSk)
				if marshalErr != nil {
					logger.Error("PrepareProposal[deal]: failed to marshal ea_sk", "err", marshalErr)
					return txs
				}
				plaintext = eaSkBytes
			}

			env, err := ecies.Encrypt(G, recipientPk.Point, plaintext, rand.Reader)
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
			Creator:          proposerValAddr,
			VoteRoundId:      round.VoteRoundId,
			EaPk:             eaPkBytes,
			Payloads:         payloads,
			Threshold:        uint32(t),
			VerificationKeys: verificationKeys,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(dealMsg, voteapi.TagDealExecutiveAuthorityKey)
		if err != nil {
			logger.Error("PrepareProposal[deal]: failed to encode deal tx", "err", err)
			return txs
		}

		// The dealer does NOT write their share/ea_sk to disk here. The ack handler
		// handles all validators uniformly: when the dealer is next the block proposer
		// after DEALT status is set, it decrypts its own payload and writes share.<round_id>
		// (or ea_sk.<round_id> in legacy mode) just like any other validator.

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
// Threshold mode (round.Threshold > 0): verifies share_i * G == VK_i and
// writes the share to <eaSkDir>/share.<hex(round_id)>.
//
// Legacy mode (round.Threshold == 0): verifies ea_sk * G == ea_pk and
// writes ea_sk to <eaSkDir>/ea_sk.<hex(round_id)>.
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

		// Verify the decrypted scalar against the expected public commitment and
		// determine where on disk to write it.
		//
		// Threshold mode (round.Threshold > 0): the payload contains share_i.
		//   Expected: share_i * G == VK_i (from round.VerificationKeys[validatorIdx]).
		//   Write to: share.<round_id>
		//
		// Legacy mode (round.Threshold == 0): the payload contains ea_sk.
		//   Expected: ea_sk * G == ea_pk
		//   Write to: ea_sk.<round_id>
		var diskPath string
		if round.Threshold > 0 {
			// Find this validator's 0-based index to look up the correct VK.
			validatorIdx := -1
			for i, v := range round.CeremonyValidators {
				if v.ValidatorAddress == proposerValAddr {
					validatorIdx = i
					break
				}
			}
			if validatorIdx < 0 || validatorIdx >= len(round.VerificationKeys) {
				logger.Error("PrepareProposal[ack]: validator index out of range for VK lookup",
					"proposer", proposerValAddr,
					"round", hex.EncodeToString(round.VoteRoundId))
				return txs
			}

			expectedVK := round.VerificationKeys[validatorIdx]
			computedVK := G.Mul(recoveredSk.Scalar).ToAffineCompressed()
			if !bytesEqual(computedVK, expectedVK) {
				logger.Error("PrepareProposal[ack]: share_i * G != VK_i — dealer sent bad share",
					"proposer", proposerValAddr,
					"validator_index", validatorIdx+1,
					"round", hex.EncodeToString(round.VoteRoundId))
				return txs
			}
			if eaSkDir != "" {
				diskPath = sharePathForRound(eaSkDir, round.VoteRoundId)
			}
		} else {
			// Legacy: verify the full ea_sk.
			if !bytesEqual(G.Mul(recoveredSk.Scalar).ToAffineCompressed(), round.EaPk) {
				logger.Error("PrepareProposal[ack]: ea_sk * G != ea_pk — dealer sent garbage",
					"proposer", proposerValAddr,
					"round", hex.EncodeToString(round.VoteRoundId))
				return txs
			}
			if eaSkDir != "" {
				diskPath = eaSkPathForRound(eaSkDir, round.VoteRoundId)
			}
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

// loadEaSkForRound reads the per-round ea_sk file and returns the parsed key.
// Returns a non-nil error if the file doesn't exist (non-dealer validators).
func loadEaSkForRound(dir string, roundID []byte) (*elgamal.SecretKey, error) {
	if dir == "" {
		return nil, fmt.Errorf("ea_sk dir is empty")
	}
	path := eaSkPathForRound(dir, roundID)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return elgamal.UnmarshalSecretKey(raw)
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
	return elgamal.UnmarshalSecretKey(raw)
}
