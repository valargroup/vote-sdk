package keeper_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

type fakeAccountKeeper struct {
	accounts map[string]sdk.AccountI
	created  []string
}

func newFakeAccountKeeper() *fakeAccountKeeper {
	return &fakeAccountKeeper{accounts: make(map[string]sdk.AccountI)}
}

func (f *fakeAccountKeeper) GetAccount(_ context.Context, addr sdk.AccAddress) sdk.AccountI {
	return f.accounts[addr.String()]
}

func (f *fakeAccountKeeper) NewAccountWithAddress(_ context.Context, addr sdk.AccAddress) sdk.AccountI {
	f.created = append(f.created, addr.String())
	return authtypes.NewBaseAccountWithAddress(addr)
}

func (f *fakeAccountKeeper) SetAccount(_ context.Context, account sdk.AccountI) {
	f.accounts[account.GetAddress().String()] = account
}

func (f *fakeAccountKeeper) seedAccount(addr string) {
	accAddr, err := sdk.AccAddressFromBech32(addr)
	if err != nil {
		panic(err)
	}
	f.accounts[accAddr.String()] = authtypes.NewBaseAccountWithAddress(accAddr)
}

func testAddr(seed byte) string {
	return sdk.AccAddress(bytes.Repeat([]byte{seed}, 20)).String()
}

func TestEndorserIDValidation(t *testing.T) {
	require.NoError(t, types.ValidateEndorserID("zodl"))
	require.NoError(t, types.ValidateEndorserID("zodl-team-1"))
	require.ErrorIs(t, types.ValidateEndorserID(""), types.ErrInvalidEndorserID)
	require.ErrorIs(t, types.ValidateEndorserID("ZODL"), types.ErrInvalidEndorserID)
	require.ErrorIs(t, types.ValidateEndorserID("zodl_team"), types.ErrInvalidEndorserID)
}

func (s *KeeperTestSuite) TestEndorsers_KeeperCRUDAndIteration() {
	kv := s.keeper.OpenKVStore(s.ctx)
	roundID := bytes.Repeat([]byte{0xA1}, types.RoundIDLen)
	addr := testAddr(0x01)

	_, found, err := s.keeper.GetEndorser(kv, "zodl")
	s.Require().NoError(err)
	s.Require().False(found)

	s.Require().NoError(s.keeper.SetEndorser(kv, "zodl", addr))
	got, found, err := s.keeper.GetEndorser(kv, "zodl")
	s.Require().NoError(err)
	s.Require().True(found)
	s.Require().Equal(addr, got)

	var endorsers []*types.Endorser
	s.Require().NoError(s.keeper.IterateEndorsers(kv, func(e *types.Endorser) bool {
		endorsers = append(endorsers, e)
		return false
	}))
	s.Require().Equal([]*types.Endorser{{EndorserId: "zodl", Address: addr}}, endorsers)

	s.Require().NoError(s.keeper.AddEndorsedRound(kv, "zodl", roundID))
	s.Require().NoError(s.keeper.AddEndorsedRound(kv, "zodl", roundID))
	endorsed, err := s.keeper.IsRoundEndorsed(kv, "zodl", roundID)
	s.Require().NoError(err)
	s.Require().True(endorsed)

	var rounds [][]byte
	s.Require().NoError(s.keeper.IterateEndorsedRounds(kv, "zodl", func(id []byte) bool {
		rounds = append(rounds, id)
		return false
	}))
	s.Require().Equal([][]byte{roundID}, rounds)

	s.Require().NoError(s.keeper.DeleteEndorser(kv, "zodl"))
	_, found, err = s.keeper.GetEndorser(kv, "zodl")
	s.Require().NoError(err)
	s.Require().False(found)
	endorsed, err = s.keeper.IsRoundEndorsed(kv, "zodl", roundID)
	s.Require().NoError(err)
	s.Require().True(endorsed)
}

