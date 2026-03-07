package votetree

// KV store reverse-FFI callbacks.
//
// Go exports C functions that Rust's KvShardStore calls for every KV read,
// write, delete, and iteration. Each call dispatches through a cgo.Handle to
// the current KvStoreProxy, which holds the per-block store.KVStore.
//
// Buffer ownership: get_fn / iter_next_fn return C.malloc-allocated buffers.
// Rust copies the data and then calls free_buf_fn (svKvFreeBuf) to release
// them. Write callbacks receive Rust-owned slices; they must copy if needed.
//
// Iterator handles: svKvIterCreate stores the store.Iterator in a
// cgo.Handle and returns the handle value as an unsafe.Pointer. svKvIterFree
// closes the iterator and deletes the handle.
//
// Thread safety: EndBlocker is single-threaded. No locking is needed.

/*
#include "../../circuits/include/shielded_vote_circuits.h"
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
*/
import "C"

import (
	"log"
	"runtime/cgo"
	"unsafe"

	"cosmossdk.io/core/store"
)

// recoverHandle dereferences a C-malloc'd uintptr_t to recover the cgo.Handle
// stored there. Used by all KV callbacks to recover the KvStoreProxy from the
// void* ctx that Rust passes back.
func recoverHandle(ctx unsafe.Pointer) cgo.Handle {
	return cgo.Handle(*(*C.uint64_t)(ctx))
}

// KvStoreProxy is a stable Go struct whose address never changes across blocks.
// The cgo.Handle passed to Rust points here. SetStore must be called before
// every tree operation so Rust callbacks reach the correct block's KV store.
type KvStoreProxy struct {
	Current store.KVStore
}

// SetStore swaps in the KV store for the current block. Called once per block
// by the Keeper before any tree FFI call.
func (p *KvStoreProxy) SetStore(s store.KVStore) { p.Current = s }

// ---------------------------------------------------------------------------
// Exported C callbacks
// ---------------------------------------------------------------------------

//export svKvGet
func svKvGet(
	ctx unsafe.Pointer,
	keyPtr *C.uint8_t, keyLen C.size_t,
	outVal **C.uint8_t, outValLen *C.size_t,
) C.int32_t {
	h := recoverHandle(ctx)
	proxy, ok := h.Value().(*KvStoreProxy)
	if !ok {
		return -1
	}
	key := C.GoBytes(unsafe.Pointer(keyPtr), C.int(keyLen))
	val, err := proxy.Current.Get(key)
	if err != nil {
		log.Printf("votetree: svKvGet: store error (key len=%d): %v", len(key), err)
		return -1 // hard error: store corruption, closed store, etc.
	}
	if val == nil {
		return 1 // not found
	}
	// Allocate C buffer and copy value in.
	ptr := C.malloc(C.size_t(len(val)))
	if ptr == nil {
		log.Printf("votetree: svKvGet: C.malloc failed (val len=%d)", len(val))
		return -1
	}
	C.memcpy(ptr, unsafe.Pointer(&val[0]), C.size_t(len(val)))
	*outVal = (*C.uint8_t)(ptr)
	*outValLen = C.size_t(len(val))
	return 0
}

//export svKvSet
func svKvSet(
	ctx unsafe.Pointer,
	keyPtr *C.uint8_t, keyLen C.size_t,
	valPtr *C.uint8_t, valLen C.size_t,
) C.int32_t {
	h := recoverHandle(ctx)
	proxy, ok := h.Value().(*KvStoreProxy)
	if !ok {
		return -1
	}
	key := C.GoBytes(unsafe.Pointer(keyPtr), C.int(keyLen))
	val := C.GoBytes(unsafe.Pointer(valPtr), C.int(valLen))
	if err := proxy.Current.Set(key, val); err != nil {
		log.Printf("votetree: svKvSet: store error (key len=%d, val len=%d): %v", len(key), len(val), err)
		return -1
	}
	return 0
}

