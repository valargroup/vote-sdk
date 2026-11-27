package keeper

import (
	"context"

	"cosmossdk.io/core/store"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TreeSizeForTest exposes the tree handle Size() for testing. Returns 0 if the
// handle has not been initialized yet for the given round.
func (k *Keeper) TreeSizeForTest(roundID []byte) uint64 {
	rt := k.getOrCreateRoundTree(roundID)
	if rt.handle == nil {
		return 0
	}
	return rt.handle.Size()
}

// StoreServiceForTest exposes the store service so tests can create a second
// Keeper backed by the same underlying store (simulating node restart).
func (k *Keeper) StoreServiceForTest() store.KVStoreService {
	return k.storeService
}

// ExecuteCreateVotingSessionForTest runs round creation as an approved
// coordinator action does, without the transaction's ValidateBasic.
func ExecuteCreateVotingSessionForTest(k *Keeper, ctx context.Context, msg *types.MsgCreateVotingSession) (*types.MsgCreateVotingSessionResponse, error) {
	return msgServer{k: k}.executeCreateVotingSession(ctx, msg)
}
