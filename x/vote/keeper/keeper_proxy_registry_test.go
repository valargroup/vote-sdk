package keeper_test

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestRegistryStoreDefaults checks what an empty registry reads as
// (contracts-v1 §3.2, §9): next_index 1, zero params, no entries, used keys
// or handle records, and that reading writes nothing.
func (s *ProxyRegistryTestSuite) TestRegistryStoreDefaults() {
	kv := s.kv()
	next, err := s.keeper.GetProxyNextDelegateIndex(kv)
	s.Require().NoError(err)
	s.Require().Equal(uint32(1), next)

	params, err := s.keeper.GetProxyDelegationParams(kv)
	s.Require().NoError(err)
	s.Require().True(proto.Equal(&types.ProxyDelegationParams{}, params), "absent params read as the zero value: %v", params)

	_, err = s.keeper.GetProxyDelegateEntry(kv, 1)
	proxytest.RequireCode(s.T(), err, types.ErrDelegateNotFound, 53)
	_, err = s.keeper.GetProxyDelegateEntry(kv, 0)
	proxytest.RequireCode(s.T(), err, types.ErrDelegateNotFound, 53)

	_, found, err := s.keeper.GetProxyUsedDelegateKey(kv, make([]byte, 32))
	s.Require().NoError(err)
	s.Require().False(found)
	_, _, err = s.keeper.GetProxyUsedDelegateKey(kv, make([]byte, 31))
	proxytest.RequireCode(s.T(), err, types.ErrInvalidField, 9)
	_, found, err = s.keeper.GetProxyDelegateByHandle(kv, types.DelegateProviderX, "alice")
	s.Require().NoError(err)
	s.Require().False(found)

	s.requireNoRegistryKeys()
}

// TestRegistryStoreLayout checks each registry value's key and encoding
// (contracts-v1 §3.2) and that it reads back.
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

	gotEntry, err := s.keeper.GetProxyDelegateEntry(kv, 300)
	s.Require().NoError(err)
	s.Require().True(proto.Equal(entry, gotEntry))
	owner, found, err := s.keeper.GetProxyUsedDelegateKey(kv, dk)
	s.Require().NoError(err)
	s.Require().True(found)
	s.Require().Equal(uint32(300), owner)
	owner, found, err = s.keeper.GetProxyDelegateByHandle(kv, entry.Provider, entry.Handle)
	s.Require().NoError(err)
	s.Require().True(found)
	s.Require().Equal(uint32(300), owner)
	s.Require().Equal(uint32(301), s.nextIndex())
	gotParams, err := s.keeper.GetProxyDelegationParams(kv)
	s.Require().NoError(err)
	s.Require().True(proto.Equal(params, gotParams))
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

// TestIterateProxyDelegateEntries checks that entries come in ascending
// index order, strictly after startAfter, and that the callback can stop.
func (s *ProxyRegistryTestSuite) TestIterateProxyDelegateEntries() {
	kv := s.kv()
	for _, index := range []uint32{65_536, 1, 256, 1 << 30, 2, 255} {
		s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, &types.ProxyDelegateEntry{Index: index}))
	}
	collect := func(startAfter uint32, limit int) []uint32 {
		var got []uint32
		s.Require().NoError(s.keeper.IterateProxyDelegateEntries(kv, startAfter, func(entry *types.ProxyDelegateEntry) bool {
			got = append(got, entry.Index)
			return len(got) == limit
		}))
		return got
	}
	s.Require().Equal([]uint32{1, 2, 255, 256, 65_536, 1 << 30}, collect(0, -1))
	s.Require().Equal([]uint32{255, 256, 65_536, 1 << 30}, collect(2, -1))
	s.Require().Equal([]uint32{256, 65_536}, collect(255, 2))
	s.Require().Empty(collect(1<<30, -1))
	s.Require().Empty(collect(math.MaxUint32, -1))
}

// TestRegistryStoreRejectsCorruptValues checks that a used-key or next_index
// value that is not four bytes is an error, not a silent misread.
func (s *ProxyRegistryTestSuite) TestRegistryStoreRejectsCorruptValues() {
	kv := s.kv()
	dk := proxytest.Pub(proxytest.Key("corrupt"))
	usedKey, err := types.ProxyUsedDelegateKeyKey(dk)
	s.Require().NoError(err)
	s.Require().NoError(kv.Set(usedKey, []byte{1, 2, 3}))
	_, _, err = s.keeper.GetProxyUsedDelegateKey(kv, dk)
	s.Require().ErrorContains(err, "expected 4")

	s.Require().NoError(kv.Set(types.ProxyNextDelegateIndexKey, binary.BigEndian.AppendUint64(nil, 2)))
	_, err = s.keeper.GetProxyNextDelegateIndex(kv)
	s.Require().ErrorContains(err, "expected 4")
}

// routingVectorHex decodes a lowercase hex field of a vector file.
func routingVectorHex(t *testing.T, s string) []byte {
	t.Helper()
	require.Equal(t, strings.ToLower(s), s, "hex is lowercase")
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}
