package cmd

import (
	"context"
	"fmt"

	"cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"

	"github.com/z-cale/zally/app"
	"github.com/z-cale/zally/crypto/votetree"
	"github.com/z-cale/zally/internal/helper"
)

// addHelperFlags registers helper server CLI flags on the start command.
func addHelperFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("no-helper", false, "Disable the helper server")
	cmd.Flags().String("helper-db-path", "", "Path to the helper SQLite database (default: $HOME/.zallyd/helper.db)")
}

// helperPostSetup starts the helper server background processor.
// It captures the ZallyApp reference via the closure in initRootCmd.
func helperPostSetup(
	zallyApp **app.ZallyApp,
) func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
	return func(svrCtx *server.Context, clientCtx client.Context, ctx context.Context, g *errgroup.Group) error {
		if *zallyApp == nil {
			return fmt.Errorf("helper: app not initialized")
		}

		logger := svrCtx.Logger.With("module", "helper")

		// Read config.
		cfg := readHelperConfig(svrCtx.Viper, logger)

		// Check CLI flag override.
		if v, _ := svrCtx.Viper.Get("no-helper").(bool); v {
			cfg.Disable = true
		}
		if cfg.Disable {
			logger.Info("helper server disabled")
			return nil
		}
		if !helperHalo2Available() {
			logger.Info("helper server disabled: binary built without halo2 support")
			return nil
		}

		// CLI flag override for DB path.
		if dbPath := svrCtx.Viper.GetString("helper-db-path"); dbPath != "" {
			cfg.DBPath = dbPath
		}

		// Create the tree accessor that reads directly from the keeper's KV store.
		treeReader := &keeperTreeReader{
			app:    *zallyApp,
			logger: logger,
		}

		// Use the default (stub) proof generator — the real FFI prover is
		// connected via build tag. See prove.go / prove_default.go.
		prover := &halo2Prover{}

		homeDir := svrCtx.Config.RootDir
		h, err := helper.New(cfg, treeReader, prover, homeDir, logger)
		if err != nil {
			return fmt.Errorf("helper: %w", err)
		}
		if h == nil {
			return nil // disabled
		}

		// Set helper on the app so RegisterAPIRoutes can register HTTP routes.
		(*zallyApp).SetHelper(h)

		// Start the background processor in the errgroup.
		// Close the store after the processor exits to avoid
		// "database is closed" errors from concurrent queries.
		g.Go(func() error {
			err := h.Start(ctx)
			h.Close()
			return err
		})

		logger.Info("helper server started")
		return nil
	}
}

// readHelperConfig reads the [helper] section from app.toml via viper.
func readHelperConfig(v *viper.Viper, logger log.Logger) helper.Config {
	cfg := helper.DefaultConfig()

	if v.IsSet("helper.disable") {
		cfg.Disable = v.GetBool("helper.disable")
	}
	if v.IsSet("helper.api_token") {
		cfg.APIToken = v.GetString("helper.api_token")
	}
	if v.IsSet("helper.db_path") {
		cfg.DBPath = v.GetString("helper.db_path")
	}
	if v.IsSet("helper.mean_delay") {
		cfg.MeanDelay = v.GetInt("helper.mean_delay")
	}
	if v.IsSet("helper.process_interval") {
		cfg.ProcessInterval = v.GetInt("helper.process_interval")
	}
	if v.IsSet("helper.chain_api_port") {
		cfg.ChainAPIPort = v.GetInt("helper.chain_api_port")
	}
	if v.IsSet("helper.max_concurrent_proofs") {
		cfg.MaxConcurrentProofs = v.GetInt("helper.max_concurrent_proofs")
	}

	return cfg
}

// keeperTreeReader implements helper.TreeReader by reading directly from the
// vote keeper's KV store.
type keeperTreeReader struct {
	app    *app.ZallyApp
	logger log.Logger
}

// GetTreeStatus returns lightweight tree statistics without reading leaf data.
func (r *keeperTreeReader) GetTreeStatus() (helper.TreeStatus, error) {
	ctx := r.app.NewUncachedContext(false, cmtproto.Header{})
	kvStore := r.app.VoteKeeper.OpenKVStore(ctx)

	treeState, err := r.app.VoteKeeper.GetCommitmentTreeState(kvStore)
	if err != nil {
		return helper.TreeStatus{}, fmt.Errorf("get tree state: %w", err)
	}

	var anchorHeight uint64
	latestHeight := uint64(r.app.LastBlockHeight())
	for h := latestHeight; h > 0; h-- {
		root, err := r.app.VoteKeeper.GetCommitmentRootAtHeight(kvStore, h)
		if err != nil {
			continue
		}
		if root != nil {
			anchorHeight = h
			break
		}
	}

	return helper.TreeStatus{
		LeafCount:    treeState.NextIndex,
		AnchorHeight: anchorHeight,
	}, nil
}

// MerklePath returns the 772-byte Poseidon Merkle authentication path for the
// leaf at position, anchored to the checkpoint at anchorHeight.
//
// Instead of loading all leaves and rebuilding an ephemeral tree (O(n)), this
// opens a fresh KV-backed TreeHandle that reads only the frontier shard, cap,
// and checkpoints (O(1)), then calls Path() which traverses O(depth) shards.
// The handle is created from a snapshot KV store separate from the EndBlocker's
// kvProxy, so there is no concurrency conflict.
func (r *keeperTreeReader) MerklePath(position uint64, anchorHeight uint32) ([]byte, error) {
	ctx := r.app.NewUncachedContext(false, cmtproto.Header{})
	kvStore := r.app.VoteKeeper.OpenKVStore(ctx)

	treeState, err := r.app.VoteKeeper.GetCommitmentTreeState(kvStore)
	if err != nil {
		return nil, fmt.Errorf("get tree state: %w", err)
	}

	// Create a KV-backed handle from the snapshot store. ShardTree reads only
	// the frontier shard + cap + checkpoints on creation — no leaf replay.
	proxy := &votetree.KvStoreProxy{Current: kvStore}
	h, err := votetree.NewTreeHandleWithKV(proxy, treeState.NextIndex)
	if err != nil {
		return nil, fmt.Errorf("create kv tree handle: %w", err)
	}
	defer h.Close()

	return h.Path(position, anchorHeight)
}

// halo2Prover wraps the CGo proof generation function.
type halo2Prover struct{}

func (p *halo2Prover) GenerateShareRevealProof(
	merklePath []byte,
	shareComms [16][32]byte,
	primaryBlind [32]byte,
	encC1X [32]byte,
	encC2X [32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	return halo2GenerateShareRevealProof(
		merklePath, shareComms, primaryBlind, encC1X, encC2X,
		shareIndex, proposalID, voteDecision, roundID,
	)
}