//export svKvDelete
func svKvDelete(ctx unsafe.Pointer, keyPtr *C.uint8_t, keyLen C.size_t) C.int32_t {
	h := recoverHandle(ctx)
	proxy, ok := h.Value().(*KvStoreProxy)
	if !ok {
		return -1
	}
	key := C.GoBytes(unsafe.Pointer(keyPtr), C.int(keyLen))
	if err := proxy.Current.Delete(key); err != nil {
		log.Printf("votetree: svKvDelete: store error (key len=%d): %v", len(key), err)
		return -1
	}
	return 0
}

//export svKvIterCreate
func svKvIterCreate(
	ctx unsafe.Pointer,
	prefixPtr *C.uint8_t, prefixLen C.size_t,
	reverse C.uint8_t,
) unsafe.Pointer {
	proxyH := recoverHandle(ctx)
	proxy, ok := proxyH.Value().(*KvStoreProxy)
	if !ok {
		return nil
	}
	prefix := C.GoBytes(unsafe.Pointer(prefixPtr), C.int(prefixLen))

	// Compute the end key = prefix with last byte incremented (standard prefix scan).
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		end[i]++
		if end[i] != 0 {
			break
		}
		if i == 0 {
			end = nil // overflow — open end
		}
	}

	var iter store.Iterator
	var err error
	if reverse != 0 {
		iter, err = proxy.Current.ReverseIterator(prefix, end)
	} else {
		iter, err = proxy.Current.Iterator(prefix, end)
	}
	if err != nil {
		log.Printf("votetree: svKvIterCreate: store error (prefix len=%d, reverse=%v): %v", len(prefix), reverse != 0, err)
		return nil
	}
	if iter == nil {
		log.Printf("votetree: svKvIterCreate: store returned nil iterator (prefix len=%d)", len(prefix))
		return nil
	}
	iterH := cgo.NewHandle(iter)
	iterPtr := (*C.uint64_t)(C.malloc(C.size_t(unsafe.Sizeof(C.uint64_t(0)))))
	*iterPtr = C.uint64_t(iterH)
	return unsafe.Pointer(iterPtr)
}

//export svKvIterNext
func svKvIterNext(
	iterPtr unsafe.Pointer,
	outKey **C.uint8_t, outKeyLen *C.size_t,
	outVal **C.uint8_t, outValLen *C.size_t,
) C.int32_t {
	h := cgo.Handle(*(*C.uint64_t)(iterPtr))
	iter, ok := h.Value().(store.Iterator)
	if !ok {
		return -1 // corrupted handle: not a store.Iterator
	}
	if !iter.Valid() {
		return 1 // exhausted
	}
	key := iter.Key()
	val := iter.Value()
	iter.Next()

	kPtr := C.malloc(C.size_t(len(key)))
	if kPtr == nil {
		log.Printf("votetree: svKvIterNext: C.malloc failed for key (len=%d)", len(key))
		return -1
	}
	C.memcpy(kPtr, unsafe.Pointer(&key[0]), C.size_t(len(key)))
	*outKey = (*C.uint8_t)(kPtr)
	*outKeyLen = C.size_t(len(key))

	vPtr := C.malloc(C.size_t(len(val)))
	if vPtr == nil {
		log.Printf("votetree: svKvIterNext: C.malloc failed for value (len=%d)", len(val))
		C.free(kPtr)
		return -1
	}
	C.memcpy(vPtr, unsafe.Pointer(&val[0]), C.size_t(len(val)))
	*outVal = (*C.uint8_t)(vPtr)
	*outValLen = C.size_t(len(val))
	return 0
}

//export svKvIterFree
func svKvIterFree(iterPtr unsafe.Pointer) {
	h := cgo.Handle(*(*C.uint64_t)(iterPtr))
	iter, ok := h.Value().(store.Iterator)
	if !ok {
		h.Delete()
		C.free(iterPtr)
		return
	}
	iter.Close()
	h.Delete()
	C.free(iterPtr)
}

//export svKvFreeBuf
func svKvFreeBuf(ptr *C.uint8_t, _ C.size_t) {
	C.free(unsafe.Pointer(ptr))
}

