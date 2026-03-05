package keeper_test

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/mikelodder7/curvey"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/crypto/shamir"
	zallytest "github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testEncShare generates a valid 64-byte ElGamal ciphertext for testing.
func testEncShare(s *MsgServerTestSuite, value uint64) []byte {
	_, pk := elgamal.KeyGen(rand.Reader)
	ct, err := elgamal.Encrypt(pk, value, rand.Reader)
	s.Require().NoError(err)
	bz, err := elgamal.MarshalCiphertext(ct)
	s.Require().NoError(err)
	return bz
}

// testEncShareWithPK generates a valid 64-byte ElGamal ciphertext using a specific public key.
func testEncShareWithPK(s *MsgServerTestSuite, pk *elgamal.PublicKey, value uint64) []byte {
	ct, err := elgamal.Encrypt(pk, value, rand.Reader)
	s.Require().NoError(err)
	bz, err := elgamal.MarshalCiphertext(ct)
	s.Require().NoError(err)
	return bz
}

// validPointBytes returns 32-byte compressed Pallas point = seed * G.
// Produces distinct, deterministic, on-curve points for each seed value.
func validPointBytes(seed int) []byte {
	s := new(curvey.ScalarPallas).New(seed)
	return elgamal.PallasGenerator().Mul(s).ToAffineCompressed()
}

// validEntry returns a well-formed PartialDecryptionEntry with a valid on-curve point.
func validEntry(proposalID, decision uint32) *types.PartialDecryptionEntry {
	return &types.PartialDecryptionEntry{
		ProposalId:     proposalID,
		VoteDecision:   decision,
		PartialDecrypt: validPointBytes(int(proposalID*10 + decision)),
	}
}

// msgPdRoundID is the deterministic round ID used across SubmitPartialDecryption tests.
var msgPdRoundID = bytes.Repeat([]byte{0xAB}, 32)

// validatorSet returns n dummy ceremony validators with addresses "zvote1validator1" ... "zvote1validatorN".
// ShamirIndex is set to i+1 (1-based), matching what CreateVotingSession assigns in production.
func validatorSet(n int) []*types.ValidatorPallasKey {
	v := make([]*types.ValidatorPallasKey, n)
	for i := range v {
		v[i] = &types.ValidatorPallasKey{
			ValidatorAddress: "zvote1validator" + string(rune('1'+i)),
			PallasPk:         bytes.Repeat([]byte{byte(i + 1)}, 32),
			ShamirIndex:      uint32(i + 1),
		}
	}
	return v
}

// setupTallyingRoundThreshold creates a TALLYING round with the given threshold
// and ceremony_validators in the KV store. The round has two proposals, each
// with two vote options.
func (s *MsgServerTestSuite) setupTallyingRoundThreshold(
	roundID []byte,
	threshold uint32,
	validators []*types.ValidatorPallasKey,
) {
	vks := make([][]byte, len(validators))
	for i := range vks {
		vks[i] = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: roundID,
		Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
		EaPk:        bytes.Repeat([]byte{0x10}, 32),
		Threshold:   threshold,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Prop 1", Options: []*types.VoteOption{
				{Index: 0, Label: "Yes"},
				{Index: 1, Label: "No"},
			}},
			{Id: 2, Title: "Prop 2", Options: []*types.VoteOption{
				{Index: 0, Label: "Yes"},
				{Index: 1, Label: "No"},
			}},
		},
		CeremonyValidators: validators,
		VerificationKeys:   vks,
	}))
}

// setupThresholdTallyRound seeds a TALLYING round with the given threshold and
// two proposals (each with two options).
func (s *MsgServerTestSuite) setupThresholdTallyRound(roundID []byte, threshold uint32, validators []*types.ValidatorPallasKey) {
	s.setupTallyingRoundThreshold(roundID, threshold, validators)
}

// tallyAccumulator describes a (proposal, decision, value) triple for threshold tally tests.
type tallyAccumulator struct {
	proposalID uint32
	decision   uint32
	value      uint64
}

