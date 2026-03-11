//! RedPallas (RedDSA over Pallas) signature verification for SpendAuth.
//!
//! This module wraps the `reddsa` crate's Orchard SpendAuth verification,
//! providing a simple byte-oriented API that the FFI layer can call.

use reddsa::{orchard, Signature, VerificationKey};

/// Verify a RedPallas SpendAuth signature.
///
/// # Arguments
/// * `rk_bytes`  - 32-byte randomized spend authorization verification key (compressed Pallas point).
/// * `sighash`   - The message hash that was signed (typically 32 bytes, but any length is accepted).
/// * `sig_bytes` - 64-byte RedPallas signature.
///
/// # Errors
/// Returns `reddsa::Error` if the verification key cannot be deserialized
/// or the signature does not verify.
pub fn verify_spend_auth_sig(
    rk_bytes: &[u8; 32],
    sighash: &[u8],
    sig_bytes: &[u8; 64],
) -> Result<(), reddsa::Error> {
    let vk = VerificationKey::<orchard::SpendAuth>::try_from(*rk_bytes)?;
    let sig = Signature::<orchard::SpendAuth>::from(*sig_bytes);
    vk.verify(sighash, &sig)
}
