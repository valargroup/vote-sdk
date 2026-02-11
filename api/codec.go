// Package api provides the JSON REST API and wire-format codec for vote
// transactions that bypass the Cosmos SDK Tx envelope.
package api

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/z-cale/zally/x/vote/types"
)

// Vote transaction type tags. The first byte of the wire format identifies
// the message type. Tags 0x01–0x04 are reserved for vote transactions;
// any other first byte is assumed to be a standard Cosmos SDK Tx.
const (
	TagSetupVoteRound       byte = 0x01
	TagRegisterDelegation   byte = 0x02
	TagCreateVoteCommitment byte = 0x03
	TagRevealVoteShare      byte = 0x04
)

// IsVoteTag returns true if b is a valid vote transaction type tag.
func IsVoteTag(b byte) bool {
	return b >= TagSetupVoteRound && b <= TagRevealVoteShare
}

// TagForMessage returns the wire-format tag for a VoteMessage.
func TagForMessage(msg types.VoteMessage) (byte, error) {
	switch msg.(type) {
	case *types.MsgSetupVoteRound:
		return TagSetupVoteRound, nil
	case *types.MsgRegisterDelegation:
		return TagRegisterDelegation, nil
	case *types.MsgCreateVoteCommitment:
		return TagCreateVoteCommitment, nil
	case *types.MsgRevealVoteShare:
		return TagRevealVoteShare, nil
	default:
		return 0, fmt.Errorf("unknown vote message type: %T", msg)
	}
}

// EncodeVoteTx serializes a vote message into the wire format:
//
//	[1 byte: msg_type_tag] [N bytes: protobuf-encoded message]
func EncodeVoteTx(msg types.VoteMessage) ([]byte, error) {
	tag, err := TagForMessage(msg)
	if err != nil {
		return nil, err
	}

	body, err := proto.Marshal(msg.(proto.Message))
	if err != nil {
		return nil, fmt.Errorf("protobuf marshal failed: %w", err)
	}

	raw := make([]byte, 1+len(body))
	raw[0] = tag
	copy(raw[1:], body)
	return raw, nil
}

// DecodeVoteTx decodes raw wire-format bytes into a tag and VoteMessage.
// Returns an error if the bytes are too short, the tag is invalid, or
// protobuf decoding fails.
func DecodeVoteTx(raw []byte) (byte, types.VoteMessage, error) {
	if len(raw) < 2 {
		return 0, nil, fmt.Errorf("vote tx too short: %d bytes", len(raw))
	}

	tag := raw[0]
	body := raw[1:]

	var msg proto.Message
	switch tag {
	case TagSetupVoteRound:
		msg = &types.MsgSetupVoteRound{}
	case TagRegisterDelegation:
		msg = &types.MsgRegisterDelegation{}
	case TagCreateVoteCommitment:
		msg = &types.MsgCreateVoteCommitment{}
	case TagRevealVoteShare:
		msg = &types.MsgRevealVoteShare{}
	default:
		return 0, nil, fmt.Errorf("invalid vote tx tag: 0x%02x", tag)
	}

	if err := proto.Unmarshal(body, msg); err != nil {
		return 0, nil, fmt.Errorf("protobuf unmarshal failed for tag 0x%02x: %w", tag, err)
	}

	voteMsg, ok := msg.(types.VoteMessage)
	if !ok {
		return 0, nil, fmt.Errorf("decoded message does not implement VoteMessage: %T", msg)
	}

	return tag, voteMsg, nil
}

