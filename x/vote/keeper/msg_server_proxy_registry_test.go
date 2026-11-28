package keeper_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// register (contracts-v1 §5.3)
// ---------------------------------------------------------------------------

// TestRegister checks a registration: it takes next_index, which moves on,
// writes the entry as ACTIVE with its key and handle records, and emits
// delegate_registered.
func (s *ProxyRegistryTestSuite) TestRegister() {
	s.openRegistry()
	dk := proxytest.Key("alice/1")
	s.ctx = s.ctx.WithEventManager(sdk.NewEventManager())
	resp, err := s.deliver(s.registerOp(newEntryRegistration(dk, "alice"), 1))
	s.Require().NoError(err)
	s.Require().Equal(uint32(1), resp.DelegateIndex)

	s.Require().True(proto.Equal(&types.ProxyDelegateEntry{
		Index:            1,
		Dk:               proxytest.Pub(dk),
		Provider:         types.DelegateProviderX,
		Handle:           "alice",
		ProofId:          "1790000000000000000",
		RegisteredHeight: 100,
	}, s.entry(1)))
	s.Require().Equal(uint32(2), s.nextIndex())
	s.Require().Equal(uint32(1), s.usedKeyOwner(dk))
	s.Require().Equal(uint32(1), s.handleOwner("alice"))

	s.Require().Equal(map[string]string{
		types.AttributeKeyDelegateIndex: "1",
		types.AttributeKeyProvider:      "1",
		types.AttributeKeyHandle:        "alice",
		types.AttributeKeyProofID:       "1790000000000000000",
	}, eventAttributes(s.T(), s.ctx.EventManager().Events(), types.EventTypeDelegateRegistered))

	s.Require().Equal(uint32(2), s.registerNew(proxytest.Key("bob/1"), "bob"))
	s.Require().Equal(uint32(3), s.nextIndex())
}

// TestRegisterRejectsReusedKey checks that a key is never registered twice
// (§5.3 step 2): not for another handle, and not after its entry retired.
func (s *ProxyRegistryTestSuite) TestRegisterRejectsReusedKey() {
	s.openRegistry()
	k1 := proxytest.Key("alice/1")
	d := s.registerNew(k1, "alice")

	_, err := s.deliver(s.registerOp(newEntryRegistration(k1, "bob"), 1))
	proxytest.RequireCode(s.T(), err, types.ErrDelegateKeyReused, 61)

	s.mustDeliver(s.retireOp(d, k1))
	_, err = s.deliver(s.registerOp(newEntryRegistration(k1, "alice"), 1))
	proxytest.RequireCode(s.T(), err, types.ErrDelegateKeyReused, 61)
	s.Require().Equal(uint32(2), s.nextIndex())
}

// TestRegisterOneLiveEntryPerHandle checks register step 3: a handle with an
// ACTIVE or SUSPENDED entry cannot be registered again, whatever the key, so
// a hijacked account cannot add one; the same name at the other provider is
// another handle; and once the entry retires the handle registers again
// under a new index.
func (s *ProxyRegistryTestSuite) TestRegisterOneLiveEntryPerHandle() {
	s.openRegistry()
	k1, k2 := proxytest.Key("alice/1"), proxytest.Key("alice/2")
	d := s.registerNew(k1, "alice")

	again := s.registerOp(newEntryRegistration(k2, "alice"), 1)
	_, err := s.deliver(again)
	proxytest.RequireCode(s.T(), err, types.ErrHandleTaken, 62)
	s.setStatus(d, types.DelegateStatusSuspended, 1)
	_, err = s.deliver(again)
	proxytest.RequireCode(s.T(), err, types.ErrHandleTaken, 62)

	github := proxytest.Registration(proxytest.Key("alice/github"), "alice")
	github.Provider, github.ProofId = types.DelegateProviderGitHub, "0123abcd"
	s.Require().Equal(uint32(2), s.mustDeliver(proxytest.RegisterOp(s.ctx.ChainID(), github, proxytest.Key("alice/github"), s.blockTime()-60, s.blockTime()+60, 1)))

	s.setStatus(d, types.DelegateStatusRetired, 2)
	s.Require().Equal(uint32(3), s.mustDeliver(again))
	s.Require().Equal(uint32(3), s.handleOwner("alice"))
	s.Require().Equal(types.DelegateStatusRetired, s.entry(d).Status)
}

