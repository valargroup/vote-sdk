package keeper_test

import (
	"bytes"
	"encoding/hex"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

// validEntry returns a well-formed PartialDecryptionEntry.
func validEntry(proposalID, decision uint32) *types.PartialDecryptionEntry {
	return &types.PartialDecryptionEntry{
		ProposalId:     proposalID,
		VoteDecision:   decision,
		PartialDecrypt: bytes.Repeat([]byte{byte(proposalID*10 + decision)}, 32),
	}
}

// msgPdRoundID is the deterministic round ID used across SubmitPartialDecryption tests.
var msgPdRoundID = bytes.Repeat([]byte{0xAB}, 32)

// validatorSet returns n dummy ceremony validators with addresses "zvote1validator1" ... "zvote1validatorN".
func validatorSet(n int) []*types.ValidatorPallasKey {
	v := make([]*types.ValidatorPallasKey, n)
	for i := range v {
		v[i] = &types.ValidatorPallasKey{
			ValidatorAddress: "zvote1validator" + string(rune('1'+i)),
			PallasPk:         bytes.Repeat([]byte{byte(i + 1)}, 32),
		}
	}
	return v
}

// ---------------------------------------------------------------------------
// Happy path — entries stored correctly
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

// ---------------------------------------------------------------------------
// Event emission
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Rejection cases — table-driven
// ---------------------------------------------------------------------------

// rejectCase describes one input that the handler must reject.
type rejectCase struct {
	name        string
	// setup runs before the handler call. If nil, a default threshold=1, n=1
	// TALLYING round is seeded from msgPdRoundID with validators[0] as sole member.
	setup       func(s *MsgServerTestSuite, validators []*types.ValidatorPallasKey)
	// nValidators controls how many validators validatorSet() creates.
	// Defaults to 1 when zero.
	nValidators int
	// buildCtx optionally overrides the sdk.Context (e.g. for CheckTx/ReCheckTx).
	buildCtx    func(s *MsgServerTestSuite) sdk.Context
	// buildMsg returns the message to submit. validators comes from validatorSet(nValidators).
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
		// --- mempool guards ---
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

		// --- round validation ---
		{
			name:  "round not found",
			setup: func(*MsgServerTestSuite, []*types.ValidatorPallasKey) {}, // no round seeded
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

		// --- validator_index ---
		{
			name:        "validator_index=0 (below 1-based range)",
			nValidators: 2,
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress,
					ValidatorIndex: 0, Entries: []*types.PartialDecryptionEntry{validEntry(1, 0)},
				}
			},
			wantErr:     types.ErrInvalidField,
			errContains: "out of range",
		},
		{
			name:        "validator_index exceeds n",
			nValidators: 2,
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[1].ValidatorAddress,
					ValidatorIndex: 99, Entries: []*types.PartialDecryptionEntry{validEntry(1, 0)},
				}
			},
			wantErr:     types.ErrInvalidField,
			errContains: "out of range",
		},
		{
			name:        "creator does not match validator_index",
			nValidators: 3,
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId:    msgPdRoundID,
					Creator:        v[2].ValidatorAddress, // index 3, but ValidatorIndex=1
					ValidatorIndex: 1,
					Entries:        []*types.PartialDecryptionEntry{validEntry(1, 0)},
				}
			},
			wantErr:     types.ErrInvalidField,
			errContains: "maps to",
		},

		// --- entry-level validation ---
		{
			name:    "empty entries",
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
			wantErr: types.ErrInvalidField, errContains: "32 bytes",
		},
		{
			name: "partial_decrypt wrong length (31 bytes)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 31)}},
				}
			},
			wantErr: types.ErrInvalidField, errContains: "32 bytes",
		},
		{
			name: "partial_decrypt wrong length (33 bytes)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 33)}},
				}
			},
			wantErr: types.ErrInvalidField, errContains: "32 bytes",
		},
		{
			name: "proposal_id=0 (below 1-based range)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 0, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 32)}},
				}
			},
			wantErr: types.ErrInvalidProposalID,
		},
		{
			name: "proposal_id out of range (99)",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 99, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{0x01}, 32)}},
				}
			},
			wantErr: types.ErrInvalidProposalID,
		},
		{
			name: "vote_decision out of range for proposal",
			buildMsg: func(v []*types.ValidatorPallasKey) *types.MsgSubmitPartialDecryption {
				return &types.MsgSubmitPartialDecryption{
					VoteRoundId: msgPdRoundID, Creator: v[0].ValidatorAddress, ValidatorIndex: 1,
					// proposal 1 has options 0 and 1 only; 2 is out of range
					Entries: []*types.PartialDecryptionEntry{{ProposalId: 1, VoteDecision: 2, PartialDecrypt: bytes.Repeat([]byte{0x01}, 32)}},
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

// ---------------------------------------------------------------------------
// Duplicate submission
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Multiple validators — independent KV storage
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_MultipleValidators_IndependentStorage() {
	validators := validatorSet(3)
	s.setupTallyingRoundThreshold(msgPdRoundID, 2, validators)

	for i, v := range validators {
		_, err := s.msgServer.SubmitPartialDecryption(s.ctx, &types.MsgSubmitPartialDecryption{
			VoteRoundId:    msgPdRoundID,
			Creator:        v.ValidatorAddress,
			ValidatorIndex: uint32(i + 1),
			Entries: []*types.PartialDecryptionEntry{
				{ProposalId: 1, VoteDecision: 0, PartialDecrypt: bytes.Repeat([]byte{byte(i + 0x10)}, 32)},
				{ProposalId: 1, VoteDecision: 1, PartialDecrypt: bytes.Repeat([]byte{byte(i + 0x20)}, 32)},
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
		s.Require().Equal(bytes.Repeat([]byte{byte(i + 0x10)}, 32), entry.PartialDecrypt,
			"wrong partial_decrypt for validator %d", idx)
	}

	count, err := s.keeper.CountPartialDecryptionValidators(kv, msgPdRoundID)
	s.Require().NoError(err)
	s.Require().Equal(3, count)
}

// ---------------------------------------------------------------------------
// dleq_proof stored verbatim (no verification in Step 1)
// ---------------------------------------------------------------------------

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
			PartialDecrypt: bytes.Repeat([]byte{0x01}, 32),
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

// ---------------------------------------------------------------------------
// Integration: GetPartialDecryptionsForRound groups results correctly
// ---------------------------------------------------------------------------

func (s *MsgServerTestSuite) TestSubmitPartialDecryption_GetForRoundIntegration() {
	validators := validatorSet(2)
	s.setupTallyingRoundThreshold(msgPdRoundID, 2, validators)

	d1 := bytes.Repeat([]byte{0xAA}, 32)
	d2 := bytes.Repeat([]byte{0xBB}, 32)

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
