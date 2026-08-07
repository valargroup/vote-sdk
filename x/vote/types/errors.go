package types

import "cosmossdk.io/errors"

// x/vote module sentinel errors.
var (
	ErrDuplicateNullifier  = errors.Register(ModuleName, 2, "nullifier already spent")
	ErrRoundNotFound       = errors.Register(ModuleName, 3, "vote round not found")
	ErrRoundNotActive      = errors.Register(ModuleName, 4, "vote round is not active")
	ErrInvalidProof        = errors.Register(ModuleName, 5, "invalid zero-knowledge proof")
	ErrInvalidSignature    = errors.Register(ModuleName, 6, "invalid RedPallas signature")
	ErrInvalidAnchorHeight = errors.Register(ModuleName, 7, "invalid commitment tree anchor height")
	ErrInvalidRoundID      = errors.Register(ModuleName, 8, "invalid vote round ID")
	ErrInvalidField        = errors.Register(ModuleName, 9, "invalid message field")
	ErrRoundAlreadyExists  = errors.Register(ModuleName, 10, "vote round already exists")
	ErrCommitmentTreeFull  = errors.Register(ModuleName, 11, "commitment tree is full")
	ErrRoundNotTallying    = errors.Register(ModuleName, 12, "vote round is not in tallying state")
	ErrInvalidProposalID   = errors.Register(ModuleName, 13, "invalid proposal ID")
	ErrTallyMismatch       = errors.Register(ModuleName, 14, "tally entry does not match on-chain accumulator")

	// EA key ceremony errors.
	ErrCeremonyWrongStatus    = errors.Register(ModuleName, 21, "operation invalid for current ceremony status")
	ErrDuplicateRegistration  = errors.Register(ModuleName, 22, "validator already registered pallas key")
	ErrDuplicatePallasKey     = errors.Register(ModuleName, 34, "pallas key already registered by another validator")
	ErrInvalidPallasPoint     = errors.Register(ModuleName, 23, "invalid pallas point")
	ErrPayloadMismatch        = errors.Register(ModuleName, 24, "dealer payload count does not match validator count")
	ErrDuplicateAck           = errors.Register(ModuleName, 25, "validator already acknowledged")
	ErrDuplicateContribution  = errors.Register(ModuleName, 33, "validator already contributed to DKG")
	ErrNotRegisteredValidator = errors.Register(ModuleName, 26, "validator not in ceremony validator list")
	ErrCeremonySessionActive  = errors.Register(ModuleName, 27, "ceremony session is in progress")
	ErrInvalidThreshold       = errors.Register(ModuleName, 28, "invalid threshold parameters")
	ErrInsufficientValidators = errors.Register(ModuleName, 29, "insufficient eligible validators")

	// Pallas key rotation errors.
	ErrCeremonyInProgress = errors.Register(ModuleName, 35, "cannot rotate key while participating in an active ceremony")
	ErrNoPallasKey        = errors.Register(ModuleName, 36, "validator has no registered pallas key")
	ErrSameKey            = errors.Register(ModuleName, 37, "new pallas key is identical to the current key")

	// Vote-manager authorization errors.
	ErrNotAuthorized        = errors.Register(ModuleName, 30, "sender is not authorized")
	ErrNoVoteManagers       = errors.Register(ModuleName, 31, "no vote-manager set configured")
	ErrEmptyVoteManagerSet  = errors.Register(ModuleName, 38, "vote-manager set must be non-empty")
	ErrDuplicateVoteManager = errors.Register(ModuleName, 39, "vote-manager address appears more than once")

	// Authorized send errors.
	ErrUnauthorizedSend = errors.Register(ModuleName, 32, "sender not authorized to send to recipient")

	// Upgrade scheduling errors.
	ErrUpgradeUnavailable = errors.Register(ModuleName, 40, "upgrade scheduler unavailable")
	ErrUpgradePlanExists  = errors.Register(ModuleName, 41, "upgrade plan already scheduled")

	// Endorser registry errors.
	ErrInvalidEndorserID = errors.Register(ModuleName, 42, "invalid endorser id")
	ErrEndorserNotFound  = errors.Register(ModuleName, 43, "endorser not found")

	// Coordinator action errors.
	ErrCoordinatorActionNotFound    = errors.Register(ModuleName, 45, "coordinator action not found")
	ErrCoordinatorActionExpired     = errors.Register(ModuleName, 46, "coordinator action expired")
	ErrCoordinatorAlreadyApproved   = errors.Register(ModuleName, 47, "coordinator action already approved")
	ErrUnsupportedCoordinatorAction = errors.Register(ModuleName, 48, "unsupported coordinator action")
	ErrInvalidCoordinatorAction     = errors.Register(ModuleName, 49, "invalid coordinator action")
)
