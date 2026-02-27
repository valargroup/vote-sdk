package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/suite"

	zallytest "github.com/z-cale/zally/testutil"
	"github.com/z-cale/zally/x/vote/types"
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
		Creator:           "zvote1admin",
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
		VoteEndTime:       2_000_000,
		NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
		NcRoot:            bytes.Repeat([]byte{0x04}, 32),
		VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
		Proposals: []*types.Proposal{
			{Id: 1, Title: "Proposal A", Description: "First", Options: zallytest.DefaultOptions()},
			{Id: 2, Title: "Proposal B", Description: "Second", Options: zallytest.DefaultOptions()},
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
		{
			name:        "invalid: empty vk_zkp1",
			modify:      func(m *types.MsgCreateVotingSession) { m.VkZkp1 = nil },
			expectErr:   true,
			errContains: "vk_zkp1",
		},
		{
			name:        "invalid: empty vk_zkp2",
			modify:      func(m *types.MsgCreateVotingSession) { m.VkZkp2 = nil },
			expectErr:   true,
			errContains: "vk_zkp2",
		},
		{
			name:        "invalid: empty vk_zkp3",
			modify:      func(m *types.MsgCreateVotingSession) { m.VkZkp3 = nil },
			expectErr:   true,
			errContains: "vk_zkp3",
		},
		{
			name:        "invalid: zero proposals",
			modify:      func(m *types.MsgCreateVotingSession) { m.Proposals = nil },
			expectErr:   true,
			errContains: "proposals count",
		},
		{
			name: "invalid: 17 proposals (exceeds max)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = make([]*types.Proposal, 17)
				for i := range m.Proposals {
					m.Proposals[i] = &types.Proposal{Id: uint32(i), Title: "P", Options: zallytest.DefaultOptions()}
				}
			},
			expectErr:   true,
			errContains: "proposals count",
		},
		{
			name: "invalid: proposal with empty title",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "", Description: "No title", Options: zallytest.DefaultOptions()},
				}
			},
			expectErr:   true,
			errContains: "title",
		},
		{
			name: "invalid: proposal ID mismatch (non-sequential)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok", Options: zallytest.DefaultOptions()},
					{Id: 5, Title: "B", Description: "bad id", Options: zallytest.DefaultOptions()},
				}
			},
			expectErr:   true,
			errContains: "proposal id mismatch",
		},
		{
			name: "valid: single proposal",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "Only Option", Description: "Single", Options: zallytest.DefaultOptions()},
				}
			},
		},
		{
			name: "valid: 16 proposals (max)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = make([]*types.Proposal, 16)
				for i := range m.Proposals {
					m.Proposals[i] = &types.Proposal{Id: uint32(i + 1), Title: "P", Options: zallytest.DefaultOptions()}
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
				Creator:     "zvote1admin",
				Entries: []*types.TallyEntry{
					{ProposalId: 1, VoteDecision: 1, TotalValue: 1000},
				},
			},
		},
		{
			name: "valid: multiple entries",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "zvote1admin",
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
				Creator:     "zvote1admin",
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
				Creator:     "zvote1admin",
				Entries:     nil,
			},
			expectErr: false,
		},
		{
			name: "invalid: duplicate (proposal_id, vote_decision) pair",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "zvote1admin",
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