func (s *KeeperTestSuite) TestEndorsers_KeeperMappingMethods_TableDriven() {
	tests := []struct {
		name        string
		preID       string
		preAddr     string
		endorserID  string
		address     string
		wantSetErr  error
		wantFound   bool
		wantAddress string
	}{
		{
			name:        "create mapping",
			endorserID:  "zodl",
			address:     testAddr(0x10),
			wantFound:   true,
			wantAddress: testAddr(0x10),
		},
		{
			name:        "rotate mapping overwrites address",
			preID:       "zodl",
			preAddr:     testAddr(0x10),
			endorserID:  "zodl",
			address:     testAddr(0x11),
			wantFound:   true,
			wantAddress: testAddr(0x11),
		},
		{
			name:       "empty address rejected",
			endorserID: "zodl",
			address:    "",
			wantSetErr: types.ErrInvalidField,
		},
		{
			name:       "invalid id rejected",
			endorserID: "ZODL",
			address:    testAddr(0x12),
			wantSetErr: types.ErrInvalidEndorserID,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			if tc.preID != "" {
				s.Require().NoError(s.keeper.SetEndorser(kv, tc.preID, tc.preAddr))
			}

			err := s.keeper.SetEndorser(kv, tc.endorserID, tc.address)
			if tc.wantSetErr != nil {
				s.Require().ErrorIs(err, tc.wantSetErr)
				return
			}
			s.Require().NoError(err)

			got, found, err := s.keeper.GetEndorser(kv, tc.endorserID)
			s.Require().NoError(err)
			s.Require().Equal(tc.wantFound, found)
			s.Require().Equal(tc.wantAddress, got)
		})
	}
}

func (s *KeeperTestSuite) TestEndorsers_KeeperDeleteMethods_TableDriven() {
	tests := []struct {
		name       string
		preID      string
		preAddr    string
		deleteID   string
		wantErr    error
		wantFound  bool
		wantRemain bool
	}{
		{
			name:      "delete existing mapping",
			preID:     "zodl",
			preAddr:   testAddr(0x20),
			deleteID:  "zodl",
			wantFound: false,
		},
		{
			name:      "delete missing mapping is idempotent",
			deleteID:  "zodl",
			wantFound: false,
		},
		{
			name:     "invalid id rejected",
			deleteID: "ZODL",
			wantErr:  types.ErrInvalidEndorserID,
		},
		{
			name:       "delete one mapping leaves others intact",
			preID:      "zodl",
			preAddr:    testAddr(0x21),
			deleteID:   "zodl",
			wantRemain: true,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			if tc.preID != "" {
				s.Require().NoError(s.keeper.SetEndorser(kv, tc.preID, tc.preAddr))
			}
			if tc.wantRemain {
				s.Require().NoError(s.keeper.SetEndorser(kv, "other", testAddr(0x22)))
			}

			err := s.keeper.DeleteEndorser(kv, tc.deleteID)
			if tc.wantErr != nil {
				s.Require().ErrorIs(err, tc.wantErr)
				return
			}
			s.Require().NoError(err)

			_, found, err := s.keeper.GetEndorser(kv, tc.deleteID)
			s.Require().NoError(err)
			s.Require().Equal(tc.wantFound, found)
			if tc.wantRemain {
				got, found, err := s.keeper.GetEndorser(kv, "other")
				s.Require().NoError(err)
				s.Require().True(found)
				s.Require().Equal(testAddr(0x22), got)
			}
		})
	}
}

