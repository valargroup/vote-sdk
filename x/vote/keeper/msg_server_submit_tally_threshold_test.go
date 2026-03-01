package keeper_test

import (
	"bytes"
	"crypto/rand"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/crypto/shamir"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupThresholdTallyRound seeds a TALLYING round with the given threshold and
// two proposals (each with two options).
func (s *MsgServerTestSuite) setupThresholdTallyRound(roundID []byte, threshold uint32, validators []*types.ValidatorPallasKey) {
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
				{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"},
			}},
			{Id: 2, Title: "Prop 2", Options: []*types.VoteOption{
				{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"},
			}},
		},
		CeremonyValidators: validators,
		VerificationKeys:   vks,
	}))
}

// storeThresholdPartials generates a (t, n) Shamir split of ea_sk, populates
// a tally accumulator for each (proposalID, decision, value) triple, and stores
// D_i = share_i * C1 for every validatorIdx in submitIdxs.
//
// Returns the ea_sk (for reference) and the accumulated ciphertexts
// keyed by (proposalID<<32 | decision).
type tallyAccumulator struct {
	proposalID uint32
	decision   uint32
	value      uint64
}

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
// TestSubmitTally_ThresholdMode
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitTally_ThresholdMode() {
	validators := validatorSet(3)

	type testCase struct {
		name string
		// setup and msg receive a per-row round ID so rows can't pollute each other.
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
				// No tally ciphertext stored — accBytes will be nil.
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
		// Use a unique round ID per row to prevent KV state from leaking between
		// sub-tests (the suite's SetupTest runs once per method, not per s.Run).
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
