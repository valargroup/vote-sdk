package types

// DeadlineReached reports whether a nonzero timeout has elapsed since start.
// The subtraction form avoids wrapping when start plus timeout exceeds uint64.
func DeadlineReached(start, timeout, now uint64) bool {
	return timeout > 0 && now >= start && now-start >= timeout
}