func (s *KeeperTestSuite) TestEndorsers_KeeperEndorsementMethods_TableDriven() {
	validRoundID := bytes.Repeat([]byte{0xB1}, types.RoundIDLen)
	otherRoundID := bytes.Repeat([]byte{0xB2}, types.RoundIDLen)

	tests := []struct {
		name            string
		addEndorserID   string
		addRoundID      []byte
		checkEndorserID string
		checkRoundID    []byte
		wantAddErr      error
		wantCheckErr    error
		wantEndorsed    bool
		wantIterErr     error
		wantIterRounds  [][]byte
	}{
		{
			name:            "add and read endorsement",
			addEndorserID:   "zodl",
			addRoundID:      validRoundID,
			checkEndorserID: "zodl",
			checkRoundID:    validRoundID,
			wantEndorsed:    true,
			wantIterRounds:  [][]byte{validRoundID},
		},
		{
			name:            "missing endorsement reads false",
			checkEndorserID: "zodl",
			checkRoundID:    validRoundID,
			wantEndorsed:    false,
			wantIterRounds:  nil,
		},
		{
			name:          "invalid id rejected on add",
			addEndorserID: "ZODL",
			addRoundID:    validRoundID,
			wantAddErr:    types.ErrInvalidEndorserID,
		},
		{
			name:          "invalid round rejected on add",
			addEndorserID: "zodl",
			addRoundID:    []byte{0x01},
			wantAddErr:    types.ErrInvalidRoundIDLen,
		},
		{
			name:            "invalid id rejected on check",
			checkEndorserID: "ZODL",
			checkRoundID:    validRoundID,
			wantCheckErr:    types.ErrInvalidEndorserID,
		},
		{
			name:            "invalid round rejected on check",
			checkEndorserID: "zodl",
			checkRoundID:    []byte{0x01},
			wantCheckErr:    types.ErrInvalidRoundIDLen,
		},
		{
			name:           "invalid id rejected on iterate",
			addEndorserID:  "zodl",
			addRoundID:     otherRoundID,
			wantIterErr:    types.ErrInvalidEndorserID,
			wantIterRounds: nil,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			if tc.addEndorserID != "" {
				err := s.keeper.AddEndorsedRound(kv, tc.addEndorserID, tc.addRoundID)
				if tc.wantAddErr != nil {
					s.Require().ErrorIs(err, tc.wantAddErr)
					return
				}
				s.Require().NoError(err)
				s.Require().NoError(s.keeper.AddEndorsedRound(kv, tc.addEndorserID, tc.addRoundID))
			}

			if tc.checkEndorserID != "" {
				got, err := s.keeper.IsRoundEndorsed(kv, tc.checkEndorserID, tc.checkRoundID)
				if tc.wantCheckErr != nil {
					s.Require().ErrorIs(err, tc.wantCheckErr)
					return
				}
				s.Require().NoError(err)
				s.Require().Equal(tc.wantEndorsed, got)
			}

			iterID := tc.addEndorserID
			if iterID == "" {
				iterID = tc.checkEndorserID
			}
			if tc.wantIterErr != nil {
				iterID = "ZODL"
			}
			var rounds [][]byte
			err := s.keeper.IterateEndorsedRounds(kv, iterID, func(roundID []byte) bool {
				rounds = append(rounds, roundID)
				return false
			})
			if tc.wantIterErr != nil {
				s.Require().ErrorIs(err, tc.wantIterErr)
				return
			}
			s.Require().NoError(err)
			s.Require().Equal(tc.wantIterRounds, rounds)
		})
	}
}

func (s *KeeperTestSuite) TestEndorsers_KeeperDeleteEndorsementMethods_TableDriven() {
	validRoundID := bytes.Repeat([]byte{0xC1}, types.RoundIDLen)
	otherRoundID := bytes.Repeat([]byte{0xC2}, types.RoundIDLen)

	tests := []struct {
		name             string
		preEndorserID    string
		preRoundID       []byte
		deleteEndorserID string
		deleteRoundID    []byte
		wantDeleteErr    error
		wantEndorsed     bool
	}{
		{
			name:             "delete existing endorsement",
			preEndorserID:    "zodl",
			preRoundID:       validRoundID,
			deleteEndorserID: "zodl",
			deleteRoundID:    validRoundID,
			wantEndorsed:     false,
		},
		{
			name:             "delete missing endorsement is idempotent",
			deleteEndorserID: "zodl",
			deleteRoundID:    validRoundID,
			wantEndorsed:     false,
		},
		{
			name:             "delete one endorsement leaves others intact",
			preEndorserID:    "zodl",
			preRoundID:       otherRoundID,
			deleteEndorserID: "zodl",
			deleteRoundID:    validRoundID,
			wantEndorsed:     true,
		},
		{
			name:             "invalid id rejected",
			deleteEndorserID: "ZODL",
			deleteRoundID:    validRoundID,
			wantDeleteErr:    types.ErrInvalidEndorserID,
		},
		{
			name:             "invalid round rejected",
			deleteEndorserID: "zodl",
			deleteRoundID:    []byte{0x01},
			wantDeleteErr:    types.ErrInvalidRoundIDLen,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			if tc.preEndorserID != "" {
				s.Require().NoError(s.keeper.AddEndorsedRound(kv, tc.preEndorserID, tc.preRoundID))
			}

			err := s.keeper.DeleteEndorsedRound(kv, tc.deleteEndorserID, tc.deleteRoundID)
			if tc.wantDeleteErr != nil {
				s.Require().ErrorIs(err, tc.wantDeleteErr)
				return
			}
			s.Require().NoError(err)

			checkRoundID := tc.preRoundID
			if checkRoundID == nil {
				checkRoundID = tc.deleteRoundID
			}
			endorsed, err := s.keeper.IsRoundEndorsed(kv, "zodl", checkRoundID)
			s.Require().NoError(err)
			s.Require().Equal(tc.wantEndorsed, endorsed)
		})
	}
}

