package app

import (
	"encoding/hex"
	"fmt"
	"sync"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	voteapi "github.com/valargroup/shielded-vote/api"
	"github.com/valargroup/shielded-vote/crypto/elgamal"
	"github.com/valargroup/shielded-vote/crypto/shamir"
	votekeeper "github.com/valargroup/shielded-vote/x/vote/keeper"
	"github.com/valargroup/shielded-vote/x/vote/types"
)

// bsgsDefaultBound is the upper bound for the baby-step giant-step discrete
// log solver. 2^28 supports vote totals up to ~268 million.
const bsgsDefaultBound = 1 << 28

// PrepareProposalInjector is a function that may inject txs into the block
// proposal. It receives the current tx list and returns the (possibly modified)
// tx list. Injectors should prepend their txs before the existing ones.
type PrepareProposalInjector = func(ctx sdk.Context, req *abci.RequestPrepareProposal, txs [][]byte) [][]byte

// resolveProposer maps the block proposer's consensus address to their
// validator operator address. Returns ("", err) on failure.
func resolveProposer(ctx sdk.Context, stakingKeeper *stakingkeeper.Keeper, proposerAddr []byte) (string, error) {
	consAddr := sdk.ConsAddress(proposerAddr)
	val, err := stakingKeeper.GetValidatorByConsAddr(ctx, consAddr)
	if err != nil {
		return "", err
	}
	return val.OperatorAddress, nil
}

// ComposedPrepareProposalHandler composes ceremony deal, ceremony ack,
// threshold partial decryption, and tally injection into a single
// sdk.PrepareProposalHandler. Injectors run sequentially:
//
//	deal → ack → partialDecrypt → tally
func ComposedPrepareProposalHandler(
	dealInjector PrepareProposalInjector,
	ackInjector PrepareProposalInjector,
	partialDecryptInjector PrepareProposalInjector,
	tallyHandler sdk.PrepareProposalHandler,
) sdk.PrepareProposalHandler {
	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		// Start with the mempool txs from CometBFT.
		txs := req.Txs

		// Run ceremony deal injection (may prepend MsgDealExecutiveAuthorityKey).
		txs = dealInjector(ctx, req, txs)

		// Run ceremony ack injection (may prepend MsgAckExecutiveAuthorityKey).
		txs = ackInjector(ctx, req, txs)

		// Run threshold partial decryption injection (may prepend MsgSubmitPartialDecryption).
		txs = partialDecryptInjector(ctx, req, txs)

		// Run tally injection by creating a modified request with the updated txs.
		modifiedReq := *req
		modifiedReq.Txs = txs
		return tallyHandler(ctx, &modifiedReq)
	}
}

