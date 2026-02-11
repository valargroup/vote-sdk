package main

import (
	"fmt"
	"os"

	"github.com/z-cale/zally/app"
	"github.com/z-cale/zally/cmd/zvoted/cmd"

	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
)

func main() {
	rootCmd := cmd.NewRootCmd()
	if err := svrcmd.Execute(rootCmd, "ZVOTE", app.DefaultNodeHome); err != nil {
		fmt.Fprintln(rootCmd.OutOrStderr(), err)
		os.Exit(1)
	}
}
