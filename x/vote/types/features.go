package types

// AtomicVoteBatchesEnabled must remain false in this state-compatible
// groundwork release. Set it to true only when the coordinated activation
// upgrade handler is registered; this lets the implementation merge while old
// and new validators continue accepting the same transaction set.
const AtomicVoteBatchesEnabled = false
