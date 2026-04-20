// Package api provides the JSON REST API and wire-format codec for vote
// transactions that bypass the Cosmos SDK Tx envelope.
package api

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// Vote transaction type tags. The first byte of the wire format identifies
// the message type. Tags 0x02–0x05 are vote-round transactions that use
// ZKP/RedPallas authentication. Tags 0x08, 0x0D, and 0x0E are ceremony/tally
// tags auto-injected by PrepareProposal that also use the custom wire format.
//
// Tags 0x01, 0x06, 0x09, 0x0C are reserved for messages that use the standard
// Cosmos SDK Tx envelope (MsgCreateVotingSession, MsgRegisterPallasKey,
// MsgCreateValidatorWithPallasKey, MsgUpdateVoteManagers). They are not used by
// any encoder or decoder here — see sdk/README.md for the full byte map.
// Tag 0x0A is deliberately absent: it collides with the standard Cosmos Tx
// protobuf encoding (field 1, wire type 2).
const (
	TagDelegateVote byte = 0x02
	TagCastVote     byte = 0x03
	TagRevealShare  byte = 0x04
	TagSubmitTally  byte = 0x05

	// Auto-injected by PrepareProposal; never client-signed.
	TagAckExecutiveAuthorityKey byte = 0x08

	// TagContributeDKG (0x0E) is auto-injected by PrepareProposal during the
	// Joint-Feldman DKG phase. Like Ack, it uses the custom wire format.
	TagContributeDKG byte = 0x0E

	// TagSubmitPartialDecryption (0x0D) is auto-injected by PrepareProposal
	// during the TALLYING phase of a threshold-mode round. Like MsgAck, it is
	// never client-signed and uses the custom wire format.
	TagSubmitPartialDecryption byte = 0x0D
)

// IsCustomTag returns true if b is a valid custom transaction type tag
// (vote-round 0x02–0x05 or ceremony 0x08/0x0D/0x0E). Other ceremony
// messages use standard Cosmos SDK transactions.
func IsCustomTag(b byte) bool {
	return IsVoteTag(b) || IsCeremonyTag(b)
}

// IsVoteTag returns true if b is a vote-round transaction type tag (0x02–0x05).
// MsgCreateVotingSession (0x01) now uses standard Cosmos SDK transactions.
func IsVoteTag(b byte) bool {
	return b >= TagDelegateVote && b <= TagSubmitTally
}

// IsCeremonyTag returns true if b is an auto-injected ceremony/tally
// transaction type tag that uses the custom wire format and is never
// client-signed. Currently: ContributeDKG (0x0E), Ack (0x08),
// and SubmitPartialDecryption (0x0D).
func IsCeremonyTag(b byte) bool {
	return b == TagContributeDKG ||
		b == TagAckExecutiveAuthorityKey ||
		b == TagSubmitPartialDecryption
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

// EncodeCeremonyTx serializes an auto-injected message into the custom wire format:
//
//	[1 byte: msg_type_tag] [N bytes: protobuf-encoded message]
//
// Tags 0x08 (Ack), 0x0D (SubmitPartialDecryption), and 0x0E (ContributeDKG)
// use the custom wire format — all are auto-injected by PrepareProposal and
// never client-signed.
func EncodeCeremonyTx(msg proto.Message, tag byte) ([]byte, error) {
	if !IsCeremonyTag(tag) {
		return nil, fmt.Errorf("invalid auto-inject tag: 0x%02x (only 0x08, 0x0D, 0x0E use custom wire format)", tag)
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
// message (proto.Message). Tags 0x08 (Ack), 0x0D (PartialDecrypt), and
// 0x0E (ContributeDKG) use the custom wire format.
func DecodeCeremonyTx(raw []byte) (byte, proto.Message, error) {
	if len(raw) < 2 {
		return 0, nil, fmt.Errorf("ceremony tx too short: %d bytes", len(raw))
	}

	tag := raw[0]
	body := raw[1:]

	var msg proto.Message
	switch tag {
	case TagContributeDKG:
		msg = &types.MsgContributeDKG{}
	case TagAckExecutiveAuthorityKey:
		msg = &types.MsgAckExecutiveAuthorityKey{}
	case TagSubmitPartialDecryption:
		msg = &types.MsgSubmitPartialDecryption{}
	default:
		return 0, nil, fmt.Errorf("invalid auto-inject tx tag: 0x%02x (only 0x08, 0x0D, 0x0E use custom wire format)", tag)
	}

	if err := proto.Unmarshal(body, msg); err != nil {
		return 0, nil, fmt.Errorf("protobuf unmarshal failed for tag 0x%02x: %w", tag, err)
	}

	return tag, msg, nil
}
