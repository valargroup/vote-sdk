package keeper_test

import (
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover the proxy delegate registry
// (docs/proxy-delegation/contracts-v1.md §3.2, §5.3, §7, §8, §9) with
// the dormancy gate open, so they run only under -tags proxyenabled.

const (
	// proxyTestChainID is the chain the shared vectors sign for (§13).
	proxyTestChainID = "zvote-1"
	// proxyTestTime is the suite's first block time, inside the vector
	// attestations' windows.
	proxyTestTime uint64 = 1_790_000_000
	// proxyTestDay is one day in seconds.
	proxyTestDay uint64 = 86_400
)

// ProxyRegistryTestSuite runs registry ops against a fresh store.
type ProxyRegistryTestSuite struct {
	suite.Suite
	ctx       sdk.Context
	keeper    *keeper.Keeper
	msgServer types.MsgServer
	queries   types.QueryServer
}

// TestProxyRegistryTestSuite runs the registry suite, which needs the gate
// open.
func TestProxyRegistryTestSuite(t *testing.T) {
	proxytest.SkipUnlessEnabled(t)
	suite.Run(t, new(ProxyRegistryTestSuite))
}

// SetupTest gives each test an empty store on chain zvote-1 at height 100
// and proxyTestTime.
func (s *ProxyRegistryTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)
	s.ctx = testCtx.Ctx.WithChainID(proxyTestChainID).WithBlockHeight(100)
	s.setBlockTime(proxyTestTime)
	s.keeper = keeper.NewKeeper(runtime.NewKVStoreService(key), svtest.TestAuthority, log.NewNopLogger(), nil, nil)
	s.msgServer = keeper.NewMsgServerImpl(s.keeper)
	s.queries = keeper.NewQueryServerImpl(s.keeper)
}

// kv opens the vote store at the current context.
func (s *ProxyRegistryTestSuite) kv() store.KVStore {
	return s.keeper.OpenKVStore(s.ctx)
}

// blockTime returns the current block time in unix seconds.
func (s *ProxyRegistryTestSuite) blockTime() uint64 {
	return uint64(s.ctx.BlockTime().Unix())
}

// setBlockTime moves the context to unix time t.
func (s *ProxyRegistryTestSuite) setBlockTime(t uint64) {
	s.ctx = s.ctx.WithBlockTime(time.Unix(int64(t), 0).UTC())
}

// setParams stores params listing verifiers verifierIDs, keyed by
// proxytest.Verifier, with the given threshold and max_delegates.
func (s *ProxyRegistryTestSuite) setParams(threshold, maxDelegates uint32, verifierIDs ...uint32) {
	params := &types.ProxyDelegationParams{VerifierThreshold: threshold, MaxDelegates: maxDelegates}
	for _, id := range verifierIDs {
		params.Verifiers = append(params.Verifiers, &types.ProxyVerifier{VerifierId: id, Pubkey: proxytest.Pub(proxytest.Verifier(id))})
	}
	s.Require().NoError(types.ValidateProxyDelegationParams(params))
	s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), params))
}

// openRegistry stores params with verifier 1 at threshold 1 and room for 100
// delegates.
func (s *ProxyRegistryTestSuite) openRegistry() {
	s.setParams(1, 100, 1)
}

// proxyRegistration is a register op before signing: dk for the X account
// handle.
type proxyRegistration struct {
	dk     ed25519.PrivateKey
	handle string
}

// newEntryRegistration registers dk for the X account handle.
func newEntryRegistration(dk ed25519.PrivateKey, handle string) proxyRegistration {
	return proxyRegistration{dk: dk, handle: handle}
}

// fields returns the REGISTER and ATTEST fields of r.
func (r proxyRegistration) fields() *types.DelegateRegister {
	return proxytest.Registration(r.dk, r.handle)
}

// registerOp builds r's op with dk_sig over REGISTER and one attestation per
// verifier id, each valid from an hour before to an hour after the block
// time.
func (s *ProxyRegistryTestSuite) registerOp(r proxyRegistration, verifierIDs ...uint32) *types.MsgDelegateOp {
	return proxytest.RegisterOp(s.ctx.ChainID(), r.fields(), r.dk, s.blockTime()-3600, s.blockTime()+3600, verifierIDs...)
}

// retireOp builds the op retiring entry index, signed by its key dk.
func (s *ProxyRegistryTestSuite) retireOp(index uint32, dk ed25519.PrivateKey) *types.MsgDelegateOp {
	return proxytest.RetireOp(s.ctx.ChainID(), index, dk)
}

// deliver runs op through the enabled handler as FinalizeBlock does: in a
// cache whose writes and events reach s.ctx only on success.
func (s *ProxyRegistryTestSuite) deliver(op *types.MsgDelegateOp) (*types.MsgDelegateOpResponse, error) {
	cacheCtx, write := s.ctx.CacheContext()
	resp, err := s.msgServer.DelegateOp(cacheCtx, op)
	if err == nil {
		write()
	}
	return resp, err
}

