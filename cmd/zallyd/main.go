package main

import (
	"fmt"
	"os"

	"github.com/z-cale/zally/app"
	"github.com/z-cale/zally/cmd/zallyd/cmd"
	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp/halo2"

	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
)

func main() {
	// Reject binaries built without real cryptographic verifiers. A binary built
	// with `make install` (no build tags) silently accepts all proofs and
	// signatures via mock verifiers. Always use `make install-ffi` for production.
	if redpallas.IsMock || halo2.IsMock {
		fmt.Fprintln(os.Stderr, "error: zallyd started with mock cryptographic verifiers — "+
			"rebuild with `make install-ffi` (requires -tags halo2,redpallas)")
		os.Exit(1)
	}

	rootCmd := cmd.NewRootCmd()
	if err := svrcmd.Execute(rootCmd, "ZALLY", app.DefaultNodeHome); err != nil {
		fmt.Fprintln(rootCmd.OutOrStderr(), err)
		os.Exit(1)
	}
}