// storeThresholdPartials generates a (t, n) Shamir split of ea_sk, populates
// a tally accumulator for each (proposalID, decision, value) triple, and stores
// D_i = share_i * C1 for every validatorIdx in submitIdxs.
//
// Returns the ea_sk and ea_pk (for reference).
func (s *MsgServerTestSuite) storeThresholdPartials(
	roundID []byte,
	threshold, nValidators int,
	submitIdxs []int, // 1-based
	accumulators []tallyAccumulator,
) (eaSk *elgamal.SecretKey, eaPk *elgamal.PublicKey) {
	s.T().Helper()
	kv := s.keeper.OpenKVStore(s.ctx)

	eaSk, eaPk = elgamal.KeyGen(rand.Reader)
	shares, _, err := shamir.Split(eaSk.Scalar, threshold, nValidators)
	s.Require().NoError(err)

	// Encrypt values and store in tally KV.
	for _, acc := range accumulators {
		ct, err := elgamal.Encrypt(eaPk, acc.value, rand.Reader)
		s.Require().NoError(err)
		ctBytes, err := elgamal.MarshalCiphertext(ct)
		s.Require().NoError(err)
		s.Require().NoError(s.keeper.AddToTally(kv, roundID, acc.proposalID, acc.decision, ctBytes))
	}

	// For each submitting validator, compute and store D_i = share_i * C1.
	for _, idx := range submitIdxs {
		share := shares[idx-1]
		var entries []*types.PartialDecryptionEntry

		for _, acc := range accumulators {
			ctBytes, err := s.keeper.GetTally(kv, roundID, acc.proposalID, acc.decision)
			s.Require().NoError(err)
			ct, err := elgamal.UnmarshalCiphertext(ctBytes)
			s.Require().NoError(err)

			Di := ct.C1.Mul(share.Value)
			entries = append(entries, &types.PartialDecryptionEntry{
				ProposalId:     acc.proposalID,
				VoteDecision:   acc.decision,
				PartialDecrypt: Di.ToAffineCompressed(),
			})
		}
		s.Require().NoError(s.keeper.SetPartialDecryptions(kv, roundID, uint32(idx), entries))
	}

	return eaSk, eaPk
}