// mustDeliver runs op and returns the index it touched.
func (s *ProxyRegistryTestSuite) mustDeliver(op *types.MsgDelegateOp) uint32 {
	resp, err := s.deliver(op)
	s.Require().NoError(err)
	return resp.DelegateIndex
}

// registerNew registers dk for the X account handle, attested by verifier 1,
// and returns the new entry's index.
func (s *ProxyRegistryTestSuite) registerNew(dk ed25519.PrivateKey, handle string) uint32 {
	return s.mustDeliver(s.registerOp(newEntryRegistration(dk, handle), 1))
}

// setStatus stores entry index with the given status and reason, as a
// coordinator payload would leave it, keeping its handle record in step.
func (s *ProxyRegistryTestSuite) setStatus(index, status, reason uint32) {
	entry := s.entry(index)
	entry.Status, entry.StatusReason = status, reason
	kv := s.kv()
	s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, entry))
	if status == types.DelegateStatusRetired {
		s.Require().NoError(kv.Delete(types.ProxyDelegateHandleKey(entry.Provider, entry.Handle)))
	} else {
		s.Require().NoError(s.keeper.SetProxyDelegateByHandle(kv, entry.Provider, entry.Handle, index))
	}
}

// entry returns stored entry index.
func (s *ProxyRegistryTestSuite) entry(index uint32) *types.ProxyDelegateEntry {
	entry, err := s.keeper.GetProxyDelegateEntry(s.kv(), index)
	s.Require().NoError(err)
	return entry
}

// usedKeyOwner returns the entry dk was registered to, failing if it never
// was.
func (s *ProxyRegistryTestSuite) usedKeyOwner(dk ed25519.PrivateKey) uint32 {
	index, found, err := s.keeper.GetProxyUsedDelegateKey(s.kv(), proxytest.Pub(dk))
	s.Require().NoError(err)
	s.Require().True(found)
	return index
}

// handleOwner returns the live entry of the X account handle, or 0 if it has
// none.
func (s *ProxyRegistryTestSuite) handleOwner(handle string) uint32 {
	index, _, err := s.keeper.GetProxyDelegateByHandle(s.kv(), types.DelegateProviderX, handle)
	s.Require().NoError(err)
	return index
}

// nextIndex returns the stored next_index.
func (s *ProxyRegistryTestSuite) nextIndex() uint32 {
	next, err := s.keeper.GetProxyNextDelegateIndex(s.kv())
	s.Require().NoError(err)
	return next
}

// seedEntries stores n ACTIVE GitHub entries with fresh keys at indices
// from, from+1, ..., with their key and handle records, and sets next_index
// after them, as earlier registrations would have.
func (s *ProxyRegistryTestSuite) seedEntries(from, n uint32) {
	kv := s.kv()
	for index := from; index < from+n; index++ {
		dk := proxytest.Pub(proxytest.Key(fmt.Sprintf("seeded/%d", index)))
		handle := fmt.Sprintf("seeded-%d", index)
		s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, &types.ProxyDelegateEntry{
			Index:    index,
			Dk:       dk,
			Provider: types.DelegateProviderGitHub,
			Handle:   handle,
			ProofId:  "0123456789abcdef",
		}))
		s.Require().NoError(s.keeper.SetProxyUsedDelegateKey(kv, dk, index))
		s.Require().NoError(s.keeper.SetProxyDelegateByHandle(kv, types.DelegateProviderGitHub, handle, index))
	}
	s.Require().NoError(s.keeper.SetProxyNextDelegateIndex(kv, from+n))
}

// requireNoRegistryKeys fails if the store holds any key under 0x1A.
func (s *ProxyRegistryTestSuite) requireNoRegistryKeys() {
	s.requireNoKeysUnder([]byte{0x1A})
}

// requireNoRegistrations fails if the store holds an entry, a used key, a
// handle record or next_index, as no failed op may write.
func (s *ProxyRegistryTestSuite) requireNoRegistrations() {
	for _, prefix := range [][]byte{types.ProxyDelegateEntryPrefix, types.ProxyUsedDelegateKeyPrefix, types.ProxyDelegateHandlePrefix, types.ProxyNextDelegateIndexKey} {
		s.requireNoKeysUnder(prefix)
	}
}

// requireNoKeysUnder fails if the store holds a key that starts with prefix.
func (s *ProxyRegistryTestSuite) requireNoKeysUnder(prefix []byte) {
	iter, err := s.kv().Iterator(prefix, types.PrefixEndBytes(prefix))
	s.Require().NoError(err)
	defer iter.Close()
	if iter.Valid() {
		s.Failf("unexpected key", "key %x under prefix %x", iter.Key(), prefix)
	}
}

// eventAttributes returns the attributes of the only event of type
// eventType, failing unless exactly one was emitted.
func eventAttributes(t *testing.T, events sdk.Events, eventType string) map[string]string {
	t.Helper()
	var found []sdk.Event
	for _, event := range events {
		if event.Type == eventType {
			found = append(found, event)
		}
	}
	require.Len(t, found, 1, "events of type %s", eventType)
	attributes := make(map[string]string, len(found[0].Attributes))
	for _, attribute := range found[0].Attributes {
		attributes[attribute.Key] = attribute.Value
	}
	return attributes
}