// TallyPrepareProposalHandler returns a PrepareProposalHandler that injects
// MsgSubmitTally for any round in TALLYING state.
//
// Threshold mode (round.Threshold > 0): reads stored partial decryptions from
// KV, waits until at least round.Threshold validators have submitted, then
// performs Lagrange interpolation in the exponent and BSGS. No DLEQ proof is
// generated (Step 1 trust model).
//
// Legacy mode (round.Threshold == 0): loads ea_sk from
// <eaSkDir>/ea_sk.<hex(round_id)> and decrypts directly with a DLEQ proof.
func TallyPrepareProposalHandler(
	voteKeeper *votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	eaSkDir string,
	logger log.Logger,
) sdk.PrepareProposalHandler {
	var (
		// Per-round ea_sk cache (legacy mode only): round_id_hex -> secret key.
		skCache   = make(map[string]*elgamal.SecretKey)
		skCacheMu sync.Mutex

		bsgOnce sync.Once
		bsgs    *elgamal.BSGSTable
	)

	loadSkForRound := func(roundID []byte) (*elgamal.SecretKey, error) {
		roundHex := hex.EncodeToString(roundID)

		skCacheMu.Lock()
		defer skCacheMu.Unlock()

		if sk, ok := skCache[roundHex]; ok {
			return sk, nil
		}

		sk, err := loadEaSkForRound(eaSkDir, roundID)
		if err != nil {
			return nil, err
		}
		skCache[roundHex] = sk
		return sk, nil
	}

	loadBSGS := func() *elgamal.BSGSTable {
		bsgOnce.Do(func() {
			logger.Info("PrepareProposal: building BSGS table", "bound", bsgsDefaultBound)
			bsgs = elgamal.NewBSGSTable(bsgsDefaultBound)
			logger.Info("PrepareProposal: BSGS table ready")
		})
		return bsgs
	}

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		// Start with the transactions from CometBFT (NoOpMempool passes them through).
		txs := req.Txs

		if eaSkDir == "" {
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}

		proposerValAddr, err := resolveProposer(ctx, stakingKeeper, req.ProposerAddress)
		if err != nil {
			logger.Error("PrepareProposal: failed to resolve proposer validator", "err", err)
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}

		kvStore := voteKeeper.OpenKVStore(ctx)

		// Prevent unbounded cache growth by evicting key cache entries for finalized rounds.
		evictFinalizedSkEntries(kvStore, voteKeeper, skCache, &skCacheMu, logger)

		// Find the first round in TALLYING state. We limit to one round per block
		// to bound PrepareProposal latency (BSGS decryption is expensive).
		var tallyRound *types.VoteRound
		if err := voteKeeper.IterateTallyingRounds(kvStore, func(round *types.VoteRound) bool {
			tallyRound = round
			return true // stop after first
		}); err != nil {
			logger.Error("PrepareProposal: failed to iterate tallying rounds", "err", err)
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}

		if tallyRound == nil {
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}

		var entries []*types.TallyEntry

		if tallyRound.Threshold > 0 {
			// Check whether any votes were cast (non-empty tally accumulators).
			hasAccumulators, err := roundHasAccumulators(kvStore, voteKeeper, tallyRound)
			if err != nil {
				logger.Error("PrepareProposal: failed to check tally accumulators", "err", err)
				return &abci.ResponsePrepareProposal{Txs: txs}, nil
			}

			if hasAccumulators {
				// Threshold mode: wait until t validators have submitted partial decryptions.
				validatorCount, err := voteKeeper.CountPartialDecryptionValidators(kvStore, tallyRound.VoteRoundId)
				if err != nil {
					logger.Error("PrepareProposal: failed to count partial decryptions", "err", err)
					return &abci.ResponsePrepareProposal{Txs: txs}, nil
				}
				if validatorCount < int(tallyRound.Threshold) {
					logger.Info("PrepareProposal: waiting for threshold partial decryptions",
						"round", hex.EncodeToString(tallyRound.VoteRoundId),
						"have", validatorCount, "need", tallyRound.Threshold)
					return &abci.ResponsePrepareProposal{Txs: txs}, nil
				}

				entries, err = decryptRoundTalliesThreshold(kvStore, voteKeeper, tallyRound, loadBSGS())
				if err != nil {
					logger.Error("PrepareProposal: threshold tally decryption failed",
						"round", hex.EncodeToString(tallyRound.VoteRoundId), "err", err)
					return &abci.ResponsePrepareProposal{Txs: txs}, nil
				}
			} else {
				logger.Info("PrepareProposal: no votes cast, finalizing with empty tally",
					"round", hex.EncodeToString(tallyRound.VoteRoundId))
			}
		} else {
			// Legacy mode: decrypt with the full ea_sk loaded from disk.
			eaSk, err := loadSkForRound(tallyRound.VoteRoundId)
			if err != nil {
				logger.Warn("PrepareProposal: no EA key for round, skipping tally",
					"round", hex.EncodeToString(tallyRound.VoteRoundId), "err", err)
				return &abci.ResponsePrepareProposal{Txs: txs}, nil
			}

			entries, err = decryptRoundTallies(kvStore, voteKeeper, tallyRound, eaSk, loadBSGS())
			if err != nil {
				logger.Error("PrepareProposal: failed to decrypt tally",
					"round", hex.EncodeToString(tallyRound.VoteRoundId), "err", err)
				return &abci.ResponsePrepareProposal{Txs: txs}, nil
			}
		}

		msg := &types.MsgSubmitTally{
			VoteRoundId: tallyRound.VoteRoundId,
			Creator:     proposerValAddr,
			Entries:     entries,
		}

		txBytes, err := voteapi.EncodeVoteTx(msg)
		if err != nil {
			logger.Error("PrepareProposal: failed to encode tally tx",
				"round", hex.EncodeToString(tallyRound.VoteRoundId), "err", err)
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}

		logger.Info("PrepareProposal: injecting MsgSubmitTally",
			"round", hex.EncodeToString(tallyRound.VoteRoundId),
			"entries", len(entries),
			"threshold_mode", tallyRound.Threshold > 0)
		txs = append([][]byte{txBytes}, txs...)

		return &abci.ResponsePrepareProposal{Txs: txs}, nil
	}
}

