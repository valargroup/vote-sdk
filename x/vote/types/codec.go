package types

import (
	gogoproto "github.com/cosmos/gogoproto/proto"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func init() {
	// The Cosmos SDK's unknownproto.RejectUnknownFields resolves nested message
	// types via gogoproto.MessageType(), which only checks the gogoproto (v1)
	// global registry. Our types are generated with protoc-gen-go v2, which
	// registers with the v2 registry only. Bridge all types that appear as
	// nested messages inside transaction Msg types into the gogoproto registry
	// so that standard Cosmos SDK tx decoding succeeds.
	gogoproto.RegisterType((*DealerPayload)(nil), "zvote.v1.DealerPayload")
	gogoproto.RegisterType((*Proposal)(nil), "zvote.v1.Proposal")
	gogoproto.RegisterType((*VoteOption)(nil), "zvote.v1.VoteOption")
	gogoproto.RegisterType((*TallyEntry)(nil), "zvote.v1.TallyEntry")
	gogoproto.RegisterType((*PartialDecryptionEntry)(nil), "zvote.v1.PartialDecryptionEntry")
}

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
		&MsgSubmitPartialDecryption{},
		&MsgRegisterPallasKey{},
		&MsgDealExecutiveAuthorityKey{},
		&MsgAckExecutiveAuthorityKey{},
		&MsgCreateValidatorWithPallasKey{},
		&MsgSetVoteManager{},
	)
}