// ---------------------------------------------------------------------------
// RevealShare
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestRevealShare() {
	roundID := bytes.Repeat([]byte{0x30}, 32)

	tests := []struct {
		name        string
		setup       func()
		msg         func() *types.MsgRevealShare
		expectErr   bool
		errContains string
		check       func()
	}{
		{
			name:  "happy path: nullifier recorded and tally accumulated",
			setup: func() { s.setupActiveRound(roundID) },
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF1}, 32),
					EncShare:                 testEncShare(s, 500),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF2}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				has, err := s.keeper.HasNullifier(kv, types.NullifierTypeShare, roundID, bytes.Repeat([]byte{0xF1}, 32))
				s.Require().NoError(err)
				s.Require().True(has)

				tally, err := s.keeper.GetTally(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().NotNil(tally, "tally should be stored")
				s.Require().Len(tally, 64, "tally should be 64 bytes (ElGamal ciphertext)")
			},
		},
		{
			name: "tally accumulates across multiple reveals via HomomorphicAdd",
			setup: func() {
				s.setupActiveRound(roundID)
				// Use same keypair for both shares so accumulation works.
				_, pk := elgamal.KeyGen(rand.Reader)
				// First reveal.
				_, err := s.msgServer.RevealShare(s.ctx, &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF3}, 32),
					EncShare:                 testEncShareWithPK(s, pk, 300),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF4}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				})
				s.Require().NoError(err)
			},
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF5}, 32),
					EncShare:                 testEncShare(s, 200),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF6}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				tally, err := s.keeper.GetTally(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().NotNil(tally)
				s.Require().Len(tally, 64, "accumulated tally should be 64 bytes")
			},
		},
		{
			name:  "invalid proposal_id rejected",
			setup: func() { s.setupActiveRound(roundID) },
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xF7}, 32),
					EncShare:                 testEncShare(s, 100),
					ProposalId:               5, // out of range
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xF8}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			expectErr:   true,
			errContains: "invalid proposal ID",
		},
		{
			name: "duplicate share nullifier rejected (double-count)",
			setup: func() {
				s.setupActiveRound(roundID)
				first := &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xFA}, 32),
					EncShare:                 testEncShare(s, 100),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xFB}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
				_, err := s.msgServer.RevealShare(s.ctx, first)
				s.Require().NoError(err)
			},
			msg: func() *types.MsgRevealShare {
				return &types.MsgRevealShare{
					ShareNullifier:           bytes.Repeat([]byte{0xFA}, 32), // same as first
					EncShare:                 testEncShare(s, 200),
					ProposalId:               1,
					VoteDecision:             1,
					Proof:                    bytes.Repeat([]byte{0xFC}, 64),
					VoteRoundId:              roundID,
					VoteCommTreeAnchorHeight: 10,
				}
			},
			expectErr:   true,
			errContains: "nullifier already",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			_, err := s.msgServer.RevealShare(s.ctx, tc.msg())
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
				if tc.check != nil {
					tc.check()
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SubmitTally (legacy DLEQ mode)
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitTally() {
	roundID := bytes.Repeat([]byte{0x40}, 32)
	creator := "zvote1creator"

	// Generate a real EA keypair for DLEQ proof tests.
	eaSk, eaPk := elgamal.KeyGen(rand.Reader)
	eaPkBytes := eaPk.Point.ToAffineCompressed()

	// Helper: set up a TALLYING round with an encrypted tally accumulator
	// using the real EA public key.
	setupTallyingRoundWithAccumulator := func() (*elgamal.Ciphertext, []byte) {
		kv := s.keeper.OpenKVStore(s.ctx)
		s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
			VoteRoundId: roundID,
			VoteEndTime: 500_000,
			Creator:     creator,
			Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
			EaPk:        eaPkBytes,
			Proposals: []*types.Proposal{
				{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
				{Id: 2, Title: "Proposal B", Description: "Second", Options: zallytest.DefaultOptions()},
			},
		}))
		// Pre-populate the tally accumulator with a ciphertext encrypted under the real EA key.
		encShareBytes := testEncShareWithPK(s, eaPk, 500)
		s.Require().NoError(s.keeper.AddToTally(kv, roundID, 1, 1, encShareBytes))

		ct, err := elgamal.UnmarshalCiphertext(encShareBytes)
		s.Require().NoError(err)
		return ct, encShareBytes
	}

	// Helper: generate a valid DLEQ proof for the given ciphertext and value.
	makeDLEQProof := func(ct *elgamal.Ciphertext, value uint64) []byte {
		proof, err := elgamal.GenerateDLEQProof(eaSk, ct, value)
		s.Require().NoError(err)
		return proof
	}

	tests := []struct {
		name        string
		setup       func()
		msg         func() *types.MsgSubmitTally
		expectErr   bool
		errContains string
		check       func(resp *types.MsgSubmitTallyResponse)
	}{
		{
			name: "happy path: round finalized with valid DLEQ proof",
			msg: func() *types.MsgSubmitTally {
				ct, _ := setupTallyingRoundWithAccumulator()
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500, DecryptionProof: makeDLEQProof(ct, 500)},
					},
				}
			},
			check: func(resp *types.MsgSubmitTallyResponse) {
				s.Require().Equal(uint32(1), resp.FinalizedEntries)

				kv := s.keeper.OpenKVStore(s.ctx)

				// Round is FINALIZED.
				round, err := s.keeper.GetVoteRound(kv, roundID)
				s.Require().NoError(err)
				s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)

				// TallyResult is stored (uint64 decrypted value from EA).
				result, err := s.keeper.GetTallyResult(kv, roundID, 1, 1)
				s.Require().NoError(err)
				s.Require().NotNil(result)
				s.Require().Equal(uint64(500), result.TotalValue)
				s.Require().Equal(uint32(1), result.ProposalId)
				s.Require().Equal(uint32(1), result.VoteDecision)
			},
		},
		{
			name: "rejected: invalid DLEQ proof (wrong value)",
			msg: func() *types.MsgSubmitTally {
				ct, _ := setupTallyingRoundWithAccumulator()
				// Generate proof for 500 but claim 999.
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 999, DecryptionProof: makeDLEQProof(ct, 500)},
					},
				}
			},
			expectErr:   true,
			errContains: "tally entry does not match",
		},
		{
			name: "rejected: missing DLEQ proof when accumulator exists",
			msg: func() *types.MsgSubmitTally {
				setupTallyingRoundWithAccumulator()
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
					},
				}
			},
			expectErr:   true,
			errContains: "tally entry does not match",
		},
		{
			name: "rejected: entry references non-existent proposal",
			msg: func() *types.MsgSubmitTally {
				setupTallyingRoundWithAccumulator()
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 5, VoteDecision: 1, TotalValue: 500},
					},
				}
			},
			expectErr:   true,
			errContains: "invalid proposal ID",
		},
		{
			name: "rejected: round is ACTIVE not TALLYING",
			setup: func() {
				s.setupActiveRound(roundID)
			},
			msg: func() *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     "zvote1creator",
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
					},
				}
			},
			expectErr:   true,
			errContains: "not in tallying state",
		},
		{
			name: "rejected: round is already FINALIZED",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
					VoteRoundId: roundID,
					VoteEndTime: 500_000,
					Creator:     creator,
					Status:      types.SessionStatus_SESSION_STATUS_FINALIZED,
				}))
			},
			msg: func() *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
					},
				}
			},
			expectErr:   true,
			errContains: "not in tallying state",
		},
		{
			name: "accepted: different creator with valid DLEQ proof",
			msg: func() *types.MsgSubmitTally {
				ct, _ := setupTallyingRoundWithAccumulator()
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     "zvote1othervalidator",
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500, DecryptionProof: makeDLEQProof(ct, 500)},
					},
				}
			},
			check: func(resp *types.MsgSubmitTallyResponse) {
				s.Require().Equal(uint32(1), resp.FinalizedEntries)
			},
		},
		{
			name: "rejected: round does not exist",
			msg: func() *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: bytes.Repeat([]byte{0xFF}, 32),
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
					},
				}
			},
			expectErr:   true,
			errContains: "vote round not found",
		},
		{
			name: "happy path: zero-valued entry for (proposal, decision) with no reveals",
			msg: func() *types.MsgSubmitTally {
				ct, _ := setupTallyingRoundWithAccumulator()
				// proposal 1 / decision 0 has no reveals → accumulator nil → no proof needed.
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500, DecryptionProof: makeDLEQProof(ct, 500)},
						{ProposalId: 1, VoteDecision: 0, TotalValue: 0},
					},
				}
			},
			check: func(resp *types.MsgSubmitTallyResponse) {
				s.Require().Equal(uint32(2), resp.FinalizedEntries)
			},
		},
		{
			name: "rejected: non-zero value claimed for nil accumulator",
			msg: func() *types.MsgSubmitTally {
				ct, _ := setupTallyingRoundWithAccumulator()
				return &types.MsgSubmitTally{
					VoteRoundId: roundID,
					Creator:     creator,
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 1, TotalValue: 500, DecryptionProof: makeDLEQProof(ct, 500)},
						{ProposalId: 1, VoteDecision: 0, TotalValue: 42}, // no accumulator for decision 0
					},
				}
			},
			expectErr:   true,
			errContains: "tally entry does not match",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			if tc.setup != nil {
				tc.setup()
			}
			resp, err := s.msgServer.SubmitTally(s.ctx, tc.msg())
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
				if tc.check != nil {
					tc.check(resp)
				}
			}
		})
	}
}

