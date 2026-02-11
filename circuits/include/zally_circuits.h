/*
 * zally_circuits.h — C header for Zally circuit verification and signature FFI.
 *
 * This header declares the C-compatible functions exported by the
 * zally-circuits Rust static library (libzally_circuits.a).
 *
 * Used by Go CGo bindings in crypto/zkp/halo2/ and crypto/redpallas/.
 */

#ifndef ZALLY_CIRCUITS_H
#define ZALLY_CIRCUITS_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* -----------------------------------------------------------------------
 * Halo2 toy circuit verification
 * ----------------------------------------------------------------------- */

/*
 * Verify a toy circuit proof (constant * a^2 * b^2 = c).
 *
 * Parameters:
 *   proof_ptr        - Pointer to serialized Halo2 proof bytes.
 *   proof_len        - Length of the proof byte array.
 *   public_input_ptr - Pointer to the public input (Pallas Fp, 32-byte LE).
 *   public_input_len - Length of the public input byte array (must be 32).
 *
 * Returns:
 *    0  on successful verification.
 *   -1  if inputs are invalid (null pointers or wrong lengths).
 *   -2  if the proof does not verify.
 *   -3  if there is an internal deserialization error.
 */
int32_t zally_verify_toy_proof(
    const uint8_t* proof_ptr,
    size_t proof_len,
    const uint8_t* public_input_ptr,
    size_t public_input_len
);

/* -----------------------------------------------------------------------
 * RedPallas SpendAuth signature verification
 * ----------------------------------------------------------------------- */

/*
 * Verify a RedPallas SpendAuth signature.
 *
 * Parameters:
 *   rk_ptr      - Pointer to the 32-byte randomized verification key.
 *   rk_len      - Length of the rk byte array (must be 32).
 *   sighash_ptr - Pointer to the 32-byte sighash (message that was signed).
 *   sighash_len - Length of the sighash byte array (must be 32).
 *   sig_ptr     - Pointer to the 64-byte RedPallas signature.
 *   sig_len     - Length of the signature byte array (must be 64).
 *
 * Returns:
 *    0  on successful verification.
 *   -1  if inputs are invalid (null pointers or wrong lengths).
 *   -2  if the signature does not verify.
 *   -3  if there is a deserialization error (e.g. invalid verification key).
 */
int32_t zally_verify_redpallas_sig(
    const uint8_t* rk_ptr,
    size_t rk_len,
    const uint8_t* sighash_ptr,
    size_t sighash_len,
    const uint8_t* sig_ptr,
    size_t sig_len
);

#ifdef __cplusplus
}
#endif

#endif /* ZALLY_CIRCUITS_H */
