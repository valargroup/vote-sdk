package vote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	autocliv1 "cosmossdk.io/api/cosmos/autocli/v1"
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
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

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
			ProvideRegisterPallasKeySigner,
			ProvideDealExecutiveAuthorityKeySigner,
			ProvideAckExecutiveAuthorityKeySigner,
			ProvideCreateValidatorWithPallasKeySigner,
			ProvideSetVoteManagerSigner,
		),
	)
}

// ---------------------------------------------------------------------------
// Custom signers for vote and ceremony messages
// ---------------------------------------------------------------------------
//
// The SDK's InterfaceRegistry requires every Msg service message to have
// either a cosmos.msg.v1.signer protobuf option or a custom GetSigners
// function.
//
// Vote-round messages (0x01–0x05) bypass the Cosmos SDK Tx envelope and use
// ZKP/RedPallas authentication, so they use no-op signers.
//
// Ceremony messages (except MsgAckExecutiveAuthorityKey) are standard Cosmos
// SDK transactions and use real signers derived from their creator field.
// MsgAckExecutiveAuthorityKey stays noop because it is auto-injected by
// PrepareProposal and never client-signed.

// noopSignerFn is a GetSignersFunc that returns nil — vote messages have no
// standard Cosmos signers.
func noopSignerFn(proto.Message) ([][]byte, error) { return nil, nil }

// ceremonyCreatorSignerFn extracts the signer from a ceremony message's
// "creator" field (a valoper bech32 address) and returns the corresponding
// account address bytes. Used for all ceremony messages that have a creator
// field and go through standard Cosmos SDK signature verification.
func ceremonyCreatorSignerFn(msg proto.Message) ([][]byte, error) {
	fd := msg.ProtoReflect().Descriptor().Fields().ByName("creator")
	if fd == nil {
		return nil, fmt.Errorf("message %s has no creator field", msg.ProtoReflect().Descriptor().FullName())
	}
	creator := msg.ProtoReflect().Get(fd).String()

	// creator is a valoper address; convert to account address bytes for signing.
	valAddr, err := sdk.ValAddressFromBech32(creator)
	if err != nil {
		// Fall back to acc address parse in case it's already an acc address.
		accAddr, accErr := sdk.AccAddressFromBech32(creator)
		if accErr != nil {
			return nil, fmt.Errorf("invalid creator address %q: %w", creator, err)
		}
		return [][]byte{accAddr}, nil
	}
	return [][]byte{sdk.AccAddress(valAddr)}, nil
}

// createValidatorWithPallasKeySignerFn extracts the signer from the embedded
// staking_msg's ValidatorAddress field. MsgCreateValidatorWithPallasKey has
// no creator field; the signer is the account behind the validator address
// in the embedded MsgCreateValidator.
func createValidatorWithPallasKeySignerFn(msg proto.Message) ([][]byte, error) {
	fd := msg.ProtoReflect().Descriptor().Fields().ByName("staking_msg")
	if fd == nil {
		return nil, fmt.Errorf("MsgCreateValidatorWithPallasKey has no staking_msg field")
	}
	stakingMsgBytes := msg.ProtoReflect().Get(fd).Bytes()

	stakingMsg := &stakingtypes.MsgCreateValidator{}
	if err := stakingMsg.Unmarshal(stakingMsgBytes); err != nil {
		return nil, fmt.Errorf("failed to decode staking_msg for signer extraction: %w", err)
	}

	valAddr, err := sdk.ValAddressFromBech32(stakingMsg.ValidatorAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid validator address in staking_msg: %w", err)
	}
	return [][]byte{sdk.AccAddress(valAddr)}, nil
}

func ProvideCreateVotingSessionSigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgCreateVotingSession"),
		Fn:      ceremonyCreatorSignerFn,
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

func ProvideRegisterPallasKeySigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgRegisterPallasKey"),
		Fn:      ceremonyCreatorSignerFn,
	}
}

func ProvideDealExecutiveAuthorityKeySigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgDealExecutiveAuthorityKey"),
		Fn:      ceremonyCreatorSignerFn,
	}
}

// MsgAckExecutiveAuthorityKey stays noop: it is auto-injected by
// PrepareProposal and never goes through standard Cosmos SDK signing.
func ProvideAckExecutiveAuthorityKeySigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgAckExecutiveAuthorityKey"),
		Fn:      noopSignerFn,
	}
}

func ProvideCreateValidatorWithPallasKeySigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgCreateValidatorWithPallasKey"),
		Fn:      createValidatorWithPallasKeySignerFn,
	}
}

