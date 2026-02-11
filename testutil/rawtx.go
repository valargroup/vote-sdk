package testutil

import (
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
