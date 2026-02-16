package app

import (
	"os"
	"sync"

	abci "github.com/cometbft/cometbft/abci/types"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	voteapi "github.com/z-cale/zally/api"
	"github.com/z-cale/zally/crypto/elgamal"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// bsgsDefaultBound is the upper bound for the baby-step giant-step discrete
// log solver. 2^32 supports vote totals up to ~4 billion zatoshi.
const bsgsDefaultBound = 1 << 32

// TallyPrepareProposalHandler returns a PrepareProposalHandler that wraps the
// default behavior (passing through req.Txs) and additionally injects
// MsgSubmitTally transactions for any rounds in TALLYING state.
//
// The block proposer decrypts the on-chain ElGamal accumulators using the EA
// secret key loaded from eaSkPath. If the key file is absent or empty, the
// handler passes through transactions unchanged (allowing non-EA validators
// to skip tally injection).
func TallyPrepareProposalHandler(
	voteKeeper votekeeper.Keeper,
	stakingKeeper *stakingkeeper.Keeper,
	eaSkPath string,
	logger log.Logger,
) sdk.PrepareProposalHandler {
	var (
		skOnce  sync.Once
		sk      *elgamal.SecretKey
		skErr   error
		bsgOnce sync.Once
		bsgs    *elgamal.BSGSTable
	)

	loadSk := func() (*elgamal.SecretKey, error) {
		skOnce.Do(func() {
			if eaSkPath == "" {
				skErr = os.ErrNotExist
				return
			}
			raw, err := os.ReadFile(eaSkPath)
			if err != nil {
				skErr = err
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("!! FAILED TO LOAD EA SECRET KEY — AUTO-TALLY IS DISABLED !!")
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("EA secret key load error", "path", eaSkPath, "err", err)
				return
			}
			sk, skErr = elgamal.UnmarshalSecretKey(raw)
			if skErr != nil {
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("!! FAILED TO PARSE EA SECRET KEY — AUTO-TALLY IS DISABLED !!")
				logger.Error("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
				logger.Error("EA secret key parse error", "path", eaSkPath, "err", skErr)
			}
		})
		return sk, skErr
	}

	loadBSGS := func() *elgamal.BSGSTable {
		bsgOnce.Do(func() {
			bsgs = elgamal.NewBSGSTable(bsgsDefaultBound)
		})
		return bsgs
	}

	return func(ctx sdk.Context, req *abci.RequestPrepareProposal) (*abci.ResponsePrepareProposal, error) {
		// Start with the transactions from CometBFT (NoOpMempool passes them through).
		txs := req.Txs

		eaSk, err := loadSk()
		if err != nil {
			// No EA key available — pass through without injecting tally txs.
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}

		table := loadBSGS()

		// Resolve proposer consensus address to validator operator address.
		consAddr := sdk.ConsAddress(req.ProposerAddress)
		val, err := stakingKeeper.GetValidatorByConsAddr(ctx, consAddr)
		if err != nil {
			logger.Error("PrepareProposal: failed to resolve proposer validator", "err", err)
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}
		proposerValAddr := val.OperatorAddress

		kvStore := voteKeeper.OpenKVStore(ctx)

		// Find all rounds in TALLYING state.
		var tallyingRounds []*types.VoteRound
		if err := voteKeeper.IterateTallyingRounds(kvStore, func(round *types.VoteRound) bool {
			r := *round // copy
			tallyingRounds = append(tallyingRounds, &r)
			return false
		}); err != nil {
			logger.Error("PrepareProposal: failed to iterate tallying rounds", "err", err)
			return &abci.ResponsePrepareProposal{Txs: txs}, nil
		}

		// Build and prepend a MsgSubmitTally for each tallying round.
		var injectedTxs [][]byte
		for _, round := range tallyingRounds {
			entries, err := decryptRoundTallies(kvStore, voteKeeper, round, eaSk, table)
			if err != nil {
				logger.Error("PrepareProposal: failed to decrypt tally",
					"round", round.VoteRoundId, "err", err)
				continue // skip this round, try the next
			}
			if len(entries) == 0 {
				continue // no accumulator entries, nothing to submit
			}

			msg := &types.MsgSubmitTally{
				VoteRoundId: round.VoteRoundId,
				Creator:     proposerValAddr,
				Entries:     entries,
			}

			txBytes, err := voteapi.EncodeVoteTx(msg)
			if err != nil {
				logger.Error("PrepareProposal: failed to encode tally tx",
					"round", round.VoteRoundId, "err", err)
				continue
			}
			injectedTxs = append(injectedTxs, txBytes)
		}

		// Prepend injected tally txs before the mempool txs.
		if len(injectedTxs) > 0 {
			txs = append(injectedTxs, txs...)
		}

		return &abci.ResponsePrepareProposal{Txs: txs}, nil
	}
}

// decryptRoundTallies decrypts all accumulated ciphertexts for a round and
// returns the corresponding TallyEntry slice.
func decryptRoundTallies(
	kvStore store.KVStore,
	voteKeeper votekeeper.Keeper,
	round *types.VoteRound,
	sk *elgamal.SecretKey,
	bsgs *elgamal.BSGSTable,
) ([]*types.TallyEntry, error) {
	var entries []*types.TallyEntry

	for proposalIdx := range round.Proposals {
		proposalID := uint32(proposalIdx)

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

			entries = append(entries, &types.TallyEntry{
				ProposalId:   proposalID,
				VoteDecision: decision,
				TotalValue:   totalValue,
			})
		}
	}

	return entries, nil
}
