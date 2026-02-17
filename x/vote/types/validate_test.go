package types_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/suite"

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
			{Id: 1, Title: "Proposal A", Description: "First"},
			{Id: 2, Title: "Proposal B", Description: "Second"},
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
					m.Proposals[i] = &types.Proposal{Id: uint32(i), Title: "P"}
				}
			},
			expectErr:   true,
			errContains: "proposals count",
		},
		{
			name: "invalid: proposal with empty title",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "", Description: "No title"},
				}
			},
			expectErr:   true,
			errContains: "title",
		},
		{
			name: "invalid: proposal ID mismatch (non-sequential)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "A", Description: "ok"},
					{Id: 5, Title: "B", Description: "bad id"},
				}
			},
			expectErr:   true,
			errContains: "proposal id mismatch",
		},
		{
			name: "valid: single proposal",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{
					{Id: 1, Title: "Only Option", Description: "Single"},
				}
			},
		},
		{
			name: "valid: 16 proposals (max)",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = make([]*types.Proposal, 16)
				for i := range m.Proposals {
					m.Proposals[i] = &types.Proposal{Id: uint32(i + 1), Title: "P"}
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
			name: "invalid: empty entries",
			msg: &types.MsgSubmitTally{
				VoteRoundId: bytes.Repeat([]byte{0x01}, 32),
				Creator:     "zvote1admin",
				Entries:     nil,
			},
			expectErr:   true,
			errContains: "entries cannot be empty",
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
