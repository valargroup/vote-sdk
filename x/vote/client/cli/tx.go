package cli

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/client/tx"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// GetTxCmd returns the transaction commands for the vote module grouped under
// "svoted tx vote".
func GetTxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        types.ModuleName,
		Short:                      "Vote module transaction subcommands",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(
		// Ceremony commands — require standard Cosmos SDK signing by a validator.
		CmdRegisterPallasKey(),
		CmdRotatePallasKey(),
		CmdCreateValidatorWithPallasKey(),
		// Admin commands — signed by any current vote manager (any-of-N).
		CmdUpdateVoteManagers(),
		CmdCreateVotingSession(),
		CmdSubmitTally(),
		// Token transfer — uses whitelisted MsgAuthorizedSend.
		CmdAuthorizedSend(),
	)

	return cmd
}

// CmdRegisterPallasKey broadcasts MsgRegisterPallasKey.
// Called by each validator to register their Pallas public key before the
// EA key deal step.
func CmdRegisterPallasKey() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register-pallas-key",
		Short: "Register a Pallas public key for the EA key ceremony",
		Long: `Register the node's pre-generated Pallas public key to participate in
the Election Authority key ceremony.

The public key is read from <home>/pallas.pk (written by 'svoted pallas-keygen').
The --from key must correspond to a bonded validator. The same address is
used as the ceremony creator field.

Example:
  svoted tx vote register-pallas-key --from myvalidator --chain-id svote-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			pkPath := filepath.Join(clientCtx.HomeDir, "pallas.pk")
			pallasPk, err := os.ReadFile(pkPath)
			if err != nil {
				return fmt.Errorf("reading pallas.pk from %s: %w", pkPath, err)
			}

			msg := &types.MsgRegisterPallasKey{
				Creator:  clientCtx.GetFromAddress().String(),
				PallasPk: pallasPk,
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// CmdRotatePallasKey broadcasts MsgRotatePallasKey.
// Replaces the validator's registered Pallas public key with a new one.
func CmdRotatePallasKey() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rotate-pallas-key",
		Short: "Replace the registered Pallas public key with a new one",
		Long: `Rotate the node's Pallas public key in the global ceremony registry.

The new public key is read from <home>/pallas.pk (written by 'svoted pallas-keygen').
The validator must already have a registered key and must not be participating
in any in-flight ceremony (PENDING round).

