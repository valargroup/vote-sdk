package app

import (
	"encoding/hex"
	"sync"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	voteapi "github.com/valargroup/shielded-vote/api"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
	votekeeper "github.com/valargroup/shielded-vote/x/vote/keeper"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// PartialDecryptPrepareProposalInjector returns a PrepareProposalInjector that
// handles the partial decryption phase of threshold-mode tally.
//
// When a round is in TALLYING state with Threshold > 0 and the block proposer
// has not yet submitted a partial decryption for that round, it:
//
//  1. Loads the proposer's Shamir share from <eaSkDir>/share.<hex(round_id)>
//  2. Finds the proposer's 1-based validator_index in ceremony_validators
//  3. Computes D_i = share_i * C1 for every non-empty tally accumulator
//  4. Injects MsgSubmitPartialDecryption (tag 0x0D)
//
// If eaSkDir is empty, the share file is absent, or the proposer is not a
// ceremony validator, injection is skipped gracefully.
//
// Legacy rounds (Threshold == 0) are ignored here; they are handled by the
// existing TallyPrepareProposalHandler which decrypts with the full ea_sk.
func PartialDecryptPrepareProposalInjector(
	voteKeeper *votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	eaSkDir string,
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
		share, err := loadShareForRound(eaSkDir, roundID)
		if err != nil {
			return nil, err
		}
		shareCache[roundHex] = share
		return share, nil
	}

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte {
		if eaSkDir == "" {
			return txs
		}

		proposerValAddr, err := resolveProposer(ctx, stakingKeeper, req.ProposerAddress)
		if err != nil {
			return txs
		}

		kvStore := voteKeeper.OpenKVStore(ctx)

		// Prevent unbounded cache growth by evicting key cache entries for finalized rounds.
		evictFinalizedSkEntries(kvStore, voteKeeper, shareCache, &shareCacheMu, logger)

		// Find the first TALLYING round in threshold mode.
		var tallyRound *types.VoteRound
		if err := voteKeeper.IterateTallyingRounds(kvStore, func(round *types.VoteRound) bool {
			if round.Threshold > 0 {
				tallyRound = round
				return true // stop at first match
			}
			return false
		}); err != nil {
			logger.Error("PrepareProposal[partial-decrypt]: failed to iterate tallying rounds", "err", err)
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

		for _, proposal := range tallyRound.Proposals {
			tallyMap, err := voteKeeper.GetProposalTally(kvStore, tallyRound.VoteRoundId, proposal.Id)
			if err != nil {
				logger.Error("PrepareProposal[partial-decrypt]: failed to read tally",
					"round", hex.EncodeToString(tallyRound.VoteRoundId),
					"proposal", proposal.Id, "err", err)
				return txs
			}

			for decision, ctBytes := range tallyMap {
				ct, err := elgamal.UnmarshalCiphertext(ctBytes)
				if err != nil {
					logger.Error("PrepareProposal[partial-decrypt]: failed to unmarshal ciphertext",
						"proposal", proposal.Id, "decision", decision, "err", err)
					return txs
				}

				// D_i = share_i * C1  (partial ElGamal decryption)
				Di := ct.C1.Mul(share.Scalar)
				// Validate the result is on the curve before encoding.
				if !Di.IsOnCurve() {
					logger.Error("PrepareProposal[partial-decrypt]: D_i is not on curve",
						"proposal", proposal.Id, "decision", decision)
					return txs
				}

				entries = append(entries, &types.PartialDecryptionEntry{
					ProposalId:     proposal.Id,
					VoteDecision:   decision,
					PartialDecrypt: Di.ToAffineCompressed(),
					// DleqProof is empty in Step 1; added in Step 2.
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
