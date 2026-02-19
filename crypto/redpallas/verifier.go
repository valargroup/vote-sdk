package redpallas

// RedPallasVerifier implements Verifier using real RedPallas signature
// verification via CGo bindings to the Rust reddsa crate. Only available
// when built with the "redpallas" build tag.
type RedPallasVerifier struct{}

// NewVerifier returns a RedPallasVerifier backed by the Rust FFI library.
func NewVerifier() Verifier { return RedPallasVerifier{} }

// Verify verifies a RedPallas SpendAuth signature via the Rust FFI.
func (v RedPallasVerifier) Verify(rk, sighash, sig []byte) error {
	return VerifySpendAuthSig(rk, sighash, sig)
}
