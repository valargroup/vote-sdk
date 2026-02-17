package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	voteapi "github.com/z-cale/zally/api"
)

// CustomTxDecoder returns a TxDecoder that handles vote wire format,
// ceremony wire format, and standard Cosmos SDK Tx encoding.
//
// Custom transactions use a simple tagged binary format:
//
//	[1 byte: msg_type_tag] [N bytes: protobuf message]
//
// Tags 0x01–0x05 are vote-round messages, 0x06+ are ceremony messages.
// These tag bytes do not collide with valid Cosmos Tx protobuf encodings,
// which start with a field tag byte (typically 0x0a for field 1,
// length-delimited).
//
// If the first byte is a custom tag, the tx is decoded into a VoteTxWrapper
// that implements sdk.Tx. Otherwise, the standard Cosmos TxDecoder is used.
func CustomTxDecoder(standardDecoder sdk.TxDecoder) sdk.TxDecoder {
	return func(txBytes []byte) (sdk.Tx, error) {
		if len(txBytes) > 1 && voteapi.IsCustomTag(txBytes[0]) {
			if voteapi.IsCeremonyTag(txBytes[0]) {
				tag, ceremonyMsg, err := voteapi.DecodeCeremonyTx(txBytes)
				if err != nil {
					return nil, err
				}
				return &voteapi.VoteTxWrapper{
					RawBytes:    txBytes,
					Tag:         tag,
					CeremonyMsg: ceremonyMsg.(sdk.Msg),
				}, nil
			}
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