func (s *KeeperTestSuite) TestEndorsers_KeeperIterators_TableDriven() {
	tests := []struct {
		name             string
		endorsers        []*types.Endorser
		endorsements     []*types.EndorsedRound
		stopEndorsers    bool
		stopEndorsements bool
		wantEndorsers    []*types.Endorser
		wantRoundIDs     [][]byte
	}{
		{
			name: "iterate all endorsers in key order",
			endorsers: []*types.Endorser{
				{EndorserId: "zodl-b", Address: testAddr(0x30)},
				{EndorserId: "zodl-a", Address: testAddr(0x31)},
			},
			wantEndorsers: []*types.Endorser{
				{EndorserId: "zodl-a", Address: testAddr(0x31)},
				{EndorserId: "zodl-b", Address: testAddr(0x30)},
			},
		},
		{
			name:          "endorser iterator can stop early",
			stopEndorsers: true,
			endorsers: []*types.Endorser{
				{EndorserId: "zodl-a", Address: testAddr(0x32)},
				{EndorserId: "zodl-b", Address: testAddr(0x33)},
			},
			wantEndorsers: []*types.Endorser{
				{EndorserId: "zodl-a", Address: testAddr(0x32)},
			},
		},
		{
			name: "iterate endorsed rounds in key order",
			endorsements: []*types.EndorsedRound{
				{EndorserId: "zodl", VoteRoundId: bytes.Repeat([]byte{0x42}, types.RoundIDLen)},
				{EndorserId: "zodl", VoteRoundId: bytes.Repeat([]byte{0x41}, types.RoundIDLen)},
				{EndorserId: "other", VoteRoundId: bytes.Repeat([]byte{0x40}, types.RoundIDLen)},
			},
			wantRoundIDs: [][]byte{
				bytes.Repeat([]byte{0x41}, types.RoundIDLen),
				bytes.Repeat([]byte{0x42}, types.RoundIDLen),
			},
		},
		{
			name:             "endorsed round iterator can stop early",
			stopEndorsements: true,
			endorsements: []*types.EndorsedRound{
				{EndorserId: "zodl", VoteRoundId: bytes.Repeat([]byte{0x51}, types.RoundIDLen)},
				{EndorserId: "zodl", VoteRoundId: bytes.Repeat([]byte{0x52}, types.RoundIDLen)},
			},
			wantRoundIDs: [][]byte{
				bytes.Repeat([]byte{0x51}, types.RoundIDLen),
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			for _, endorser := range tc.endorsers {
				s.Require().NoError(s.keeper.SetEndorser(kv, endorser.EndorserId, endorser.Address))
			}
			for _, endorsement := range tc.endorsements {
				s.Require().NoError(s.keeper.AddEndorsedRound(kv, endorsement.EndorserId, endorsement.VoteRoundId))
			}

			var gotEndorsers []*types.Endorser
			s.Require().NoError(s.keeper.IterateEndorsers(kv, func(endorser *types.Endorser) bool {
				gotEndorsers = append(gotEndorsers, endorser)
				return tc.stopEndorsers
			}))
			if tc.wantEndorsers != nil {
				s.Require().Equal(tc.wantEndorsers, gotEndorsers)
			}

			var gotRoundIDs [][]byte
			s.Require().NoError(s.keeper.IterateEndorsedRounds(kv, "zodl", func(roundID []byte) bool {
				gotRoundIDs = append(gotRoundIDs, roundID)
				return tc.stopEndorsements
			}))
			if tc.wantRoundIDs != nil {
				s.Require().Equal(tc.wantRoundIDs, gotRoundIDs)
			}
		})
	}
}

