package app

import (
	"io"
	"os"
	"strings"
	"sync/atomic"

	dbm "github.com/cosmos/cosmos-db"

	clienthelpers "cosmossdk.io/client/v2/helpers"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	consensuskeeper "github.com/cosmos/cosmos-sdk/x/consensus/keeper"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	"github.com/cosmos/cosmos-sdk/x/auth/ante"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/internal/admin"
	"github.com/valargroup/vote-sdk/internal/helper"
	"github.com/valargroup/vote-sdk/internal/ui"
	slashingkeeper "github.com/valargroup/vote-sdk/x/slashing/keeper"
	votekeeper "github.com/valargroup/vote-sdk/x/vote/keeper"
)

// DefaultNodeHome is the default home directory for the svoted daemon.
var DefaultNodeHome string

var (
	_ runtime.AppI            = (*SvoteApp)(nil)
	_ servertypes.Application = (*SvoteApp)(nil)
)

// SvoteApp extends an ABCI application for the Shielded-Vote chain.
// Built from a stripped-down Cosmos SDK simapp with only the minimal
// modules needed for block production (auth, bank, staking, slashing,
// consensus, genutil).
type SvoteApp struct {
	*runtime.App
	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Codec
	txConfig          client.TxConfig
	interfaceRegistry codectypes.InterfaceRegistry

	// Keepers for the minimal module set.
	AccountKeeper         authkeeper.AccountKeeper
	BankKeeper            bankkeeper.BaseKeeper
	StakingKeeper         *stakingkeeper.Keeper
	SlashingKeeper        slashingkeeper.Keeper
	ConsensusParamsKeeper consensuskeeper.Keeper

	// Vote module keeper.
	VoteKeeper *votekeeper.Keeper

	// CometBFT RPC endpoint for the vote API handler (read from app.toml vote.comet_rpc).
	cometRPC string

	// Helper server (set externally by PostSetup, may be nil).
	helperRef atomic.Pointer[helper.Helper]

	// Admin server (set externally by PostSetup, may be nil).
	adminRef atomic.Pointer[admin.Admin]

	// UI dist path (set externally by PostSetup; empty = UI disabled).
	uiDistPath string
}

func init() {
	var err error
	DefaultNodeHome, err = clienthelpers.GetNodeHomeDirectory(".svoted")
	if err != nil {
		panic(err)
	}
}

// NewSvoteApp returns a reference to an initialized SvoteApp.
func NewSvoteApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) *SvoteApp {
	var (
		app        = &SvoteApp{}
		appBuilder *runtime.AppBuilder

		// Merge the AppConfig and runtime configuration.
		appConfig = depinject.Configs(
			AppConfig,
			depinject.Supply(
				appOpts,
				logger,
			),
		)
	)

	if err := depinject.Inject(appConfig,
		&appBuilder,
		&app.appCodec,
		&app.legacyAmino,
		&app.txConfig,
		&app.interfaceRegistry,
		&app.AccountKeeper,
		&app.BankKeeper,
		&app.StakingKeeper,
		&app.SlashingKeeper,
		&app.ConsensusParamsKeeper,
		&app.VoteKeeper,
	); err != nil {
		panic(err)
	}

	app.App = appBuilder.Build(db, traceStore, baseAppOptions...)

	// Install custom TxDecoder that handles both vote wire format
	// ([tag || protobuf_msg]) and standard Cosmos Tx encoding.
	standardDecoder := app.TxConfig().TxDecoder()
	app.SetTxDecoder(CustomTxDecoder(standardDecoder))

	// Register streaming services.
	if err := app.RegisterStreamingServices(appOpts, app.kvStoreKeys()); err != nil {
		panic(err)
	}

	// Set a dual-mode ante handler:
	// - Vote txs (VoteTxWrapper): custom ZKP/RedPallas validation
	// - Standard Cosmos txs: standard SDK ante chain (sig verify, fees)
	app.setAnteHandler(app.txConfig)

	// Read config paths for auto-injection handlers.
	// Expand environment variables (e.g. $HOME) in paths from app.toml.
	eaSkPathRaw, _ := appOpts.Get("vote.ea_sk_path").(string)
	eaSkPath := os.ExpandEnv(eaSkPathRaw)
	pallasSkPathRaw, _ := appOpts.Get("vote.pallas_sk_path").(string)
	pallasSkPath := os.ExpandEnv(pallasSkPathRaw)
	app.cometRPC, _ = appOpts.Get("vote.comet_rpc").(string)
	logger.Info("Auto-injection config",
		"vote.ea_sk_path", eaSkPath,
		"vote.pallas_sk_path", pallasSkPath,
		"vote.comet_rpc", app.cometRPC)

	// Derive ceremony data directory from legacy ea_sk_path.
	ceremonyDir := ceremonyDirFromPath(eaSkPath)

	// Install composed PrepareProposal handler:
	// 1. DKG contribution injection: each validator contributes to Joint-Feldman DKG
	// 2. Ceremony ack injection: auto-ack when ceremony is DEALT (DKG or dealer path)
	// 3. Threshold partial decryption: submit D_i = share * C1 when TALLYING (threshold mode)
	// 4. Tally injection: Lagrange-combine partials (threshold) or decrypt directly (legacy)
	ceremonyDKGHandler := CeremonyDKGContributionPrepareProposalHandler(
		app.VoteKeeper,
		app.StakingKeeper,
		pallasSkPath,
		ceremonyDir,
		logger,
	)
	ceremonyAckHandler := CeremonyAckPrepareProposalHandler(
		app.VoteKeeper,
		app.StakingKeeper,
		pallasSkPath,
		ceremonyDir,
		logger,
	)
	partialDecryptHandler := PartialDecryptPrepareProposalInjector(
		app.VoteKeeper,
		app.StakingKeeper,
		ceremonyDir,
		logger,
	)
	tallyHandler := TallyPrepareProposalHandler(
		app.VoteKeeper,
		app.StakingKeeper,
		ceremonyDir,
		logger,
	)
	app.SetPrepareProposal(ComposedPrepareProposalHandler(
		ceremonyDKGHandler,
		ceremonyAckHandler,
		partialDecryptHandler,
		tallyHandler,
		logger,
	))

	// Install ProcessProposal handler that validates injected ack and tally txs.
	app.SetProcessProposal(ProcessProposalHandler(
		app.VoteKeeper,
		logger,
	))

	if err := app.Load(loadLatest); err != nil {
		panic(err)
	}

	return app
}