Example:
  svoted tx vote rotate-pallas-key --from myvalidator --chain-id svote-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			pkPath := filepath.Join(clientCtx.HomeDir, "pallas.pk")
			pallasPk, err := os.ReadFile(pkPath)
			if err != nil {
				return fmt.Errorf("reading pallas.pk from %s: %w", pkPath, err)
			}

			msg := &types.MsgRotatePallasKey{
				Creator:     clientCtx.GetFromAddress().String(),
				NewPallasPk: pallasPk,
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// CmdCreateValidatorWithPallasKey broadcasts MsgCreateValidatorWithPallasKey.
// Atomically creates a new validator and registers its Pallas key in the
// ceremony state, replacing the two-step MsgCreateValidator + MsgRegisterPallasKey
// flow for post-genesis validators.
func CmdCreateValidatorWithPallasKey() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-validator-with-pallas-key [staking-msg-json-file]",
		Short: "Create a validator and register its Pallas key atomically",
		Long: `Broadcast an MsgCreateValidatorWithPallasKey transaction.

Arguments:
  staking-msg-json-file Path to a JSON file containing a
                        cosmos.staking.v1beta1.MsgCreateValidator payload
                        (same JSON shape as 'svoted tx staking create-validator
                        --generate-only' produces).

The Pallas public key is read from <home>/pallas.pk (written by 'svoted pallas-keygen').
The staking JSON is re-encoded to protobuf binary and embedded in the transaction;
the chain atomically calls the staking module's CreateValidator and then registers
the Pallas key.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			pkPath := filepath.Join(clientCtx.HomeDir, "pallas.pk")
			pallasPk, err := os.ReadFile(pkPath)
			if err != nil {
				return fmt.Errorf("reading pallas.pk from %s: %w", pkPath, err)
			}

			jsonData, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading staking-msg file: %w", err)
			}

			// Unmarshal JSON → MsgCreateValidator, then re-encode to protobuf binary.
			stakingMsg := &stakingtypes.MsgCreateValidator{}
			if err := clientCtx.Codec.UnmarshalJSON(jsonData, stakingMsg); err != nil {
				return fmt.Errorf("parsing staking msg JSON: %w", err)
			}
			stakingMsgBytes, err := stakingMsg.Marshal()
			if err != nil {
				return fmt.Errorf("encoding staking msg: %w", err)
			}

			msg := &types.MsgCreateValidatorWithPallasKey{
				StakingMsg: stakingMsgBytes,
				PallasPk:   pallasPk,
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// CmdUpdateVoteManagers broadcasts MsgUpdateVoteManagers.
// Atomically replaces the vote-manager set with the given addresses. Callable by any
// current vote manager (any-of-N).
func CmdUpdateVoteManagers() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update-vote-managers --vote-manager <addr> [--vote-manager <addr> ...]",
		Short: "Atomically replace the vote-manager set",
		Long: `Broadcast an MsgUpdateVoteManagers transaction.

Flags:
  --vote-manager  Repeatable. Bech32 account address (sv1...) of a vote manager
           in the new set. Pass the flag once per vote manager. The full
           list replaces the existing set atomically.

The --from signer must be a current vote manager. Balances are not moved — each vote manager
holds their own funds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			newVoteManagers, err := cmd.Flags().GetStringArray("vote-manager")
			if err != nil {
				return err
			}
			if len(newVoteManagers) == 0 {
				return fmt.Errorf("at least one --vote-manager flag is required")
			}

			msg := &types.MsgUpdateVoteManagers{
				Creator:         clientCtx.GetFromAddress().String(),
				NewVoteManagers: newVoteManagers,
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	cmd.Flags().StringArray("vote-manager", nil, "Vote-manager bech32 address (repeatable; all specified addresses form the new set)")
	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// CmdAuthorizedSend broadcasts MsgAuthorizedSend.
// Transfers tokens using the whitelisted MsgAuthorizedSend instead of the
// blocked bank MsgSend.
func CmdAuthorizedSend() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "authorized-send [to-address] [amount] [denom]",
		Short: "Send tokens via MsgAuthorizedSend (whitelisted transfer)",
		Long: `Broadcast an MsgAuthorizedSend transaction.

Arguments:
  to-address  Recipient bech32 account address (sv1...)
  amount      Integer amount to send (e.g. 200000)
  denom       Token denomination (e.g. usvote)

The --from flag specifies the sender. Unlike 'bank send', this message
is whitelisted by the chain's MessageWhitelistDecorator.

Example:
  svoted tx vote authorized-send sv1abc... 200000 usvote --from mykey`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			msg := &types.MsgAuthorizedSend{
				FromAddress: clientCtx.GetFromAddress().String(),
				ToAddress:   args[0],
				Amount:      args[1],
				Denom:       args[2],
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// CmdCreateVotingSession broadcasts MsgCreateVotingSession.
// Callable by any current vote manager. Accepts a JSON file because the message
// carries a structured proposal list and large binary blobs.
func CmdCreateVotingSession() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-voting-session [msg-json-file]",
		Short: "Create a new voting session (vote-manager only)",
		Long: `Broadcast an MsgCreateVotingSession from a JSON description file.

All byte fields are hex-encoded in the JSON.  Required fields:

  snapshot_height     (uint64) — Block height of the ZSA/nullifier snapshot
  snapshot_blockhash  (hex)    — 32-byte block hash at snapshot_height
  proposals_hash      (hex)    — SHA-256 of the canonical proposals list
  vote_end_time       (int64)  — Unix timestamp after which voting closes
  nullifier_imt_root  (hex)    — Root of the incremental Merkle tree of nullifiers
  nc_root             (hex)    — Note commitment tree root at snapshot_height
  proposals           (array)  — 1-15 proposals, each with id (1-based uint32),
                                 title (string), and options (2-8 elements with
                                 index (0-based uint32) and label (ASCII string))

Example:
  {
    "snapshot_height": 1000,
    "snapshot_blockhash": "aabb...",
    "proposals_hash": "ccdd...",
    "vote_end_time": 1893456000,
    "nullifier_imt_root": "eeff...",
    "nc_root": "0011...",
    "proposals": [
      {
        "id": 1,
        "title": "Upgrade proposal",
        "options": [
          {"index": 0, "label": "Yes"},
          {"index": 1, "label": "No"},
          {"index": 2, "label": "Abstain"}
        ]
      }
    ]
  }`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading msg file: %w", err)
			}

			var input struct {
				SnapshotHeight    uint64 `json:"snapshot_height"`
				SnapshotBlockhash string `json:"snapshot_blockhash"`
				ProposalsHash     string `json:"proposals_hash"`
				VoteEndTime       uint64 `json:"vote_end_time"`
				NullifierImtRoot  string `json:"nullifier_imt_root"`
				NcRoot            string `json:"nc_root"`
				Description       string `json:"description"`
				Title             string `json:"title"`
				Proposals         []struct {
					Id      uint32 `json:"id"`
					Title   string `json:"title"`
					Options []struct {
						Index uint32 `json:"index"`
						Label string `json:"label"`
					} `json:"options"`
				} `json:"proposals"`
			}
			if err := json.Unmarshal(data, &input); err != nil {
				return fmt.Errorf("parsing msg JSON: %w", err)
			}

			decodeHex := func(field, s string) ([]byte, error) {
				b, err := hex.DecodeString(s)
				if err != nil {
					return nil, fmt.Errorf("field %q: invalid hex: %w", field, err)
				}
				return b, nil
			}

			snapshotBlockhash, err := decodeHex(types.SessionKeyBlockhash, input.SnapshotBlockhash)
			if err != nil {
				return err
			}
			proposalsHash, err := decodeHex(types.SessionKeyProposalsHash, input.ProposalsHash)
			if err != nil {
				return err
			}
			nullifierImtRoot, err := decodeHex(types.SessionKeyNullifierImtRoot, input.NullifierImtRoot)
			if err != nil {
				return err
			}
			ncRoot, err := decodeHex(types.SessionKeyNcRoot, input.NcRoot)
			if err != nil {
				return err
			}

			proposals := make([]*types.Proposal, len(input.Proposals))
			for i, p := range input.Proposals {
				opts := make([]*types.VoteOption, len(p.Options))
				for j, o := range p.Options {
					opts[j] = &types.VoteOption{
						Index: o.Index,
						Label: o.Label,
					}
				}
				proposals[i] = &types.Proposal{
					Id:      p.Id,
					Title:   p.Title,
					Options: opts,
				}
			}

			msg := &types.MsgCreateVotingSession{
				Creator:           clientCtx.GetFromAddress().String(),
				SnapshotHeight:    input.SnapshotHeight,
				SnapshotBlockhash: snapshotBlockhash,
				ProposalsHash:     proposalsHash,
				VoteEndTime:       input.VoteEndTime,
				NullifierImtRoot:  nullifierImtRoot,
				NcRoot:            ncRoot,
				Proposals:         proposals,
				Description:       input.Description,
				Title:             input.Title,
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}

// CmdSubmitTally broadcasts MsgSubmitTally.
// Called by a vote manager after off-chain tally computation.
func CmdSubmitTally() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "submit-tally [vote-round-id-hex] [entries-json-file]",
		Short: "Submit finalized tally results for a vote round (vote-manager only)",
		Long: `Broadcast an MsgSubmitTally transaction.

Arguments:
  vote-round-id-hex  32-byte vote round identifier, hex-encoded
  entries-json-file  Path to a JSON file with an array of TallyEntry objects.
                     Each element must have:
                       "proposal_id"    (uint32) — 1-based proposal ID
                       "vote_decision"  (uint32) — option index being tallied
                       "total_value"    (uint64) — decrypted aggregate (zatoshi)

Example entries.json:
  [
    {"proposal_id": 1, "vote_decision": 0, "total_value": 150000000},
    {"proposal_id": 1, "vote_decision": 1, "total_value":  50000000}
  ]`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			roundID, err := hex.DecodeString(args[0])
			if err != nil {
				return fmt.Errorf("invalid vote-round-id-hex: %w", err)
			}

			data, err := os.ReadFile(args[1])
			if err != nil {
				return fmt.Errorf("reading entries file: %w", err)
			}

			var rawEntries []struct {
				ProposalId   uint32 `json:"proposal_id"`
				VoteDecision uint32 `json:"vote_decision"`
				TotalValue   uint64 `json:"total_value"`
			}
			if err := json.Unmarshal(data, &rawEntries); err != nil {
				return fmt.Errorf("parsing entries JSON: %w", err)
			}

			entries := make([]*types.TallyEntry, len(rawEntries))
			for i, r := range rawEntries {
				entries[i] = &types.TallyEntry{
					ProposalId:   r.ProposalId,
					VoteDecision: r.VoteDecision,
					TotalValue:   r.TotalValue,
				}
			}

			msg := &types.MsgSubmitTally{
				Creator:     clientCtx.GetFromAddress().String(),
				VoteRoundId: roundID,
				Entries:     entries,
			}

			return tx.GenerateOrBroadcastTxCLI(clientCtx, cmd.Flags(), msg)
		},
	}

	flags.AddTxFlagsToCmd(cmd)
	return cmd
}
