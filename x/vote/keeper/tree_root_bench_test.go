package keeper

import (
	"encoding/binary"
	"testing"

	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
)

func BenchmarkUnchangedTreeRoot(b *testing.B) {
	for _, cached := range []bool{false, true} {
		name := "recompute"
		if cached {
			name = "reuse"
		}
		b.Run(name, func(b *testing.B) {
			key := storetypes.NewKVStoreKey("vote")
			ctx := testutil.DefaultContextWithDB(b, key, storetypes.NewTransientStoreKey("transient"))
			k := NewKeeper(runtime.NewKVStoreService(key), "", log.NewNopLogger(), nil, nil)
			kv := k.OpenKVStore(ctx.Ctx)
			roundID := make([]byte, 32)
			for i := uint64(1); i <= 17; i++ {
				leaf := make([]byte, 32)
				binary.LittleEndian.PutUint64(leaf, i)
				if _, err := k.AppendCommitment(kv, roundID, leaf); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := k.ComputeTreeRoot(kv, roundID, 17, 1); err != nil {
				b.Fatal(err)
			}
			rt := k.getOrCreateRoundTree(roundID)
			defer rt.handle.Close()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !cached {
					// Preserve the warm handle but perform the old root read each time.
					rt.root = nil
				}
				if _, err := k.ComputeTreeRoot(kv, roundID, 17, uint64(i+2)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