// TestRegisterRegistryCap checks that a registration needs next_index <=
// max_delegates (§5.3 step 6). Retired entries still count.
func (s *ProxyRegistryTestSuite) TestRegisterRegistryCap() {
	key := func(i int) ed25519.PrivateKey { return proxytest.Key(fmt.Sprintf("cap/%d", i)) }
	s.setParams(1, 2, 1)
	s.Require().Equal(uint32(1), s.registerNew(key(1), "a"))
	s.Require().Equal(uint32(2), s.registerNew(key(2), "b"))
	_, err := s.deliver(s.registerOp(newEntryRegistration(key(3), "c"), 1))
	proxytest.RequireCode(s.T(), err, types.ErrRegistryFull, 60)
	s.Require().Equal(uint32(3), s.nextIndex())

	s.mustDeliver(s.retireOp(1, key(1)))
	_, err = s.deliver(s.registerOp(newEntryRegistration(key(3), "c"), 1))
	proxytest.RequireCode(s.T(), err, types.ErrRegistryFull, 60)

	s.setParams(1, 3, 1)
	s.Require().Equal(uint32(3), s.registerNew(key(3), "c"))
}

// TestRegisterThresholdCountsDistinctVerifiers checks register step 5's
// count: attestations are deduplicated by verifier id and pubkey before they
// meet verifier_threshold.
func (s *ProxyRegistryTestSuite) TestRegisterThresholdCountsDistinctVerifiers() {
	for i, c := range []struct {
		name string
		ids  []uint32
		ok   bool
	}{
		{"one verifier twice", []uint32{1, 1}, false},
		{"two verifiers", []uint32{1, 2}, true},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.setParams(2, 100, 1, 2, 3)
			_, err := s.deliver(s.registerOp(newEntryRegistration(proxytest.Key(fmt.Sprintf("threshold/%d", i)), "alice"), c.ids...))
			if c.ok {
				s.Require().NoError(err)
			} else {
				proxytest.RequireCode(s.T(), err, types.ErrInsufficientAttestations, 65)
			}
		})
	}

	s.Run("two ids sharing one key", func() {
		s.SetupTest()
		shared := proxytest.Verifier(1)
		s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), &types.ProxyDelegationParams{
			Verifiers: []*types.ProxyVerifier{
				{VerifierId: 1, Pubkey: proxytest.Pub(shared)},
				{VerifierId: 2, Pubkey: proxytest.Pub(shared)},
			},
			VerifierThreshold: 2,
			MaxDelegates:      100,
		}))
		r := newEntryRegistration(proxytest.Key("shared"), "alice")
		op := s.registerOp(r, 1)
		second := proxytest.Attestation(s.ctx.ChainID(), 1, r.fields(), s.blockTime()-60, s.blockTime()+60)
		second.VerifierId = 2 // signed with the key that params give id 2
		op.Register.Attestations = append(op.Register.Attestations, second)
		_, err := s.deliver(op)
		proxytest.RequireCode(s.T(), err, types.ErrInsufficientAttestations, 65)
	})
}

// TestRegisterAttestationWindow checks not_before <= blockTime < expires_at
// in FinalizeBlock (§5.3 step 5).
func (s *ProxyRegistryTestSuite) TestRegisterAttestationWindow() {
	notBefore, expiresAt := proxyTestTime, proxyTestTime+3600
	for _, c := range []struct {
		name      string
		blockTime uint64
		ok        bool
	}{
		{"before not_before", notBefore - 1, false},
		{"at not_before", notBefore, true},
		{"at expires_at", expiresAt, false},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.openRegistry()
			s.setBlockTime(c.blockTime)
			r := newEntryRegistration(proxytest.Key("window"), "alice")
			op := s.registerOp(r)
			op.Register.Attestations = []*types.DelegateAttestation{proxytest.Attestation(s.ctx.ChainID(), 1, r.fields(), notBefore, expiresAt)}
			_, err := s.deliver(op)
			if c.ok {
				s.Require().NoError(err)
			} else {
				proxytest.RequireCode(s.T(), err, types.ErrInvalidAttestation, 64)
			}
		})
	}
}

