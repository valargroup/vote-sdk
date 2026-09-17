package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/spf13/cobra"

	"github.com/valargroup/vote-sdk/ffi/roundid"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// CmdVerifyRound checks the round ID's commitment to its setup fields. It does
// not rebuild the IMT or authenticate the Zcash snapshot's accepted history.
func CmdVerifyRound() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verify-round [vote-round-id-hex]",
		Short: "Check the latest or selected round's identity and print its IMT inputs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			latest, _ := cmd.Flags().GetBool("latest")
			if latest && len(args) != 0 {
				return fmt.Errorf("choose --latest or a round ID, not both")
			}
			expectedChain, _ := cmd.Flags().GetString("expected-chain-id")
			if strings.TrimSpace(expectedChain) == "" {
				return fmt.Errorf("--expected-chain-id is required")
			}
			clientCtx, err := client.GetClientQueryContext(cmd)
			if err != nil {
				return err
			}
			node, err := clientCtx.GetNode()
			if err != nil {
				return err
			}
			status, err := node.Status(cmd.Context())
			if err != nil {
				return fmt.Errorf("query voting node status: %w", err)
			}
			if status.NodeInfo.Network != expectedChain {
				return fmt.Errorf("voting chain mismatch: expected %q, got %q", expectedChain, status.NodeInfo.Network)
			}
			if status.SyncInfo.CatchingUp || status.SyncInfo.LatestBlockHeight <= 0 {
				return fmt.Errorf("voting node is not caught up")
			}
			// Keep selection and round retrieval at one committed query height.
			if clientCtx.Height == 0 {
				clientCtx = clientCtx.WithHeight(status.SyncInfo.LatestBlockHeight)
			}
			var round *types.VoteRound
			if len(args) == 0 {
				resp := &types.QueryListRoundsResponse{}
				// ListRounds returns every registered round and has no pagination.
				if err := queryVoteModule(clientCtx, "/svote.v1.Query/ListRounds", &types.QueryListRoundsRequest{}, resp); err != nil {
					return err
				}
				round, err = latestVerificationRound(resp.Rounds)
				if err != nil {
					return err
				}
			} else {
				id, err := hex.DecodeString(args[0])
				if err != nil || len(id) != types.RoundIDLen {
					return fmt.Errorf("round ID must be 32 bytes of hex")
				}
				resp := &types.QueryVoteRoundResponse{}
				if err := queryVoteModule(clientCtx, "/svote.v1.Query/VoteRound", &types.QueryVoteRoundRequest{VoteRoundId: id}, resp); err != nil {
					return err
				}
				round = resp.Round
				if round == nil || !bytes.Equal(round.VoteRoundId, id) {
					return fmt.Errorf("requested round was not returned")
				}
			}
			report, err := checkRoundIdentity(round)
			if err != nil {
				return err
			}
			report.ChainID, report.QueryHeight = expectedChain, clientCtx.Height
			output, _ := cmd.Flags().GetString(flags.FlagOutput)
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(report); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Round: %s (%s)\nVoting chain: %s at height %d\nStatus: %s\nSnapshot height: %d\nSnapshot block hash: %s\nIMT circuit root: %s\nRound ID matches: %t\nIMT rebuild: not performed by this command\n",
					report.RoundID, report.Title, report.ChainID, report.QueryHeight, report.Status, report.SnapshotHeight, report.SnapshotBlockhash, report.NullifierIMTRoot, report.RoundIDMatches)
			}
			if !report.RoundIDMatches {
				return fmt.Errorf("round ID does not match its committed fields")
			}
			return nil
		},
	}
	flags.AddQueryFlagsToCmd(cmd)
	cmd.Flags().Bool("latest", false, "Select the newest registered round, regardless of approval or ceremony status (default when no ID is given)")
	cmd.Flags().String("expected-chain-id", "", "Required expected voting chain ID")
	return cmd
}

type roundIdentityReport struct {
	ChainID           string `json:"chain_id"`
	QueryHeight       int64  `json:"query_height"`
	RoundID           string `json:"round_id"`
	ComputedRoundID   string `json:"computed_round_id"`
	RoundIDMatches    bool   `json:"round_id_matches"`
	Title             string `json:"title"`
	Status            string `json:"status"`
	CreatedAtHeight   uint64 `json:"created_at_height"`
	SnapshotHeight    uint64 `json:"snapshot_height"`
	SnapshotBlockhash string `json:"snapshot_blockhash"`
	ProposalsHash     string `json:"proposals_hash"`
	VoteEndTime       uint64 `json:"vote_end_time"`
	NullifierIMTRoot  string `json:"nullifier_imt_root"`
	NCRoot            string `json:"nc_root"`
}

// latestVerificationRound deliberately includes pending and unattested rounds.
// Two rounds created in one block are ambiguous without an explicit ID.
func latestVerificationRound(rounds []*types.VoteRound) (*types.VoteRound, error) {
	var latest *types.VoteRound
	var tied []string
	for _, round := range rounds {
		if round == nil {
			return nil, fmt.Errorf("round list contains a missing round")
		}
		if latest == nil || round.CreatedAtHeight > latest.CreatedAtHeight {
			latest = round
			tied = []string{hex.EncodeToString(round.VoteRoundId)}
		} else if round.CreatedAtHeight == latest.CreatedAtHeight {
			tied = append(tied, hex.EncodeToString(round.VoteRoundId))
		}
	}
	if latest == nil {
		return nil, fmt.Errorf("no registered voting rounds")
	}
	if len(tied) > 1 {
		sort.Strings(tied)
		return nil, fmt.Errorf("multiple rounds at latest creation height %d; specify a round ID: %s", latest.CreatedAtHeight, strings.Join(tied, ", "))
	}
	return latest, nil
}

func checkRoundIdentity(round *types.VoteRound) (*roundIdentityReport, error) {
	if round == nil || len(round.VoteRoundId) != 32 || round.CreatedAtHeight == 0 || round.SnapshotHeight == 0 {
		return nil, fmt.Errorf("round is missing its identity or heights")
	}
	id, err := roundid.DeriveRoundID(round.CreatedAtHeight, round.SnapshotBlockhash, round.ProposalsHash, round.VoteEndTime, round.NullifierImtRoot, round.NcRoot)
	if err != nil {
		return nil, fmt.Errorf("derive round ID: %w", err)
	}
	return &roundIdentityReport{
		RoundID: hex.EncodeToString(round.VoteRoundId), ComputedRoundID: hex.EncodeToString(id[:]), RoundIDMatches: bytes.Equal(id[:], round.VoteRoundId),
		Title: round.Title, Status: round.Status.String(), CreatedAtHeight: round.CreatedAtHeight,
		SnapshotHeight: round.SnapshotHeight, SnapshotBlockhash: hex.EncodeToString(round.SnapshotBlockhash),
		ProposalsHash: hex.EncodeToString(round.ProposalsHash), VoteEndTime: round.VoteEndTime,
		NullifierIMTRoot: hex.EncodeToString(round.NullifierImtRoot), NCRoot: hex.EncodeToString(round.NcRoot),
	}, nil
}
