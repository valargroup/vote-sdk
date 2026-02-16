package vote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	"cosmossdk.io/x/tx/signing"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"

	"github.com/z-cale/zally/x/vote/keeper"
	modulev1 "github.com/z-cale/zally/x/vote/module/v1"
	"github.com/z-cale/zally/x/vote/types"
)

// Compile-time interface assertions.
var (
	_ appmodule.AppModule     = AppModule{}
	_ appmodule.HasEndBlocker = AppModule{}
	_ module.HasName          = AppModule{}
	_ module.HasServices      = AppModule{}
)

// ----------------------------------------------------------------------------
// Depinject registration
// ----------------------------------------------------------------------------

func init() {
	appmodule.Register(
		&modulev1.Module{},
		appmodule.Provide(
			ProvideModule,
			ProvideCreateVotingSessionSigner,
			ProvideDelegateVoteSigner,
			ProvideCastVoteSigner,
			ProvideRevealShareSigner,
			ProvideSubmitTallySigner,
		),
	)
}

// ---------------------------------------------------------------------------
// Custom signers for vote messages
// ---------------------------------------------------------------------------
//
// Vote transactions bypass the Cosmos SDK Tx envelope and use ZKP/RedPallas
// authentication instead of standard Cosmos signatures. The SDK's
// InterfaceRegistry requires every Msg service message to have either a
// cosmos.msg.v1.signer protobuf option or a custom GetSigners function.
//
// We satisfy this by providing no-op signers via depinject. Each function
// returns a signing.CustomGetSigner (a ManyPerContainerType), which the
// runtime collects into []signing.CustomGetSigner for ProvideInterfaceRegistry.

// noopSignerFn is a GetSignersFunc that returns nil — vote messages have no
// standard Cosmos signers.
func noopSignerFn(proto.Message) ([][]byte, error) { return nil, nil }

func ProvideCreateVotingSessionSigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgCreateVotingSession"),
		Fn:      noopSignerFn,
	}
}

func ProvideDelegateVoteSigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgDelegateVote"),
		Fn:      noopSignerFn,
	}
}

func ProvideCastVoteSigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgCastVote"),
		Fn:      noopSignerFn,
	}
}

func ProvideRevealShareSigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgRevealShare"),
		Fn:      noopSignerFn,
	}
}

func ProvideSubmitTallySigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgSubmitTally"),
		Fn:      noopSignerFn,
	}
}

// ModuleInputs defines the inputs needed to create the vote module.
type ModuleInputs struct {
	depinject.In

	StoreService  store.KVStoreService
	Cdc           codec.Codec
	Logger        log.Logger
	Config        *modulev1.Module
	StakingKeeper *stakingkeeper.Keeper
}

// ModuleOutputs defines the outputs produced by the vote module.
type ModuleOutputs struct {
	depinject.Out

	Module appmodule.AppModule
	Keeper keeper.Keeper
}

// ProvideModule is called by depinject to construct the vote module and keeper.
func ProvideModule(in ModuleInputs) ModuleOutputs {
	k := keeper.NewKeeper(
		in.StoreService,
		in.Config.Authority,
		in.Logger,
		in.StakingKeeper,
	)

	m := NewAppModule(k, in.Cdc)

	return ModuleOutputs{
		Module: m,
		Keeper: k,
	}
}

// ----------------------------------------------------------------------------
// AppModule implementation
// ----------------------------------------------------------------------------

// AppModule implements the Cosmos SDK AppModule interface for the vote module.
type AppModule struct {
	keeper keeper.Keeper
	cdc    codec.Codec
}

// NewAppModule creates a new AppModule.
func NewAppModule(keeper keeper.Keeper, cdc codec.Codec) AppModule {
	return AppModule{keeper: keeper, cdc: cdc}
}

// IsOnePerModuleType implements depinject.OnePerModuleType.
func (AppModule) IsOnePerModuleType() {}

// IsAppModule implements appmodule.AppModule.
func (AppModule) IsAppModule() {}

// Name returns the module name.
func (AppModule) Name() string {
	return types.ModuleName
}

// RegisterInterfaces registers the vote module's message types with the
// InterfaceRegistry, required for MsgServiceRouter to accept vote messages.
func (AppModule) RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	types.RegisterInterfaces(registry)
}

