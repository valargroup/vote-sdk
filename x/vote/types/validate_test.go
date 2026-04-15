package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/suite"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

type ValidateBasicTestSuite struct {
	suite.Suite
}

func TestValidateBasicTestSuite(t *testing.T) {
	suite.Run(t, new(ValidateBasicTestSuite))
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func validCreateSession() *types.MsgCreateVotingSession {
	return &types.MsgCreateVotingSession{
		Creator:           "sv1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
		VoteEndTime:       2_000_000,
		NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
		NcRoot:            bytes.Repeat([]byte{0x04}, 32),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First", Options: svtest.DefaultOptions()},
			{Id: 2, Title: "Proposal B", Description: "Second", Options: svtest.DefaultOptions()},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: MsgCreateVotingSession.ValidateBasic — new session fields
// ---------------------------------------------------------------------------

func (s *ValidateBasicTestSuite) TestCreateVotingSession_NewFieldsValidation() {
	tests := []struct {
		name        string
		modify      func(*types.MsgCreateVotingSession)
		expectErr   bool
		errContains string
	}{
		{
			name:   "valid: all fields correct",
			modify: func(m *types.MsgCreateVotingSession) {},
		},
		// ea_pk is no longer in MsgCreateVotingSession; sourced from CeremonyState.
		// vk_zkp1/2/3 removed: verifying keys are compiled into the Rust Halo2
		// verifier binary and cannot be overridden per-session.
		{
			name:        "invalid: zero proposals",
			modify:      func(m *types.MsgCreateVotingSession) { m.Proposals = nil },
			expectErr:   true,
			errContains: "proposals count",
		},
		{
			name: "invalid: 16 proposals (exceeds max; circuit bit 0 is sentinel, only 1-15 usable)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = make([]*types.Proposal, 16)
				for i := range m.Proposals {
					m.Proposals[i] = &types.Proposal{Id: uint32(i + 1), Title: "P", Options: svtest.DefaultOptions()}
				}
			},
			expectErr:   true,
			errContains: "proposals count",
		},
		{
			name: "invalid: proposal with empty title",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "", Description: "No title", Options: svtest.DefaultOptions()},
				}
			},
			expectErr:   true,
			errContains: "title",
		},
		{
			name: "invalid: proposal ID mismatch (non-sequential)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: svtest.DefaultOptions()},
					{Id: 5, Title: "B", Description: "bad id", Options: svtest.DefaultOptions()},
				}
			},
			expectErr:   true,
			errContains: "proposal id mismatch",
		},
		{
			name: "valid: single proposal",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "Only Option", Description: "Single", Options: svtest.DefaultOptions()},
				}
			},
		},
		{
			name: "valid: 15 proposals (max; circuit supports bit positions 1-15)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = make([]*types.Proposal, 15)
				for i := range m.Proposals {
					m.Proposals[i] = &types.Proposal{Id: uint32(i + 1), Title: "P", Options: svtest.DefaultOptions()}
				}
			},
		},
		{
			name: "invalid: proposal with too few options",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: []*types.VoteOption{
						{Index: 0, Label: "Only one"},
					}},
				}
			},
			expectErr:   true,
			errContains: "must have 2-8 options",
		},
		{
			name: "invalid: proposal with too many options (9)",
			modify: func(m *types.MsgCreateVotingSession) {
				opts := make([]*types.VoteOption, 9)
				for i := range opts {
					opts[i] = &types.VoteOption{Index: uint32(i), Label: "Opt"}
				}
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: opts},
				}
			},
			expectErr:   true,
			errContains: "must have 2-8 options",
		},
		{
			name: "invalid: option index not sequential",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: []*types.VoteOption{
						{Index: 0, Label: "Support"},
						{Index: 5, Label: "Oppose"},
					}},
				}
			},
			expectErr:   true,
			errContains: "option index mismatch",
		},
		{
			name: "invalid: option with empty label",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: []*types.VoteOption{
						{Index: 0, Label: "Support"},
						{Index: 1, Label: ""},
					}},
				}
			},
			expectErr:   true,
			errContains: "label cannot be empty",
		},
		{
			name: "invalid: option with non-ASCII label",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: []*types.VoteOption{
						{Index: 0, Label: "Support"},
						{Index: 1, Label: "Opposé"},
					}},
				}
			},
			expectErr:   true,
			errContains: "ASCII",
		},
		{
			name: "valid: 8 options (max)",
			modify: func(m *types.MsgCreateVotingSession) {
				opts := make([]*types.VoteOption, 8)
				for i := range opts {
					opts[i] = &types.VoteOption{Index: uint32(i), Label: "Candidate"}
				}
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: opts},
				}
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			msg := validCreateSession()
			tc.modify(msg)
			err := msg.ValidateBasic()
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: MsgSubmitTally.ValidateBasic
// ---------------------------------------------------------------------------