func (s *KeeperTestSuite) TestEndorsers_MsgServerAuthAndIdempotency() {
	kv := s.keeper.OpenKVStore(s.ctx)
	manager := testAddr(0x01)
	endorser := testAddr(0x02)
	stranger := testAddr(0x03)
	roundID := bytes.Repeat([]byte{0xA2}, types.RoundIDLen)

	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{Addresses: []string{manager}}))
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: roundID,
		VoteEndTime: activeEndTime,
		Creator:     manager,
	}))

	msgServer := keeper.NewMsgServerImpl(s.keeper)
	_, err := msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: stranger,
		Payload: coordinatorPayloadForMessage(s.T(), &types.MsgSetEndorser{
			Creator:    stranger,
			EndorserId: "zodl",
			Address:    endorser,
		}),
	})
	s.Require().ErrorIs(err, types.ErrNotAuthorized)

	_, err = msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager,
		Payload: coordinatorPayloadForMessage(s.T(), &types.MsgSetEndorser{
			Creator:    manager,
			EndorserId: "zodl",
			Address:    endorser,
		}),
	})
	s.Require().NoError(err)

	_, err = msgServer.EndorseRound(s.ctx, &types.MsgEndorseRound{
		Creator:     stranger,
		EndorserId:  "zodl",
		VoteRoundId: roundID,
	})
	s.Require().ErrorIs(err, types.ErrNotAuthorized)

	_, err = msgServer.EndorseRound(s.ctx, &types.MsgEndorseRound{
		Creator:     endorser,
		EndorserId:  "missing",
		VoteRoundId: roundID,
	})
	s.Require().ErrorIs(err, types.ErrEndorserNotFound)

	_, err = msgServer.EndorseRound(s.ctx, &types.MsgEndorseRound{
		Creator:     endorser,
		EndorserId:  "zodl",
		VoteRoundId: bytes.Repeat([]byte{0xFF}, types.RoundIDLen),
	})
	s.Require().ErrorIs(err, types.ErrRoundNotFound)

	_, err = msgServer.EndorseRound(s.ctx, &types.MsgEndorseRound{
		Creator:     endorser,
		EndorserId:  "zodl",
		VoteRoundId: roundID,
	})
	s.Require().NoError(err)
	_, err = msgServer.EndorseRound(s.ctx, &types.MsgEndorseRound{
		Creator:     endorser,
		EndorserId:  "zodl",
		VoteRoundId: roundID,
	})
	s.Require().NoError(err)

	var endorsed [][]byte
	s.Require().NoError(s.keeper.IterateEndorsedRounds(kv, "zodl", func(id []byte) bool {
		endorsed = append(endorsed, id)
		return false
	}))
	s.Require().Len(endorsed, 1)

	_, err = msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
		Creator: manager,
		Payload: coordinatorPayloadForMessage(s.T(), &types.MsgSetEndorser{
			Creator:    manager,
			EndorserId: "zodl",
			Address:    "",
		}),
	})
	s.Require().NoError(err)
	_, found, err := s.keeper.GetEndorser(kv, "zodl")
	s.Require().NoError(err)
	s.Require().False(found)
}

func (s *KeeperTestSuite) TestEndorsers_SetEndorserDirectRequiresCoordinatorAction() {
	kv := s.keeper.OpenKVStore(s.ctx)
	manager := testAddr(0x01)
	endorser := testAddr(0x02)
	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{Addresses: []string{manager}}))

	msgServer := keeper.NewMsgServerImpl(s.keeper)
	_, err := msgServer.SetEndorser(s.ctx, &types.MsgSetEndorser{
		Creator:    manager,
		EndorserId: "zodl",
		Address:    endorser,
	})
	s.Require().ErrorIs(err, types.ErrCoordinatorActionRequired)

	_, found, err := s.keeper.GetEndorser(kv, "zodl")
	s.Require().NoError(err)
	s.Require().False(found)
}