func ProvideSetVoteManagerSigner() signing.CustomGetSigner {
	return signing.CustomGetSigner{
		MsgType: protoreflect.FullName("zvote.v1.MsgSetVoteManager"),
		Fn:      ceremonyCreatorSignerFn,
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
// Both QueryServer and MsgServer are registered. Vote-round messages bypass
// the Cosmos SDK Tx envelope (using a raw [tag || protobuf] wire format) with
// ZKP/RedPallas authentication. Ceremony messages (except MsgAck) use
// standard Cosmos SDK transactions with signature verification and validator
// gating. All messages are routed to the keeper via BaseApp's MsgServiceRouter.
func (am AppModule) RegisterServices(cfg module.Configurator) {
	types.RegisterQueryServer(cfg.QueryServer(), keeper.NewQueryServerImpl(am.keeper))
	types.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.keeper))
}

// AutoCLIOptions implements autocli.HasAutoCLIConfig.
func (AppModule) AutoCLIOptions() *autocliv1.ModuleOptions {
	return &autocliv1.ModuleOptions{
		Query: &autocliv1.ServiceCommandDescriptor{
			Service: "zvote.v1.Query",
			RpcCommandOptions: []*autocliv1.RpcCommandOptions{
				{
					RpcMethod: "CommitmentTreeAtHeight",
					Use:       "commitment-tree [height]",
					Short:     "Query the commitment tree root at a specific block height",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "height"},
					},
				},
				{
					RpcMethod: "LatestCommitmentTree",
					Use:       "latest-tree",
					Short:     "Query the latest commitment tree state",
				},
				{
					RpcMethod: "VoteRound",
					Use:       "vote-round [vote-round-id]",
					Short:     "Query a vote round by its hex-encoded ID",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "vote_round_id"},
					},
				},
				{
					RpcMethod: "ProposalTally",
					Use:       "proposal-tally [vote-round-id] [proposal-id]",
					Short:     "Query the accumulated tally for a proposal within a vote round",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "vote_round_id"},
						{ProtoField: "proposal_id"},
					},
				},
				{
					RpcMethod: "TallyResults",
					Use:       "tally-results [vote-round-id]",
					Short:     "Query finalized tally results for a vote round",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "vote_round_id"},
					},
				},
				{
					RpcMethod: "CommitmentLeaves",
					Use:       "commitment-leaves [from-height] [to-height]",
					Short:     "Query commitment tree leaves in a block height range",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "from_height"},
						{ProtoField: "to_height"},
					},
				},
				{
					RpcMethod: "ActiveRound",
					Use:       "active-round",
					Short:     "Query the currently active voting round",
				},
				{
					RpcMethod: "CeremonyState",
					Use:       "ceremony-state",
					Short:     "Query the current EA key ceremony lifecycle state",
				},
				{
					RpcMethod: "VoteManager",
					Use:       "vote-manager",
					Short:     "Query the current vote manager address",
				},
				{
					RpcMethod: "ListRounds",
					Use:       "list-rounds",
					Short:     "List all stored vote rounds",
				},
				{
					RpcMethod: "VoteSummary",
					Use:       "vote-summary [vote-round-id]",
					Short:     "Query a denormalized summary of a vote round including proposals and options",
					PositionalArgs: []*autocliv1.PositionalArgDescriptor{
						{ProtoField: "vote_round_id"},
					},
				},
			},
		},
	}
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

	// --- 3. Per-round ceremony DEALT phase timeout ---
	// Only the DEALT phase has a timeout. REGISTERING persists indefinitely
	// until a deal is injected by a proposer.
	// On DEALT timeout with >= 1/3 acks: strip non-ackers, confirm ceremony,
	// transition round to ACTIVE.
	// On DEALT timeout with < 1/3 acks: reset ceremony to REGISTERING for re-deal.
	// Collect round IDs with expired ceremony deadlines (avoid mutating store during iteration).
	var ceremonyTimeoutIDs [][]byte
	if err := am.keeper.IteratePendingRounds(kvStore, func(round *types.VoteRound) bool {
		if round.CeremonyStatus == types.CeremonyStatus_CEREMONY_STATUS_DEALT &&
			round.CeremonyPhaseTimeout > 0 &&
			blockTime >= round.CeremonyPhaseStart+round.CeremonyPhaseTimeout {
			id := make([]byte, len(round.VoteRoundId))
			copy(id, round.VoteRoundId)
			ceremonyTimeoutIDs = append(ceremonyTimeoutIDs, id)
		}
		return false // continue iterating
	}); err != nil {
		return err
	}

	for _, roundID := range ceremonyTimeoutIDs {
		round, err := am.keeper.GetVoteRound(kvStore, roundID)
		if err != nil {
			return err
		}
		if round == nil {
			continue
		}
		oldCeremonyStatus := round.CeremonyStatus

		if keeper.OneThirdAcked(round) {
			// >= 1/3 acked: strip non-ackers, confirm ceremony, activate round.
			keeper.StripNonAckersFromRound(round)
			round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
			round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE

			if err := am.keeper.SetVoteRound(kvStore, round); err != nil {
				return err
			}

			ctx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventTypeCeremonyStatusChange,
				sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", round.VoteRoundId)),
				sdk.NewAttribute(types.AttributeKeyOldStatus, oldCeremonyStatus.String()),
				sdk.NewAttribute(types.AttributeKeyNewStatus, round.CeremonyStatus.String()),
			))
			ctx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventTypeRoundStatusChange,
				sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", round.VoteRoundId)),
				sdk.NewAttribute(types.AttributeKeyOldStatus, types.SessionStatus_SESSION_STATUS_PENDING.String()),
				sdk.NewAttribute(types.AttributeKeyNewStatus, types.SessionStatus_SESSION_STATUS_ACTIVE.String()),
			))
		} else {
			// < 1/3 acks: reset ceremony for re-deal by next proposer.
			round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_REGISTERING
			round.CeremonyPayloads = nil
			round.CeremonyAcks = nil
			round.CeremonyDealer = ""
			round.CeremonyPhaseStart = 0
			round.CeremonyPhaseTimeout = 0
			round.EaPk = nil

			if err := am.keeper.SetVoteRound(kvStore, round); err != nil {
				return err
			}

			ctx.EventManager().EmitEvent(sdk.NewEvent(
				types.EventTypeCeremonyStatusChange,
				sdk.NewAttribute(types.AttributeKeyRoundID, fmt.Sprintf("%x", round.VoteRoundId)),
				sdk.NewAttribute(types.AttributeKeyOldStatus, oldCeremonyStatus.String()),
				sdk.NewAttribute(types.AttributeKeyNewStatus, round.CeremonyStatus.String()),
			))
		}
	}

	return nil
}

