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

	voteapi "github.com/z-cale/zally/api"
	"github.com/z-cale/zally/crypto/ecies"
	"github.com/z-cale/zally/crypto/elgamal"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// eaSkPathForRound returns the per-round ea_sk file path:
//
//	<dir>/ea_sk.<hex(round_id)>
func eaSkPathForRound(dir string, roundID []byte) string {
	return filepath.Join(dir, "ea_sk."+hex.EncodeToString(roundID))
}

// CeremonyDealPrepareProposalHandler returns a PrepareProposalInjector that
// checks whether a PENDING round needs a deal and, if so, generates a fresh
// ea_sk, ECIES-encrypts it to each ceremony validator, and injects a
// MsgDealExecutiveAuthorityKey.
//
// The proposer must be in the round's CeremonyValidators to deal. The
// generated ea_sk is written to <eaSkDir>/ea_sk.<hex(round_id)> for the
// auto-tally system to pick up later.
func CeremonyDealPrepareProposalHandler(
	voteKeeper votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	pallasSkPath string,
	eaSkDir string,
	logger log.Logger,
) PrepareProposalInjector {
	var (
		skOnce sync.Once
		sk     *elgamal.SecretKey
		skErr  error
	)

	loadPallasSk := func() (*elgamal.SecretKey, error) {
		skOnce.Do(func() {
			if pallasSkPath == "" {
				logger.Warn("PrepareProposal: vote.pallas_sk_path is empty — auto-deal disabled")
				skErr = os.ErrNotExist
				return
			}
			logger.Info("PrepareProposal: loading Pallas secret key for deal", "path", pallasSkPath)
			raw, err := os.ReadFile(pallasSkPath)
			if err != nil {
				skErr = err
				logger.Error("PrepareProposal[deal]: failed to load Pallas secret key",
					"path", pallasSkPath, "err", err)
				return
			}
			sk, skErr = elgamal.UnmarshalSecretKey(raw)
			if skErr != nil {
				logger.Error("PrepareProposal[deal]: failed to parse Pallas secret key",
					"path", pallasSkPath, "err", skErr)
			}
		})
		return sk, skErr
	}

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		// Verify we have a Pallas key. The deal handler needs the Pallas SK
		// only to confirm this node is configured as a validator. The actual
		// ECIES encryption uses each validator's public key from the registry.
		// We load pallasSk to confirm we ARE a valid ceremony participant
		// (but don't need it for encryption).
		if _, err := loadPallasSk(); err != nil {
			return txs
		}

		// Resolve proposer.
		consAddr := sdk.ConsAddress(req.ProposerAddress)
		val, err := stakingKeeper.GetValidatorByConsAddr(ctx, consAddr)
		if err != nil {
			return txs
		}
		proposerValAddr := val.OperatorAddress

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
		eaSkBytes, err := elgamal.MarshalSecretKey(eaSk)
		if err != nil {
			logger.Error("PrepareProposal[deal]: failed to marshal ea_sk", "err", err)
			return txs
		}
		eaPkBytes := eaPk.Point.ToAffineCompressed()
		G := elgamal.PallasGenerator()

		// ECIES-encrypt ea_sk to each ceremony validator's Pallas PK.
		payloads := make([]*types.DealerPayload, len(round.CeremonyValidators))
		for i, v := range round.CeremonyValidators {
			recipientPk, err := elgamal.UnmarshalPublicKey(v.PallasPk)
			if err != nil {
				logger.Error("PrepareProposal[deal]: invalid Pallas PK for validator",
					"validator", v.ValidatorAddress, "err", err)
				return txs
			}
			env, err := ecies.Encrypt(G, recipientPk.Point, eaSkBytes, rand.Reader)
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
			Creator:     proposerValAddr,
			VoteRoundId: round.VoteRoundId,
			EaPk:        eaPkBytes,
			Payloads:    payloads,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(dealMsg, voteapi.TagDealExecutiveAuthorityKey)
		if err != nil {
			logger.Error("PrepareProposal[deal]: failed to encode deal tx", "err", err)
			return txs
		}

		// Write ea_sk to per-round path for auto-tally.
		if eaSkDir != "" {
			path := eaSkPathForRound(eaSkDir, round.VoteRoundId)
			if err := os.WriteFile(path, eaSkBytes, 0600); err != nil {
				logger.Error("PrepareProposal[deal]: failed to write ea_sk",
					"path", path, "err", err)
				// Continue — deal injection is more important.
			} else {
				logger.Info("PrepareProposal[deal]: ea_sk written to disk", "path", path)
			}
		}

		logger.Info("PrepareProposal[deal]: injecting MsgDealExecutiveAuthorityKey",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(round.VoteRoundId),
			"validators", len(payloads))
		return append([][]byte{txBytes}, txs...)
	}
}