func (s *KeeperTestSuite) TestEndorsers_MsgClearRoundEndorsement_TableDriven() {
	manager := testAddr(0x01)
	endorser := testAddr(0x02)
	stranger := testAddr(0x03)
	roundID := bytes.Repeat([]byte{0xC3}, types.RoundIDLen)
	otherRoundID := bytes.Repeat([]byte{0xC4}, types.RoundIDLen)
	missingRoundID := bytes.Repeat([]byte{0xFF}, types.RoundIDLen)

	tests := []struct {
		name           string
		creator        string
		endorserID     string
		voteRoundID    []byte
		preMapping     bool
		preEndorsement bool
		wantErr        error
		wantEndorsed   bool
	}{
		{
			name:           "authorized clear removes endorsement",
			creator:        endorser,
			endorserID:     "zodl",
			voteRoundID:    roundID,
			preMapping:     true,
			preEndorsement: true,
			wantEndorsed:   false,
		},
		{
			name:         "repeated clear is idempotent",
			creator:      endorser,
			endorserID:   "zodl",
			voteRoundID:  roundID,
			preMapping:   true,
			wantEndorsed: false,
		},
		{
			name:           "unauthorized clear rejected",
			creator:        stranger,
			endorserID:     "zodl",
			voteRoundID:    roundID,
			preMapping:     true,
			preEndorsement: true,
			wantErr:        types.ErrNotAuthorized,
			wantEndorsed:   true,
		},
		{
			name:         "missing endorser mapping rejected",
			creator:      endorser,
			endorserID:   "zodl",
			voteRoundID:  roundID,
			wantErr:      types.ErrEndorserNotFound,
			wantEndorsed: false,
		},
		{
			name:         "missing round rejected",
			creator:      endorser,
			endorserID:   "zodl",
			voteRoundID:  missingRoundID,
			preMapping:   true,
			wantErr:      types.ErrRoundNotFound,
			wantEndorsed: false,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{Addresses: []string{manager}}))
			s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
				VoteRoundId: roundID,
				VoteEndTime: activeEndTime,
				Creator:     manager,
			}))
			s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
				VoteRoundId: otherRoundID,
				VoteEndTime: activeEndTime,
				Creator:     manager,
			}))
			if tc.preMapping {
				s.Require().NoError(s.keeper.SetEndorser(kv, tc.endorserID, endorser))
			}
			if tc.preEndorsement {
				s.Require().NoError(s.keeper.AddEndorsedRound(kv, tc.endorserID, tc.voteRoundID))
			}

			msgServer := keeper.NewMsgServerImpl(s.keeper)
			_, err := msgServer.ClearRoundEndorsement(s.ctx, &types.MsgClearRoundEndorsement{
				Creator:     tc.creator,
				EndorserId:  tc.endorserID,
				VoteRoundId: tc.voteRoundID,
			})
			if tc.wantErr != nil {
				s.Require().ErrorIs(err, tc.wantErr)
			} else {
				s.Require().NoError(err)
			}

			endorsed, err := s.keeper.IsRoundEndorsed(kv, tc.endorserID, tc.voteRoundID)
			s.Require().NoError(err)
			s.Require().Equal(tc.wantEndorsed, endorsed)
		})
	}
}

func (s *KeeperTestSuite) TestEndorsers_MsgSetEndorserAccountCreation_TableDriven() {
	manager := testAddr(0x01)
	stranger := testAddr(0x02)
	oldEndorser := testAddr(0x03)
	newEndorser := testAddr(0x04)

	tests := []struct {
		name           string
		creator        string
		preMapping     string
		preAccounts    []string
		address        string
		wantErr        error
		wantMapping    bool
		wantAddress    string
		wantCreated    []string
		wantAccounts   []string
		wantNoAccounts []string
	}{
		{
			name:         "missing target account",
			creator:      manager,
			address:      newEndorser,
			wantMapping:  true,
			wantAddress:  newEndorser,
			wantCreated:  []string{newEndorser},
			wantAccounts: []string{newEndorser},
		},
		{
			name:         "existing target account",
			creator:      manager,
			preAccounts:  []string{newEndorser},
			address:      newEndorser,
			wantMapping:  true,
			wantAddress:  newEndorser,
			wantAccounts: []string{newEndorser},
		},
		{
			name:         "rotate to missing target",
			creator:      manager,
			preMapping:   oldEndorser,
			address:      newEndorser,
			wantMapping:  true,
			wantAddress:  newEndorser,
			wantCreated:  []string{newEndorser},
			wantAccounts: []string{newEndorser},
		},
		{
			name:         "clear mapping",
			creator:      manager,
			preMapping:   oldEndorser,
			preAccounts:  []string{oldEndorser},
			address:      "",
			wantMapping:  false,
			wantAccounts: []string{oldEndorser},
		},
		{
			name:           "non-vote-manager rejected",
			creator:        stranger,
			address:        newEndorser,
			wantErr:        types.ErrNotAuthorized,
			wantMapping:    false,
			wantNoAccounts: []string{newEndorser},
		},
		{
			name:           "invalid endorser address rejected",
			creator:        manager,
			address:        "not-a-bech32",
			wantErr:        types.ErrInvalidField,
			wantMapping:    false,
			wantNoAccounts: []string{newEndorser},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)
			accounts := newFakeAccountKeeper()
			s.keeper.SetAccountKeeper(accounts)
			s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{Addresses: []string{manager}}))

			for _, addr := range tc.preAccounts {
				accounts.seedAccount(addr)
			}
			if tc.preMapping != "" {
				s.Require().NoError(s.keeper.SetEndorser(kv, "zodl", tc.preMapping))
			}

			msgServer := keeper.NewMsgServerImpl(s.keeper)
			_, err := msgServer.ProposeCoordinatorAction(s.ctx, &types.MsgProposeCoordinatorAction{
				Creator: tc.creator,
				Payload: coordinatorPayloadForMessage(s.T(), &types.MsgSetEndorser{
					Creator:    tc.creator,
					EndorserId: "zodl",
					Address:    tc.address,
				}),
			})
			if tc.wantErr != nil {
				s.Require().ErrorIs(err, tc.wantErr)
			} else {
				s.Require().NoError(err)
			}

			got, found, err := s.keeper.GetEndorser(kv, "zodl")
			s.Require().NoError(err)
			s.Require().Equal(tc.wantMapping, found)
			if tc.wantMapping {
				s.Require().Equal(tc.wantAddress, got)
			}
			s.Require().Equal(tc.wantCreated, accounts.created)
			for _, addr := range tc.wantAccounts {
				accAddr, err := sdk.AccAddressFromBech32(addr)
				s.Require().NoError(err)
				s.Require().NotNil(accounts.GetAccount(s.ctx, accAddr))
			}
			for _, addr := range tc.wantNoAccounts {
				accAddr, err := sdk.AccAddressFromBech32(addr)
				s.Require().NoError(err)
				s.Require().Nil(accounts.GetAccount(s.ctx, accAddr))
			}
		})
	}
}

