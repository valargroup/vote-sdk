package types

import (
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// RegisterInterfaces registers the vote module's message types with the
// InterfaceRegistry. This is required for the MsgServiceRouter to accept
// vote messages during RegisterService.
//
// We only call RegisterImplementations (not msgservice.RegisterMsgServiceDesc)
// because our protobuf types are generated with protoc-gen-go v2, which uses a
// different file descriptor registry than what RegisterMsgServiceDesc expects.
func RegisterInterfaces(registry codectypes.InterfaceRegistry) {
	registry.RegisterImplementations((*sdk.Msg)(nil),
		&MsgCreateVotingSession{},
		&MsgDelegateVote{},
		&MsgCastVote{},
		&MsgRevealShare{},
		&MsgSubmitTally{},
		&MsgRegisterPallasKey{},
		&MsgDealExecutiveAuthorityKey{},
		&MsgAckExecutiveAuthorityKey{},
		&MsgCreateValidatorWithPallasKey{},
		&MsgReInitializeElectionAuthority{},
		&MsgSetVoteManager{},
	)
}
