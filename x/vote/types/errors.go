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
	ErrSighashMismatch       = errors.Register(ModuleName, 15, "sighash does not match message")
	ErrRoundAlreadyExists    = errors.Register(ModuleName, 10, "vote round already exists")
	ErrCommitmentTreeFull    = errors.Register(ModuleName, 11, "commitment tree is full")
	ErrRoundNotTallying      = errors.Register(ModuleName, 12, "vote round is not in tallying state")
	ErrInvalidProposalID     = errors.Register(ModuleName, 13, "invalid proposal ID")
	ErrTallyMismatch         = errors.Register(ModuleName, 14, "tally entry does not match on-chain accumulator")

	// EA key ceremony errors.
	ErrCeremonyNotReady       = errors.Register(ModuleName, 20, "ceremony not in confirmed status")
	ErrCeremonyWrongStatus    = errors.Register(ModuleName, 21, "operation invalid for current ceremony status")
	ErrDuplicateRegistration  = errors.Register(ModuleName, 22, "validator already registered pallas key")
	ErrInvalidPallasPoint     = errors.Register(ModuleName, 23, "invalid pallas point")
	ErrPayloadMismatch        = errors.Register(ModuleName, 24, "dealer payload count does not match validator count")
	ErrDuplicateAck           = errors.Register(ModuleName, 25, "validator already acknowledged")
	ErrNotRegisteredValidator  = errors.Register(ModuleName, 26, "validator not in ceremony validator list")
	ErrCeremonySessionActive   = errors.Register(ModuleName, 27, "ceremony session is in progress")
	ErrVotingSessionActive     = errors.Register(ModuleName, 28, "voting session is active or tallying")

	// Vote manager errors.
	ErrNotAuthorized  = errors.Register(ModuleName, 30, "sender is not authorized")
	ErrNoVoteManager  = errors.Register(ModuleName, 31, "no vote manager set")
)