// TestRegisterRejectsInvalidAttestations checks that every attestation must
// name a params verifier and carry its signature over this op's ATTEST, even
// when the valid ones would meet the threshold (§5.3 step 5).
func (s *ProxyRegistryTestSuite) TestRegisterRejectsInvalidAttestations() {
	r := newEntryRegistration(proxytest.Key("alice/1"), "alice")
	for _, c := range []struct {
		name   string
		mutate func(op *types.DelegateRegister)
	}{
		{"unknown verifier", func(op *types.DelegateRegister) { op.Attestations[0].VerifierId = 7 }},
		{"signed by another verifier", func(op *types.DelegateRegister) {
			op.Attestations[0].Sig = proxytest.Attestation(s.ctx.ChainID(), 2, r.fields(), op.Attestations[0].NotBefore, op.Attestations[0].ExpiresAt).Sig
		}},
		{"attests another key", func(op *types.DelegateRegister) {
			other := r
			other.dk = proxytest.Key("mallory")
			op.Attestations[0].Sig = proxytest.Attestation(s.ctx.ChainID(), 1, other.fields(), op.Attestations[0].NotBefore, op.Attestations[0].ExpiresAt).Sig
		}},
		{"window not the one signed", func(op *types.DelegateRegister) { op.Attestations[0].NotBefore-- }},
		{"signed for another chain", func(op *types.DelegateRegister) {
			digest, err := types.ComputeDelegateAttestDigest("svote-1", r.fields(), op.Attestations[0].NotBefore, op.Attestations[0].ExpiresAt)
			s.Require().NoError(err)
			op.Attestations[0].Sig = ed25519.Sign(proxytest.Verifier(1), digest)
		}},
		{"a valid attestation and an invalid one", func(op *types.DelegateRegister) {
			invalid := proxytest.Attestation(s.ctx.ChainID(), 9, r.fields(), op.Attestations[0].NotBefore, op.Attestations[0].ExpiresAt)
			op.Attestations = append(op.Attestations, invalid)
		}},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.setParams(1, 100, 1, 2)
			op := s.registerOp(r, 1)
			c.mutate(op.Register)
			_, err := s.deliver(op)
			proxytest.RequireCode(s.T(), err, types.ErrInvalidAttestation, 64)
			s.requireNoRegistrations()
		})
	}
}

// TestRegisterClosedWithoutVerifiers checks register step 1: with no params,
// or params without verifiers, registration is closed.
func (s *ProxyRegistryTestSuite) TestRegisterClosedWithoutVerifiers() {
	op := s.registerOp(newEntryRegistration(proxytest.Key("alice/1"), "alice"), 1)
	_, err := s.deliver(op)
	proxytest.RequireCode(s.T(), err, types.ErrRegistrationClosed, 59)

	s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), &types.ProxyDelegationParams{MaxDelegates: 100}))
	_, err = s.deliver(op)
	proxytest.RequireCode(s.T(), err, types.ErrRegistrationClosed, 59)

	s.openRegistry()
	s.Require().Equal(uint32(1), s.mustDeliver(op))
}

