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
		VkZkp1:            bytes.Repeat([]byte{0x06}, 64),
		VkZkp2:            bytes.Repeat([]byte{0x07}, 64),
		VkZkp3:            bytes.Repeat([]byte{0x08}, 64),
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
		// --- option_groups ---
		{
			name: "valid: one group for multi-option camp, standalone options ungrouped",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "Sprout sunset", Description: "When?",
					Options: []*types.VoteOption{
						{Index: 0, Label: "Immediately"},
						{Index: 1, Label: "One year"},
						{Index: 2, Label: "Two years"},
						{Index: 3, Label: "When quantum threat"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "Fixed date", OptionIndices: []uint32{1, 2}},
					},
				}}
			},
		},
		{
			name: "valid: multiple groups, some options ungrouped",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "Complex", Description: "Many camps",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B1"},
						{Index: 2, Label: "B2"},
						{Index: 3, Label: "C1"},
						{Index: 4, Label: "C2"},
						{Index: 5, Label: "D"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "Camp B", OptionIndices: []uint32{1, 2}},
						{Id: 1, Label: "Camp C", OptionIndices: []uint32{3, 4}},
					},
				}}
			},
		},
		{
			name: "valid: proposal without groups",
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "Simple", Description: "Binary",
					Options: svtest.DefaultOptions(),
				}}
			},
		},
		{
			name:        "invalid: group with only 1 option (use standalone instead)",
			errContains: "at least 2 options",
			expectErr:   true,
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "A", Description: "d",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B"},
						{Index: 2, Label: "C"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "Solo", OptionIndices: []uint32{0}},
					},
				}}
			},
		},
		{
			name:        "invalid: group with empty option_indices",
			errContains: "at least 2 options",
			expectErr:   true,
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "A", Description: "d",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B"},
						{Index: 2, Label: "C"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "Empty", OptionIndices: nil},
					},
				}}
			},
		},
		{
			name:        "invalid: group ID not sequential",
			errContains: "group id mismatch",
			expectErr:   true,
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "A", Description: "d",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B"},
						{Index: 2, Label: "C"},
						{Index: 3, Label: "D"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "G0", OptionIndices: []uint32{0, 1}},
						{Id: 5, Label: "G5", OptionIndices: []uint32{2, 3}},
					},
				}}
			},
		},
		{
			name:        "invalid: group with empty label",
			errContains: "label cannot be empty",
			expectErr:   true,
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "A", Description: "d",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B"},
						{Index: 2, Label: "C"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "", OptionIndices: []uint32{0, 1}},
					},
				}}
			},
		},
		{
			name:        "invalid: group with non-ASCII label",
			errContains: "ASCII",
			expectErr:   true,
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "A", Description: "d",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B"},
						{Index: 2, Label: "C"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "Résultat", OptionIndices: []uint32{0, 1}},
					},
				}}
			},
		},
		{
			name:        "invalid: group references out-of-range option index",
			errContains: "references option index 5",
			expectErr:   true,
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "A", Description: "d",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B"},
						{Index: 2, Label: "C"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "G0", OptionIndices: []uint32{0, 5}},
					},
				}}
			},
		},
		{
			name:        "invalid: overlapping option indices across groups",
			errContains: "appears in both group",
			expectErr:   true,
			modify: func(m *types.MsgCreateVotingSession) {
				m.Proposals = []*types.Proposal{{
					Id: 1, Title: "A", Description: "d",
					Options: []*types.VoteOption{
						{Index: 0, Label: "A"},
						{Index: 1, Label: "B"},
						{Index: 2, Label: "C"},
						{Index: 3, Label: "D"},
					},
					OptionGroups: []*types.OptionGroup{
						{Id: 0, Label: "G0", OptionIndices: []uint32{0, 1}},
						{Id: 1, Label: "G1", OptionIndices: []uint32{1, 2}},
					},
				}}
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
