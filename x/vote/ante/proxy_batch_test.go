package ante_test

import (
	"bytes"
	"errors"
	"fmt"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"golang.org/x/crypto/blake2b"

	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/tx1"
	"github.com/valargroup/vote-sdk/ffi/votetree"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests run 0x09 batches through the ante with the dormancy gate open
// (docs/proxy-delegation/contracts-v1.md §5.1): what the ante adds to
// keeper.CheckProxyDelegationBatch, whose order the keeper suite covers. That
// is ValidateBasic first, the signatures and proofs after every state check
// and in order, RecheckTx skipping them, the admission height in each mode,
// registered codes, the D1 signatures and the ZKP4 root chain.

const (
	// proxyBatchAnteHeight is the admission height: the FinalizeBlock
	// height, and one above the last committed height CheckTx sees.
	proxyBatchAnteHeight = 200
	// proxyBatchAnteAnchor is the anchor height of anchored batches.
	proxyBatchAnteAnchor = 40
)

// proxyBatchAnteRoot is the tree root stored at proxyBatchAnteAnchor.
var proxyBatchAnteRoot = svtest.FpLE(0xA0C)

// proxyMode is one way the ante runs a batch.
type proxyMode struct {
	name    string
	ctx     sdk.Context
	recheck bool
}

// proxyModes returns CheckTx and RecheckTx with the header height 0, as just
// after a restart, and FinalizeBlock at proxyBatchAnteHeight.
func (s *ValidateTestSuite) proxyModes() []proxyMode {
	return []proxyMode{
		{name: "check", ctx: s.proxyCtx().WithIsCheckTx(true)},
		{name: "recheck", ctx: s.proxyCtx().WithIsReCheckTx(true), recheck: true},
		{name: "finalize", ctx: s.proxyCtx().WithBlockHeight(proxyBatchAnteHeight)},
	}
}

// lastCommittedAt returns a LastCommittedHeight that reports height.
func lastCommittedAt(height int64) func() int64 {
	return func() int64 { return height }
}

// proxyOpts returns m's options with the given verifiers, admitting at
// proxyBatchAnteHeight.
func proxyOpts(m proxyMode, sig redpallas.Verifier, zk zkp.Verifier) ante.ValidateOpts {
	return ante.ValidateOpts{
		IsRecheck:           m.recheck,
		SigVerifier:         sig,
		ZKPVerifier:         zk,
		LastCommittedHeight: lastCommittedAt(proxyBatchAnteHeight - 1),
	}
}

// setupProxyBatchRound stores proxyAnteRound, changed by mutate, with an EA
// key and proxyBatchAnteRoot at proxyBatchAnteAnchor (see setupProxyRound).
func (s *ValidateTestSuite) setupProxyBatchRound(mutate func(*types.VoteRound)) {
	s.setupProxyRound(func(r *types.VoteRound) {
		r.EaPk = append([]byte(nil), svtest.DummyPallasPoint...)
		mutate(r)
	})
	s.Require().NoError(s.keeper.SetCommitmentRootAtHeight(s.keeper.OpenKVStore(s.ctx), proxyAnteRound, proxyBatchAnteAnchor, proxyBatchAnteRoot))
}

// proxyAnteDC returns a DC whose fields derive from seed, as canonical Fp
// encodings.
func proxyAnteDC(seed uint64) *types.ProxyDelegationAction {
	return &types.ProxyDelegationAction{
		VanNullifier:              svtest.FpLE(seed<<8 | 1),
		RVpk:                      append([]byte(nil), svtest.DummyPallasPoint...),
		VoteAuthorityNoteNew:      svtest.FpLE(seed<<8 | 2),
		ProxyDelegationCommitment: svtest.FpLE(seed<<8 | 3),
		DcSlotNullifier:           svtest.FpLE(seed<<8 | 4),
		Proof:                     []byte("mock-zkp4-proof"),
		VoteAuthSig:               bytes.Repeat([]byte{byte(seed)}, 64),
	}
}

// proxyAnteBatch returns a batch of n DCs in proxyAnteRound expiring at
// proxyBatchAnteHeight + 10, anchored at proxyBatchAnteAnchor or, with
// registration, carrying one at anchor 0.
func proxyAnteBatch(n int, registration bool) *types.MsgProxyDelegationBatch {
	msg := &types.MsgProxyDelegationBatch{
		VoteRoundId:              append([]byte(nil), proxyAnteRound...),
		VoteCommTreeAnchorHeight: proxyBatchAnteAnchor,
		ExpiryHeight:             proxyBatchAnteHeight + 10,
	}
	for i := range n {
		msg.Dcs = append(msg.Dcs, proxyAnteDC(uint64(0x10+i)))
	}
	if registration {
		msg.VoteCommTreeAnchorHeight = 0
		msg.Registration = svtest.ValidDelegation(proxyAnteRound, 0x51)
	}
	return msg
}

// proxyBatchWrites returns every nullifier key and the round's tree state,
// to show the ante writes neither.
func (s *ValidateTestSuite) proxyBatchWrites() []string {
	kv := s.keeper.OpenKVStore(s.ctx)
	iter, err := kv.Iterator(types.NullifierPrefix, types.PrefixEndBytes(types.NullifierPrefix))
	s.Require().NoError(err)
	defer iter.Close()
	var writes []string
	for ; iter.Valid(); iter.Next() {
		writes = append(writes, fmt.Sprintf("%x", iter.Key()))
	}
	tree, err := s.keeper.GetCommitmentTreeState(kv, proxyAnteRound)
	s.Require().NoError(err)
	return append(writes, fmt.Sprintf("tree %d", tree.NextIndex))
}

// requireVoteCode fails unless err is want with its registered code in
// codespace vote.
func (s *ValidateTestSuite) requireVoteCode(err error, want *errorsmod.Error) {
	proxytest.RequireCode(s.T(), err, want, want.ABCICode())
}

// proxyCallLog records the order of signature and proof checks.
type proxyCallLog struct{ calls []string }

// proxySigVerifier records each signature check in log, with its key and
// digest, and fails the failAt-th one (0-based), or none when negative.
type proxySigVerifier struct {
	log     *proxyCallLog
	failAt  int
	keys    [][]byte
	digests [][]byte
}

// Verify records the check and fails it when it is the failAt-th.
func (v *proxySigVerifier) Verify(rk, digest, _ []byte) error {
	call := len(v.digests)
	v.log.calls = append(v.log.calls, "sig")
	v.keys = append(v.keys, bytes.Clone(rk))
	v.digests = append(v.digests, bytes.Clone(digest))
	if call == v.failAt {
		return fmt.Errorf("mock signature failure at call %d", call)
	}
	return nil
}

// proxyZKPVerifier records ZKP #1 and ZKP #4 checks in log, with their
// inputs, and fails the kinds named in fail ("zkp1", "zkp4").
type proxyZKPVerifier struct {
	log        *proxyCallLog
	fail       map[string]bool
	delegation []zkp.DelegationInputs
	proxy      []zkp.ProxyDelegationInputs
}

// VerifyDelegation records a ZKP #1 check.
func (v *proxyZKPVerifier) VerifyDelegation(_ []byte, inputs zkp.DelegationInputs) error {
	v.log.calls = append(v.log.calls, "zkp1")
	v.delegation = append(v.delegation, inputs)
	if v.fail["zkp1"] {
		return errors.New("mock ZKP #1 failure")
	}
	return nil
}

// VerifyVoteCommitment is never reached by a 0x09 batch.
func (v *proxyZKPVerifier) VerifyVoteCommitment([]byte, zkp.VoteCommitmentInputs) error {
	return errors.New("unexpected ZKP #2")
}

// VerifyVoteShare is never reached by a 0x09 batch.
func (v *proxyZKPVerifier) VerifyVoteShare([]byte, zkp.VoteShareInputs) error {
	return errors.New("unexpected ZKP #3")
}

// VerifyProxyDelegation records a ZKP #4 check.
func (v *proxyZKPVerifier) VerifyProxyDelegation(_ []byte, inputs zkp.ProxyDelegationInputs) error {
	v.log.calls = append(v.log.calls, "zkp4")
	v.proxy = append(v.proxy, inputs)
	if v.fail["zkp4"] {
		return errors.New("mock ZKP #4 failure")
	}
	return nil
}

// newProxyVerifiers returns a signature and a proof verifier sharing one
// call log; the first fails its sigFailAt-th check (0-based; negative fails
// none) and the second the kinds in zkpFail.
func newProxyVerifiers(sigFailAt int, zkpFail ...string) (*proxySigVerifier, *proxyZKPVerifier, *proxyCallLog) {
	log := &proxyCallLog{}
	fail := map[string]bool{}
	for _, kind := range zkpFail {
		fail[kind] = true
	}
	return &proxySigVerifier{log: log, failAt: sigFailAt}, &proxyZKPVerifier{log: log, fail: fail}, log
}

// TestProxyBatchAnteAcceptsValidBatch checks that valid batches, with and
// without a registration, pass CheckTx, RecheckTx and FinalizeBlock without
// writing.
func (s *ValidateTestSuite) TestProxyBatchAnteAcceptsValidBatch() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	before := s.proxyBatchWrites()
	for _, registration := range []bool{false, true} {
		for _, m := range s.proxyModes() {
			s.Run(fmt.Sprintf("%s/registration=%t", m.name, registration), func() {
				msg := proxyAnteBatch(types.MaxProxyDelegationDCs, registration)
				s.Require().NoError(ante.ValidateVoteTx(m.ctx, msg, s.keeper, proxyOpts(m, redpallas.NewMockVerifier(), zkp.NewMockVerifier())))
			})
		}
	}
	s.Require().Equal(before, s.proxyBatchWrites())
}