func (s *MsgServerTestSuite) TestSubmitTally_EmitsEvent() {
	s.SetupTest()
	roundID := bytes.Repeat([]byte{0x50}, 32)
	creator := "zvote1creator"

	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: roundID,
		VoteEndTime: 500_000,
		Creator:     creator,
		Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
	}))

	_, err := s.msgServer.SubmitTally(s.ctx, &types.MsgSubmitTally{
		VoteRoundId: roundID,
		Creator:     creator,
	})
	s.Require().NoError(err)

	events := s.ctx.EventManager().Events()
	found := false
	for _, e := range events {
		if e.Type == types.EventTypeSubmitTally {
			found = true
			for _, attr := range e.Attributes {
				if attr.Key == types.AttributeKeyRoundID {
					expected := fmt.Sprintf("%x", roundID)
					s.Require().Equal(expected, attr.Value)
				}
				if attr.Key == types.AttributeKeyNewStatus {
					s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED.String(), attr.Value)
				}
			}
		}
	}
	s.Require().True(found, "expected %s event", types.EventTypeSubmitTally)
}

func (s *MsgServerTestSuite) TestSubmitTally_FinalizedRejectsShares() {
	s.SetupTest()
	roundID := bytes.Repeat([]byte{0x60}, 32)
	creator := "zvote1creator"

	// Create a TALLYING round.
	kv := s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: roundID,
		VoteEndTime: 500_000,
		Creator:     creator,
		Status:      types.SessionStatus_SESSION_STATUS_TALLYING,
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
		},
	}))

	// Finalize it.
	_, err := s.msgServer.SubmitTally(s.ctx, &types.MsgSubmitTally{
		VoteRoundId: roundID,
		Creator:     creator,
	})
	s.Require().NoError(err)

	// Attempt to submit a reveal share — should fail because round is FINALIZED.
	_, err = s.msgServer.RevealShare(s.ctx, &types.MsgRevealShare{
		ShareNullifier:           bytes.Repeat([]byte{0xF1}, 32),
		EncShare:                 testEncShare(s, 100),
		ProposalId:               1,
		VoteDecision:             1,
		Proof:                    bytes.Repeat([]byte{0xF2}, 64),
		VoteRoundId:              roundID,
		VoteCommTreeAnchorHeight: 10,
	})
	// RevealShare validates proposal_id which succeeds, but the ante handler
	// would reject it. At the keeper level, RevealShare doesn't check status,
	// so we verify the status is FINALIZED which the ante handler uses.
	kv = s.keeper.OpenKVStore(s.ctx)
	round, err2 := s.keeper.GetVoteRound(kv, roundID)
	s.Require().NoError(err2)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)
}

