// Package api provides the JSON REST API and wire-format codec for vote
// transactions that bypass the Cosmos SDK Tx envelope.
package api

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/z-cale/zally/x/vote/types"
)

// Vote transaction type tags. The first byte of the wire format identifies
// the message type. Tags 0x02–0x05 are vote-round transactions that use
// ZKP/RedPallas authentication. Tags 0x07–0x08 are ceremony tags auto-injected
// by PrepareProposal (deal + ack). MsgCreateVotingSession (0x01) and other
// ceremony messages flow through standard Cosmos SDK transactions.
const (
	TagCreateVotingSession byte = 0x01
	TagDelegateVote        byte = 0x02
	TagCastVote            byte = 0x03
	TagRevealShare         byte = 0x04
	TagSubmitTally         byte = 0x05

	// Ceremony tags — Deal (0x07) and Ack (0x08) use custom wire format
	// (auto-injected by PrepareProposal). Others are standard Cosmos txs.
	TagRegisterPallasKey              byte = 0x06
	TagDealExecutiveAuthorityKey      byte = 0x07
	TagAckExecutiveAuthorityKey       byte = 0x08
	TagCreateValidatorWithPallasKey         byte = 0x09
	TagSetVoteManager                      byte = 0x0C
)

// IsCustomTag returns true if b is a valid custom transaction type tag
// (vote-round 0x02–0x05 or ceremony 0x07–0x08). Other ceremony messages
// use standard Cosmos SDK transactions.
func IsCustomTag(b byte) bool {
	return IsVoteTag(b) || IsCeremonyTag(b)
}

// IsVoteTag returns true if b is a vote-round transaction type tag (0x02–0x05).
// MsgCreateVotingSession (0x01) now uses standard Cosmos SDK transactions.
func IsVoteTag(b byte) bool {
	return b >= TagDelegateVote && b <= TagSubmitTally
}

// IsCeremonyTag returns true if b is a ceremony transaction type tag that
// uses the custom wire format. MsgDealExecutiveAuthorityKey (0x07) and
// MsgAckExecutiveAuthorityKey (0x08) are auto-injected by PrepareProposal
// and never client-signed. All other ceremony messages flow through standard
// Cosmos SDK transactions with proper signature verification.
func IsCeremonyTag(b byte) bool {
	return b == TagDealExecutiveAuthorityKey || b == TagAckExecutiveAuthorityKey
}

// TagForMessage returns the wire-format tag for a VoteMessage.
func TagForMessage(msg types.VoteMessage) (byte, error) {
	switch msg.(type) {
	case *types.MsgDelegateVote:
		return TagDelegateVote, nil
	case *types.MsgCastVote:
		return TagCastVote, nil
	case *types.MsgRevealShare:
		return TagRevealShare, nil
	case *types.MsgSubmitTally:
		return TagSubmitTally, nil
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
	case TagDelegateVote:
		msg = &types.MsgDelegateVote{}
	case TagCastVote:
		msg = &types.MsgCastVote{}
	case TagRevealShare:
		msg = &types.MsgRevealShare{}
	case TagSubmitTally:
		msg = &types.MsgSubmitTally{}
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

// EncodeCeremonyTx serializes a ceremony message into the custom wire format:
//
//	[1 byte: msg_type_tag] [N bytes: protobuf-encoded message]
//
// Tags 0x07 (Deal) and 0x08 (Ack) use the custom wire format — both are
// auto-injected by PrepareProposal and never client-signed.
func EncodeCeremonyTx(msg proto.Message, tag byte) ([]byte, error) {
	if !IsCeremonyTag(tag) {
		return nil, fmt.Errorf("invalid ceremony tag: 0x%02x (only 0x07-0x08 use custom wire format)", tag)
	}

	body, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("protobuf marshal failed: %w", err)
	}

	raw := make([]byte, 1+len(body))
	raw[0] = tag
	copy(raw[1:], body)
	return raw, nil
}

// DecodeCeremonyTx decodes raw wire-format bytes into a tag and a ceremony
// message (proto.Message). Tags 0x07 (Deal) and 0x08 (Ack) use the custom
// wire format; all other ceremony messages are standard Cosmos txs.
func DecodeCeremonyTx(raw []byte) (byte, proto.Message, error) {
	if len(raw) < 2 {
		return 0, nil, fmt.Errorf("ceremony tx too short: %d bytes", len(raw))
	}

	tag := raw[0]
	body := raw[1:]

	var msg proto.Message
	switch tag {
	case TagDealExecutiveAuthorityKey:
		msg = &types.MsgDealExecutiveAuthorityKey{}
	case TagAckExecutiveAuthorityKey:
		msg = &types.MsgAckExecutiveAuthorityKey{}
	default:
		return 0, nil, fmt.Errorf("invalid ceremony tx tag: 0x%02x (only 0x07-0x08 use custom wire format)", tag)
	}

	if err := proto.Unmarshal(body, msg); err != nil {
		return 0, nil, fmt.Errorf("protobuf unmarshal failed for tag 0x%02x: %w", tag, err)
	}

	return tag, msg, nil
}