// TestProxyBatchAnteCodes checks the ante's own steps in contracts-v1 §5.1
// order: ValidateBasic before any state check, every state check (here the
// last, tree capacity) before any signature or proof, then steps 6 to 8.
// Each case fails one check while every later check would fail too, and the
// ante answers the earlier check's registered code in CheckTx and
// FinalizeBlock. RecheckTx runs steps 1 to 5 and skips steps 6 to 8.
func (s *ValidateTestSuite) TestProxyBatchAnteCodes() {
	proxytest.SkipUnlessEnabled(s.T())
	kv := func() storeSetter { return storeSetter{s} }
	for _, c := range []struct {
		name         string
		registration bool
		setup        func(msg *types.MsgProxyDelegationBatch)
		failSig      int // fails the failSig-th signature check, from 1; 0 fails none
		zkpFail      []string
		want         *errorsmod.Error
		crypto       bool // a step 6-8 failure, which RecheckTx skips
		contains     string
	}{
		{name: "intra-message van nullifier", setup: func(msg *types.MsgProxyDelegationBatch) {
			msg.Dcs[1].VanNullifier = msg.Dcs[0].VanNullifier
			msg.VoteRoundId = testRoundID
		}, want: types.ErrInvalidField, contains: "repeats a van_nullifier"},
		{name: "tree full", setup: func(*types.MsgProxyDelegationBatch) {
			kv().fillTree(0)
		}, failSig: 1, zkpFail: []string{"zkp1", "zkp4"}, want: types.ErrCommitmentTreeFull},
		{name: "r_vpk off the curve", setup: func(msg *types.MsgProxyDelegationBatch) {
			msg.Dcs[1].RVpk = bytes.Repeat([]byte{0xFF}, 32)
		}, zkpFail: []string{"zkp4"}, want: types.ErrInvalidSignature, crypto: true, contains: "dcs[1] r_vpk"},
		{name: "DC signature", failSig: 2, zkpFail: []string{"zkp4"}, want: types.ErrInvalidSignature, crypto: true, contains: "dcs[1]"},
		{name: "registration signature", registration: true, failSig: 3, zkpFail: []string{"zkp1", "zkp4"}, want: types.ErrInvalidSignature, crypto: true, contains: "registration"},
		{name: "registration proof", registration: true, zkpFail: []string{"zkp1", "zkp4"}, want: types.ErrInvalidProof, crypto: true, contains: "registration"},
		{name: "no root at the anchor", setup: func(msg *types.MsgProxyDelegationBatch) {
			msg.VoteCommTreeAnchorHeight = proxyBatchAnteAnchor + 1
		}, zkpFail: []string{"zkp4"}, want: types.ErrInvalidAnchorHeight, crypto: true},
		{name: "DC proof", zkpFail: []string{"zkp4"}, want: types.ErrInvalidProof, crypto: true, contains: "dcs[0]"},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.setupProxyBatchRound(func(*types.VoteRound) {})
			msg := proxyAnteBatch(2, c.registration)
			if c.setup != nil {
				c.setup(msg)
			}
			before := s.proxyBatchWrites()

			for _, m := range s.proxyModes() {
				sig, zk, _ := newProxyVerifiers(c.failSig-1, c.zkpFail...)
				err := ante.ValidateVoteTx(m.ctx, msg, s.keeper, proxyOpts(m, sig, zk))
				if m.recheck && c.crypto {
					s.Require().NoError(err, m.name)
					s.Require().Empty(sig.digests, "RecheckTx checks no signature")
					s.Require().Empty(zk.proxy, "RecheckTx checks no proof")
					continue
				}
				s.requireVoteCode(err, c.want)
				if c.contains != "" {
					s.Require().ErrorContains(err, c.contains, m.name)
				}
			}
			s.Require().Equal(before, s.proxyBatchWrites())
		})
	}
}

