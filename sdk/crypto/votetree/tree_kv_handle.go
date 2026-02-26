package votetree

// NewTreeHandleWithKV is in a separate file from the //export callbacks
// because CGO requires that //export declarations and references to those
// exported functions as C function pointers live in different .go files.

/*
#include "../../circuits/include/zally_circuits.h"
#include <stdint.h>

// Forward-declare the Go-exported KV callbacks so we can take their address.
extern int32_t zallyKvGet(void*, const uint8_t*, size_t, uint8_t**, size_t*);
extern int32_t zallyKvSet(void*, const uint8_t*, size_t, const uint8_t*, size_t);
extern int32_t zallyKvDelete(void*, const uint8_t*, size_t);
extern void*   zallyKvIterCreate(void*, const uint8_t*, size_t, uint8_t);
extern int32_t zallyKvIterNext(void*, uint8_t**, size_t*, uint8_t**, size_t*);
extern void    zallyKvIterFree(void*);
extern void    zallyKvFreeBuf(uint8_t*, size_t);
*/
import "C"

import (
	"runtime/cgo"
	"unsafe"
)

// NewTreeHandleWithKV creates a KV-backed tree handle that reads/writes shards,
// the cap, and checkpoints directly through the provided KvStoreProxy.
//
// proxy must remain alive for the lifetime of the handle. Its address is
// recovered by the KV callbacks via the cgo.Handle stored in proxyHandle;
// Go updates proxy.Current before each tree call so Rust always accesses the
// correct block's store.KVStore.
// nextPosition is CommitmentTreeState.NextIndex (0 on first boot).
func NewTreeHandleWithKV(proxy *KvStoreProxy, nextPosition uint64) *TreeHandle {
	// Wrap proxy in a cgo.Handle so it can be passed through the CGO boundary
	// safely. KvStoreProxy contains a Go interface (store.KVStore) which holds
	// Go pointers; passing a raw *KvStoreProxy to C would violate CGO pointer
	// rules. The handle is an opaque integer — safe to pass as ctx.
	h := cgo.NewHandle(proxy)
	ctx := unsafe.Pointer(uintptr(h))
	ptr := C.zally_vote_tree_create_with_kv(
		ctx,
		C.ZallyKvGetFn(C.zallyKvGet),
		C.ZallyKvSetFn(C.zallyKvSet),
		C.ZallyKvDeleteFn(C.zallyKvDelete),
		C.ZallyKvIterCreateFn(C.zallyKvIterCreate),
		C.ZallyKvIterNextFn(C.zallyKvIterNext),
		C.ZallyKvIterFreeFn(C.zallyKvIterFree),
		C.ZallyKvFreeBufFn(C.zallyKvFreeBuf),
		C.uint64_t(nextPosition),
	)
	return &TreeHandle{ptr: unsafe.Pointer(ptr), proxyHandle: h}
}