// decryptRoundTallies decrypts all accumulated ciphertexts for a round and
// returns the corresponding TallyEntry slice.
func decryptRoundTallies(
	kvStore store.KVStore,
	voteKeeper *votekeeper.Keeper,
	round *types.VoteRound,
	sk *elgamal.SecretKey,
	bsgs *elgamal.BSGSTable,
) ([]*types.TallyEntry, error) {
	var entries []*types.TallyEntry

	for proposalIdx := range round.Proposals {
		proposalID := round.Proposals[proposalIdx].Id

		tallyMap, err := voteKeeper.GetProposalTally(kvStore, round.VoteRoundId, proposalID)
		if err != nil {
			return nil, err
		}

		for decision, ctBytes := range tallyMap {
			ct, err := elgamal.UnmarshalCiphertext(ctBytes)
			if err != nil {
				return nil, err
			}

			// Decrypt: C2 - sk*C1 = v*G
			vG := elgamal.DecryptToPoint(sk, ct)

			// Solve discrete log: v*G → v
			totalValue, err := bsgs.Solve(vG)
			if err != nil {
				return nil, err
			}

			// Generate DLEQ proof that the decryption is correct.
			proof, err := elgamal.GenerateDLEQProof(sk, ct, totalValue)
			if err != nil {
				return nil, fmt.Errorf("DLEQ proof generation failed: %w", err)
			}

			entries = append(entries, &types.TallyEntry{
				ProposalId:      proposalID,
				VoteDecision:    decision,
				TotalValue:      totalValue,
				DecryptionProof: proof,
			})
		}
	}

	return entries, nil
}