// ---------------------------------------------------------------------------
// SubmitTally: threshold mode
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitTally_ThresholdMode() {
	validators := validatorSet(3)

	type testCase struct {
		name        string
		setup       func(roundID []byte)
		msg         func(roundID []byte) *types.MsgSubmitTally
		wantErr     bool
		errContains string
		check       func(roundID []byte)
	}

	cases := []testCase{
		// --- happy paths ---
		{
			name: "t=2 n=2 correct value accepted and round finalized",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators[:2])
				s.storeThresholdPartials(rid, 2, 2, []int{1, 2},
					[]tallyAccumulator{{proposalID: 1, decision: 0, value: 42}})
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries:     []*types.TallyEntry{{ProposalId: 1, VoteDecision: 0, TotalValue: 42}},
				}
			},
			check: func(rid []byte) {
				kv := s.keeper.OpenKVStore(s.ctx)
				round, err := s.keeper.GetVoteRound(kv, rid)
				s.Require().NoError(err)
				s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)
				result, err := s.keeper.GetTallyResult(kv, rid, 1, 0)
				s.Require().NoError(err)
				s.Require().Equal(uint64(42), result.TotalValue)
			},
		},
		{
			name: "multiple accumulators all verified and stored",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators[:2])
				s.storeThresholdPartials(rid, 2, 2, []int{1, 2}, []tallyAccumulator{
					{proposalID: 1, decision: 0, value: 10},
					{proposalID: 1, decision: 1, value: 20},
					{proposalID: 2, decision: 0, value: 30},
				})
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries: []*types.TallyEntry{
						{ProposalId: 1, VoteDecision: 0, TotalValue: 10},
						{ProposalId: 1, VoteDecision: 1, TotalValue: 20},
						{ProposalId: 2, VoteDecision: 0, TotalValue: 30},
					},
				}
			},
			check: func(rid []byte) {
				kv := s.keeper.OpenKVStore(s.ctx)
				for _, want := range []struct{ pid, dec uint32; val uint64 }{
					{1, 0, 10}, {1, 1, 20}, {2, 0, 30},
				} {
					r, err := s.keeper.GetTallyResult(kv, rid, want.pid, want.dec)
					s.Require().NoError(err)
					s.Require().Equal(want.val, r.TotalValue, "proposal=%d decision=%d", want.pid, want.dec)
				}
			},
		},
		{
			name: "nil accumulator (no votes) accepts TotalValue=0",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators[:2])
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries:     []*types.TallyEntry{{ProposalId: 1, VoteDecision: 0, TotalValue: 0}},
				}
			},
			check: func(rid []byte) {
				kv := s.keeper.OpenKVStore(s.ctx)
				round, _ := s.keeper.GetVoteRound(kv, rid)
				s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)
			},
		},
		{
			name: "more than threshold partials still reconstructs correctly",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators)
				s.storeThresholdPartials(rid, 2, 3, []int{1, 2, 3},
					[]tallyAccumulator{{proposalID: 1, decision: 0, value: 77}})
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries:     []*types.TallyEntry{{ProposalId: 1, VoteDecision: 0, TotalValue: 77}},
				}
			},
			check: func(rid []byte) {
				kv := s.keeper.OpenKVStore(s.ctx)
				r, _ := s.keeper.GetTallyResult(kv, rid, 1, 0)
				s.Require().Equal(uint64(77), r.TotalValue)
			},
		},

		// --- error paths ---
		{
			name: "wrong TotalValue rejected (C2 - combined != totalValue*G)",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators[:2])
				s.storeThresholdPartials(rid, 2, 2, []int{1, 2},
					[]tallyAccumulator{{proposalID: 1, decision: 0, value: 42}})
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries:     []*types.TallyEntry{{ProposalId: 1, VoteDecision: 0, TotalValue: 999}},
				}
			},
			wantErr:     true,
			errContains: "C2 - combined_partial != totalValue*G",
		},
		{
			name: "no partial decryptions stored for accumulator",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators[:2])
				kv := s.keeper.OpenKVStore(s.ctx)
				ct, _ := elgamal.Encrypt(&elgamal.PublicKey{Point: elgamal.PallasGenerator()}, 5, rand.Reader)
				ctBytes, _ := elgamal.MarshalCiphertext(ct)
				s.Require().NoError(s.keeper.AddToTally(kv, rid, 1, 0, ctBytes))
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries:     []*types.TallyEntry{{ProposalId: 1, VoteDecision: 0, TotalValue: 5}},
				}
			},
			wantErr:     true,
			errContains: "no partial decryptions stored",
		},
		{
			name: "insufficient partials (1 stored, threshold=2)",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators[:2])
				s.storeThresholdPartials(rid, 2, 2, []int{1}, // only validator 1
					[]tallyAccumulator{{proposalID: 1, decision: 0, value: 42}})
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries:     []*types.TallyEntry{{ProposalId: 1, VoteDecision: 0, TotalValue: 42}},
				}
			},
			wantErr:     true,
			errContains: "Lagrange combination failed",
		},
		{
			name: "nil accumulator with non-zero value rejected",
			setup: func(rid []byte) {
				s.setupThresholdTallyRound(rid, 2, validators[:2])
			},
			msg: func(rid []byte) *types.MsgSubmitTally {
				return &types.MsgSubmitTally{
					VoteRoundId: rid,
					Creator:     "zvote1proposer",
					Entries:     []*types.TallyEntry{{ProposalId: 1, VoteDecision: 0, TotalValue: 1}},
				}
			},
			wantErr:     true,
			errContains: "claims value 1 but no accumulator exists",
		},
	}

	for i, tc := range cases {
		roundID := bytes.Repeat([]byte{byte(0xD0 + i)}, 32)

		s.Run(tc.name, func() {
			tc.setup(roundID)
			resp, err := s.msgServer.SubmitTally(s.ctx, tc.msg(roundID))

			if tc.wantErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
				return
			}

			s.Require().NoError(err)
			s.Require().NotNil(resp)
			if tc.check != nil {
				tc.check(roundID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SubmitPartialDecryption
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_HappyPath() {
	validators := validatorSet(3)
	s.setupTallyingRoundThreshold(msgPdRoundID, 2, validators)

	msg := &types.MsgSubmitPartialDecryption{
		VoteRoundId:    msgPdRoundID,
		Creator:        validators[0].ValidatorAddress,
		ValidatorIndex: 1,
		Entries: []*types.PartialDecryptionEntry{
			validEntry(1, 0),
			validEntry(1, 1),
			validEntry(2, 0),
		},
	}

	resp, err := s.msgServer.SubmitPartialDecryption(s.ctx, msg)
	s.Require().NoError(err)
	s.Require().NotNil(resp)

	kv := s.keeper.OpenKVStore(s.ctx)
	for _, entry := range msg.Entries {
		got, err := s.keeper.GetPartialDecryption(kv, msgPdRoundID, 1, entry.ProposalId, entry.VoteDecision)
		s.Require().NoError(err)
		s.Require().NotNil(got)
		s.Require().Equal(entry.PartialDecrypt, got.PartialDecrypt)
	}
}

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_EmitsEvent() {
	validators := validatorSet(1)
	s.setupTallyingRoundThreshold(msgPdRoundID, 1, validators)

	em := sdk.NewEventManager()
	ctx := s.ctx.WithEventManager(em)

	_, err := s.msgServer.SubmitPartialDecryption(ctx, &types.MsgSubmitPartialDecryption{
		VoteRoundId:    msgPdRoundID,
		Creator:        validators[0].ValidatorAddress,
		ValidatorIndex: 1,
		Entries:        []*types.PartialDecryptionEntry{validEntry(1, 0)},
	})
	s.Require().NoError(err)

	var found bool
	for _, ev := range em.Events() {
		if ev.Type != types.EventTypeSubmitPartialDecryption {
			continue
		}
		found = true
		attrs := make(map[string]string, len(ev.Attributes))
		for _, a := range ev.Attributes {
			attrs[a.Key] = a.Value
		}
		s.Require().Equal(hex.EncodeToString(msgPdRoundID), attrs[types.AttributeKeyRoundID])
		s.Require().Equal(validators[0].ValidatorAddress, attrs[types.AttributeKeyCreator])
		s.Require().Equal("1", attrs[types.AttributeKeyValidatorIndex])
		s.Require().Equal("1", attrs[types.AttributeKeyEntryCount])
	}
	s.Require().True(found, "submit_partial_decryption event not emitted")
}

// rejectCase describes one input that the handler must reject.
type rejectCase struct {
	name        string
	setup       func(s *MsgServerTestSuite, validators []*types.ValidatorPallasKey)
	nValidators int
	buildCtx    func(s *MsgServerTestSuite) sdk.Context
	buildMsg    func(validators []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption
	wantErr     error
	errContains string
}

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_Rejections() {
	defaultMsg := func(validators []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
		return &types.MsgSubmitPartialDecryption{
			VoteRoundId:    msgPdRoundID,
			Creator:        validators[0].ValidatorAddress,
			ValidatorIndex: 1,
			Entries:        []*types.PartialDecryptionEntry{validEntry(1, 0)},
		}
	}
	defaultSetup := func(s *MsgServerTestSuite, validators []*types.ValidatorPallasKey) {
		s.setupTallyingRoundThreshold(msgPdRoundID, 1, validators)
	}

	cases := []rejectCase{
		{
			name:     "CheckTx rejected",
			buildCtx: func(s *MsgServerTestSuite) sdk.Context { return s.ctx.WithIsCheckTx(true) },
			buildMsg: defaultMsg,
			wantErr:  types.ErrInvalidField,
			errContains: "mempool",
		},
		{
			name:     "ReCheckTx rejected",
			buildCtx: func(s *MsgServerTestSuite) sdk.Context { return s.ctx.WithIsReCheckTx(true) },
			buildMsg: defaultMsg,
			wantErr:  types.ErrInvalidField,
		},
		{
			name:  "round not found",
			setup: func(*MsgServerTestSuite, []*types.ValidatorPallasKey) {},
			buildMsg: func([]*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId:    bytes.Repeat([]byte{0xFF}, 32),
					Creator:        "zvote1validator1",
					ValidatorIndex: 1,
					Entries:        []*types.PartialDecryptionEntry{validEntry(1, 0)},
				}
			},
			wantErr: types.ErrRoundNotFound,
		},
		{
			name: "ACTIVE round (wrong status)",
			setup: func(s *MsgServerTestSuite, _ []*types.ValidatorPallasKey) {
				s.setupActiveRound(msgPdRoundID)
			},
			buildMsg: defaultMsg,
			wantErr:  types.ErrRoundNotTallying,
		},
		{
			name: "legacy round (threshold=0)",
			setup: func(s *MsgServerTestSuite, v []*types.ValidatorPallasKey) {
				s.setupTallyingRoundThreshold(msgPdRoundID, 0, v)
			},
			buildMsg:    defaultMsg,
			wantErr:     types.ErrInvalidField,
			errContains: "threshold > 0",
		},
		{
			name:        "validator_index=0 does not match shamir_index",
			nValidators: 2,
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress,
					ValidatorIndex: 0, Entries: []*types.PartialDecryptionEntry{validEntry(1, 0)},
				}
			},
			wantErr:     types.ErrInvalidField,
			errContains: "shamir_index",
		},
		{
			name:        "validator_index does not match shamir_index",
			nValidators: 2,
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[1].ValidatorAddress,
					ValidatorIndex: 99, Entries: []*types.PartialDecryptionEntry{validEntry(1, 0)},
				}
			},
			wantErr:     types.ErrInvalidField,
			errContains: "shamir_index",
		},
		{
			name:        "creator does not match validator_index",
			nValidators: 3,
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId:    msgPdRoundID,
					Creator:        v[2].ValidatorAddress,
					ValidatorIndex: 1,
					Entries:        []*types.PartialDecryptionEntry{validEntry(1, 0)},
				}
			},
			wantErr:     types.ErrInvalidField,
			errContains: "shamir_index",
		},
		{
			name: "empty entries",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress,
					ValidatorIndex: 1, Entries: nil,
				}
			},
			wantErr:     types.ErrInvalidField,
			errContains: "entries cannot be empty",
		},
		{
			name: "partial_decrypt too short (0 bytes)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 0, PartialDecrypt: nil}},
				}
			},
			wantErr: types.ErrInvalidField, errContains: "not a valid Pallas point",
		},
		{
			name: "partial_decrypt wrong length (31 bytes)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 31)}},
				}
			},
			wantErr: types.ErrInvalidField, errContains: "not a valid Pallas point",
		},
		{
			name: "partial_decrypt wrong length (33 bytes)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 33)}},
				}
			},
			wantErr: types.ErrInvalidField, errContains: "not a valid Pallas point",
		},
		{
			name: "partial_decrypt 32 bytes but not on curve",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0xFF}, 32)}},
				}
			},
			wantErr: types.ErrInvalidField, errContains: "not a valid Pallas point",
		},
		{
			name: "proposal_id=0 (below 1-based range)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 0, VoteDecision: 0, PartialDecrypt: validPointBytes(1)}},
				}
			},
			wantErr: types.ErrInvalidProposalID,
		},
		{
			name: "proposal_id out of range (99)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 99, VoteDecision: 0, PartialDecrypt: validPointBytes(1)}},
				}
			},
			wantErr: types.ErrInvalidProposalID,
		},
		{
			name: "vote_decision out of range for proposal",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 2, PartialDecrypt: validPointBytes(1)}},
				}
			},
			wantErr: types.ErrInvalidField, errContains: "out of range",
		},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			n := tc.nValidators
			if n == 0 {
				n = 1
			}
			validators := validatorSet(n)

			if tc.setup != nil {
				tc.setup(s, validators)
			} else {
				defaultSetup(s, validators)
			}

			ctx := s.ctx
			if tc.buildCtx != nil {
				ctx = tc.buildCtx(s)
			}

			_, err := s.msgServer.SubmitPartialDecryption(ctx, tc.buildMsg(validators))
			s.Require().Error(err)
			s.Require().ErrorIs(err, tc.wantErr)
			if tc.errContains != "" {
				s.Require().Contains(err.Error(), tc.errContains)
			}
		})
	}
}

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_RejectsDuplicate() {
	validators := validatorSet(2)
	s.setupTallyingRoundThreshold(msgPdRoundID, 2, validators)

	msg := &types.MsgSubmitPartialDecryption{
		VoteRoundId:    msgPdRoundID,
		Creator:        validators[0].ValidatorAddress,
		ValidatorIndex: 1,
		Entries:        []*types.PartialDecryptionEntry{validEntry(1, 0)},
	}

	_, err := s.msgServer.SubmitPartialDecryption(s.ctx, msg)
	s.Require().NoError(err, "first submission should succeed")

	_, err = s.msgServer.SubmitPartialDecryption(s.ctx, msg)
	s.Require().Error(err, "second submission must be rejected")
	s.Require().ErrorIs(err, types.ErrInvalidField)
	s.Require().Contains(err.Error(), "already submitted")
}

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_MultipleValidators_IndependentStorage() {
	validators := validatorSet(3)
	s.setupTallyingRoundThreshold(msgPdRoundID, 2, validators)

	pointsD0 := make([][]byte, len(validators))
	for i := range validators {
		pointsD0[i] = validPointBytes(100 + i)
	}

	for i, v := range validators {
		_, err := s.msgServer.SubmitPartialDecryption(s.ctx, &types.MsgSubmitPartialDecryption{
			VoteRoundId:    msgPdRoundID,
			Creator:        v.ValidatorAddress,
			ValidatorIndex: uint32(i + 1),
			Entries: []*types.PartialDecryptionEntry{
				{ProposalId: 1, VoteDecision: 0, PartialDecrypt: pointsD0[i]},
				{ProposalId: 1, VoteDecision: 1, PartialDecrypt: validPointBytes(200 + i)},
			},
		})
		s.Require().NoError(err, "validator %d submission should succeed", i+1)
	}

	kv := s.keeper.OpenKVStore(s.ctx)
	for i := range validators {
		idx := uint32(i + 1)
		entry, err := s.keeper.GetPartialDecryption(kv, msgPdRoundID, idx, 1, 0)
		s.Require().NoError(err)
		s.Require().NotNil(entry)
		s.Require().Equal(pointsD0[i], entry.PartialDecrypt,
			"wrong partial_decrypt for validator %d", idx)
	}

	count, err := s.keeper.CountPartialDecryptionValidators(kv, msgPdRoundID)
	s.Require().NoError(err)
	s.Require().Equal(3, count)
}

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_DleqProofStoredVerbatim() {
	validators := validatorSet(1)
	s.setupTallyingRoundThreshold(msgPdRoundID, 1, validators)

	dleqProof := bytes.Repeat([]byte{0xDE}, 64)
	_, err := s.msgServer.SubmitPartialDecryption(s.ctx, &types.MsgSubmitPartialDecryption{
		VoteRoundId:    msgPdRoundID,
		Creator:        validators[0].ValidatorAddress,
		ValidatorIndex: 1,
		Entries: []*types.PartialDecryptionEntry{{
			ProposalId:     1,
			VoteDecision:   0,
			PartialDecrypt: validPointBytes(42),
			DleqProof:      dleqProof,
		}},
	})
	s.Require().NoError(err)

	kv := s.keeper.OpenKVStore(s.ctx)
	got, err := s.keeper.GetPartialDecryption(kv, msgPdRoundID, 1, 1, 0)
	s.Require().NoError(err)
	s.Require().Equal(dleqProof, got.DleqProof,
		"dleq_proof must be stored verbatim in Step 1 (no verification)")
}

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_GetForRoundIntegration() {
	validators := validatorSet(2)
	s.setupTallyingRoundThreshold(msgPdRoundID, 2, validators)

	d1 := validPointBytes(301)
	d2 := validPointBytes(302)

	for i, v := range validators {
		_, err := s.msgServer.SubmitPartialDecryption(s.ctx, &types.MsgSubmitPartialDecryption{
			VoteRoundId:    msgPdRoundID,
			Creator:        v.ValidatorAddress,
			ValidatorIndex: uint32(i + 1),
			Entries: []*types.PartialDecryptionEntry{{
				ProposalId:     1,
				VoteDecision:   0,
				PartialDecrypt: [][]byte{d1, d2}[i],
			}},
		})
		s.Require().NoError(err)
	}

	kv := s.keeper.OpenKVStore(s.ctx)
	grouped, err := s.keeper.GetPartialDecryptionsForRound(kv, msgPdRoundID)
	s.Require().NoError(err)

	partials := grouped[keeper.AccumulatorKey(1, 0)]
	s.Require().Len(partials, 2)

	byIdx := make(map[uint32][]byte, 2)
	for _, p := range partials {
		byIdx[p.ValidatorIndex] = p.PartialDecrypt
	}
	s.Require().Equal(d1, byIdx[1])
	s.Require().Equal(d2, byIdx[2])
}
