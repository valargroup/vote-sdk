package keeper_test

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestRegistryStoreLayout checks each registry value's key and encoding
// (contracts-v1 §3.2).
func (s *ProxyRegistryTestSuite) TestRegistryStoreLayout() {
	kv := s.kv()
	dk := proxytest.Pub(proxytest.Key("layout"))
	entry := &types.ProxyDelegateEntry{
		Index:            300,
		Dk:               dk,
		Provider:         types.DelegateProviderGitHub,
		Handle:           "layout",
		ProofId:          "0123abcd",
		Status:           types.DelegateStatusSuspended,
		StatusReason:     4,
		RegisteredHeight: 7,
	}
	params := &types.ProxyDelegationParams{
		Verifiers:         []*types.ProxyVerifier{{VerifierId: 9, Pubkey: proxytest.Pub(proxytest.Verifier(9))}},
		VerifierThreshold: 1,
		MaxDelegates:      2_000,
		ProxyDcPaused:     true,
	}
	s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, entry))
	s.Require().NoError(s.keeper.SetProxyUsedDelegateKey(kv, dk, 300))
	s.Require().NoError(s.keeper.SetProxyDelegateByHandle(kv, entry.Provider, entry.Handle, 300))
	s.Require().NoError(s.keeper.SetProxyNextDelegateIndex(kv, 301))
	s.Require().NoError(s.keeper.SetProxyDelegationParams(kv, params))

	deterministic := func(m proto.Message) []byte {
		bz, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		s.Require().NoError(err)
		return bz
	}
	for _, c := range []struct {
		key, value []byte
	}{
		{[]byte{0x1A, 0x01, 0, 0, 0x01, 0x2c}, deterministic(entry)},
		{append([]byte{0x1A, 0x02}, dk...), []byte{0, 0, 0x01, 0x2c}},
		{append([]byte{0x1A, 0x03, 0x02}, "layout"...), []byte{0, 0, 0x01, 0x2c}},
		{[]byte{0x1A, 0x04}, []byte{0, 0, 0x01, 0x2d}},
		{[]byte{0x1A, 0x06}, deterministic(params)},
	} {
		got, err := kv.Get(c.key)
		s.Require().NoError(err)
		s.Require().Equal(c.value, got, "value at %x", c.key)
	}
}

// TestRegistryEntryValueVectors checks that the keeper stores every
// entry_value case of the chain vectors as exactly its expected bytes, the
// value wallets prove and decode (contracts-v1 §11.6, §13), and reads it
// back. This is the one entry_value check; the file's hash pins its cases.
func (s *ProxyRegistryTestSuite) TestRegistryEntryValueVectors() {
	var file struct {
		EntryValue []struct {
			Name   string `json:"name"`
			Inputs struct {
				Index            uint32 `json:"index"`
				DK               string `json:"dk"`
				Provider         uint32 `json:"provider"`
				Handle           string `json:"handle"`
				ProofID          string `json:"proof_id"`
				Status           uint32 `json:"status"`
				StatusReason     uint32 `json:"status_reason"`
				RegisteredHeight uint64 `json:"registered_height"`
			} `json:"inputs"`
			Expected struct {
				Value string `json:"value"`
			} `json:"expected"`
		} `json:"entry_value"`
	}
	s.Require().NoError(json.Unmarshal(svtest.ProxyDelegationChainV1Vectors(), &file))
	s.Require().NotEmpty(file.EntryValue)

	for _, c := range file.EntryValue {
		s.Run(c.Name, func() {
			in := c.Inputs
			entry := &types.ProxyDelegateEntry{
				Index:            in.Index,
				Dk:               routingVectorHex(s.T(), in.DK),
				Provider:         in.Provider,
				Handle:           in.Handle,
				ProofId:          in.ProofID,
				Status:           in.Status,
				StatusReason:     in.StatusReason,
				RegisteredHeight: in.RegisteredHeight,
			}
			s.Require().NoError(types.ValidateEd25519Key(entry.Dk), "every stored key passes the key rule")
			kv := s.kv()
			s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, entry))
			stored, err := kv.Get(types.ProxyDelegateEntryKey(in.Index))
			s.Require().NoError(err)
			s.Require().Equal(c.Expected.Value, fmt.Sprintf("%x", stored))

			got, err := s.keeper.GetProxyDelegateEntry(kv, in.Index)
			s.Require().NoError(err)
			s.Require().True(proto.Equal(entry, got))
		})
	}
}