// storeSetter writes the state the capacity cases need.
type storeSetter struct{ s *ValidateTestSuite }

// fillTree leaves room in proxyAnteRound's tree for exactly room more
// leaves.
func (w storeSetter) fillTree(room uint64) {
	w.s.Require().NoError(w.s.keeper.SetCommitmentTreeState(w.s.keeper.OpenKVStore(w.s.ctx), proxyAnteRound,
		&types.CommitmentTreeState{NextIndex: types.ProxyMaxTreePosition + 1 - room}))
}

// TestProxyBatchAnteAdmissionHeight checks the height h that expiry_height
// is checked against (contracts-v1 §5.1 step 4): the block height in
// FinalizeBlock, and in CheckTx and RecheckTx the last committed height plus
// 1 from the ante options, whatever the header height says, including 0
// just after a restart.
func (s *ValidateTestSuite) TestProxyBatchAnteAdmissionHeight() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	msg := proxyAnteBatch(1, false)
	msg.ExpiryHeight = 101

	for _, c := range []struct {
		name      string
		ctx       sdk.Context
		recheck   bool
		committed int64 // the store's last committed height
		expired   bool
	}{
		{name: "finalize below expiry", ctx: s.proxyCtx().WithBlockHeight(100)},
		{name: "finalize at expiry", ctx: s.proxyCtx().WithBlockHeight(101), expired: true},
		{name: "check after restart below expiry", ctx: s.proxyCtx().WithIsCheckTx(true), committed: 99},
		{name: "check after restart at expiry", ctx: s.proxyCtx().WithIsCheckTx(true), committed: 100, expired: true},
		{name: "check ignores a high header", ctx: s.proxyCtx().WithIsCheckTx(true).WithBlockHeight(500), committed: 99},
		{name: "check ignores a low header", ctx: s.proxyCtx().WithIsCheckTx(true).WithBlockHeight(50), committed: 100, expired: true},
		{name: "recheck after restart below expiry", ctx: s.proxyCtx().WithIsReCheckTx(true), recheck: true, committed: 99},
		{name: "recheck after restart at expiry", ctx: s.proxyCtx().WithIsReCheckTx(true), recheck: true, committed: 100, expired: true},
	} {
		s.Run(c.name, func() {
			opts := ante.ValidateOpts{
				IsRecheck:           c.recheck,
				SigVerifier:         redpallas.NewMockVerifier(),
				ZKPVerifier:         zkp.NewMockVerifier(),
				LastCommittedHeight: lastCommittedAt(c.committed),
			}
			err := ante.ValidateVoteTx(c.ctx, msg, s.keeper, opts)
			if c.expired {
				s.requireVoteCode(err, types.ErrProxyBatchExpired)
				return
			}
			s.Require().NoError(err)
		})
	}

	// Without the store height, CheckTx refuses rather than guess.
	opts := mockOpts()
	s.Require().Error(ante.ValidateVoteTx(s.proxyCtx().WithIsCheckTx(true), msg, s.keeper, opts))
	s.Require().NoError(ante.ValidateVoteTx(s.proxyCtx().WithBlockHeight(100), msg, s.keeper, opts))
}

