package types_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// proxyProtoFields transcribes the proxy delegation messages of
// docs/proxy-delegation/contracts-v1.md §2, §3.1 and §8, one line per field.
var proxyProtoFields = map[protoreflect.FullName][]string{
	"svote.v1.ProxyDelegationAction": {
		"bytes van_nullifier = 1", "bytes r_vpk = 2", "bytes vote_authority_note_new = 3",
		"bytes proxy_delegation_commitment = 4", "bytes dc_slot_nullifier = 5", "bytes proof = 6",
		"bytes vote_auth_sig = 7",
	},
	"svote.v1.MsgProxyDelegationBatch": {
		"bytes vote_round_id = 1", "uint64 vote_comm_tree_anchor_height = 2", "MsgDelegateVote registration = 3",
		"repeated ProxyDelegationAction dcs = 4", "uint64 expiry_height = 5",
	},
	"svote.v1.MsgProxyDelegationBatchResponse": {"bytes batch_digest = 1"},
	"svote.v1.DelegateAttestation": {
		"uint32 verifier_id = 1", "uint64 not_before = 2", "uint64 expires_at = 3", "bytes sig = 4",
	},
	"svote.v1.DelegateRegister": {
		"bytes dk = 1", "uint32 provider = 2", "string handle = 3", "string proof_id = 4",
		"repeated DelegateAttestation attestations = 5", "bytes dk_sig = 6",
	},
	"svote.v1.DelegateRetire": {"uint32 delegate_index = 1", "bytes dk_sig = 2"},
	"svote.v1.DelegateBallot": {
		"bytes vote_round_id = 1", "uint32 delegate_index = 2", "repeated uint32 options = 3", "bytes dk_sig = 4",
	},
	"svote.v1.MsgDelegateOp": {
		"DelegateRegister register = 1", "DelegateRetire retire = 2", "DelegateBallot ballot = 3",
	},
	"svote.v1.MsgDelegateOpResponse": {"uint32 delegate_index = 1"},
	"svote.v1.ProxyVerifier":         {"uint32 verifier_id = 1", "bytes pubkey = 2"},
	"svote.v1.MsgSetProxyDelegationParams": {
		"string creator = 1", "repeated ProxyVerifier verifiers = 2", "uint32 verifier_threshold = 3",
		"uint32 max_delegates = 4",
	},
	"svote.v1.MsgSetProxyDelegationPause": {"string creator = 1", "bool paused = 2"},
	"svote.v1.MsgSetDelegateStatus": {
		"string creator = 1", "repeated uint32 delegate_indexes = 2", "uint32 action = 3", "uint32 reason = 4",
	},
	"svote.v1.ProxyDelegationRoundState": {
		"bool enabled = 1", "uint32 next_delegate_index_at_creation = 2", "bool pools_routed = 3",
	},
	"svote.v1.ProxyDelegateEntry": {
		"uint32 index = 1", "bytes dk = 2", "uint32 provider = 3", "string handle = 4", "string proof_id = 5",
		"uint32 status = 6", "uint32 status_reason = 7", "uint64 registered_height = 8",
	},
	"svote.v1.DelegateBallotRecord": {
		"bytes vote_round_id = 1", "uint32 delegate_index = 2", "repeated uint32 options = 3", "uint64 height = 4",
	},
	"svote.v1.ProxyDelegationParams": {
		"repeated ProxyVerifier verifiers = 1", "uint32 verifier_threshold = 2", "uint32 max_delegates = 3",
		"bool proxy_dc_paused = 4",
	},
	"svote.v1.QueryProxyDelegatesRequest":         {"uint32 start_after = 1", "uint32 limit = 2"},
	"svote.v1.QueryProxyDelegatesResponse":        {"repeated ProxyDelegateEntry delegates = 1", "uint32 next_start_after = 2"},
	"svote.v1.QueryProxyDelegateRequest":          {"uint32 delegate_index = 1"},
	"svote.v1.QueryProxyDelegateResponse":         {"ProxyDelegateEntry delegate = 1"},
	"svote.v1.QueryProxyDelegateByKeyRequest":     {"bytes dk = 1"},
	"svote.v1.QueryProxyDelegateByKeyResponse":    {"ProxyDelegateEntry delegate = 1"},
	"svote.v1.QueryProxyDelegateByHandleRequest":  {"uint32 provider = 1", "string handle = 2"},
	"svote.v1.QueryProxyDelegateByHandleResponse": {"ProxyDelegateEntry delegate = 1"},
	"svote.v1.QueryDelegateBallotsRequest": {
		"bytes vote_round_id = 1", "uint32 start_after = 2", "uint32 limit = 3",
	},
	"svote.v1.QueryDelegateBallotsResponse":       {"repeated DelegateBallotRecord ballots = 1", "uint32 next_start_after = 2"},
	"svote.v1.QueryProxyDelegationParamsRequest":  {},
	"svote.v1.QueryProxyDelegationParamsResponse": {"ProxyDelegationParams params = 1", "uint32 next_delegate_index = 2"},
	"svote.v1.CircuitFingerprint":                 {"string name = 1", "bytes vk_blake2b = 2"},
}

