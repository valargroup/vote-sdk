package api

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	protov2 "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// unknownField99 is field 99 with varint value 1, which no vote message has.
var unknownField99 = protoreflect.RawFields{0x98, 0x06, 0x01}

// proxyBatchForCodecTest returns a 0x09 batch with a registration and two DCs.
func proxyBatchForCodecTest() *types.MsgProxyDelegationBatch {
	roundID := bytes.Repeat([]byte{0x01}, 32)
	dc := func(seed byte) *types.ProxyDelegationAction {
		return &types.ProxyDelegationAction{
			VanNullifier:              bytes.Repeat([]byte{seed}, 32),
			RVpk:                      bytes.Repeat([]byte{seed + 1}, 32),
			VoteAuthorityNoteNew:      bytes.Repeat([]byte{seed + 2}, 32),
			ProxyDelegationCommitment: bytes.Repeat([]byte{seed + 3}, 32),
			DcSlotNullifier:           bytes.Repeat([]byte{seed + 4}, 32),
			Proof:                     []byte("proof"),
			VoteAuthSig:               bytes.Repeat([]byte{seed + 5}, 64),
		}
	}
	return &types.MsgProxyDelegationBatch{
		VoteRoundId: roundID,
		Registration: &types.MsgDelegateVote{
			VoteRoundId:   roundID,
			VanCmx:        bytes.Repeat([]byte{0x02}, 32),
			GovNullifiers: [][]byte{bytes.Repeat([]byte{0x03}, 32)},
		},
		Dcs:          []*types.ProxyDelegationAction{dc(0x10), dc(0x20)},
		ExpiryHeight: 2_000_500,
	}
}

// delegateOpsForCodecTest returns one MsgDelegateOp per variant.
func delegateOpsForCodecTest() map[string]*types.MsgDelegateOp {
	return map[string]*types.MsgDelegateOp{
		"register": {Register: &types.DelegateRegister{
			Dk:       bytes.Repeat([]byte{0x41}, 32),
			Provider: 2,
			Handle:   "alice-example",
			ProofId:  "0123456789abcdef",
			Attestations: []*types.DelegateAttestation{
				{VerifierId: 1, NotBefore: 10, ExpiresAt: 20, Sig: bytes.Repeat([]byte{0x43}, 64)},
				{VerifierId: 2, NotBefore: 10, ExpiresAt: 20, Sig: bytes.Repeat([]byte{0x44}, 64)},
			},
			DkSig: bytes.Repeat([]byte{0x45}, 64),
		}},
		"retire": {Retire: &types.DelegateRetire{
			DelegateIndex: 3,
			DkSig:         bytes.Repeat([]byte{0x52}, 64),
		}},
		"ballot": {Ballot: &types.DelegateBallot{
			VoteRoundId:   bytes.Repeat([]byte{0x01}, 32),
			DelegateIndex: 3,
			Options:       []uint32{1, 0, 7},
			DkSig:         bytes.Repeat([]byte{0x61}, 64),
		}},
	}
}

func TestProxyDelegationTagsDormant(t *testing.T) {
	proxytest.SkipUnlessDormant(t)
	for _, tag := range []byte{TagProxyDelegationBatch, TagDelegateOp} {
		require.False(t, IsVoteTag(tag), "tag 0x%02x", tag)
		require.False(t, IsCustomTag(tag), "tag 0x%02x", tag)
	}

	tags := map[types.VoteMessage]byte{proxyBatchForCodecTest(): TagProxyDelegationBatch}
	for _, op := range delegateOpsForCodecTest() {
		tags[op] = TagDelegateOp
	}
	for msg, tag := range tags {
		_, err := TagForMessage(msg)
		require.ErrorIs(t, err, types.ErrProxyDelegationDisabled)
		_, err = EncodeVoteTx(msg)
		require.ErrorIs(t, err, types.ErrProxyDelegationDisabled)

		// Even a canonical body behind the tag is not a vote tx.
		body, err := (protov2.MarshalOptions{Deterministic: true}).Marshal(msg.(protov2.Message))
		require.NoError(t, err)
		_, _, err = DecodeVoteTx(append([]byte{tag}, body...))
		require.ErrorContains(t, err, "invalid vote tx tag")
	}
}

