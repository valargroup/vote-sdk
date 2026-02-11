package types

// ExpectedKeepers defines interfaces for keepers that the vote module depends on.
// Currently the vote module has no external keeper dependencies, but this file
// follows the standard Cosmos SDK module pattern for future extensibility.

// NOTE: The vote module is self-contained. It does not depend on auth, bank,
// or staking keepers because vote transactions bypass the standard Cosmos Tx
// envelope and use their own authorization model (RedPallas + ZKP).