func (s *ValidateBasicTestSuite) TestSubmitTally_ValidateBasic() {
	tests := []struct {
		name        string
		msg         *types.MsgSubmitTally
		expectErr   bool
		errContains string
	}{
		{
			name: "valid: all fields correct",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "sv1admin",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 1000},
				},
			},
		},
		{
			name: "valid: multiple entries",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "sv1admin",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 0, TotalValue: 500},
					{ProposalId: 1, VoteDecision: 1, TotalValue: 1000},
					{ProposalId: 2, VoteDecision: 1, TotalValue: 200},
				},
			},
		},
		{
			name: "invalid: empty vote_round_id",
			msg: &types.MsgSubmitTally{
				VoteRoundId: nil,
				Creator:     "sv1admin",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 1000},
				},
			},
			expectErr:   true,
			errContains: "vote_round_id",
		},
		{
			name: "invalid: empty creator",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 1000},
				},
			},
			expectErr:   true,
			errContains: "creator",
		},
		{
			name: "valid: empty entries (zero-vote round)",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "sv1admin",
				Entries:     nil,
			},
			expectErr: false,
		},
		{
			name: "invalid: duplicate (proposal_id, vote_decision) pair",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "sv1admin",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 500},
					{ProposalId: 1, VoteDecision: 1, TotalValue: 600},
				},
			},
			expectErr:   true,
			errContains: "duplicate entry",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			err := tc.msg.ValidateBasic()
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func validDelegateVote() *types.MsgDelegateVote {
	return &types.MsgDelegateVote{
		Rk:                 bytes.Repeat([]byte{0x01}, 32),
		SpendAuthSig:       bytes.Repeat([]byte{0x02}, 64),
		SignedNoteNullifier: bytes.Repeat([]byte{0x03}, 32),
		CmxNew:             bytes.Repeat([]byte{0x04}, 32),
		VanCmx:             bytes.Repeat([]byte{0x05}, 32),
		GovNullifiers: [][]byte{
			bytes.Repeat([]byte{0x10}, 32),
			bytes.Repeat([]byte{0x11}, 32),
			bytes.Repeat([]byte{0x12}, 32),
			bytes.Repeat([]byte{0x13}, 32),
			bytes.Repeat([]byte{0x14}, 32),
		},
		Proof:       bytes.Repeat([]byte{0x06}, 128),
		VoteRoundId: bytes.Repeat([]byte{0x07}, 32),
		Sighash:     bytes.Repeat([]byte{0x08}, 32),
	}
}

// ---------------------------------------------------------------------------
// Tests: MsgDelegateVote.ValidateBasic
// ---------------------------------------------------------------------------

func (s *ValidateBasicTestSuite) TestRotatePallasKey_ValidateBasic() {
	tests := []struct {
		name        string
		msg         *types.MsgRotatePallasKey
		expectErr   bool
		errContains string
	}{
		{
			name: "valid: all fields correct",
			msg: &types.MsgRotatePallasKey{
				Creator:     "sv1somevalidator",
				NewPallasPk: bytes.Repeat([]byte{0x01}, 32),
			},
		},
		{
			name: "invalid: empty creator",
			msg: &types.MsgRotatePallasKey{
				Creator:     "",
				NewPallasPk: bytes.Repeat([]byte{0x01}, 32),
			},
			expectErr:   true,
			errContains: "creator cannot be empty",
		},
		{
			name: "invalid: nil new_pallas_pk",
			msg: &types.MsgRotatePallasKey{
				Creator:     "sv1somevalidator",
				NewPallasPk: nil,
			},
			expectErr:   true,
			errContains: "new_pallas_pk must be 32 bytes",
		},
		{
			name: "invalid: new_pallas_pk too short",
			msg: &types.MsgRotatePallasKey{
				Creator:     "sv1somevalidator",
				NewPallasPk: bytes.Repeat([]byte{0x01}, 16),
			},
			expectErr:   true,
			errContains: "new_pallas_pk must be 32 bytes",
		},
		{
			name: "invalid: new_pallas_pk too long",
			msg: &types.MsgRotatePallasKey{
				Creator:     "sv1somevalidator",
				NewPallasPk: bytes.Repeat([]byte{0x01}, 33),
			},
			expectErr:   true,
			errContains: "new_pallas_pk must be 32 bytes",
		},
		{
			name: "invalid: new_pallas_pk is identity (all zeros)",
			msg: &types.MsgRotatePallasKey{
				Creator:     "sv1somevalidator",
				NewPallasPk: make([]byte, 32),
			},
			expectErr:   true,
			errContains: "identity point",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			err := tc.msg.ValidateBasic()
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
				s.Require().ErrorIs(err, types.ErrInvalidField)
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

func (s *ValidateBasicTestSuite) TestDelegateVote_ValidateBasic() {
	tests := []struct {
		name        string
		modify      func(*types.MsgDelegateVote)
		expectErr   bool
		errContains string
	}{
		{
			name:   "valid: distinct gov_nullifiers",
			modify: func(m *types.MsgDelegateVote) {},
		},
		{
			name: "invalid: duplicate gov_nullifiers",
			modify: func(m *types.MsgDelegateVote) {
				m.GovNullifiers[1] = m.GovNullifiers[0]
			},
			expectErr:   true,
			errContains: "duplicate gov_nullifiers",
		},
		{
			name: "invalid: duplicate gov_nullifiers (non-adjacent)",
			modify: func(m *types.MsgDelegateVote) {
				m.GovNullifiers[4] = m.GovNullifiers[0]
			},
			expectErr:   true,
			errContains: "duplicate gov_nullifiers",
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			msg := validDelegateVote()
			tc.modify(msg)
			err := msg.ValidateBasic()
			if tc.expectErr {
				s.Require().Error(err)
				if tc.errContains != "" {
					s.Require().Contains(err.Error(), tc.errContains)
				}
			} else {
				s.Require().NoError(err)
			}
		})
	}
}
