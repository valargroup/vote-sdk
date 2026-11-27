package types

import (
	stderrors "errors"
	"fmt"

	"cosmossdk.io/errors"
)

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

	// ErrClientCircuitKeyMismatch reports that a client built its delegation
	// proof with a different delegation circuit key than this node verifies
	// with, so the proof cannot verify and the client must be updated.
	//
	// It is returned only by the HTTP API, which compares the key fingerprint a
	// client sends with the node's own before broadcasting. Consensus never
	// returns it: CheckTx, proposal validation, and block execution do not see
	// the client's fingerprint and are unaffected by it.
	ErrClientCircuitKeyMismatch = errors.Register(ModuleName, 50, "client delegation circuit key does not match this chain")

	// Proxy delegation errors (docs/proxy-delegation/contracts-v1.md §6).
	// Codes 15, 20 and 44 were used before and are never reused; 56, 63 and
	// 67 are unused.
	ErrProxyDelegationPaused    = errors.Register(ModuleName, 51, "proxy delegation is paused")
	ErrProxyBatchExpired        = errors.Register(ModuleName, 52, "proxy delegation batch expired")
	ErrDelegateNotFound         = errors.Register(ModuleName, 53, "delegate not found")
	ErrDelegateSuspended        = errors.Register(ModuleName, 54, "delegate is suspended")
	ErrDelegateRetired          = errors.Register(ModuleName, 55, "delegate is retired")
	ErrDelegateBallotExists     = errors.Register(ModuleName, 57, "delegate ballot already exists")
	ErrInvalidBallot            = errors.Register(ModuleName, 58, "invalid delegate ballot")
	ErrRegistrationClosed       = errors.Register(ModuleName, 59, "delegate registration is closed")
	ErrRegistryFull             = errors.Register(ModuleName, 60, "delegate registry is full")
	ErrDelegateKeyReused        = errors.Register(ModuleName, 61, "delegate key was already registered")
	ErrHandleTaken              = errors.Register(ModuleName, 62, "handle has a live delegate entry")
	ErrInvalidAttestation       = errors.Register(ModuleName, 64, "invalid delegate attestation")
	ErrInsufficientAttestations = errors.Register(ModuleName, 65, "too few valid delegate attestations")
	ErrInvalidDelegateKey       = errors.Register(ModuleName, 66, "invalid delegate key")
	ErrInvalidProxyParams       = errors.Register(ModuleName, 68, "invalid proxy delegation params")
	ErrProxyDelegationDisabled  = errors.Register(ModuleName, 69, "proxy delegation is disabled")
)

// CommitmentRootUnavailableError identifies a reveal-share anchor whose
// commitment root is absent from local state. It unwraps through the standard
// Go error chain but intentionally does not implement Cause, preserving the
// ABCI result of the previous fmt-wrapped error.
type CommitmentRootUnavailableError struct {
	AnchorHeight uint64
}

func (e *CommitmentRootUnavailableError) Error() string {
	return fmt.Sprintf("%s: no commitment tree root at height %d", ErrInvalidProof, e.AnchorHeight)
}

func (e *CommitmentRootUnavailableError) Unwrap() error {
	return ErrInvalidProof
}

// SurfaceRegisteredCode returns err reporting the ABCI code and codespace of
// the first registered error in its Go error chain, with err's message and
// chain unchanged. ABCIInfo follows Cause, not Unwrap, so a registered error
// wrapped with fmt.Errorf reports code 1 (docs/proxy-delegation/contracts-v1.md
// §0). An error that already reports a registered code, or holds none, is
// returned unchanged.
func SurfaceRegisteredCode(err error) error {
	if err == nil {
		return nil
	}
	if codespace, _, _ := errors.ABCIInfo(err, false); codespace != errors.UndefinedCodespace {
		return err
	}
	var registered *errors.Error
	if !stderrors.As(err, &registered) {
		return err
	}
	return &registeredCodeError{err: err, code: registered}
}

// registeredCodeError is err reporting code's ABCI code; see
// SurfaceRegisteredCode.
type registeredCodeError struct {
	err  error
	code *errors.Error
}

// Error returns the wrapped error's message.
func (e *registeredCodeError) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error, so errors.Is and errors.As see its chain.
func (e *registeredCodeError) Unwrap() error { return e.err }

// Cause returns the registered error whose code and codespace ABCIInfo
// reports.
func (e *registeredCodeError) Cause() error { return e.code }