// decryptRoundTalliesThreshold decrypts all accumulated ciphertexts for a
// threshold-mode round using Lagrange interpolation in the exponent.
//
// For each non-empty (proposal, decision) accumulator it:
//  1. Retrieves all stored D_i = share_i * C1 partial decryptions from KV.
//  2. Calls shamir.CombinePartials to compute sum(λ_i * D_i) = ea_sk * C1.
//  3. Computes v*G = C2 - (ea_sk * C1).
//  4. Solves v with BSGS.
//
// No DecryptionProof is included in Step 1 (the DLEQ field stays nil).
// The on-chain SubmitTally handler verifies by re-deriving the same Lagrange
// combination from the stored partials.
func decryptRoundTalliesThreshold(
	kvStore store.KVStore,
	voteKeeper *votekeeper.Keeper,
	round *types.VoteRound,
	bsgs *elgamal.BSGSTable,
) ([]*types.TallyEntry, error) {
	// Load all partial decryptions for the round, grouped by accumulator key.
	pdMap, err := voteKeeper.GetPartialDecryptionsForRound(kvStore, round.VoteRoundId)
	if err != nil {
		return nil, fmt.Errorf("failed to get partial decryptions: %w", err)
	}

	var entries []*types.TallyEntry

	for _, proposal := range round.Proposals {
		tallyMap, err := voteKeeper.GetProposalTally(kvStore, round.VoteRoundId, proposal.Id)
		if err != nil {
			return nil, err
		}

		for decision, ctBytes := range tallyMap {
			ct, err := elgamal.UnmarshalCiphertext(ctBytes)
			if err != nil {
				return nil, err
			}

			accKey := votekeeper.AccumulatorKey(proposal.Id, decision)
			storedPartials := pdMap[accKey]

			if len(storedPartials) == 0 {
				return nil, fmt.Errorf("no partial decryptions stored for accumulator (proposal=%d, decision=%d)",
					proposal.Id, decision)
			}

			// Convert stored entries to shamir.PartialDecryption values.
			shamirPartials := make([]shamir.PartialDecryption, len(storedPartials))
			for i, pd := range storedPartials {
				point, err := elgamal.UnmarshalPoint(pd.PartialDecrypt)
				if err != nil {
					return nil, fmt.Errorf("invalid partial_decrypt for validator %d: %w", pd.ValidatorIndex, err)
				}
				shamirPartials[i] = shamir.PartialDecryption{
					Index: int(pd.ValidatorIndex),
					Di:    point,
				}
			}

			// Lagrange interpolation in the exponent: sum(λ_i * D_i) = ea_sk * C1.
			skC1, err := shamir.CombinePartials(shamirPartials, int(round.Threshold))
			if err != nil {
				return nil, fmt.Errorf("Lagrange combination failed for (proposal=%d, decision=%d): %w",
					proposal.Id, decision, err)
			}

			// v*G = C2 - ea_sk*C1
			vG := ct.C2.Sub(skC1)

			totalValue, err := bsgs.Solve(vG)
			if err != nil {
				return nil, fmt.Errorf("BSGS solve failed for (proposal=%d, decision=%d): %w",
					proposal.Id, decision, err)
			}

			entries = append(entries, &types.TallyEntry{
				ProposalId:   proposal.Id,
				VoteDecision: decision,
				TotalValue:   totalValue,
				// DecryptionProof is nil in Step 1; added in Step 2 with DLEQ.
			})
		}
	}

	return entries, nil
}

// evictFinalizedSkEntries removes secret keys from the cache for rounds that
// have been finalized (or no longer exist). This bounds cache growth and limits
// exposure of old key material.
func evictFinalizedSkEntries(
	kvStore store.KVStore,
	voteKeeper *votekeeper.Keeper,
	cache map[string]*elgamal.SecretKey,
	mu *sync.Mutex,
	logger log.Logger,
) {
	mu.Lock()
	defer mu.Unlock()

	for roundHex, sk := range cache {
		roundID, err := hex.DecodeString(roundHex)
		if err != nil {
			delete(cache, roundHex)
			continue
		}
		round, err := voteKeeper.GetVoteRound(kvStore, roundID)
		if err != nil || round.Status == types.SessionStatus_SESSION_STATUS_FINALIZED {
			zeroScalar(sk.Scalar)
			delete(cache, roundHex)
			logger.Debug("PrepareProposal: evicted cached sk", "round", roundHex)
		}
	}
}

// roundHasAccumulators reports whether any (proposal, decision) tally
// accumulator is non-empty for the given round, i.e. at least one vote was
// cast. Used to distinguish the zero-vote case, which can be finalized
// immediately without waiting for partial decryptions.
func roundHasAccumulators(
	kvStore store.KVStore,
	voteKeeper *votekeeper.Keeper,
	round *types.VoteRound,
) (bool, error) {
	for _, proposal := range round.Proposals {
		tallyMap, err := voteKeeper.GetProposalTally(kvStore, round.VoteRoundId, proposal.Id)
		if err != nil {
			return false, err
		}
		if len(tallyMap) > 0 {
			return true, nil
		}
	}
	return false, nil
}
