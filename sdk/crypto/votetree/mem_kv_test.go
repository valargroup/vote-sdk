package votetree

// mem_kv_test.go — test-only alias for the in-memory tree handle.

// NewTreeHandle creates a stateful tree handle backed by an in-memory Go map.
// Use this in unit tests; production code uses NewTreeHandleWithKV.
func NewTreeHandle() *TreeHandle {
	h, err := NewEphemeralTreeHandle()
	if err != nil {
		panic("NewTreeHandle: " + err.Error())
	}
	return h
}