// CeremonyAckPrepareProposalHandler returns a PrepareProposalInjector that
// checks whether a PENDING round's ceremony is in DEALT state and, if so,
// injects a MsgAckExecutiveAuthorityKey on behalf of the block proposer.
//
// The proposer decrypts their ECIES payload using the Pallas secret key
// loaded from pallasSkPath. If the key file is absent, the ceremony is not
// DEALT, or the proposer has already acked, injection is skipped gracefully.
//
// After successful decryption, the ea_sk is written to <eaSkDir>/ea_sk.<hex(round_id)>
// so the auto-tally system can pick it up.
func CeremonyAckPrepareProposalHandler(
	voteKeeper votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	pallasSkPath string,
	eaSkDir string,
	logger log.Logger,
) PrepareProposalInjector {
	var (
		skOnce sync.Once
		sk     *elgamal.SecretKey
		skErr  error
	)

	loadPallasSk := func() (*elgamal.SecretKey, error) {
		skOnce.Do(func() {
			if pallasSkPath == "" {
				logger.Warn("PrepareProposal: vote.pallas_sk_path is empty — auto-ack disabled")
				skErr = os.ErrNotExist
				return
			}
			logger.Info("PrepareProposal: loading Pallas secret key for ack", "path", pallasSkPath)
			raw, err := os.ReadFile(pallasSkPath)
			if err != nil {
				skErr = err
				logger.Error("PrepareProposal[ack]: failed to load Pallas secret key",
					"path", pallasSkPath, "err", err)
				return
			}
			sk, skErr = elgamal.UnmarshalSecretKey(raw)
			if skErr != nil {
				logger.Error("PrepareProposal[ack]: failed to parse Pallas secret key",
					"path", pallasSkPath, "err", skErr)
			}
		})
		return sk, skErr
	}

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		pallasSk, err := loadPallasSk()
		if err != nil {
			return txs
		}

		// Resolve proposer.
		consAddr := sdk.ConsAddress(req.ProposerAddress)
		val, err := stakingKeeper.GetValidatorByConsAddr(ctx, consAddr)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to resolve proposer validator", "err", err)
			return txs
		}
		proposerValAddr := val.OperatorAddress

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

		eaSkBytes, err := ecies.Decrypt(pallasSk.Scalar, env)
		if err != nil {
			logger.Error("PrepareProposal[ack]: ECIES decryption failed",
				"proposer", proposerValAddr, "err", err)
			return txs
		}

		// Verify ea_sk * G == ea_pk.
		recoveredSk, err := elgamal.UnmarshalSecretKey(eaSkBytes)
		if err != nil {
			logger.Error("PrepareProposal[ack]: failed to parse decrypted ea_sk",
				"proposer", proposerValAddr, "err", err)
			return txs
		}
		G := elgamal.PallasGenerator()
		recoveredPkPoint := G.Mul(recoveredSk.Scalar)
		if !bytesEqual(recoveredPkPoint.ToAffineCompressed(), round.EaPk) {
			logger.Error("PrepareProposal[ack]: ea_sk * G != ea_pk — dealer sent garbage",
				"proposer", proposerValAddr,
				"round", hex.EncodeToString(round.VoteRoundId))
			return txs
		}

		// Compute ack_signature = SHA256("ack" || ea_pk || validator_address).
		h := sha256.New()
		h.Write([]byte("ack"))
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

		// Write ea_sk to per-round path for auto-tally.
		if eaSkDir != "" {
			path := eaSkPathForRound(eaSkDir, round.VoteRoundId)
			if err := os.WriteFile(path, eaSkBytes, 0600); err != nil {
				logger.Error("PrepareProposal[ack]: failed to write ea_sk",
					"path", path, "err", err)
				// Continue — the ack injection itself is more important.
			} else {
				logger.Info("PrepareProposal[ack]: ea_sk written to disk", "path", path)
			}
		}

		logger.Info("PrepareProposal[ack]: injecting MsgAckExecutiveAuthorityKey",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(round.VoteRoundId))
		return append([][]byte{txBytes}, txs...)
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
// Returns nil, nil if the file doesn't exist (non-dealer validators).
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
