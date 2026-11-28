package app

import "github.com/valargroup/vote-sdk/ffi/zkp"

var ValidateInjectedDKGContribution = validateInjectedDKGContribution
var AckDKGRound = ackDKGRound
var ShouldCaptureVoteAnteError = shouldCaptureVoteAnteError

// SetAnteZKPVerifier makes the apps built until restore runs verify proofs
// in their ante handler with v instead of the production verifier.
func SetAnteZKPVerifier(v zkp.Verifier) (restore func()) {
	previous := anteZKPVerifier
	anteZKPVerifier = func() zkp.Verifier { return v }
	return func() { anteZKPVerifier = previous }
}
