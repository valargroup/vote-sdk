package ante_test

import (
	"bytes"
	"crypto/ed25519"
	"time"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests run 0x0B register and retire ops through the ante with the
// dormancy gate open (docs/proxy-delegation/contracts-v1.md §5.3): every
// failure keeps its registered code in CheckTx, RecheckTx and FinalizeBlock,
// RecheckTx skips only the Ed25519 checks, and the ante never writes.

const (
	// proxyAnteChainID is the chain these ops sign for.
	proxyAnteChainID = "zvote-1"
	// proxyAnteTime is the block time of these tests.
	proxyAnteTime uint64 = 1_790_000_000
)

// proxyCtx returns the suite's context on chain zvote-1 at proxyAnteTime, in
// FinalizeBlock mode.
func (s *ValidateTestSuite) proxyCtx() sdk.Context {
	return s.ctx.WithChainID(proxyAnteChainID).WithBlockTime(time.Unix(int64(proxyAnteTime), 0).UTC())
}

// setupProxyRegistry stores a registry with room for one more entry: params
// with verifiers 1 and 2 at the given threshold and max_delegates, entry 1
// (X, "alice") with key alice/1, and entry 2 (GitHub, "bob") with key bob/1.
func (s *ValidateTestSuite) setupProxyRegistry(threshold, maxDelegates uint32) {
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetProxyDelegationParams(kv, &types.ProxyDelegationParams{
		Verifiers: []*types.ProxyVerifier{
			{VerifierId: 1, Pubkey: proxytest.Pub(proxytest.Verifier(1))},
			{VerifierId: 2, Pubkey: proxytest.Pub(proxytest.Verifier(2))},
		},
		VerifierThreshold: threshold,
		MaxDelegates:      maxDelegates,
	}))
	for _, entry := range []*types.ProxyDelegateEntry{{
		Index:    1,
		Dk:       proxytest.Pub(proxytest.Key("alice/1")),
		Provider: types.DelegateProviderX,
		Handle:   "alice",
		ProofId:  "1790000000000000000",
	}, {
		Index:    2,
		Dk:       proxytest.Pub(proxytest.Key("bob/1")),
		Provider: types.DelegateProviderGitHub,
		Handle:   "bob",
		ProofId:  "0123abcd",
	}} {
		s.Require().NoError(s.keeper.SetProxyDelegateEntry(kv, entry))
		s.Require().NoError(s.keeper.SetProxyUsedDelegateKey(kv, entry.Dk, entry.Index))
		s.Require().NoError(s.keeper.SetProxyDelegateByHandle(kv, entry.Provider, entry.Handle, entry.Index))
	}
	s.Require().NoError(s.keeper.SetProxyNextDelegateIndex(kv, 3))
}

// proxyRetireOp builds the op retiring entry index, signed by its key dk.
func proxyRetireOp(index uint32, dk ed25519.PrivateKey) *types.MsgDelegateOp {
	return proxytest.RetireOp(proxyAnteChainID, index, dk)
}

// newCarolRegistration is a valid registration for the setupProxyRegistry
// state, attested by verifier 1 over [proxyAnteTime - 60, proxyAnteTime + 60).
func newCarolRegistration() *types.MsgDelegateOp {
	dk := proxytest.Key("carol/1")
	return proxytest.RegisterOp(proxyAnteChainID, proxytest.Registration(dk, "carol"), dk, proxyAnteTime-60, proxyAnteTime+60, 1)
}

// registryValues returns every registry key and value, to show the ante
// leaves them alone.
func (s *ValidateTestSuite) registryValues() map[string][]byte {
	return proxytest.Values(s.T(), s.keeper.OpenKVStore(s.ctx), []byte{0x1A})
}

