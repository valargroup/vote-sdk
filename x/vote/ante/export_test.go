package ante

import (
	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
)

// MockOpts returns ValidateOpts with mock verifiers for tests only. Never use
// in production because all proofs and signatures will be accepted.
func MockOpts() ValidateOpts {
	return ValidateOpts{
		SigVerifier: redpallas.NewMockVerifier(),
		ZKPVerifier: zkp.NewMockVerifier(),
	}
}
