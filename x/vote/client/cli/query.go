package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// GetQueryCmd returns the query commands for the vote module grouped under
// "svoted query vote".
func GetQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Vote module query subcommands",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(CmdVerifyTally())
	return cmd
}

// CmdVerifyTally independently checks finalized tally results against the
// encrypted accumulators and validator partial decryptions stored on-chain.
func CmdVerifyTally() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify-tally [vote-round-id-hex]",
		Short: "Verify finalized tally results from local full-node state",
		Long: `Re-run the threshold tally verification for a finalized vote round.

The command fetches the round, encrypted tally accumulators, stored partial
decryptions, and finalized results through the local node query path. For each
finalized (proposal, decision) tuple it Lagrange-combines validator partial
decryptions and checks C2 - combined_partial == total_value * G.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			roundID, err := hex.DecodeString(args[0])
			if err != nil {
				return fmt.Errorf("invalid vote-round-id-hex: %w", err)
			}
			if len(roundID) != types.RoundIDLen {
				return fmt.Errorf("vote-round-id-hex must decode to %d bytes, got %d", types.RoundIDLen, len(roundID))
			}

			report, err := verifyTally(clientCtx, roundID)
			if err != nil {
				return err
			}

			output, err := cmd.Flags().GetString(flags.FlagOutput)
			if err != nil {
				return err
			}
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
			} else {
				printTallyVerificationReport(cmd, report)
			}
			if !report.Verified {
				return fmt.Errorf("tally verification failed")
			}
			return nil
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	return cmd
}

type tallyVerificationReport struct {
	VoteRoundID string                    `json:"vote_round_id"`
	Status      string                    `json:"status"`
	Threshold   uint32                    `json:"threshold"`
	Verified    bool                      `json:"verified"`
	Checks      []tallyVerificationResult `json:"checks"`
}

type tallyVerificationResult struct {
	ProposalID      uint32 `json:"proposal_id"`
	VoteDecision    uint32 `json:"vote_decision"`
	ClaimedTotal    uint64 `json:"claimed_total"`
	PartialCount    int    `json:"partial_count"`
	Accumulator     bool   `json:"accumulator"`
	Verified        bool   `json:"verified"`
	Failure         string `json:"failure,omitempty"`
	ExpectedPoint   string `json:"expected_point,omitempty"`
	RecomputedPoint string `json:"recomputed_point,omitempty"`
}

type tallyKey struct {
	proposalID   uint32
	voteDecision uint32
}

func verifyTally(clientCtx client.Context, roundID []byte) (*tallyVerificationReport, error) {
	roundResp := &types.QueryVoteRoundResponse{}
	if err := queryVoteModule(clientCtx, "/svote.v1.Query/VoteRound", &types.QueryVoteRoundRequest{VoteRoundId: roundID}, roundResp); err != nil {
		return nil, fmt.Errorf("query vote round: %w", err)
	}
	if roundResp.Round == nil {
		return nil, fmt.Errorf("vote round not found")
	}
	round := roundResp.Round

	tallyResultsResp := &types.QueryTallyResultsResponse{}
	if err := queryVoteModule(clientCtx, "/svote.v1.Query/TallyResults", &types.QueryTallyResultsRequest{VoteRoundId: roundID}, tallyResultsResp); err != nil {
		return nil, fmt.Errorf("query tally results: %w", err)
	}
	partialResp := &types.QueryPartialDecryptionsResponse{}
	if err := queryVoteModule(clientCtx, "/svote.v1.Query/PartialDecryptions", &types.QueryPartialDecryptionsRequest{VoteRoundId: roundID}, partialResp); err != nil {
		return nil, fmt.Errorf("query partial decryptions: %w", err)
	}

	resultByKey := make(map[tallyKey]*types.TallyResult, len(tallyResultsResp.Results))
	keys := make(map[tallyKey]struct{})
	for _, result := range tallyResultsResp.Results {
		key := tallyKey{proposalID: result.ProposalId, voteDecision: result.VoteDecision}
		resultByKey[key] = result
		keys[key] = struct{}{}
	}

	partialsByKey := make(map[tallyKey][]*types.StoredPartialDecryption)
	for _, entry := range partialResp.Entries {
		key := tallyKey{proposalID: entry.ProposalId, voteDecision: entry.VoteDecision}
		partialsByKey[key] = append(partialsByKey[key], entry)
	}

	accumulatorByKey := make(map[tallyKey][]byte)
	for _, proposal := range round.Proposals {
		proposalTallyResp := &types.QueryProposalTallyResponse{}
		err := queryVoteModule(clientCtx, "/svote.v1.Query/ProposalTally", &types.QueryProposalTallyRequest{
			VoteRoundId: roundID,
			ProposalId:  proposal.Id,
		}, proposalTallyResp)
		if err != nil {
			return nil, fmt.Errorf("query proposal %d tally: %w", proposal.Id, err)
		}
		for decision, acc := range proposalTallyResp.Tally {
			key := tallyKey{proposalID: proposal.Id, voteDecision: decision}
			accumulatorByKey[key] = acc
			keys[key] = struct{}{}
		}
	}

	sortedKeys := make([]tallyKey, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Slice(sortedKeys, func(i, j int) bool {
		if sortedKeys[i].proposalID != sortedKeys[j].proposalID {
			return sortedKeys[i].proposalID < sortedKeys[j].proposalID
		}
		return sortedKeys[i].voteDecision < sortedKeys[j].voteDecision
	})

	report := &tallyVerificationReport{
		VoteRoundID: hex.EncodeToString(roundID),
		Status:      round.Status.String(),
		Threshold:   round.Threshold,
		Verified:    true,
	}
	for _, key := range sortedKeys {
		check := verifyTallyEntry(round, resultByKey[key], accumulatorByKey[key], partialsByKey[key], key)
		if !check.Verified {
			report.Verified = false
		}
		report.Checks = append(report.Checks, check)
	}
	if len(report.Checks) == 0 {
		report.Verified = false
	}

	return report, nil
}

func verifyTallyEntry(
	round *types.VoteRound,
	result *types.TallyResult,
	accumulator []byte,
	partials []*types.StoredPartialDecryption,
	key tallyKey,
) tallyVerificationResult {
	check := tallyVerificationResult{
		ProposalID:   key.proposalID,
		VoteDecision: key.voteDecision,
		PartialCount: len(partials),
		Accumulator:  len(accumulator) > 0,
	}
	if result == nil {
		check.Failure = "missing finalized tally result"
		return check
	}
	check.ClaimedTotal = result.TotalValue

	if len(accumulator) == 0 {
		check.Verified = result.TotalValue == 0
		if !check.Verified {
			check.Failure = "non-zero finalized total with no encrypted accumulator"
		}
		return check
	}

	ct, err := elgamal.UnmarshalCiphertext(accumulator)
	if err != nil {
		check.Failure = fmt.Sprintf("invalid accumulator: %v", err)
		return check
	}

	shamirPartials := make([]shamir.PartialDecryption, len(partials))
	for i, partial := range partials {
		point, err := elgamal.UnmarshalPoint(partial.PartialDecrypt)
		if err != nil {
			check.Failure = fmt.Sprintf("invalid partial from validator %d: %v", partial.ValidatorIndex, err)
			return check
		}
		shamirPartials[i] = shamir.PartialDecryption{
			Index: int(partial.ValidatorIndex),
			Di:    point,
		}
	}

	skC1, err := shamir.CombinePartials(shamirPartials, int(round.Threshold))
	if err != nil {
		check.Failure = fmt.Sprintf("partial combination failed: %v", err)
		return check
	}

	recomputed := ct.C2.Sub(skC1).ToAffineCompressed()
	expected := elgamal.ValuePoint(result.TotalValue).ToAffineCompressed()
	check.ExpectedPoint = hex.EncodeToString(expected)
	check.RecomputedPoint = hex.EncodeToString(recomputed)
	check.Verified = bytes.Equal(recomputed, expected)
	if !check.Verified {
		check.Failure = "C2 - combined_partial != total_value * G"
	}
	return check
}

func queryVoteModule(clientCtx client.Context, path string, req proto.Message, resp proto.Message) error {
	bz, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal query request: %w", err)
	}
	abciResp, err := clientCtx.QueryABCI(abci.RequestQuery{
		Path: path,
		Data: bz,
	})
	if err != nil {
		return err
	}
	if abciResp.Code != 0 {
		return fmt.Errorf("query failed (code %d): %s", abciResp.Code, strings.TrimSpace(abciResp.Log))
	}
	if err := proto.Unmarshal(abciResp.Value, resp); err != nil {
		return fmt.Errorf("unmarshal query response: %w", err)
	}
	return nil
}

func printTallyVerificationReport(cmd *cobra.Command, report *tallyVerificationReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Vote round: %s\n", report.VoteRoundID)
	fmt.Fprintf(out, "Status: %s\n", report.Status)
	fmt.Fprintf(out, "Threshold: %d\n\n", report.Threshold)
	for _, check := range report.Checks {
		status := "verified"
		if !check.Verified {
			status = "FAILED"
		}
		fmt.Fprintf(out, "proposal=%d decision=%d total=%d partials=%d accumulator=%t %s",
			check.ProposalID, check.VoteDecision, check.ClaimedTotal, check.PartialCount, check.Accumulator, status)
		if check.Failure != "" {
			fmt.Fprintf(out, " (%s)", check.Failure)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out)
	if report.Verified {
		fmt.Fprintln(out, "Tally verification: OK")
	} else {
		fmt.Fprintln(out, "Tally verification: FAILED")
	}
}