func TestProxyDelegationTagsEnabled(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	require.True(t, IsVoteTag(TagProxyDelegationBatch))
	require.True(t, IsVoteTag(TagDelegateOp))
	for _, tag := range []byte{0x08, 0x0a, 0x0c, 0x0d, 0x0e} {
		require.False(t, IsVoteTag(tag), "tag 0x%02x", tag)
	}
	for tag := TagDelegateVote; tag <= TagDelegateAndCastVoteBatch; tag++ {
		require.True(t, IsVoteTag(tag), "tag 0x%02x", tag)
	}

	type encoded struct {
		tag byte
		msg types.VoteMessage
	}
	cases := map[string]encoded{"batch": {TagProxyDelegationBatch, proxyBatchForCodecTest()}}
	for name, op := range delegateOpsForCodecTest() {
		cases["op/"+name] = encoded{TagDelegateOp, op}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := EncodeVoteTx(c.msg)
			require.NoError(t, err)
			again, err := EncodeVoteTx(c.msg)
			require.NoError(t, err)
			require.Equal(t, raw, again, "encoding is deterministic")
			require.Equal(t, c.tag, raw[0])

			tag, decoded, err := DecodeVoteTx(raw)
			require.NoError(t, err)
			require.Equal(t, c.tag, tag)
			require.True(t, protov2.Equal(c.msg.(protov2.Message), decoded.(protov2.Message)))

			_, _, err = DecodeVoteTx(append(append([]byte(nil), raw...), unknownField99...))
			require.ErrorContains(t, err, "unknown fields")
		})
	}
}

// TestProxyDelegationTagsRejectNestedUnknownFields checks that unknown fields
// at any depth of a 0x09 or 0x0B body are refused, as for 0x06 and 0x07.
func TestProxyDelegationTagsRejectNestedUnknownFields(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	nested := map[string]func() types.VoteMessage{
		"batch dc": func() types.VoteMessage {
			msg := proxyBatchForCodecTest()
			msg.Dcs[1].ProtoReflect().SetUnknown(unknownField99)
			return msg
		},
		"batch registration": func() types.VoteMessage {
			msg := proxyBatchForCodecTest()
			msg.Registration.ProtoReflect().SetUnknown(unknownField99)
			return msg
		},
		"register attestation": func() types.VoteMessage {
			op := delegateOpsForCodecTest()["register"]
			op.Register.Attestations[1].ProtoReflect().SetUnknown(unknownField99)
			return op
		},
		"retire": func() types.VoteMessage {
			op := delegateOpsForCodecTest()["retire"]
			op.Retire.ProtoReflect().SetUnknown(unknownField99)
			return op
		},
		"ballot": func() types.VoteMessage {
			op := delegateOpsForCodecTest()["ballot"]
			op.Ballot.ProtoReflect().SetUnknown(unknownField99)
			return op
		},
	}
	for name, build := range nested {
		t.Run(name, func(t *testing.T) {
			raw, err := EncodeVoteTx(build())
			require.NoError(t, err)
			_, _, err = DecodeVoteTx(raw)
			require.ErrorContains(t, err, "unknown fields")
		})
	}
}

// TestProxyDelegationTagsRejectNonCanonicalEncodings checks byte equality with
// the deterministic re-encoding: a body that decodes to the same message
// through a different encoding is refused.
func TestProxyDelegationTagsRejectNonCanonicalEncodings(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	batch, err := EncodeVoteTx(proxyBatchForCodecTest())
	require.NoError(t, err)
	ballot, err := EncodeVoteTx(delegateOpsForCodecTest()["ballot"])
	require.NoError(t, err)

	cases := map[string][]byte{
		// vote_comm_tree_anchor_height = 0 written out, though proto3 omits it.
		"batch explicit default": append(append([]byte(nil), batch...), 0x10, 0x00),
		// An empty registration merged into the existing one.
		"batch repeated registration": append(append([]byte(nil), batch...), 0x1a, 0x00),
		// An empty ballot merged into the existing one.
		"op repeated ballot": append(append([]byte(nil), ballot...), 0x1a, 0x00),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := DecodeVoteTx(raw)
			require.ErrorContains(t, err, "non-canonical")
		})
	}

	// The ballot's options written unpacked decode to the same message.
	op := delegateOpsForCodecTest()["ballot"]
	inner := []byte{0x0a, 0x20}
	inner = append(inner, op.Ballot.VoteRoundId...)
	inner = append(inner, 0x10, 0x03)                // delegate_index = 3
	inner = append(inner, 0x18, 1, 0x18, 0, 0x18, 7) // options, unpacked
	inner = append(inner, 0x22, 0x40)
	inner = append(inner, op.Ballot.DkSig...)
	raw := append([]byte{TagDelegateOp, 0x1a, byte(len(inner))}, inner...)
	var unpacked types.MsgDelegateOp
	require.NoError(t, protov2.Unmarshal(raw[1:], &unpacked))
	require.True(t, protov2.Equal(op, &unpacked), "the unpacked body is the same message")
	_, _, err = DecodeVoteTx(raw)
	require.ErrorContains(t, err, "non-canonical atomic vote message protobuf encoding")
}
