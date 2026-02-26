package votetree

// KV store reverse-FFI callbacks.
//
// Go exports C functions that Rust's KvShardStore calls for every KV read,
// write, delete, and iteration. Each call dispatches through a cgo.Handle to
// the current KvStoreProxy, which holds the per-block store.KVStore.
//
// Buffer ownership: get_fn / iter_next_fn return C.malloc-allocated buffers.
// Rust copies the data and then calls free_buf_fn (zallyKvFreeBuf) to release
// them. Write callbacks receive Rust-owned slices; they must copy if needed.
//
// Iterator handles: zallyKvIterCreate stores the store.Iterator in a
// cgo.Handle and returns the handle value as an unsafe.Pointer. zallyKvIterFree
// closes the iterator and deletes the handle.
//
// Thread safety: EndBlocker is single-threaded. No locking is needed.

/*
#include "../../circuits/include/zally_circuits.h"
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
*/
import "C"

import (
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
// The cgo.Handle passed to Rust points here. Go updates Current before every
// tree operation so Rust always accesses the correct block's KV store.
type KvStoreProxy struct {
	Current store.KVStore
}

// ---------------------------------------------------------------------------
// Exported C callbacks
// ---------------------------------------------------------------------------

//export zallyKvGet
func zallyKvGet(
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
		return -1 // hard error: store corruption, closed store, etc.
	}
	if val == nil {
		return 1 // not found
	}
	// Allocate C buffer and copy value in.
	ptr := C.malloc(C.size_t(len(val)))
	if ptr == nil {
		return -1
	}
	C.memcpy(ptr, unsafe.Pointer(&val[0]), C.size_t(len(val)))
	*outVal = (*C.uint8_t)(ptr)
	*outValLen = C.size_t(len(val))
	return 0
}

//export zallyKvSet
func zallyKvSet(
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
		return -1
	}
	return 0
}

//export zallyKvDelete
func zallyKvDelete(ctx unsafe.Pointer, keyPtr *C.uint8_t, keyLen C.size_t) C.int32_t {
	h := recoverHandle(ctx)
	proxy, ok := h.Value().(*KvStoreProxy)
	if !ok {
		return -1
	}
	key := C.GoBytes(unsafe.Pointer(keyPtr), C.int(keyLen))
	if err := proxy.Current.Delete(key); err != nil {
		return -1
	}
	return 0
}

//export zallyKvIterCreate
func zallyKvIterCreate(
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
	if err != nil || iter == nil {
		return nil
	}
	iterH := cgo.NewHandle(iter)
	iterPtr := (*C.uint64_t)(C.malloc(C.size_t(unsafe.Sizeof(C.uint64_t(0)))))
	*iterPtr = C.uint64_t(iterH)
	return unsafe.Pointer(iterPtr)
}

//export zallyKvIterNext
func zallyKvIterNext(
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
		return -1
	}
	C.memcpy(kPtr, unsafe.Pointer(&key[0]), C.size_t(len(key)))
	*outKey = (*C.uint8_t)(kPtr)
	*outKeyLen = C.size_t(len(key))

	vPtr := C.malloc(C.size_t(len(val)))
	if vPtr == nil {
		C.free(kPtr)
		return -1
	}
	C.memcpy(vPtr, unsafe.Pointer(&val[0]), C.size_t(len(val)))
	*outVal = (*C.uint8_t)(vPtr)
	*outValLen = C.size_t(len(val))
	return 0
}

//export zallyKvIterFree
func zallyKvIterFree(iterPtr unsafe.Pointer) {
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

//export zallyKvFreeBuf
func zallyKvFreeBuf(ptr *C.uint8_t, _ C.size_t) {
	C.free(unsafe.Pointer(ptr))
}

