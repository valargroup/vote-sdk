package testutil

import (
	"google.golang.org/protobuf/proto"

	"github.com/z-cale/zally/api"
	"github.com/z-cale/zally/x/vote/types"
)

// MustEncodeVoteTx encodes a VoteMessage into the raw wire format
// [tag || protobuf_msg] used by the custom ABCI pipeline.
// Panics on encoding failure (safe for tests).
func MustEncodeVoteTx(msg types.VoteMessage) []byte {
	raw, err := api.EncodeVoteTx(msg)
	if err != nil {
		panic("testutil.MustEncodeVoteTx: " + err.Error())
	}
	return raw
}

// MustEncodeAckCeremonyTx encodes a MsgAckExecutiveAuthorityKey into the raw
// wire format [tag || protobuf_msg]. Only MsgAck uses the custom wire format;
// all other ceremony messages use standard Cosmos SDK transactions.
// Panics on encoding failure (safe for tests).
func MustEncodeAckCeremonyTx(msg proto.Message) []byte {
	raw, err := api.EncodeCeremonyTx(msg, api.TagAckExecutiveAuthorityKey)
	if err != nil {
		panic("testutil.MustEncodeAckCeremonyTx: " + err.Error())
	}
	return raw
}
