package types

import errorsmod "cosmossdk.io/errors"

// MaxProxyDelegationDCs is the most DCs one 0x09 batch carries: one per DC
// slot (contracts-v1 §2.1, §10).
const MaxProxyDelegationDCs = 8

// ProxyDelegationVersion is capability field 4 once proxy delegation is
// enabled: tags 0x09 and 0x0B with 8 DC slots per registration
// (contracts-v1 §8). It is 0 while dormant.
const ProxyDelegationVersion uint32 = 1

// PoolProposalID is the proposal id of a pool share: a 0x04 reveal with it
// carries the delegate index d as its vote_decision and adds to Pool[d],
// TallyKey(round, 0, d) (contracts-v1 §3.2, §5.4, §10).
const PoolProposalID uint32 = 0

// ProxyMaxTreePosition is the largest leaf index any append may fill once
// proxy delegation is enabled, since every round's vote commitment tree has
// depth 24 (contracts-v1 §5.1 step 5). See keeper.EnsureCommitmentCapacity.
const ProxyMaxTreePosition uint64 = 1<<24 - 1

// MaxDelegateAttestations is the most verifier attestations one delegate
// registration carries (contracts-v1 §2.2).
const MaxDelegateAttestations = 16

// MaxDelegateAttestationSeconds is the longest validity window of a delegate
// attestation: 72 hours (contracts-v1 §2.5).
const MaxDelegateAttestationSeconds uint64 = 259200

// DelegateProviderX is the provider code of an X account (contracts-v1 §2.2).
const DelegateProviderX uint32 = 1

// DelegateProviderGitHub is the provider code of a GitHub account
// (contracts-v1 §2.2).
const DelegateProviderGitHub uint32 = 2

// MaxDelegates is the largest max_delegates the params accept, so the
// largest registry (contracts-v1 §2.5): routing at the tally transition is
// measured up to it (docs/proxy-delegation/routing-performance.md).
const MaxDelegates uint32 = 2000

// MaxProxyVerifiers is the most verifiers the registry params list
// (contracts-v1 §2.4).
const MaxProxyVerifiers = 16

// The status of a delegate entry (contracts-v1 §3.1). A live entry is ACTIVE
// or SUSPENDED; RETIRED is final.
const (
	DelegateStatusActive    uint32 = 0
	DelegateStatusSuspended uint32 = 1
	DelegateStatusRetired   uint32 = 2
)

// MaxDelegateStatusIndexes is the most entries one MsgSetDelegateStatus names
// (contracts-v1 §2.4).
const MaxDelegateStatusIndexes = 64

// The actions of MsgSetDelegateStatus (contracts-v1 §2.4).
const (
	DelegateStatusActionSuspend uint32 = 1
	DelegateStatusActionLift    uint32 = 2
	DelegateStatusActionRetire  uint32 = 3
)

// The public reason codes of MsgSetDelegateStatus (contracts-v1 §2.4).
const (
	DelegateStatusReasonImpersonation  uint32 = 1
	DelegateStatusReasonKeyLost        uint32 = 2
	DelegateStatusReasonLegal          uint32 = 3
	DelegateStatusReasonVerifierReview uint32 = 4
	DelegateStatusReasonOther          uint32 = 99
)

// MinProxyCoordinatorApprovals is the approval floor of contracts-v1 §5.5:
// proxy payloads and proxy round creation need max(2, threshold) distinct
// vote-manager approvals, so they never execute in the proposing tx.
const MinProxyCoordinatorApprovals uint32 = 2

// ValidateDelegateHandle checks that handle is the canonical form of an
// account name at provider (contracts-v1 §2.5): for X, 1 to 15 bytes of
// [a-z0-9_]; for GitHub, 1 to 39 bytes of [a-z0-9-] with no leading or
// trailing hyphen. A failure is ErrInvalidField.
func ValidateDelegateHandle(provider uint32, handle string) error {
	var (
		maxLen int
		extra  byte
	)
	switch provider {
	case DelegateProviderX:
		maxLen, extra = 15, '_'
	case DelegateProviderGitHub:
		maxLen, extra = 39, '-'
	default:
		return errorsmod.Wrapf(ErrInvalidField, "provider must be %d or %d, got %d", DelegateProviderX, DelegateProviderGitHub, provider)
	}
	if n := len(handle); n == 0 || n > maxLen {
		return errorsmod.Wrapf(ErrInvalidField, "handle must be 1..%d bytes, got %d", maxLen, n)
	}
	for i := 0; i < len(handle); i++ {
		if c := handle[i]; !isLowerAlnum(c) && c != extra {
			return errorsmod.Wrapf(ErrInvalidField, "handle %q is not canonical", handle)
		}
	}
	if provider == DelegateProviderGitHub && (handle[0] == '-' || handle[len(handle)-1] == '-') {
		return errorsmod.Wrapf(ErrInvalidField, "handle %q starts or ends with a hyphen", handle)
	}
	return nil
}

// ValidateDelegateProofID checks that proofID is the canonical id of a proof
// at provider (contracts-v1 §2.5): for X, the post id as 1 to 20 decimal
// digits with no leading zero; for GitHub, the gist id as 1 to 40 bytes of
// [0-9a-f]. A failure is ErrInvalidField.
func ValidateDelegateProofID(provider uint32, proofID string) error {
	maxLen, isDigit := 20, func(c byte) bool { return c >= '0' && c <= '9' }
	if provider == DelegateProviderGitHub {
		maxLen, isDigit = 40, func(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }
	}
	if n := len(proofID); n == 0 || n > maxLen {
		return errorsmod.Wrapf(ErrInvalidField, "proof_id must be 1..%d bytes, got %d", maxLen, n)
	}
	for i := 0; i < len(proofID); i++ {
		if !isDigit(proofID[i]) {
			return errorsmod.Wrapf(ErrInvalidField, "proof_id %q is not canonical", proofID)
		}
	}
	if provider == DelegateProviderX && proofID[0] == '0' {
		return errorsmod.Wrapf(ErrInvalidField, "proof_id %q has a leading zero", proofID)
	}
	return nil
}

// isLowerAlnum reports whether c is in [a-z0-9].
func isLowerAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}