// TestDelegateOpAnteAcceptsValidOps checks that valid register and retire ops
// pass CheckTx, RecheckTx and the FinalizeBlock ante without writing.
func (s *ValidateTestSuite) TestDelegateOpAnteAcceptsValidOps() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyRegistry(1, 3)
	before := s.registryValues()
	ops := map[string]*types.MsgDelegateOp{
		"register": newCarolRegistration(),
		"retire":   proxyRetireOp(1, proxytest.Key("alice/1")),
	}
	modes := map[string]struct {
		ctx  sdk.Context
		opts ante.ValidateOpts
	}{
		"check":    {s.proxyCtx().WithIsCheckTx(true), mockOpts()},
		"recheck":  {s.proxyCtx().WithIsReCheckTx(true), recheckOpts()},
		"finalize": {s.proxyCtx(), mockOpts()},
	}
	for opName, op := range ops {
		for modeName, mode := range modes {
			s.Run(opName+"/"+modeName, func() {
				s.Require().NoError(ante.ValidateVoteTx(mode.ctx, op, s.keeper, mode.opts))
			})
		}
	}
	s.Require().Equal(before, s.registryValues())
}

// TestDelegateOpAnteCodes checks that a register or retire op's failure
// keeps its registered code in codespace vote through the ante (§0, §6): in
// CheckTx and FinalizeBlock always, and in RecheckTx unless it is an Ed25519
// failure, which RecheckTx does not check. The keeper suite covers each
// check and its order through keeper.ValidateDelegateOp.
func (s *ValidateTestSuite) TestDelegateOpAnteCodes() {
	proxytest.SkipUnlessEnabled(s.T())
	alice := proxytest.Key("alice/1")
	garbageSig := bytes.Repeat([]byte{0x07}, 64)
	for _, c := range []struct {
		name        string
		noVerifiers bool
		op          func() *types.MsgDelegateOp
		want        *errorsmod.Error
		code        uint32
		signature   bool // an Ed25519 failure, which RecheckTx skips
	}{
		{name: "registration closed", noVerifiers: true, op: newCarolRegistration, want: types.ErrRegistrationClosed, code: 59},
		{name: "bad dk_sig", op: func() *types.MsgDelegateOp {
			op := newCarolRegistration()
			op.Register.DkSig = garbageSig
			return op
		}, want: types.ErrInvalidSignature, code: 6, signature: true},
		{name: "unknown verifier", op: func() *types.MsgDelegateOp {
			op := newCarolRegistration()
			op.Register.Attestations[0].VerifierId = 7
			return op
		}, want: types.ErrInvalidAttestation, code: 64},
		{name: "bad attestation signature", op: func() *types.MsgDelegateOp {
			op := newCarolRegistration()
			op.Register.Attestations[0].Sig = garbageSig
			return op
		}, want: types.ErrInvalidAttestation, code: 64, signature: true},
		{name: "bad retire signature", op: func() *types.MsgDelegateOp {
			op := proxyRetireOp(1, alice)
			op.Retire.DkSig = garbageSig
			return op
		}, want: types.ErrInvalidSignature, code: 6, signature: true},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.setupProxyRegistry(1, 3)
			if c.noVerifiers {
				s.Require().NoError(s.keeper.SetProxyDelegationParams(s.keeper.OpenKVStore(s.ctx), &types.ProxyDelegationParams{MaxDelegates: 3}))
			}
			before := s.registryValues()
			op := c.op()

			requireCode := func(err error) { proxytest.RequireCode(s.T(), err, c.want, c.code) }
			requireCode(ante.ValidateVoteTx(s.proxyCtx().WithIsCheckTx(true), op, s.keeper, mockOpts()))
			requireCode(ante.ValidateVoteTx(s.proxyCtx(), op, s.keeper, mockOpts()))
			recheckErr := ante.ValidateVoteTx(s.proxyCtx().WithIsReCheckTx(true), op, s.keeper, recheckOpts())
			if c.signature {
				s.Require().NoError(recheckErr)
			} else {
				requireCode(recheckErr)
			}
			s.Require().Equal(before, s.registryValues())
		})
	}
}
