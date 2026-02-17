package api

import (
	gogoproto "github.com/cosmos/gogoproto/proto"
	protov2 "google.golang.org/protobuf/proto"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/types"
)

// Compile-time check that VoteTxWrapper implements sdk.Tx.
var _ sdk.Tx = (*VoteTxWrapper)(nil)

// VoteTxWrapper wraps a vote module message so it can flow through BaseApp's
// standard tx lifecycle (TxDecoder → AnteHandler → MsgServiceRouter).
//
// Vote transactions bypass the Cosmos SDK Tx envelope. They use a simple
// wire format: [1-byte tag || protobuf message]. This wrapper makes them
// compatible with sdk.Tx so BaseApp can process them alongside standard
// Cosmos transactions.
type VoteTxWrapper struct {
	// RawBytes is the original wire-format bytes [tag || protobuf].
	RawBytes []byte

	// Tag is the message type tag (0x01–0x05 for vote-round, 0x06–0x09 for ceremony).
	Tag byte

	// VoteMsg is the decoded vote message, used by the validation pipeline.
	// Set for vote-round tags (0x01–0x05). Nil for ceremony messages.
	VoteMsg types.VoteMessage

	// CeremonyMsg is the decoded ceremony message (tags 0x06–0x09).
	// Set for ceremony tags. Nil for vote-round messages.
	CeremonyMsg sdk.Msg
}

// GetMsgs satisfies sdk.HasMsgs. Returns the single message as sdk.Msg.
// sdk.Msg is gogoproto.Message, which our protobuf-generated types implement.
func (vtx *VoteTxWrapper) GetMsgs() []sdk.Msg {
	if vtx.CeremonyMsg != nil {
		return []sdk.Msg{vtx.CeremonyMsg}
	}
	return []sdk.Msg{vtx.VoteMsg.(gogoproto.Message)}
}

// GetMsgsV2 satisfies sdk.Tx. Returns the message as a protov2.Message.
// Our protoc-gen-go v2 generated types implement both gogoproto.Message and
// protov2.Message (google.golang.org/protobuf/proto.Message).
//
// Hand-written ceremony types (e.g. MsgCreateValidatorWithPallasKey) may not
// implement protov2.Message; in that case we return nil. BaseApp's runMsgs()
// uses GetMsgs() (gogoproto) for routing, not GetMsgsV2().
func (vtx *VoteTxWrapper) GetMsgsV2() ([]protov2.Message, error) {
	if vtx.CeremonyMsg != nil {
		if pm, ok := vtx.CeremonyMsg.(protov2.Message); ok {
			return []protov2.Message{pm}, nil
		}
		return nil, nil
	}
	return []protov2.Message{vtx.VoteMsg.(protov2.Message)}, nil
}
