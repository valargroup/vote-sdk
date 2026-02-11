//! C-compatible FFI functions for calling verification from Go via CGo.
//!
//! All functions use C calling conventions and return i32 status codes:
//!   0  = success
//!   -1 = invalid input (null pointer, wrong length, etc.)
//!   -2 = verification failed (proof/signature is invalid)
//!   -3 = internal error (deserialization, etc.)

use pasta_curves::group::ff::PrimeField;
use halo2_proofs::pasta::Fp;

use crate::toy;
use crate::redpallas;

// ---------------------------------------------------------------------------
// Halo2 toy circuit verification
// ---------------------------------------------------------------------------

/// Verify a toy circuit proof.
///
/// # Arguments
/// * `proof_ptr` - Pointer to the serialized proof bytes.
/// * `proof_len` - Length of the proof byte slice.
/// * `public_input_ptr` - Pointer to the public input (Pallas Fp, 32-byte little-endian).
/// * `public_input_len` - Length of the public input byte slice (must be 32).
///
/// # Returns
/// * `0` on successful verification.
/// * `-1` if inputs are invalid (null pointers or wrong lengths).
/// * `-2` if the proof does not verify.
/// * `-3` if there is an internal deserialization error.
///
/// # Safety
/// Caller must ensure the pointers are valid and the lengths are correct.
#[no_mangle]
pub unsafe extern "C" fn zally_verify_toy_proof(
    proof_ptr: *const u8,
    proof_len: usize,
    public_input_ptr: *const u8,
    public_input_len: usize,
) -> i32 {
    // Validate pointers and lengths.
    if proof_ptr.is_null() || public_input_ptr.is_null() {
        return -1;
    }
    if public_input_len != 32 {
        return -1;
    }
    if proof_len == 0 {
        return -1;
    }

    // Reconstruct slices from raw pointers.
    let proof = std::slice::from_raw_parts(proof_ptr, proof_len);
    let input_bytes = std::slice::from_raw_parts(public_input_ptr, public_input_len);

    // Deserialize the public input as a Pallas Fp field element (32-byte LE).
    let mut repr = [0u8; 32];
    repr.copy_from_slice(input_bytes);
    let fp_opt: Option<Fp> = Fp::from_repr(repr).into();
    let fp = match fp_opt {
        Some(f) => f,
        None => return -3,
    };

    // Run verification.
    match toy::verify_toy(proof, &fp) {
        Ok(()) => 0,
        Err(_) => -2,
    }
}

// ---------------------------------------------------------------------------
// RedPallas SpendAuth signature verification
// ---------------------------------------------------------------------------

/// Verify a RedPallas SpendAuth signature.
///
/// # Arguments
/// * `rk_ptr`      - Pointer to the 32-byte randomized verification key.
/// * `rk_len`      - Length of the rk byte slice (must be 32).
/// * `sighash_ptr` - Pointer to the 32-byte sighash (message that was signed).
/// * `sighash_len` - Length of the sighash byte slice (must be 32).
/// * `sig_ptr`     - Pointer to the 64-byte RedPallas signature.
/// * `sig_len`     - Length of the signature byte slice (must be 64).
///
/// # Returns
/// * `0`  on successful verification.
/// * `-1` if inputs are invalid (null pointers or wrong lengths).
/// * `-2` if the signature does not verify.
/// * `-3` if there is a deserialization error (e.g. rk is not a valid curve point).
///
/// # Safety
/// Caller must ensure the pointers are valid and the lengths are correct.
#[no_mangle]
pub unsafe extern "C" fn zally_verify_redpallas_sig(
    rk_ptr: *const u8,
    rk_len: usize,
    sighash_ptr: *const u8,
    sighash_len: usize,
    sig_ptr: *const u8,
    sig_len: usize,
) -> i32 {
    // Validate pointers.
    if rk_ptr.is_null() || sighash_ptr.is_null() || sig_ptr.is_null() {
        return -1;
    }
    // Validate lengths.
    if rk_len != 32 || sighash_len != 32 || sig_len != 64 {
        return -1;
    }

    // Reconstruct fixed-size arrays from raw pointers.
    let rk_slice = std::slice::from_raw_parts(rk_ptr, 32);
    let sighash = std::slice::from_raw_parts(sighash_ptr, 32);
    let sig_slice = std::slice::from_raw_parts(sig_ptr, 64);

    let mut rk_bytes = [0u8; 32];
    rk_bytes.copy_from_slice(rk_slice);

    let mut sig_bytes = [0u8; 64];
    sig_bytes.copy_from_slice(sig_slice);

    // Call the verification function.
    match redpallas::verify_spend_auth_sig(&rk_bytes, sighash, &sig_bytes) {
        Ok(()) => 0,
        Err(e) => {
            // Distinguish deserialization errors from verification failures.
            // reddsa::Error is an opaque type; verification key deserialization
            // failures and signature verification failures both return Error.
            // We use the error's Debug representation to differentiate.
            let msg = format!("{:?}", e);
            if msg.contains("MalformedVerificationKey") {
                -3
            } else {
                -2
            }
        }
    }
}
