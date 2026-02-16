package vote_test

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"

	vote "github.com/z-cale/zally/x/vote"
	"github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
)

// fpLE returns a 32-byte little-endian Pallas Fp encoding of a small integer.
// Used so commitment leaves are canonical and accepted by the votetree FFI.
func fpLE(v uint64) []byte {
	buf := make([]byte, 32)
	binary.LittleEndian.PutUint64(buf[:8], v)
	return buf
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

type EndBlockerTestSuite struct {
	suite.Suite
	ctx    sdk.Context
	keeper keeper.Keeper
	module vote.AppModule
}

func TestEndBlockerTestSuite(t *testing.T) {
	suite.Run(t, new(EndBlockerTestSuite))
}

func (s *EndBlockerTestSuite) SetupTest() {
	key := storetypes.NewKVStoreKey(types.StoreKey)
	tkey := storetypes.NewTransientStoreKey("transient_test")
	testCtx := testutil.DefaultContextWithDB(s.T(), key, tkey)

	s.ctx = testCtx.Ctx.
		WithBlockTime(time.Unix(1_000_000, 0).UTC()).
		WithBlockHeight(10)
	storeService := runtime.NewKVStoreService(key)
	s.keeper = keeper.NewKeeper(storeService, "zvote1authority", log.NewNopLogger(), nil)
	s.module = vote.NewAppModule(s.keeper, nil) // codec unused by EndBlock
}

// ---------------------------------------------------------------------------
// EndBlocker tests
// ---------------------------------------------------------------------------

func (s *EndBlockerTestSuite) TestEndBlock() {
	tests := []struct {
		name  string
		setup func()
		check func()
	}{
		{
			name:  "no-op when tree is empty",
			setup: func() {},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				root, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)
				s.Require().Nil(root) // no root stored
			},
		},
		{
			name: "computes and stores root when leaves exist",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, fpLE(1))
				s.Require().NoError(err)
				_, err = s.keeper.AppendCommitment(kv, fpLE(2))
				s.Require().NoError(err)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				// Root stored at block height 10.
				root, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)
				s.Require().NotNil(root)
				s.Require().Len(root, 32)

				// Tree state updated.
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(10), state.Height)
				s.Require().Equal(root, state.Root)
			},
		},
		{
			name: "skips when tree unchanged between blocks",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, fpLE(1))
				s.Require().NoError(err)

				// Run EndBlock at height 10 to compute root.
				s.Require().NoError(s.module.EndBlock(s.ctx))

				// Advance to height 11 (no new leaves).
				s.ctx = s.ctx.WithBlockHeight(11)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				// Root exists at height 10 but not at height 11.
				root10, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)
				s.Require().NotNil(root10)

				root11, err := s.keeper.GetCommitmentRootAtHeight(kv, 11)
				s.Require().NoError(err)
				s.Require().Nil(root11)

				// Height in state is still 10.
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(10), state.Height)
			},
		},
		{
			name: "new root stored when leaves added after previous root",
			setup: func() {
				kv := s.keeper.OpenKVStore(s.ctx)
				_, err := s.keeper.AppendCommitment(kv, fpLE(1))
				s.Require().NoError(err)

				// EndBlock at height 10.
				s.Require().NoError(s.module.EndBlock(s.ctx))

				// Add another leaf and advance height.
				_, err = s.keeper.AppendCommitment(kv, fpLE(2))
				s.Require().NoError(err)
				s.ctx = s.ctx.WithBlockHeight(11)
			},
			check: func() {
				kv := s.keeper.OpenKVStore(s.ctx)

				root10, err := s.keeper.GetCommitmentRootAtHeight(kv, 10)
				s.Require().NoError(err)

				root11, err := s.keeper.GetCommitmentRootAtHeight(kv, 11)
				s.Require().NoError(err)
				s.Require().NotNil(root11)

				// Roots differ because tree changed.
				s.Require().NotEqual(root10, root11)

				// State reflects height 11.
				state, err := s.keeper.GetCommitmentTreeState(kv)
				s.Require().NoError(err)
				s.Require().Equal(uint64(11), state.Height)
			},
		},
	}

	for _, tc := range tests {
		s.Run(tc.name, func() {
			s.SetupTest()
			tc.setup()
			s.Require().NoError(s.module.EndBlock(s.ctx))
			tc.check()
		})
	}
}
