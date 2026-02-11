package app

import (
	"io"

	dbm "github.com/cosmos/cosmos-db"

	clienthelpers "cosmossdk.io/client/v2/helpers"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/server"
	"github.com/cosmos/cosmos-sdk/server/api"
	"github.com/cosmos/cosmos-sdk/server/config"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	consensuskeeper "github.com/cosmos/cosmos-sdk/x/consensus/keeper"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	"github.com/cosmos/cosmos-sdk/x/auth/ante"

	voteapi "github.com/z-cale/zally/api"
	"github.com/z-cale/zally/crypto/redpallas"
	"github.com/z-cale/zally/crypto/zkp/halo2"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
)

// DefaultNodeHome is the default home directory for the zallyd daemon.
var DefaultNodeHome string

var (
	_ runtime.AppI            = (*ZallyApp)(nil)
	_ servertypes.Application = (*ZallyApp)(nil)
)

// ZallyApp extends an ABCI application for the Zally chain.
// Built from a stripped-down Cosmos SDK simapp with only the minimal
// modules needed for block production (auth, bank, staking, distribution,
// consensus, genutil).
type ZallyApp struct {
	*runtime.App
	legacyAmino       *codec.LegacyAmino
	appCodec          codec.Codec
	txConfig          client.TxConfig
	interfaceRegistry codectypes.InterfaceRegistry

	// Keepers for the minimal module set.
	AccountKeeper         authkeeper.AccountKeeper
	BankKeeper            bankkeeper.BaseKeeper
	StakingKeeper         *stakingkeeper.Keeper
	DistrKeeper           distrkeeper.Keeper
	ConsensusParamsKeeper consensuskeeper.Keeper

	// Vote module keeper.
	VoteKeeper votekeeper.Keeper
}

func init() {
	var err error
	DefaultNodeHome, err = clienthelpers.GetNodeHomeDirectory(".zallyd")
	if err != nil {
		panic(err)
	}
}

// NewZallyApp returns a reference to an initialized ZallyApp.
func NewZallyApp(
	logger log.Logger,
	db dbm.DB,
	traceStore io.Writer,
	loadLatest bool,
	appOpts servertypes.AppOptions,
	baseAppOptions ...func(*baseapp.BaseApp),
) *ZallyApp {
	var (
		app        = &ZallyApp{}
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
		&app.DistrKeeper,
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

	if err := app.Load(loadLatest); err != nil {
		panic(err)
	}

	return app
}

// setAnteHandler wires up the dual-mode ante handler chain.
//   - Vote transactions (VoteTxWrapper): ZKP/RedPallas validation with infinite gas
//   - Standard Cosmos transactions: standard SDK ante chain (sig verify, fees, etc.)
func (app *ZallyApp) setAnteHandler(txConfig client.TxConfig) {
	anteHandler, err := NewDualAnteHandler(DualAnteHandlerOptions{
		HandlerOptions: ante.HandlerOptions{
			AccountKeeper:   app.AccountKeeper,
			BankKeeper:      app.BankKeeper,
			SignModeHandler: txConfig.SignModeHandler(),
			SigGasConsumer:  ante.DefaultSigVerificationGasConsumer,
		},
		VoteKeeper:  app.VoteKeeper,
		SigVerifier: redpallas.NewVerifier(),
		ZKPVerifier: halo2.NewVerifier(),
	})
	if err != nil {
		panic(err)
	}

	app.SetAnteHandler(anteHandler)
}

// LegacyAmino returns the app's amino codec.
func (app *ZallyApp) LegacyAmino() *codec.LegacyAmino {
	return app.legacyAmino
}

// AppCodec returns the app's codec.
func (app *ZallyApp) AppCodec() codec.Codec {
	return app.appCodec
}

// InterfaceRegistry returns the app's InterfaceRegistry.
func (app *ZallyApp) InterfaceRegistry() codectypes.InterfaceRegistry {
	return app.interfaceRegistry
}

// TxConfig returns the app's TxConfig.
func (app *ZallyApp) TxConfig() client.TxConfig {
	return app.txConfig
}

// GetKey returns the KVStoreKey for the provided store key.
func (app *ZallyApp) GetKey(storeKey string) *storetypes.KVStoreKey {
	sk := app.UnsafeFindStoreKey(storeKey)
	kvStoreKey, ok := sk.(*storetypes.KVStoreKey)
	if !ok {
		return nil
	}
	return kvStoreKey
}

// kvStoreKeys returns all the app's KV store keys.
func (app *ZallyApp) kvStoreKeys() map[string]*storetypes.KVStoreKey {
	keys := make(map[string]*storetypes.KVStoreKey)
	for _, k := range app.GetStoreKeys() {
		if kv, ok := k.(*storetypes.KVStoreKey); ok {
			keys[kv.Name()] = kv
		}
	}
	return keys
}

// LoadHeight loads a particular height.
func (app *ZallyApp) LoadHeight(height int64) error {
	return app.LoadVersion(height)
}

// SimulationManager implements the SimulationApp interface (required by runtime.AppI).
// We don't use simulation, so this returns nil.
func (app *ZallyApp) SimulationManager() *module.SimulationManager {
	return nil
}

// RegisterAPIRoutes registers all application module routes with the provided API server.
func (app *ZallyApp) RegisterAPIRoutes(apiSvr *api.Server, apiConfig config.APIConfig) {
	app.App.RegisterAPIRoutes(apiSvr, apiConfig)

	// Register vote module REST endpoints (tx submission + queries).
	voteHandler := voteapi.NewHandler(voteapi.HandlerConfig{
		CometRPCEndpoint: "http://localhost:26657",
	})
	voteHandler.RegisterTxRoutes(apiSvr.Router)
	voteHandler.RegisterQueryRoutes(apiSvr.Router, apiSvr.ClientCtx)

	// Register swagger API.
	if err := server.RegisterSwaggerAPI(apiSvr.ClientCtx, apiSvr.Router, apiConfig.Swagger); err != nil {
		panic(err)
	}
}
