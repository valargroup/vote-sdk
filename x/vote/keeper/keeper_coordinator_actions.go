package keeper

import (
	"fmt"
	"math"

	"cosmossdk.io/core/store"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// GetNextCoordinatorActionID returns the next action ID to allocate. IDs start
// at 1, so an absent key means this is a fresh chain state.
func (k *Keeper) GetNextCoordinatorActionID(kvStore store.KVStore) (uint64, error) {
	bz, err := kvStore.Get(types.NextCoordinatorActionIDKey)
	if err != nil {
		return 0, err
	}
	if bz == nil {
		return 1, nil
	}
	if len(bz) != 8 {
		return 0, fmt.Errorf("%w: next coordinator action id has %d bytes", types.ErrInvalidCoordinatorAction, len(bz))
	}
	return getUint64BE(bz), nil
}

// SetNextCoordinatorActionID stores the next action ID to allocate.
func (k *Keeper) SetNextCoordinatorActionID(kvStore store.KVStore, actionID uint64) error {
	if actionID == 0 {
		return fmt.Errorf("%w: next action id cannot be zero", types.ErrInvalidCoordinatorAction)
	}
	bz := make([]byte, 8)
	putUint64BE(bz, actionID)
	return kvStore.Set(types.NextCoordinatorActionIDKey, bz)
}

// AllocateCoordinatorActionID returns the next action ID and advances the
// singleton counter.
func (k *Keeper) AllocateCoordinatorActionID(kvStore store.KVStore) (uint64, error) {
	actionID, err := k.GetNextCoordinatorActionID(kvStore)
	if err != nil {
		return 0, err
	}
	if actionID == math.MaxUint64 {
		return 0, fmt.Errorf("%w: coordinator action ids are exhausted", types.ErrInvalidCoordinatorAction)
	}
	if err := k.SetNextCoordinatorActionID(kvStore, actionID+1); err != nil {
		return 0, err
	}
	return actionID, nil
}

// GetCoordinatorAction retrieves a stored coordinator action by ID.
func (k *Keeper) GetCoordinatorAction(kvStore store.KVStore, actionID uint64) (*types.CoordinatorAction, error) {
	if actionID == 0 {
		return nil, fmt.Errorf("%w: action_id cannot be zero", types.ErrInvalidCoordinatorAction)
	}
	bz, err := kvStore.Get(types.CoordinatorActionKey(actionID))
	if err != nil {
		return nil, err
	}
	if bz == nil {
		return nil, fmt.Errorf("%w: %d", types.ErrCoordinatorActionNotFound, actionID)
	}
	var action types.CoordinatorAction
	if err := unmarshal(bz, &action); err != nil {
		return nil, err
	}
	return &action, nil
}

// SetCoordinatorAction stores a coordinator action.
func (k *Keeper) SetCoordinatorAction(kvStore store.KVStore, action *types.CoordinatorAction) error {
	if action == nil {
		return fmt.Errorf("%w: action cannot be nil", types.ErrInvalidCoordinatorAction)
	}
	if action.ActionId == 0 {
		return fmt.Errorf("%w: action_id cannot be zero", types.ErrInvalidCoordinatorAction)
	}
	if action.ActionId == math.MaxUint64 {
		return fmt.Errorf("%w: action_id cannot be %d", types.ErrInvalidCoordinatorAction, uint64(math.MaxUint64))
	}
	bz, err := marshal(action)
	if err != nil {
		return err
	}
	return kvStore.Set(types.CoordinatorActionKey(action.ActionId), bz)
}

// IterateCoordinatorActions scans all stored coordinator actions. Returning
// true from cb stops iteration.
func (k *Keeper) IterateCoordinatorActions(kvStore store.KVStore, cb func(action *types.CoordinatorAction) bool) error {
	prefix := types.CoordinatorActionPrefix
	end := types.PrefixEndBytes(prefix)

	iter, err := kvStore.Iterator(prefix, end)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var action types.CoordinatorAction
		if err := unmarshal(iter.Value(), &action); err != nil {
			return err
		}
		if cb(&action) {
			break
		}
	}
	return nil
}

// IteratePendingCoordinatorActions scans pending coordinator actions. Expired
// records are left unchanged here; approval/execution paths enforce expiry.
func (k *Keeper) IteratePendingCoordinatorActions(kvStore store.KVStore, cb func(action *types.CoordinatorAction) bool) error {
	return k.IterateCoordinatorActions(kvStore, func(action *types.CoordinatorAction) bool {
		if action.Status != types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING {
			return false
		}
		return cb(action)
	})
}