// setAnteHandler wires up the dual-mode ante handler chain.
//   - Vote transactions (VoteTxWrapper): ZKP/RedPallas validation with infinite gas
//   - Standard Cosmos transactions: standard SDK ante chain (sig verify, fees, etc.)
func (app *SvoteApp) setAnteHandler(txConfig client.TxConfig) {
	cryptoOpts := ProductionOpts()
	anteHandler, err := NewDualAnteHandler(DualAnteHandlerOptions{
		HandlerOptions: ante.HandlerOptions{
			AccountKeeper:   app.AccountKeeper,
			BankKeeper:      app.BankKeeper,
			SignModeHandler: txConfig.SignModeHandler(),
			SigGasConsumer:  ante.DefaultSigVerificationGasConsumer,
		},
		VoteKeeper:  app.VoteKeeper,
		SigVerifier: cryptoOpts.SigVerifier,
		ZKPVerifier: cryptoOpts.ZKPVerifier,
	})
	if err != nil {
		panic(err)
	}

	app.SetAnteHandler(anteHandler)
}

// LegacyAmino returns the app's amino codec.
func (app *SvoteApp) LegacyAmino() *codec.LegacyAmino {
	return app.legacyAmino
}

// AppCodec returns the app's codec.
func (app *SvoteApp) AppCodec() codec.Codec {
	return app.appCodec
}

// InterfaceRegistry returns the app's InterfaceRegistry.
func (app *SvoteApp) InterfaceRegistry() codectypes.InterfaceRegistry {
	return app.interfaceRegistry
}

// TxConfig returns the app's TxConfig.
func (app *SvoteApp) TxConfig() client.TxConfig {
	return app.txConfig
}

// GetKey returns the KVStoreKey for the provided store key.
func (app *SvoteApp) GetKey(storeKey string) *storetypes.KVStoreKey {
	sk := app.UnsafeFindStoreKey(storeKey)
	kvStoreKey, ok := sk.(*storetypes.KVStoreKey)
	if !ok {
		return nil
	}
	return kvStoreKey
}

// kvStoreKeys returns all the app's KV store keys.
func (app *SvoteApp) kvStoreKeys() map[string]*storetypes.KVStoreKey {
	keys := make(map[string]*storetypes.KVStoreKey)
	for _, k := range app.GetStoreKeys() {
		if kv, ok := k.(*storetypes.KVStoreKey); ok {
			keys[kv.Name()] = kv
		}
	}
	return keys
}

// LoadHeight loads a particular height.
func (app *SvoteApp) LoadHeight(height int64) error {
	return app.LoadVersion(height)
}

