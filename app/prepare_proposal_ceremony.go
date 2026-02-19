package app

import (
	"crypto/sha256"
	"os"
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

// CeremonyAckPrepareProposalHandler returns a PrepareProposalHandler that
// checks whether the EA key ceremony is in DEALT state and, if so, injects
// a MsgAckExecutiveAuthorityKey on behalf of the block proposer.
//
// The proposer decrypts their ECIES payload using the Pallas secret key
// loaded from pallasSkPath. If the key file is absent, the ceremony is not
// DEALT, or the proposer has already acked, injection is skipped gracefully.
//
// After successful decryption, the ea_sk is written to eaSkPath so the
// auto-tally system (TallyPrepareProposalHandler) can pick it up.
func CeremonyAckPrepareProposalHandler(
	voteKeeper votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	pallasSkPath string,
	eaSkPath string,
	logger log.Logger,
) func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
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
			logger.Info("PrepareProposal: loading Pallas secret key", "path", pallasSkPath)
			raw, err := os.ReadFile(pallasSkPath)
			if err != nil {
				skErr = err
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("!! FAILED TO LOAD PALLAS SECRET KEY — AUTO-ACK IS DISABLED !!")
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("Pallas secret key load error", "path", pallasSkPath, "err", err)
				return
			}
			sk, skErr = elgamal.UnmarshalSecretKey(raw)
			if skErr != nil {
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("!! FAILED TO PARSE PALLAS SECRET KEY — AUTO-ACK IS DISABLED !!")
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("Pallas secret key parse error", "path", pallasSkPath, "err", skErr)
			} else {
				logger.Info("PrepareProposal: Pallas secret key loaded successfully", "path", pallasSkPath)
			}
		})
		return sk, skErr
	}

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		pallasSk, err := loadPallasSk()
		if err != nil {
			return txs
		}

		// Resolve proposer consensus address to validator operator address.
		// The ceremony state stores valoper addresses (set by RegisterPallasKey
		// and CreateValidatorWithPallasKey), so this matches directly.
		consAddr := sdk.ConsAddress(req.ProposerAddress)
		val, err := stakingKeeper.GetValidatorByConsAddr(ctx, consAddr)
		if err != nil {
			logger.Error("PrepareProposal[ceremony]: failed to resolve proposer validator", "err", err)
			return txs
		}
		proposerValAddr := val.OperatorAddress

		kvStore := voteKeeper.OpenKVStore(ctx)

		// Check if ceremony is in DEALT state.
		state, err := voteKeeper.GetCeremonyState(kvStore)
		if err != nil {
			logger.Error("PrepareProposal[ceremony]: failed to get ceremony state", "err", err)
			return txs
		}
		if state == nil || state.Status != types.CeremonyStatus_CEREMONY_STATUS_DEALT {
			return txs
		}

		// Check if the proposer has already acked.
		if _, found := votekeeper.FindAckForValidator(state, proposerValAddr); found {
			return txs
		}

		// Check if the proposer is a registered validator in the ceremony.
		if _, found := votekeeper.FindValidatorInCeremony(state, proposerValAddr); !found {
			return txs
		}

		// Find the proposer's ECIES payload.
		var payload *types.DealerPayload
		for _, p := range state.Payloads {
			if p.ValidatorAddress == proposerValAddr {
				payload = p
				break
			}
		}
		if payload == nil {
			logger.Error("PrepareProposal[ceremony]: no payload found for proposer", "proposer", proposerValAddr)
			return txs
		}

		// Reconstruct the ECIES envelope from on-chain bytes.
		ephPk, err := elgamal.UnmarshalPublicKey(payload.EphemeralPk)
		if err != nil {
			logger.Error("PrepareProposal[ceremony]: failed to unmarshal ephemeral_pk",
				"proposer", proposerValAddr, "err", err)
			return txs
		}
		env := &ecies.Envelope{
			Ephemeral:  ephPk.Point,
			Ciphertext: payload.Ciphertext,
		}

		// ECIES decrypt ea_sk using the proposer's Pallas secret key.
		eaSkBytes, err := ecies.Decrypt(pallasSk.Scalar, env)
		if err != nil {
			logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			logger.Error("!! ECIES DECRYPTION FAILED — SKIPPING AUTO-ACK            !!")
			logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			logger.Error("PrepareProposal[ceremony]: ECIES decryption failed",
				"proposer", proposerValAddr, "err", err)
			return txs
		}

		// Verify ea_sk * G == ea_pk.
		recoveredSk, err := elgamal.UnmarshalSecretKey(eaSkBytes)
		if err != nil {
			logger.Error("PrepareProposal[ceremony]: failed to parse decrypted ea_sk",
				"proposer", proposerValAddr, "err", err)
			return txs
		}
		G := elgamal.PallasGenerator()
		recoveredPkPoint := G.Mul(recoveredSk.Scalar)
		if !bytesEqual(recoveredPkPoint.ToAffineCompressed(), state.EaPk) {
			logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			logger.Error("!! ea_sk * G != ea_pk — DEALER SENT GARBAGE, SKIPPING ACK !!")
			logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
			logger.Error("PrepareProposal[ceremony]: ea_sk verification failed",
				"proposer", proposerValAddr)
			return txs
		}

		// Compute ack_signature = SHA256("ack" || ea_pk || validator_address).
		h := sha256.New()
		h.Write([]byte("ack"))
		h.Write(state.EaPk)
		h.Write([]byte(proposerValAddr))
		ackSig := h.Sum(nil)

		// Build and encode the ack message.
		ackMsg := &types.MsgAckExecutiveAuthorityKey{
			Creator:      proposerValAddr,
			AckSignature: ackSig,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(ackMsg, voteapi.TagAckExecutiveAuthorityKey)
		if err != nil {
			logger.Error("PrepareProposal[ceremony]: failed to encode ack tx", "err", err)
			return txs
		}

		// Write ea_sk to disk so auto-tally can use it.
		if eaSkPath != "" {
			if err := os.WriteFile(eaSkPath, eaSkBytes, 0600); err != nil {
				logger.Error("PrepareProposal[ceremony]: failed to write ea_sk to disk",
					"path", eaSkPath, "err", err)
				// Continue — the ack injection itself is more important.
			} else {
				logger.Info("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Info("!! EA SECRET KEY WRITTEN TO DISK — AUTO-TALLY READY !!")
				logger.Info("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Info("PrepareProposal[ceremony]: ea_sk written to disk",
					"path", eaSkPath)
			}
		}

		logger.Info("PrepareProposal[ceremony]: injecting MsgAckExecutiveAuthorityKey",
			"proposer", proposerValAddr)
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
