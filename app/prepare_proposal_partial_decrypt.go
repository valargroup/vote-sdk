package app

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/sentry"
	votekeeper "github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// PartialDecryptPrepareProposalInjector returns a PrepareProposalInjector that
// handles the partial decryption phase of tally.
//
// When a round is in TALLYING state and the block proposer has not yet
// submitted a partial decryption for that round, it:
//
//  1. Loads the proposer's Shamir share from <ceremonyDir>/share.<hex(round_id)>
//  2. Finds the proposer's 1-based validator_index in ceremony_validators
//  3. Computes D_i = share_i * C1 for every non-empty tally accumulator
//  4. Injects MsgSubmitPartialDecryption (tag 0x0D)
//
// If ceremonyDir is empty, the share file is absent, or the proposer is not a
// ceremony validator, injection is skipped gracefully.
func PartialDecryptPrepareProposalInjector(
	voteKeeper *votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	ceremonyDir string,
	logger log.Logger,
) PrepareProposalInjector {
	var (
		// Per-round share cache: round_id_hex -> share scalar (as SecretKey).
		shareCache   = make(map[string]*elgamal.SecretKey)
		shareCacheMu sync.Mutex
	)

	loadShareForRoundCached := func(roundID []byte) (*elgamal.SecretKey, error) {
		roundHex := hex.EncodeToString(roundID)

		shareCacheMu.Lock()
		defer shareCacheMu.Unlock()

		if share, ok := shareCache[roundHex]; ok {
			return share, nil
		}
		share, err := loadShareForRound(ceremonyDir, roundID)
		if err != nil {
			return nil, err
		}
		shareCache[roundHex] = share
		return share, nil
	}

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		if ceremonyDir == "" {
			return txs
		}

		proposerValAddr, err := resolveProposer(ctx, stakingKeeper, req.ProposerAddress)
		if err != nil {
			return txs
		}

		kvStore := voteKeeper.OpenKVStore(ctx)

		// Evict share cache entries for finalized rounds to bound growth,
		// and zero-and-delete the corresponding on-disk share files.
		shareCacheMu.Lock()
		for roundHex, share := range shareCache {
			roundID, err := hex.DecodeString(roundHex)
			if err != nil {
				delete(shareCache, roundHex)
				continue
			}
			r, err := voteKeeper.GetVoteRound(kvStore, roundID)
			if err != nil || r.Status == types.SessionStatus_SESSION_STATUS_FINALIZED {
				zeroScalar(share.Scalar)
				delete(shareCache, roundHex)
				zeroAndDeleteShareFile(ceremonyDir, roundID, logger)
			}
		}
		shareCacheMu.Unlock()

		cleanOrphanedShareFiles(ceremonyDir, voteKeeper, kvStore, logger)

		// Find the first TALLYING round.
		var tallyRound *types.VoteRound
		if err := voteKeeper.IterateTallyingRounds(kvStore, func(round *types.VoteRound) bool {
			tallyRound = round
			return true // stop at first match
		}); err != nil {
			logger.Error("PrepareProposal[partial-decrypt]: failed to iterate tallying rounds", "err", err)
			sentry.CaptureErr(err, map[string]string{"handler": "PrepareProposal", "stage": "iterate_tallying_rounds_pd"})
			return txs
		}
		if tallyRound == nil {
			return txs
		}

		// Find proposer's original Shamir index in the round's ceremony set.
		// ShamirIndex is set once at round creation and survives validator stripping,
		// so it always reflects the correct x-coordinate for Lagrange interpolation.
		ceremonyVal, found := votekeeper.FindValidatorInRoundCeremony(tallyRound, proposerValAddr)
		if !found {
			// Proposer is not in the ceremony set — skip.
			return txs
		}
		validatorIndex := ceremonyVal.ShamirIndex

		// Skip if this validator has already submitted for this round.
		has, err := voteKeeper.HasPartialDecryptionsFromValidator(kvStore, tallyRound.VoteRoundId, validatorIndex)
		if err != nil {
			logger.Error("PrepareProposal[partial-decrypt]: failed to check existing submission", "err", err)
			sentry.CaptureErr(err, map[string]string{
				"handler":  "PrepareProposal",
				"stage":    "check_existing_pd",
				"round_id": hex.EncodeToString(tallyRound.VoteRoundId),
			})
			return txs
		}
		if has {
			return txs
		}

		// Load the validator's Shamir share from disk.
		share, err := loadShareForRoundCached(tallyRound.VoteRoundId)
		if err != nil {
			logger.Warn("PrepareProposal[partial-decrypt]: no share file for round, skipping",
				"round", hex.EncodeToString(tallyRound.VoteRoundId), "err", err)
			return txs
		}

		// Compute D_i = share * C1 for every non-empty tally accumulator.
		var entries []*types.PartialDecryptionEntry

		roundHex := hex.EncodeToString(tallyRound.VoteRoundId)
		for _, proposal := range tallyRound.Proposals {
			tallyMap, err := voteKeeper.GetProposalTally(kvStore, tallyRound.VoteRoundId, proposal.Id)
			if err != nil {
				logger.Error("PrepareProposal[partial-decrypt]: failed to read tally",
					"round", roundHex,
					"proposal", proposal.Id, "err", err)
				sentry.CaptureErr(err, map[string]string{
					"handler":  "PrepareProposal",
					"stage":    "read_tally",
					"round_id": roundHex,
				})
				return txs
			}

			for decision, ctBytes := range tallyMap {
				ct, err := elgamal.UnmarshalCiphertext(ctBytes)
				if err != nil {
					logger.Error("PrepareProposal[partial-decrypt]: failed to unmarshal ciphertext",
						"proposal", proposal.Id, "decision", decision, "err", err)
					sentry.CaptureErr(err, map[string]string{
						"handler":  "PrepareProposal",
						"stage":    "unmarshal_ciphertext",
						"round_id": roundHex,
					})
					return txs
				}

				// D_i = share_i * C1  (partial ElGamal decryption)
				Di := ct.C1.Mul(share.Scalar)
				if !Di.IsOnCurve() {
					offCurveErr := fmt.Errorf("D_i not on curve (proposal=%d, decision=%d)", proposal.Id, decision)
					logger.Error("PrepareProposal[partial-decrypt]: D_i is not on curve",
						"proposal", proposal.Id, "decision", decision)
					sentry.CaptureErr(offCurveErr, map[string]string{
						"handler":  "PrepareProposal",
						"stage":    "di_off_curve",
						"round_id": roundHex,
					})
					return txs
				}

				// DLEQ proof: log_G(VK_i) == log_{C1}(D_i)
				dleqProof, err := elgamal.GeneratePartialDecryptDLEQ(share.Scalar, ct.C1)
				if err != nil {
					logger.Error("PrepareProposal[partial-decrypt]: DLEQ proof generation failed",
						"proposal", proposal.Id, "decision", decision, "err", err)
					sentry.CaptureErr(err, map[string]string{
						"handler":  "PrepareProposal",
						"stage":    "dleq_proof",
						"round_id": roundHex,
					})
					return txs
				}

				entries = append(entries, &types.PartialDecryptionEntry{
					ProposalId:     proposal.Id,
					VoteDecision:   decision,
					PartialDecrypt: Di.ToAffineCompressed(),
					DleqProof:      dleqProof,
				})
			}
		}

		if len(entries) == 0 {
			// No non-empty accumulators yet — nothing to submit.
			return txs
		}

		msg := &types.MsgSubmitPartialDecryption{
			VoteRoundId:    tallyRound.VoteRoundId,
			Creator:        proposerValAddr,
			ValidatorIndex: validatorIndex,
			Entries:        entries,
		}

		txBytes, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagSubmitPartialDecryption)
		if err != nil {
			logger.Error("PrepareProposal[partial-decrypt]: failed to encode tx", "err", err)
			sentry.CaptureErr(err, map[string]string{
				"handler":  "PrepareProposal",
				"stage":    "encode_pd_tx",
				"round_id": roundHex,
			})
			return txs
		}

		logger.Info("PrepareProposal[partial-decrypt]: injecting MsgSubmitPartialDecryption",
			"proposer", proposerValAddr,
			"round", hex.EncodeToString(tallyRound.VoteRoundId),
			"validator_index", validatorIndex,
			"entries", len(entries),
			"threshold", tallyRound.Threshold)

		return append([][]byte{txBytes}, txs...)
	}
}

// cleanOrphanedShareFiles scans ceremonyDir for share.<hex> files belonging to
// rounds that are finalized or no longer exist, and zero-and-deletes them.
// This catches files that were never loaded into the in-memory cache.
func cleanOrphanedShareFiles(
	ceremonyDir string,
	voteKeeper *votekeeper.Keeper,
	kvStore store.KVStore,
	logger log.Logger,
) {
	if ceremonyDir == "" {
		return
	}
	entries, err := os.ReadDir(ceremonyDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "share.") || entry.IsDir() {
			continue
		}
		roundHex := strings.TrimPrefix(name, "share.")
		roundID, err := hex.DecodeString(roundHex)
		if err != nil {
			continue
		}
		r, err := voteKeeper.GetVoteRound(kvStore, roundID)
		if err != nil || r.Status == types.SessionStatus_SESSION_STATUS_FINALIZED {
			zeroAndDeleteShareFile(ceremonyDir, roundID, logger)
		}
	}
}