// ValidatorValoperBonded returns true if the staking module reports the given
// valoper bech32 address as bonded.
func (app *SvoteApp) ValidatorValoperBonded(valoper string) bool {
	valAddr, err := sdk.ValAddressFromBech32(valoper)
	if err != nil {
		return false
	}
	ctx := app.NewUncachedContext(false, cmtproto.Header{Height: app.LastBlockHeight()})
	val, err := app.StakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return false
	}
	return val.IsBonded()
}

// SimulationManager implements the SimulationApp interface (required by runtime.AppI).
// We don't use simulation, so this returns nil.
func (app *SvoteApp) SimulationManager() *module.SimulationManager {
	return nil
}

// RegisterAPIRoutes registers all application module routes with the provided API server.
func (app *SvoteApp) RegisterAPIRoutes(apiSvr *api.Server, apiConfig config.APIConfig) {
	app.App.RegisterAPIRoutes(apiSvr, apiConfig)

	// Register vote module REST endpoints (tx submission + queries).
	// Use the CometBFT RPC address from app.toml [vote] section so it
	// works regardless of port offsets (e.g. multi-validator local setups).
	cometRPC := app.cometRPC
	if cometRPC == "" {
		cometRPC = "http://localhost:26657"
	} else if strings.HasPrefix(cometRPC, "tcp://") {
		cometRPC = "http://" + strings.TrimPrefix(cometRPC, "tcp://")
	}
	voteHandler := voteapi.NewHandler(voteapi.HandlerConfig{
		CometRPCEndpoint: cometRPC,
		Snapshot: voteapi.SnapshotConfig{
			PIRServiceURL:    os.Getenv("SVOTE_PIR_URL"),
			LightwalletdURLs: voteapi.ParseLightwalletdURLs(os.Getenv("SVOTE_LWD_URLS")),
		},
	})
	voteHandler.RegisterTxRoutes(apiSvr.Router)
	voteHandler.RegisterQueryRoutes(apiSvr.Router, apiSvr.ClientCtx)

	// Register helper routes unconditionally; handler resolves the backing store
	// at request time, so routes are mounted even before PostSetup initializes
	// the helper runtime.
	helper.RegisterRoutesWithGetters(apiSvr.Router, func() *helper.ShareStore {
		h := app.GetHelper()
		if h == nil {
			return nil
		}
		return h.Store
	}, func() string {
		h := app.GetHelper()
		if h == nil {
			return ""
		}
		return h.APIToken
	}, func() bool {
		h := app.GetHelper()
		if h == nil {
			return false
		}
		return h.ExposeQueueStatus
	}, func() helper.TreeReader {
		h := app.GetHelper()
		if h == nil {
			return nil
		}
		return h.Tree()
	}, func() helper.VCHashFunc {
		h := app.GetHelper()
		if h == nil {
			return nil
		}
		return h.VCHash
	}, func() helper.ShareNullifierChecker {
		h := app.GetHelper()
		if h == nil {
			return nil
		}
		return h.ShareNullifierChecker
	}, app.Logger().With("module", "helper"))

	// Register admin server routes (voting-config proxy from GitHub Pages CDN).
	admin.RegisterRoutes(apiSvr.Router, func() *admin.Admin {
		return app.GetAdmin()
	}, app.Logger().With("module", "admin"))

	// Register UI runtime config (resolves SVOTE_UI_MODE; default "prod" hides
	// developer-only widgets so an unset env var can never leak them in prod).
	admin.RegisterUIConfigRoutes(apiSvr.Router, app.Logger().With("module", "admin"))

	// Register swagger API.
	if err := server.RegisterSwaggerAPI(apiSvr.ClientCtx, apiSvr.Router, apiConfig.Swagger); err != nil {
		panic(err)
	}

	// Register UI static file server (must be last — catch-all PathPrefix("/")).
	// Uses a getter so routes work even though PostSetup sets the dist path
	// after RegisterAPIRoutes runs.
	ui.RegisterRoutes(apiSvr.Router, func() string {
		return app.uiDistPath
	}, app.Logger().With("module", "ui"))
}

// SetHelper publishes the helper instance for concurrent readers.
func (app *SvoteApp) SetHelper(h *helper.Helper) {
	app.helperRef.Store(h)
}

// GetHelper returns the currently published helper instance.
func (app *SvoteApp) GetHelper() *helper.Helper {
	return app.helperRef.Load()
}

// SetAdmin publishes the admin instance for concurrent readers.
func (app *SvoteApp) SetAdmin(a *admin.Admin) {
	app.adminRef.Store(a)
}

// GetAdmin returns the currently published admin instance.
func (app *SvoteApp) GetAdmin() *admin.Admin {
	return app.adminRef.Load()
}

// SetUIDistPath sets the path to the built UI dist directory.
func (app *SvoteApp) SetUIDistPath(path string) {
	app.uiDistPath = path
}
