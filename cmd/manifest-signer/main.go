// Command manifest-signer is the reference Go implementation that produces
// and verifies the off-chain attestations wallets check before encrypting
// ballot amounts to a round's election-authority public key (ea_pk).
//
// See vote-sdk/docs/config.md for the full spec, vote-sdk/docs/runbooks/
// (sign-round-manifest.md, publish-checkpoint.md, key-rotation.md) for the
// operator workflow.
//
// Subcommands:
//
//	manifest-signer keygen          generate a fresh ed25519 keypair
//	manifest-signer sign-round      sign (round_id, ea_pk, valset_hash)
//	manifest-signer sign-checkpoint sign a CometBFT checkpoint
//	manifest-signer verify          verify a signed file (round or checkpoint)
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "manifest-signer",
		Short:         "Sign / verify shielded-vote round manifests and CometBFT checkpoints",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newKeygenCmd(),
		newSignRoundCmd(),
		newSignCheckpointCmd(),
		newVerifyCmd(),
	)
	return root
}
