package cmd

import (
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/client/debug"
	"github.com/spf13/cobra"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	"github.com/valargroup/vote-sdk/internal/delegationreplay"
)

func delegationDebugCmd() *cobra.Command {
	cmd := debug.Cmd()
	cmd.AddCommand(replayDelegationCmd())
	return cmd
}

func replayDelegationCmd() *cobra.Command {
	var compare bool
	cmd := &cobra.Command{
		Use:   "replay-delegation <bundle.json>",
		Short: "Replay a captured delegation rejection offline with the real verifier",
		Args:  cobra.ExactArgs(1),
		// Skip root config interception: replay requires no node home or network.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			if halo2.IsMock {
				return errors.New("replay requires a binary built with -tags halo2; mock verification is not supported")
			}
			bundle, err := delegationreplay.OpenBundle(args[0])
			if err != nil {
				return err
			}
			identity, err := delegationreplay.ExecutableIdentity()
			if err != nil {
				return err
			}
			result, err := delegationreplay.Replay(bundle, identity, halo2.NewVerifier(), compare)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), result)
			if err != nil {
				return err
			}
			if result == "verified unexpectedly" || result == "comparison: verified unexpectedly" {
				return errors.New("captured rejection did not reproduce")
			}
			if result == "rejected with a different error" || result == "comparison: rejected with a different error" {
				return errors.New("verification error differs from the captured rejection")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&compare, "compare", false, "Explicitly compare a different executable; output is not an exact replay")
	return cmd
}