// proxyProtoAddedFields are the fields existing messages gain (contracts-v1
// §2.3, §3.1, §8, §9).
var proxyProtoAddedFields = map[protoreflect.FullName][]string{
	"svote.v1.MsgCreateVotingSession": {"bool proxy_delegation = 12"},
	"svote.v1.VoteRound":              {"ProxyDelegationRoundState proxy_delegation = 31"},
	"svote.v1.GenesisState": {
		"repeated ProxyDelegateEntry proxy_delegates = 19", "repeated DelegateBallotRecord proxy_delegate_ballots = 20",
		"ProxyDelegationParams proxy_params = 21",
	},
	"svote.v1.QueryProtocolCapabilitiesResponse": {
		"uint32 proxy_delegation_version = 4", "repeated CircuitFingerprint circuit_fingerprints = 5",
	},
}

// fieldLine renders fd as its .proto declaration, without options.
func fieldLine(fd protoreflect.FieldDescriptor) string {
	typeName := fd.Kind().String()
	if fd.Message() != nil {
		typeName = string(fd.Message().Name())
	}
	line := fmt.Sprintf("%s %s = %d", typeName, fd.Name(), fd.Number())
	if fd.IsList() {
		line = "repeated " + line
	}
	return line
}

// findProxyMessage returns the registered descriptor named name.
func findProxyMessage(t *testing.T, name protoreflect.FullName) protoreflect.MessageType {
	t.Helper()
	mt, err := protoregistry.GlobalTypes.FindMessageByName(name)
	require.NoError(t, err, "%s is not generated", name)
	return mt
}

// TestProxyDelegationProtosMatchContract checks every new message and field,
// and the new RPCs, against the contract's declarations. REST decodes these
// messages with encoding/json, so none may use a oneof (§0).
func TestProxyDelegationProtosMatchContract(t *testing.T) {
	for name, want := range proxyProtoFields {
		desc := findProxyMessage(t, name).Descriptor()
		fields := desc.Fields()
		got := make([]string, 0, fields.Len())
		for i := 0; i < fields.Len(); i++ {
			got = append(got, fieldLine(fields.Get(i)))
		}
		require.Equal(t, want, got, "%s", name)
		require.Zero(t, desc.Oneofs().Len(), "%s must not use a oneof", name)
	}
	for name, want := range proxyProtoAddedFields {
		fields := findProxyMessage(t, name).Descriptor().Fields()
		for _, line := range want {
			number, err := strconv.Atoi(line[strings.LastIndex(line, " = ")+3:])
			require.NoError(t, err)
			fd := fields.ByNumber(protoreflect.FieldNumber(number))
			require.NotNil(t, fd, "%s has no field %d", name, number)
			require.Equal(t, line, fieldLine(fd), "%s", name)
		}
	}

	rpcs := map[protoreflect.ServiceDescriptor][]string{
		types.File_svote_v1_tx_proto.Services().ByName("Msg"): {
			"ProxyDelegationBatch(MsgProxyDelegationBatch) returns (MsgProxyDelegationBatchResponse)",
			"DelegateOp(MsgDelegateOp) returns (MsgDelegateOpResponse)",
		},
		types.File_svote_v1_query_proto.Services().ByName("Query"): {
			"ProxyDelegates(QueryProxyDelegatesRequest) returns (QueryProxyDelegatesResponse)",
			"ProxyDelegate(QueryProxyDelegateRequest) returns (QueryProxyDelegateResponse)",
			"ProxyDelegateByKey(QueryProxyDelegateByKeyRequest) returns (QueryProxyDelegateByKeyResponse)",
			"ProxyDelegateByHandle(QueryProxyDelegateByHandleRequest) returns (QueryProxyDelegateByHandleResponse)",
			"DelegateBallots(QueryDelegateBallotsRequest) returns (QueryDelegateBallotsResponse)",
			"ProxyDelegationParams(QueryProxyDelegationParamsRequest) returns (QueryProxyDelegationParamsResponse)",
		},
	}
	for service, want := range rpcs {
		for _, rpc := range want {
			name := protoreflect.Name(rpc[:strings.IndexByte(rpc, '(')])
			method := service.Methods().ByName(name)
			require.NotNil(t, method, "%s has no RPC %s", service.FullName(), name)
			got := fmt.Sprintf("%s(%s) returns (%s)", method.Name(), method.Input().Name(), method.Output().Name())
			require.Equal(t, rpc, got)
		}
	}
}
