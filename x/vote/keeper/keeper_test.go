package keeper_test

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/z-cale/zally/crypto/elgamal"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// fpLE returns a 32-byte little-endian Pallas Fp encoding of v (canonical for 0 <= v < 2^64).
// Use for commitment tree leaves so the votetree FFI accepts them.
func fpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// validCiphertextBytes generates a real serialized ElGamal ciphertext encrypting
// value v under a fresh random key. Used in tests that must pass the keeper's
// well-formedness check on the first AddToTally call.
func validCiphertextBytes(t *testing.T, v uint64) []byte {
	t.Helper()
	_, pk := elgamal.KeyGen(rand.Reader)
	ct, err := elgamal.Encrypt(pk, v, rand.Reader)
	if err != nil {
		t.Fatalf("validCiphertextBytes: Encrypt: %v", err)
	}
	bz, err := elgamal.MarshalCiphertext(ct)
	if err != nil {
		t.Fatalf("validCiphertextBytes: MarshalCiphertext: %v", err)
	}
	return bz
}

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

var (
	testBlockTime  = time.Unix(1_000_000, 0).UTC()
	activeEndTime  = uint64(2_000_000)
	expiredEndTime = uint64(999_999)
	testRoundID    = bytes.Repeat([]byte{0x01}, 32)
)

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

type KeeperTestSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper *keeper.Keeper
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(KeeperTestSuite))
}

func (s *KeeperTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.WithBlockTime(testBlockTime)
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), nil)
}

func uint64Ptr(v uint64) *uint64 { return &v }

// ---------------------------------------------------------------------------
// VoteRound CRUD
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestVoteRound_SetAndGet() {
	tests := []struct {
		name        string
		round       *types.VoteRound
		lookupID    []byte
		expectFound bool
	}{
		{
			name: "store and retrieve round",
			round: &types.VoteRound{
				VoteRoundId:       testRoundID,
				SnapshotHeight:    100,
				SnapshotBlockhash: bytes.Repeat([]byte{0x01}, 32),
				ProposalsHash:     bytes.Repeat([]byte{0x02}, 32),
				VoteEndTime:       activeEndTime,
				NullifierImtRoot:  bytes.Repeat([]byte{0x03}, 32),
				NcRoot:            bytes.Repeat([]byte{0x04}, 32),
				Creator:           "zvote1creator",
			},
			lookupID:    testRoundID,
			expectFound: true,
		},
		{
			name:        "missing round returns ErrRoundNotFound",
			lookupID:    bytes.Repeat([]byte{0xFF}, 32),
			expectFound: false,
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			kv := s.keeper.OpenKVStore(s.ctx)

			if tc.round != nil {
				s.Require().NoError(s.keeper.SetVoteRound(kv, tc.round))
			}

			got, err := s.keeper.GetVoteRound(kv, tc.lookupID)
			if tc.expectFound {
				s.Require().NoError(err)
				s.Require().Equal(tc.round.VoteRoundId, got.VoteRoundId)
				s.Require().Equal(tc.round.SnapshotHeight, got.SnapshotHeight)
				s.Require().Equal(tc.round.VoteEndTime, got.VoteEndTime)
				s.Require().Equal(tc.round.Creator, got.Creator)
			} else {
				s.Require().ErrorIs(err, types.ErrRoundNotFound)
				s.Require().Nil(got)
			}
		})
	}
}

func (s *KeeperTestSuite) TestVoteRound_OverwriteExisting() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	round := &types.VoteRound{
		VoteRoundId: testRoundID,
		VoteEndTime: 1000,
		Creator:     "original",
	}
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))

	// Overwrite with new end time.
	round.VoteEndTime = 9999
	round.Creator = "updated"
	s.Require().NoError(s.keeper.SetVoteRound(kv, round))

	got, err := s.keeper.GetVoteRound(kv, testRoundID)
	s.Require().NoError(err)
	s.Require().Equal(uint64(9999), got.VoteEndTime)
	s.Require().Equal("updated", got.Creator)
}