// TestProxyBatchAnteRecheckSkipsCrypto checks that RecheckTx skips every
// signature and proof, which CheckTx checks.
func (s *ValidateTestSuite) TestProxyBatchAnteRecheckSkipsCrypto() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	msg := proxyAnteBatch(3, true)
	for _, m := range s.proxyModes() {
		err := ante.ValidateVoteTx(m.ctx, msg, s.keeper, proxyOpts(m, errSigVerifier{}, errZKPVerifier{}))
		if m.recheck {
			s.Require().NoError(err)
			continue
		}
		s.requireVoteCode(err, types.ErrInvalidSignature)
	}
}

// TestProxyBatchAnteRootChain checks the ZKP4 public inputs (contracts-v1
// §5.1 step 8): root0 is the tree root at the anchor, or with a registration
// the single-leaf root of its van_cmx; each later root is the single-leaf
// root of the previous DC's successor VAN; every DC proves at the batch's
// anchor height, 0 with a registration; and the ZKP1 check comes after
// every signature and before any ZKP4.
func (s *ValidateTestSuite) TestProxyBatchAnteRootChain() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	singleLeafRoot := func(leaf []byte) []byte {
		root, err := votetree.SingleLeafRoot(leaf)
		s.Require().NoError(err)
		return root
	}
	for _, registration := range []bool{false, true} {
		s.Run(fmt.Sprintf("registration=%t", registration), func() {
			msg := proxyAnteBatch(3, registration)
			sig, zk, log := newProxyVerifiers(-1)
			m := s.proxyModes()[2]
			s.Require().NoError(ante.ValidateVoteTx(m.ctx, msg, s.keeper, proxyOpts(m, sig, zk)))

			wantRoot, wantAnchor := proxyBatchAnteRoot, uint64(proxyBatchAnteAnchor)
			wantCalls := []string{"sig", "sig", "sig", "zkp4", "zkp4", "zkp4"}
			if registration {
				wantRoot, wantAnchor = singleLeafRoot(msg.Registration.VanCmx), 0
				wantCalls = []string{"sig", "sig", "sig", "sig", "zkp1", "zkp4", "zkp4", "zkp4"}
				s.Require().Len(zk.delegation, 1)
				s.Require().Equal(msg.Registration.VanCmx, zk.delegation[0].VanCmx)
			}
			s.Require().Equal(wantCalls, log.calls)
			s.Require().Len(zk.proxy, 3)
			for i, in := range zk.proxy {
				dc := msg.Dcs[i]
				s.Require().Equal(wantRoot, in.VoteCommTreeRoot, "dcs[%d] root", i)
				s.Require().Equal(wantAnchor, in.AnchorHeight, "dcs[%d] anchor", i)
				s.Require().Equal(zkp.ProxyDelegationInputs{
					VanNullifier:              dc.VanNullifier,
					RVpk:                      dc.RVpk,
					VoteAuthorityNoteNew:      dc.VoteAuthorityNoteNew,
					ProxyDelegationCommitment: dc.ProxyDelegationCommitment,
					VoteCommTreeRoot:          wantRoot,
					AnchorHeight:              wantAnchor,
					VoteRoundId:               proxyAnteRound,
					EaPk:                      svtest.DummyPallasPoint,
					DcSlotNullifier:           dc.DcSlotNullifier,
				}, in)
				wantRoot = singleLeafRoot(dc.VoteAuthorityNoteNew)
			}
		})
	}
}

