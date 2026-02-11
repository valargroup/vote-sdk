package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	voteapi "github.com/z-cale/zally/api"
)

// CustomTxDecoder returns a TxDecoder that handles both vote wire format
// and standard Cosmos SDK Tx encoding.
//
// Vote transactions use a simple tagged binary format:
//
//	[1 byte: msg_type_tag (0x01–0x04)] [N bytes: protobuf message]
//
// The tag bytes (0x01–0x04) do not collide with valid Cosmos Tx protobuf
// encodings, which start with a field tag byte (typically 0x0a for field 1,
// length-delimited).
//
// If the first byte is a vote tag, the tx is decoded into a VoteTxWrapper
// that implements sdk.Tx. Otherwise, the standard Cosmos TxDecoder is used.
func CustomTxDecoder(standardDecoder sdk.TxDecoder) sdk.TxDecoder {
	return func(txBytes []byte) (sdk.Tx, error) {
		if len(txBytes) > 1 && voteapi.IsVoteTag(txBytes[0]) {
			tag, voteMsg, err := voteapi.DecodeVoteTx(txBytes)
			if err != nil {
				return nil, err
			}
			return &voteapi.VoteTxWrapper{
				RawBytes: txBytes,
				Tag:      tag,
				VoteMsg:  voteMsg,
			}, nil
		}
		return standardDecoder(txBytes)
	}
}