func (s *KeeperTestSuite) TestVoteRound_MultipleRoundsIndependent() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	id1 := bytes.Repeat([]byte{0x01}, 32)
	id2 := bytes.Repeat([]byte{0x02}, 32)

	round1 := &types.VoteRound{VoteRoundId: id1, VoteEndTime: 1000, Creator: "alice"}
	round2 := &types.VoteRound{VoteRoundId: id2, VoteEndTime: 2000, Creator: "bob"}

	s.Require().NoError(s.keeper.SetVoteRound(kv, round1))
	s.Require().NoError(s.keeper.SetVoteRound(kv, round2))

	got1, err := s.keeper.GetVoteRound(kv, id1)
	s.Require().NoError(err)
	s.Require().Equal("alice", got1.Creator)

	got2, err := s.keeper.GetVoteRound(kv, id2)
	s.Require().NoError(err)
	s.Require().Equal("bob", got2.Creator)
}

// ---------------------------------------------------------------------------
// UpdateVoteRoundStatus
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestUpdateVoteRoundStatus() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: testRoundID,
		VoteEndTime: activeEndTime,
		Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
	}))

	// Transition to TALLYING.
	s.Require().NoError(s.keeper.UpdateVoteRoundStatus(kv, testRoundID, types.SessionStatus_SESSION_STATUS_TALLYING))

	round, err := s.keeper.GetVoteRound(kv, testRoundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)

	// Transition to FINALIZED.
	s.Require().NoError(s.keeper.UpdateVoteRoundStatus(kv, testRoundID, types.SessionStatus_SESSION_STATUS_FINALIZED))

	round, err = s.keeper.GetVoteRound(kv, testRoundID)
	s.Require().NoError(err)
	s.Require().Equal(types.SessionStatus_SESSION_STATUS_FINALIZED, round.Status)

	// Missing round returns error.
	err = s.keeper.UpdateVoteRoundStatus(kv, bytes.Repeat([]byte{0xFF}, 32), types.SessionStatus_SESSION_STATUS_TALLYING)
	s.Require().ErrorIs(err, types.ErrRoundNotFound)
}

// ---------------------------------------------------------------------------
// IterateActiveRounds
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestIterateActiveRounds() {
	s.SetupTest()
	kv := s.keeper.OpenKVStore(s.ctx)

	id1 := bytes.Repeat([]byte{0x01}, 32)
	id2 := bytes.Repeat([]byte{0x02}, 32)
	id3 := bytes.Repeat([]byte{0x03}, 32)

	// Active round.
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: id1, VoteEndTime: activeEndTime, Status: types.SessionStatus_SESSION_STATUS_ACTIVE,
	}))
	// Tallying round (should be skipped).
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: id2, VoteEndTime: expiredEndTime, Status: types.SessionStatus_SESSION_STATUS_TALLYING,
	}))
	// Another active round.
	s.Require().NoError(s.keeper.SetVoteRound(kv, &types.VoteRound{
		VoteRoundId: id3, VoteEndTime: activeEndTime, Status: types.SessionStatus_SESSION_STATUS_ACTIVE,
	}))

	var activeIDs [][]byte
	err := s.keeper.IterateActiveRounds(kv, func(round *types.VoteRound) bool {
		id := make([]byte, len(round.VoteRoundId))
		copy(id, round.VoteRoundId)
		activeIDs = append(activeIDs, id)
		return false
	})
	s.Require().NoError(err)
	s.Require().Len(activeIDs, 2, "should yield only ACTIVE rounds")

	// Verify the correct IDs were returned (order follows KV store key ordering).
	s.Require().True(bytes.Equal(activeIDs[0], id1) || bytes.Equal(activeIDs[0], id3))
	s.Require().True(bytes.Equal(activeIDs[1], id1) || bytes.Equal(activeIDs[1], id3))
	s.Require().False(bytes.Equal(activeIDs[0], activeIDs[1]))
}

// ---------------------------------------------------------------------------
// Metadata accessors
// ---------------------------------------------------------------------------

func (s *KeeperTestSuite) TestGetAuthority() {
	s.SetupTest()
	s.Require().Equal("zvote1authority", s.keeper.GetAuthority())
}

func (s *KeeperTestSuite) TestLogger() {
	s.SetupTest()
	s.Require().NotNil(s.keeper.Logger())
}
