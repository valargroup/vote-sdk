package types

import "cosmossdk.io/errors"

// x/vote module sentinel errors.
var (
	ErrDuplicateNullifier    = errors.Register(ModuleName, 2, "nullifier already spent")
	ErrRoundNotFound         = errors.Register(ModuleName, 3, "vote round not found")
	ErrRoundNotActive        = errors.Register(ModuleName, 4, "vote round is not active")
	ErrInvalidProof          = errors.Register(ModuleName, 5, "invalid zero-knowledge proof")
	ErrInvalidSignature      = errors.Register(ModuleName, 6, "invalid RedPallas signature")
	ErrInvalidAnchorHeight   = errors.Register(ModuleName, 7, "invalid commitment tree anchor height")
	ErrInvalidRoundID        = errors.Register(ModuleName, 8, "invalid vote round ID")
	ErrInvalidField          = errors.Register(ModuleName, 9, "invalid message field")
	ErrRoundAlreadyExists    = errors.Register(ModuleName, 10, "vote round already exists")
	ErrCommitmentTreeFull    = errors.Register(ModuleName, 11, "commitment tree is full")
)
