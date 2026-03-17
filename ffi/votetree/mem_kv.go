package votetree

// mem_kv.go — in-memory KVStore and NewEphemeralTreeHandle.
//
// NewEphemeralTreeHandle creates a stateful tree handle backed by a Go map
// instead of the Cosmos KV store. Use when you need a temporary tree built
// from a flat leaf slice (e.g. in the helper server or tests) without
// persisting to the Cosmos KV store.
//
// The in-memory KV is wired through the same reverse-FFI callback path that
// production uses, so the full Rust ShardTree + KvShardStore code path is
// exercised with only the backing store swapped out.

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"cosmossdk.io/core/store"
)

// ---------------------------------------------------------------------------
// memKVStore — in-memory store.KVStore backed by a sorted string map
// ---------------------------------------------------------------------------

type memKVStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemKVStore() *memKVStore {
	return &memKVStore{data: make(map[string][]byte)}
}

func (m *memKVStore) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[string(key)]
	if !ok {
		return nil, nil
	}
	result := make([]byte, len(v))
	copy(result, v)
	return result, nil
}

func (m *memKVStore) Has(key []byte) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[string(key)]
	return ok, nil
}

func (m *memKVStore) Set(key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := make([]byte, len(value))
	copy(v, value)
	m.data[string(key)] = v
	return nil
}

func (m *memKVStore) Delete(key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, string(key))
	return nil
}

// sortedKeys returns all keys in [start, end) in ascending order.
// If start is nil it is treated as the zero key; if end is nil it is unbounded.
func (m *memKVStore) sortedKeys(start, end []byte) []string {
	var keys []string
	for k := range m.data {
		kb := []byte(k)
		if start != nil && bytes.Compare(kb, start) < 0 {
			continue
		}
		if end != nil && bytes.Compare(kb, end) >= 0 {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m *memKVStore) Iterator(start, end []byte) (store.Iterator, error) {
	m.mu.RLock()
	keys := m.sortedKeys(start, end)
	m.mu.RUnlock()
	return &memIterator{
		keys:  keys,
		data:  m.snapshot(),
		start: start,
		end:   end,
		pos:   0,
		rev:   false,
	}, nil
}

func (m *memKVStore) ReverseIterator(start, end []byte) (store.Iterator, error) {
	m.mu.RLock()
	keys := m.sortedKeys(start, end)
	m.mu.RUnlock()
	for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
		keys[i], keys[j] = keys[j], keys[i]
	}
	return &memIterator{
		keys:  keys,
		data:  m.snapshot(),
		start: start,
		end:   end,
		pos:   0,
		rev:   true,
	}, nil
}

func (m *memKVStore) snapshot() map[string][]byte {
	snap := make(map[string][]byte, len(m.data))
	for k, v := range m.data {
		cp := make([]byte, len(v))
		copy(cp, v)
		snap[k] = cp
	}
	return snap
}

// ---------------------------------------------------------------------------
// memIterator — implements store.Iterator
// ---------------------------------------------------------------------------

type memIterator struct {
	keys  []string
	data  map[string][]byte
	start []byte
	end   []byte
	pos   int
	rev   bool
}

func (it *memIterator) Domain() ([]byte, []byte) { return it.start, it.end }
func (it *memIterator) Valid() bool               { return it.pos < len(it.keys) }

func (it *memIterator) Next() {
	if it.pos < len(it.keys) {
		it.pos++
	}
}

func (it *memIterator) Key() []byte {
	if !it.Valid() {
		panic("memIterator.Key() called on invalid iterator")
	}
	return []byte(it.keys[it.pos])
}

func (it *memIterator) Value() []byte {
	if !it.Valid() {
		panic("memIterator.Value() called on invalid iterator")
	}
	v := it.data[it.keys[it.pos]]
	result := make([]byte, len(v))
	copy(result, v)
	return result
}

func (it *memIterator) Error() error { return nil }
func (it *memIterator) Close() error { return nil }

// ---------------------------------------------------------------------------
// NewEphemeralTreeHandle
// ---------------------------------------------------------------------------

// NewEphemeralTreeHandle creates a stateful tree handle backed by an in-memory
// Go map. Use this when you need a temporary tree (e.g. the helper server
// building a path for a single share); production block processing uses
// NewTreeHandleWithKV.
//
// The caller must call Close() on the returned handle when done.
func NewEphemeralTreeHandle() (*TreeHandle, error) {
	proxy := &KvStoreProxy{Current: newMemKVStore()}
	h, err := NewTreeHandleWithKV(proxy, 0)
	if err != nil {
		return nil, fmt.Errorf("votetree: create ephemeral tree handle: %w", err)
	}
	return h, nil
}