// RegisterServices registers the module's gRPC services with the app.
//
// Both QueryServer and MsgServer are registered. Although vote transactions
// bypass the Cosmos SDK Tx envelope (using a raw [tag || protobuf] wire format),
// the MsgServer is registered so BaseApp's MsgServiceRouter can route vote
// messages to the keeper after the custom AnteHandler validates them.
//
// The cosmos.msg.v1.signer annotation is only used by the standard SDK
// SigVerificationDecorator (which we replace with custom ZKP/RedPallas
// validation in the AnteHandler). BaseApp's runMsgs() simply looks up
// handlers by message type URL — no signer checking occurs during execution.
func (am AppModule) RegisterServices(cfg module.Configurator) {
	types.RegisterQueryServer(cfg.QueryServer(), keeper.NewQueryServerImpl(am.keeper))
	types.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.keeper))
}

// EndBlock computes the commitment tree root and transitions expired ACTIVE
// rounds to TALLYING.
func (am AppModule) EndBlock(goCtx context.Context) error {
	ctx := sdk.UnwrapSDKContext(goCtx)
	kvStore := am.keeper.OpenKVStore(ctx)

	// --- 1. Commitment tree root computation ---
	state, err := am.keeper.GetCommitmentTreeState(kvStore)
	if err != nil {
		return err
	}

	if state.NextIndex > 0 {
		root, err := am.keeper.ComputeTreeRoot(kvStore, state.NextIndex)
		if err != nil {
			return err
		}

		// Only write a new root when the tree has changed (new leaves appended).
		if !bytes.Equal(root, state.Root) {
			blockHeight := uint64(ctx.BlockHeight())

			if err := am.keeper.SetCommitmentRootAtHeight(kvStore, blockHeight, root); err != nil {
				return err
			}

			// Record the block-to-leaf-index mapping for the CommitmentLeaves query.
			// New leaves this block span [NextIndexAtRoot, NextIndex).
			leafStart := state.NextIndexAtRoot
			leafCount := state.NextIndex - leafStart
			if leafCount > 0 {
				if err := am.keeper.SetBlockLeafIndex(kvStore, blockHeight, leafStart, leafCount); err != nil {
					return err
				}
			}

			state.Root = root
			state.Height = blockHeight
			state.NextIndexAtRoot = state.NextIndex
			if err := am.keeper.SetCommitmentTreeState(kvStore, state); err != nil {
				return err
			}

			ctx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventTypeCommitmentTreeRoot,
				sdk.NewAttribute(types.AttributeKeyTreeRoot, fmt.Sprintf("%x", root)),
				sdk.NewAttribute(types.AttributeKeyBlockHeight, strconv.FormatUint(blockHeight, 10)),
			))
		}
	}

	// --- 2. Transition expired ACTIVE rounds to TALLYING ---
	blockTime := uint64(ctx.BlockTime().Unix())

	// Collect round IDs to transition (avoid mutating store during iteration).
	var expiredRoundIDs [][]byte
	if err := am.keeper.IterateActiveRounds(kvStore, func(round *types.VoteRound) bool {
		if blockTime >= round.VoteEndTime {
			// Copy the round ID since the iterator value may be reused.
			id := make([]byte, len(round.VoteRoundId))
			copy(id, round.VoteRoundId)
			expiredRoundIDs = append(expiredRoundIDs, id)
		}
		return false // continue iterating
	}); err != nil {
		return err
	}

	for _, roundID := range expiredRoundIDs {
		if err := am.keeper.UpdateVoteRoundStatus(kvStore, roundID, types.SessionStatus_SESSION_STATUS_TALLYING); err != nil {
			return err
		}

		ctx.EventManager().EmitEvent(sdk.NewEvent(
			types.EventTypeRoundStatusChange,
			sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", roundID)),
			sdk.NewAttribute(types.AttributeKeyOldStatus, types.SessionStatus_SESSION_STATUS_ACTIVE.String()),
			sdk.NewAttribute(types.AttributeKeyNewStatus, types.SessionStatus_SESSION_STATUS_TALLYING.String()),
		))
	}

	return nil
}

// DefaultGenesis returns the default genesis state as raw JSON bytes.
func (am AppModule) DefaultGenesis(_ codec.JSONCodec) json.RawMessage {
	return json.RawMessage(`{}`)
}

// ValidateGenesis performs genesis state validation.
func (am AppModule) ValidateGenesis(_ codec.JSONCodec, _ client.TxEncodingConfig, _ json.RawMessage) error {
	return nil
}

// InitGenesis initializes the module state from genesis.
func (am AppModule) InitGenesis(_ context.Context, _ codec.JSONCodec, _ json.RawMessage) {
	// No-op for Phase 2. Vote module has no initial genesis state.
}

// ExportGenesis exports the module state as genesis.
func (am AppModule) ExportGenesis(_ context.Context, _ codec.JSONCodec) json.RawMessage {
	return json.RawMessage(`{}`)
}