// TestProxyBatchAnteSignsD1 checks that every DC's vote_auth_sig is checked
// under its r_vpk over D1 computed with the context's chain id, and that a
// registration's SpendAuth signature is over its TX1 sighash instead.
func (s *ValidateTestSuite) TestProxyBatchAnteSignsD1() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	msg := proxyAnteBatch(2, true)
	msg.Dcs[1].RVpk = svtest.ValidRevealShare(proxyAnteRound, 1, 7).EncShare[:32] // another valid point
	sig, zk, _ := newProxyVerifiers(-1)
	m := s.proxyModes()[0]
	s.Require().NoError(ante.ValidateVoteTx(m.ctx.WithChainID("zvote-test-7"), msg, s.keeper, proxyOpts(m, sig, zk)))

	d1, err := types.ComputeProxyDelegationBatchSighash("zvote-test-7", msg.SighashFields())
	s.Require().NoError(err)
	other, err := types.ComputeProxyDelegationBatchSighash(proxyAnteChainID, msg.SighashFields())
	s.Require().NoError(err)
	s.Require().NotEqual(other, d1, "D1 binds the chain id")
	tx1Sighash, err := tx1.ComputeDelegationSighash(msg.Registration.Tx1Effects)
	s.Require().NoError(err)

	s.Require().Equal([][]byte{d1, d1, tx1Sighash}, sig.digests)
	s.Require().Equal([][]byte{msg.Dcs[0].RVpk, msg.Dcs[1].RVpk, msg.Registration.Rk}, sig.keys)
}