// TestRegisterRejectsInvalidDelegateKey checks the key rule on dk (§0, §5.3
// step 2) with the shared ed25519_keys vectors' identity key. The rule comes
// before dk_sig, so the identity key is refused although ZIP-215 accepts its
// signature over any message. The types vector tests run every key_rule case.
func (s *ProxyRegistryTestSuite) TestRegisterRejectsInvalidDelegateKey() {
	var keyCases struct {
		ZIP215 []struct {
			Name   string `json:"name"`
			Inputs struct {
				PublicKey string `json:"public_key"`
				Signature string `json:"signature"`
			} `json:"inputs"`
		} `json:"zip215"`
	}
	var sections map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(svtest.ProxyDelegationV1Vectors(), &sections))
	s.Require().NoError(json.Unmarshal(sections["ed25519_keys"], &keyCases))

	found := false
	for _, c := range keyCases.ZIP215 {
		if c.Name != "identity_key_accepts_any_message" {
			continue
		}
		found = true
		s.openRegistry()
		identity := routingVectorHex(s.T(), c.Inputs.PublicKey)
		sig := routingVectorHex(s.T(), c.Inputs.Signature)
		r := newEntryRegistration(proxytest.Key("alice/1"), "alice")
		fields := r.fields()
		fields.Dk = identity
		digest, err := types.ComputeDelegateRegisterDigest(s.ctx.ChainID(), fields)
		s.Require().NoError(err)
		s.Require().NoError(types.VerifyEd25519ZIP215(identity, digest, sig), "ZIP-215 accepts the forgery")

		op := s.registerOp(r)
		op.Register.Dk, op.Register.DkSig = identity, sig
		op.Register.Attestations = []*types.DelegateAttestation{proxytest.Attestation(s.ctx.ChainID(), 1, fields, s.blockTime(), s.blockTime()+60)}
		_, err = s.deliver(op)
		proxytest.RequireCode(s.T(), err, types.ErrInvalidDelegateKey, 66)
	}
	s.Require().True(found)
}

// TestRegisterRejectsInvalidDkSig checks register step 4: dk must sign this
// op's REGISTER.
func (s *ProxyRegistryTestSuite) TestRegisterRejectsInvalidDkSig() {
	r := newEntryRegistration(proxytest.Key("alice/1"), "alice")
	signOther := func(chainID string, f *types.DelegateRegister, key ed25519.PrivateKey) []byte {
		digest, err := types.ComputeDelegateRegisterDigest(chainID, f)
		s.Require().NoError(err)
		return ed25519.Sign(key, digest)
	}
	for _, c := range []struct {
		name  string
		dkSig []byte
	}{
		{"signed by another key", signOther(proxyTestChainID, r.fields(), proxytest.Key("mallory"))},
		{"signed for another chain", signOther("svote-1", r.fields(), r.dk)},
		{"scalar not canonical", append(bytes.Clone(signOther(proxyTestChainID, r.fields(), r.dk)[:32]), bytes.Repeat([]byte{0xff}, 32)...)},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.openRegistry()
			op := s.registerOp(r, 1)
			op.Register.DkSig = c.dkSig
			_, err := s.deliver(op)
			proxytest.RequireCode(s.T(), err, types.ErrInvalidSignature, 6)
			s.requireNoRegistrations()
		})
	}
}

// TestRegisterCheckOrder checks that when several register checks fail, the
// first in §5.3's order decides the code.
func (s *ProxyRegistryTestSuite) TestRegisterCheckOrder() {
	k1, k2 := proxytest.Key("alice/1"), proxytest.Key("alice/2")
	identity := append([]byte{1}, make([]byte, 31)...)
	garbageSig := bytes.Repeat([]byte{0x07}, 64)
	for _, c := range []struct {
		name     string
		setup    func()
		op       func() *types.MsgDelegateOp
		wantErr  *errorsmod.Error
		wantCode uint32
	}{
		{
			name:  "closed before the key rule",
			setup: func() {},
			op: func() *types.MsgDelegateOp {
				op := s.registerOp(newEntryRegistration(k1, "alice"), 1)
				op.Register.Dk = identity
				return op
			},
			wantErr: types.ErrRegistrationClosed, wantCode: 59,
		},
		{
			name:  "key rule before dk_sig",
			setup: s.openRegistry,
			op: func() *types.MsgDelegateOp {
				op := s.registerOp(newEntryRegistration(k1, "alice"), 1)
				op.Register.Dk, op.Register.DkSig = identity, garbageSig
				return op
			},
			wantErr: types.ErrInvalidDelegateKey, wantCode: 66,
		},
		{
			name:    "reused key before the handle",
			setup:   func() { s.openRegistry(); s.registerNew(k1, "alice") },
			op:      func() *types.MsgDelegateOp { return s.registerOp(newEntryRegistration(k1, "alice"), 1) },
			wantErr: types.ErrDelegateKeyReused, wantCode: 61,
		},
		{
			name:  "handle before dk_sig",
			setup: func() { s.openRegistry(); s.registerNew(k1, "alice") },
			op: func() *types.MsgDelegateOp {
				op := s.registerOp(newEntryRegistration(k2, "alice"), 1)
				op.Register.DkSig = garbageSig
				return op
			},
			wantErr: types.ErrHandleTaken, wantCode: 62,
		},
		{
			name:  "dk_sig before attestations",
			setup: s.openRegistry,
			op: func() *types.MsgDelegateOp {
				op := s.registerOp(newEntryRegistration(k1, "alice"), 1)
				op.Register.DkSig = garbageSig
				op.Register.Attestations[0].VerifierId = 9
				return op
			},
			wantErr: types.ErrInvalidSignature, wantCode: 6,
		},
		{
			name:    "attestations before the registry cap",
			setup:   func() { s.setParams(1, 0, 1) },
			op:      func() *types.MsgDelegateOp { return s.registerOp(newEntryRegistration(k1, "alice"), 9) },
			wantErr: types.ErrInvalidAttestation, wantCode: 64,
		},
		{
			name:    "threshold before the registry cap",
			setup:   func() { s.setParams(2, 0, 1, 2) },
			op:      func() *types.MsgDelegateOp { return s.registerOp(newEntryRegistration(k1, "alice"), 1) },
			wantErr: types.ErrInsufficientAttestations, wantCode: 65,
		},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			c.setup()
			_, err := s.deliver(c.op())
			proxytest.RequireCode(s.T(), err, c.wantErr, c.wantCode)
		})
	}
}