// DefaultVoteManagerAddress is a well-known secp256k1 account used as the
// default vote manager when no explicit manager is configured in genesis.
//
// Private key (hex): b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee
const DefaultVoteManagerAddress = "zvote15fjfr6rrs60vu4st6arrd94w5j6z7f6kxr92cg"

// DefaultGenesis returns the default genesis state as raw JSON bytes.
func (am AppModule) DefaultGenesis(_ codec.JSONCodec) json.RawMessage {
	gs := &types.GenesisState{
		VoteManager: DefaultVoteManagerAddress,
	}
	bz, err := json.Marshal(gs)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal default genesis: %v", err))
	}
	return bz
}

// ValidateGenesis performs genesis state validation.
func (am AppModule) ValidateGenesis(_ codec.JSONCodec, _ client.TxEncodingConfig, _ json.RawMessage) error {
	return nil
}

// InitGenesis initializes the module state from genesis.
// Uses sdk.Context (not context.Context) to satisfy module.HasGenesis interface.
func (am AppModule) InitGenesis(ctx sdk.Context, _ codec.JSONCodec, data json.RawMessage) {
	var gs types.GenesisState
	if err := json.Unmarshal(data, &gs); err != nil {
		panic(fmt.Sprintf("vote: failed to unmarshal genesis state: %v", err))
	}

	kvStore := am.keeper.OpenKVStore(ctx)
	if err := am.keeper.InitGenesis(kvStore, &gs); err != nil {
		panic(fmt.Sprintf("vote: InitGenesis failed: %v", err))
	}
}

// ExportGenesis exports the module state as genesis.
// Uses sdk.Context (not context.Context) to satisfy module.HasGenesis interface.
func (am AppModule) ExportGenesis(ctx sdk.Context, _ codec.JSONCodec) json.RawMessage {
	kvStore := am.keeper.OpenKVStore(ctx)
	gs, err := am.keeper.ExportGenesis(kvStore)
	if err != nil {
		panic(fmt.Sprintf("vote: ExportGenesis failed: %v", err))
	}
	bz, err := json.Marshal(gs)
	if err != nil {
		panic(fmt.Sprintf("vote: failed to marshal genesis state: %v", err))
	}
	return bz
}