// bindingSigVerifier accepts a signature only if it is bindingSign of its
// key and digest, so a test can sign digests without RedPallas keys.
type bindingSigVerifier struct{}

// bindingSign returns BLAKE2b-512 of rk and digest.
func bindingSign(rk, digest []byte) []byte {
	sum := blake2b.Sum512(append(bytes.Clone(rk), digest...))
	return sum[:]
}

// Verify accepts only bindingSign(rk, digest).
func (bindingSigVerifier) Verify(rk, digest, sig []byte) error {
	if !bytes.Equal(sig, bindingSign(rk, digest)) {
		return errors.New("signature does not bind this digest")
	}
	return nil
}

// signBatch signs every DC of msg with bindingSign over its D1 on chainID.
func (s *ValidateTestSuite) signBatch(msg *types.MsgProxyDelegationBatch, chainID string) {
	d1, err := types.ComputeProxyDelegationBatchSighash(chainID, msg.SighashFields())
	s.Require().NoError(err)
	for _, dc := range msg.Dcs {
		dc.VoteAuthSig = bindingSign(dc.RVpk, d1)
	}
}

// TestProxyBatchAnteD1Binding checks that a DC signature is valid only over
// its own batch's D1: a signature for another chain or another expiry, a DC
// grafted from another batch, and a truncated or reordered batch are all
// ErrInvalidSignature.
func (s *ValidateTestSuite) TestProxyBatchAnteD1Binding() {
	proxytest.SkipUnlessEnabled(s.T())
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	m := s.proxyModes()[2]
	opts := proxyOpts(m, bindingSigVerifier{}, zkp.NewMockVerifier())
	validate := func(msg *types.MsgProxyDelegationBatch) error {
		return ante.ValidateVoteTx(m.ctx, msg, s.keeper, opts)
	}

	msg := proxyAnteBatch(3, false)
	s.signBatch(msg, proxyAnteChainID)
	s.Require().NoError(validate(msg))

	s.Run("another chain", func() {
		msg := proxyAnteBatch(3, false)
		s.signBatch(msg, "zvote-2")
		s.requireVoteCode(validate(msg), types.ErrInvalidSignature)
	})
	s.Run("another expiry", func() {
		msg := proxyAnteBatch(3, false)
		s.signBatch(msg, proxyAnteChainID)
		msg.ExpiryHeight++
		s.requireVoteCode(validate(msg), types.ErrInvalidSignature)
	})
	s.Run("grafted DC", func() {
		donor := proxyAnteBatch(1, false)
		donor.Dcs[0] = proxyAnteDC(0x60)
		s.signBatch(donor, proxyAnteChainID)
		s.Require().NoError(validate(donor))

		// The batch's own DCs re-sign over the grafted D1; the donor's DC
		// keeps its signature over its own batch.
		msg := proxyAnteBatch(2, false)
		msg.Dcs = append(msg.Dcs, donor.Dcs[0])
		graftedSig := donor.Dcs[0].VoteAuthSig
		s.signBatch(msg, proxyAnteChainID)
		msg.Dcs[2].VoteAuthSig = graftedSig
		err := validate(msg)
		s.requireVoteCode(err, types.ErrInvalidSignature)
		s.Require().ErrorContains(err, "dcs[2]")
	})
	s.Run("truncated", func() {
		msg := proxyAnteBatch(3, false)
		s.signBatch(msg, proxyAnteChainID)
		msg.Dcs = msg.Dcs[:2]
		err := validate(msg)
		s.requireVoteCode(err, types.ErrInvalidSignature)
		s.Require().ErrorContains(err, "dcs[0]")
	})
	s.Run("reordered", func() {
		msg := proxyAnteBatch(3, false)
		s.signBatch(msg, proxyAnteChainID)
		msg.Dcs[0], msg.Dcs[1] = msg.Dcs[1], msg.Dcs[0]
		s.requireVoteCode(validate(msg), types.ErrInvalidSignature)
	})
}