// ---------------------------------------------------------------------------
// retire (contracts-v1 §5.3)
// ---------------------------------------------------------------------------

// TestRetire checks that retire sets the entry RETIRED with reason 0, frees
// its handle, keeps its key record and emits delegate_retired; and that a
// retried retire is ErrDelegateRetired (55), which a client reads as success
// (§6).
func (s *ProxyRegistryTestSuite) TestRetire() {
	s.openRegistry()
	k1 := proxytest.Key("alice/1")
	d := s.registerNew(k1, "alice")

	s.ctx = s.ctx.WithEventManager(sdk.NewEventManager())
	retire := s.retireOp(d, k1)
	s.Require().Equal(d, s.mustDeliver(retire))
	entry := s.entry(d)
	s.Require().Equal(types.DelegateStatusRetired, entry.Status)
	s.Require().Zero(entry.StatusReason)
	s.Require().Zero(s.handleOwner("alice"))
	s.Require().Equal(d, s.usedKeyOwner(k1))
	s.Require().Equal(map[string]string{types.AttributeKeyDelegateIndex: "1"},
		eventAttributes(s.T(), s.ctx.EventManager().Events(), types.EventTypeDelegateRetired))

	_, err := s.deliver(retire)
	proxytest.RequireCode(s.T(), err, types.ErrDelegateRetired, 55)
}

// TestRetireChecks checks the retire checks and their order: the entry
// exists (53), is not RETIRED (55) or SUSPENDED (54), so a suspension is not
// escaped by registering again, and dk_sig is the entry's key over this
// op's RETIRE (6).
func (s *ProxyRegistryTestSuite) TestRetireChecks() {
	s.openRegistry()
	k1, k2 := proxytest.Key("alice/1"), proxytest.Key("bob/1")
	alice, bob := s.registerNew(k1, "alice"), s.registerNew(k2, "bob")

	_, err := s.deliver(s.retireOp(9, k1))
	proxytest.RequireCode(s.T(), err, types.ErrDelegateNotFound, 53)

	for name, op := range map[string]*types.MsgDelegateOp{
		"another entry's key":      s.retireOp(alice, k2),
		"signed for another entry": {Retire: &types.DelegateRetire{DelegateIndex: alice, DkSig: s.retireOp(bob, k1).Retire.DkSig}},
		"signed for another chain": proxytest.RetireOp("svote-1", alice, k1),
	} {
		_, err := s.deliver(op)
		proxytest.RequireCode(s.T(), err, types.ErrInvalidSignature, 6)
		s.Require().Equal(types.DelegateStatusActive, s.entry(alice).Status, name)
	}

	// The status decides before the signature.
	garbage := &types.MsgDelegateOp{Retire: &types.DelegateRetire{DelegateIndex: alice, DkSig: bytes.Repeat([]byte{0x07}, 64)}}
	s.setStatus(alice, types.DelegateStatusSuspended, 1)
	_, err = s.deliver(garbage)
	proxytest.RequireCode(s.T(), err, types.ErrDelegateSuspended, 54)
	_, err = s.deliver(s.retireOp(alice, k1))
	proxytest.RequireCode(s.T(), err, types.ErrDelegateSuspended, 54)
	s.Require().Equal(alice, s.handleOwner("alice"))

	s.setStatus(alice, types.DelegateStatusRetired, 1)
	_, err = s.deliver(garbage)
	proxytest.RequireCode(s.T(), err, types.ErrDelegateRetired, 55)
}