func (s *KeeperTestSuite) TestEndorsers_QueryServer() {
	kv := s.keeper.OpenKVStore(s.ctx)
	addr := testAddr(0x04)
	roundID := bytes.Repeat([]byte{0xA3}, types.RoundIDLen)
	s.Require().NoError(s.keeper.SetEndorser(kv, "zodl", addr))
	s.Require().NoError(s.keeper.AddEndorsedRound(kv, "zodl", roundID))

	queryServer := keeper.NewQueryServerImpl(s.keeper)
	endorsers, err := queryServer.Endorsers(s.ctx, &types.QueryEndorsersRequest{})
	s.Require().NoError(err)
	s.Require().Equal([]*types.Endorser{{EndorserId: "zodl", Address: addr}}, endorsers.Endorsers)

	endorsedRounds, err := queryServer.EndorsedRounds(s.ctx, &types.QueryEndorsedRoundsRequest{EndorserId: "zodl"})
	s.Require().NoError(err)
	s.Require().Equal([][]byte{roundID}, endorsedRounds.VoteRoundIds)
}

func (s *KeeperTestSuite) TestEndorsers_GenesisRoundTrip() {
	kv := s.keeper.OpenKVStore(s.ctx)
	manager := testAddr(0x05)
	endorser := testAddr(0x06)
	roundID := bytes.Repeat([]byte{0xA4}, types.RoundIDLen)

	s.Require().NoError(s.keeper.SetVoteManagers(kv, &types.VoteManagerSet{Addresses: []string{manager}}))
	s.Require().NoError(s.keeper.SetEndorser(kv, "zodl", endorser))
	s.Require().NoError(s.keeper.AddEndorsedRound(kv, "zodl", roundID))

	exported, err := s.keeper.ExportGenesis(kv)
	s.Require().NoError(err)
	s.Require().Equal([]*types.Endorser{{EndorserId: "zodl", Address: endorser}}, exported.Endorsers)
	s.Require().Equal([]*types.EndorsedRound{{EndorserId: "zodl", VoteRoundId: roundID}}, exported.EndorsedRounds)

	s.SetupTest()
	kv = s.keeper.OpenKVStore(s.ctx)
	s.Require().NoError(s.keeper.InitGenesis(kv, exported))

	got, found, err := s.keeper.GetEndorser(kv, "zodl")
	s.Require().NoError(err)
	s.Require().True(found)
	s.Require().Equal(endorser, got)

	endorsed, err := s.keeper.IsRoundEndorsed(kv, "zodl", roundID)
	s.Require().NoError(err)
	s.Require().True(endorsed)
}