// ---------------------------------------------------------------------------
// The handler around the ops
// ---------------------------------------------------------------------------

// TestDelegateOpHandlerValidateBasic checks that the handler runs
// ValidateBasic: a malformed op is ErrInvalidField (9) and writes nothing.
// proxy_dormancy_test.go covers the closed gate.
func (s *ProxyRegistryTestSuite) TestDelegateOpHandlerValidateBasic() {
	s.openRegistry()
	malformed := s.registerOp(newEntryRegistration(proxytest.Key("alice/1"), "alice"), 1)
	malformed.Register.Provider = 3
	_, err := s.deliver(malformed)
	proxytest.RequireCode(s.T(), err, types.ErrInvalidField, 9)
	s.requireNoRegistrations()
}

// TestValidateDelegateOpSignatures checks that ValidateDelegateOp verifies
// signatures only when asked, as RecheckTx skips them, while every state
// check still runs, and that it never writes.
func (s *ProxyRegistryTestSuite) TestValidateDelegateOpSignatures() {
	s.openRegistry()
	k1 := proxytest.Key("alice/1")
	op := s.registerOp(newEntryRegistration(k1, "alice"), 1)
	op.Register.DkSig = bytes.Repeat([]byte{0x07}, 64)
	op.Register.Attestations[0].Sig = bytes.Repeat([]byte{0x07}, 64)
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(s.ctx, op, true), types.ErrInvalidSignature, 6)
	s.Require().NoError(s.keeper.ValidateDelegateOp(s.ctx, op, false))
	s.requireNoRegistrations()

	s.registerNew(k1, "bob")
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(s.ctx, op, false), types.ErrDelegateKeyReused, 61)

	retire := s.retireOp(1, k1)
	retire.Retire.DkSig = bytes.Repeat([]byte{0x07}, 64)
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(s.ctx, retire, true), types.ErrInvalidSignature, 6)
	s.Require().NoError(s.keeper.ValidateDelegateOp(s.ctx, retire, false))
	s.Require().Equal(types.DelegateStatusActive, s.entry(1).Status)
}

// TestValidateDelegateOpUnsetBlockTime checks the attestation window after a
// restart, when CheckTx runs with the header time unset: CheckTx and
// RecheckTx skip the window, FinalizeBlock never does (§5.3 step 5).
func (s *ProxyRegistryTestSuite) TestValidateDelegateOpUnsetBlockTime() {
	s.openRegistry()
	r := newEntryRegistration(proxytest.Key("alice/1"), "alice")
	op := s.registerOp(r)
	op.Register.Attestations = []*types.DelegateAttestation{proxytest.Attestation(s.ctx.ChainID(), 1, r.fields(), 10, 20)}
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(s.ctx, op, true), types.ErrInvalidAttestation, 64)

	unset := s.ctx.WithBlockTime(time.Time{})
	s.Require().Negative(unset.BlockTime().Unix())
	s.Require().NoError(s.keeper.ValidateDelegateOp(unset.WithIsCheckTx(true), op, true))
	s.Require().NoError(s.keeper.ValidateDelegateOp(unset.WithIsReCheckTx(true), op, false))
	proxytest.RequireCode(s.T(), s.keeper.ValidateDelegateOp(unset, op, true), types.ErrInvalidAttestation, 64)
	s.requireNoRegistrations()
}
