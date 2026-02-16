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

/* -----------------------------------------------------------------------
 * Vote commitment tree — Poseidon Merkle root and path
 * ----------------------------------------------------------------------- */

/*
 * Compute the Poseidon Merkle root of a vote commitment tree.
 *
 * Builds a fresh tree from leaf_count leaves, checkpoints it, and
 * returns the 32-byte root. This is a stateless call matching the
 * Go keeper pattern (read all leaves from KV, compute root).
 *
 * Parameters:
 *   leaves_ptr - Pointer to flat byte array of leaves.
 *                Each leaf is 32 bytes (Pallas Fp, little-endian canonical).
 *                Total size: leaf_count * 32.
 *   leaf_count - Number of leaves. May be 0 (empty tree root returned).
 *   root_out   - Pointer to a 32-byte output buffer for the root.
 *
 * Returns:
 *    0  on success (root written to root_out).
 *   -1  if inputs are invalid (null root_out, null leaves_ptr with count>0).
 *   -3  if a leaf contains a non-canonical field element encoding.
 */
int32_t zally_vote_tree_root(
    const uint8_t* leaves_ptr,
    size_t leaf_count,
    uint8_t* root_out
);

/*
 * Compute a Poseidon Merkle authentication path for a leaf in the tree.
 *
 * Builds a fresh tree from leaf_count leaves, checkpoints it, and
 * returns the serialized authentication path (772 bytes):
 *   - Bytes [0..4):    position (u32 LE)
 *   - Bytes [4..772):  auth path (24 sibling hashes, 32 bytes each, leaf→root)
 *
 * Parameters:
 *   leaves_ptr - Pointer to flat byte array of leaves (each 32 bytes LE Fp).
 *   leaf_count - Number of leaves (must be > 0).
 *   position   - Leaf index for which to generate the path.
 *   path_out   - Pointer to a 772-byte output buffer.
 *
 * Returns:
 *    0  on success (path written to path_out).
 *   -1  if inputs are invalid (null pointers, zero leaf_count).
 *   -2  if position is out of range (>= leaf_count).
 *   -3  if a leaf contains a non-canonical field element encoding.
 */
int32_t zally_vote_tree_path(
    const uint8_t* leaves_ptr,
    size_t leaf_count,
    uint64_t position,
    uint8_t* path_out
);

/* -----------------------------------------------------------------------
 * Delegation circuit (ZKP #1) — real Halo2 proof verification
 * ----------------------------------------------------------------------- */

/*
 * Verify a real delegation circuit proof (ZKP #1, 15 conditions, K=14).
 *
 * The public inputs are passed as a flat byte array of 11 x 32-byte
 * chunks (352 bytes total), in order:
 *   [nf_signed, rk_compressed, cmx_new, gov_comm, vote_round_id,
 *    nc_root, nf_imt_root, gov_null_1, gov_null_2, gov_null_3, gov_null_4]
 *
 * rk_compressed is a 32-byte compressed Pallas curve point. The FFI
 * decompresses it into (rk_x, rk_y) for the circuit's 12 field elements.
 *
 * Parameters:
 *   proof_ptr         - Pointer to serialized Halo2 proof bytes.
 *   proof_len         - Length of the proof byte array.
 *   public_inputs_ptr - Pointer to 352 bytes (11 x 32-byte chunks).
 *   public_inputs_len - Length of the public inputs byte array (must be 352).
 *
 * Returns:
 *    0  on successful verification.
 *   -1  if inputs are invalid (null pointers or wrong lengths).
 *   -2  if the proof does not verify.
 *   -3  if there is an internal deserialization error (e.g. invalid rk).
 */
int32_t zally_verify_delegation_proof(
    const uint8_t* proof_ptr,
    size_t proof_len,
    const uint8_t* public_inputs_ptr,
    size_t public_inputs_len
);

/* -----------------------------------------------------------------------
 * Vote proof circuit (ZKP #2) — real Halo2 proof verification
 * ----------------------------------------------------------------------- */

/*
 * Verify a real vote proof circuit proof (ZKP #2, 11 conditions, K=14).
 *
 * The public inputs are passed as a flat byte array of 8 x 32-byte
 * chunks (256 bytes total), in order:
 *   [van_nullifier, vote_authority_note_new, vote_commitment,
 *    vote_comm_tree_root, anchor_height_le, proposal_id_le,
 *    voting_round_id, ea_pk_compressed]
 *
 * Slots 0-3 and 6 are 32-byte Pallas Fp field element encodings.
 * Slot 4 is a uint64 LE value zero-padded to 32 bytes (anchor height).
 * Slot 5 is a uint32 LE value zero-padded to 32 bytes (proposal ID).
 * Slot 7 (ea_pk_compressed) is a 32-byte compressed Pallas curve point.
 * The FFI decompresses it into (ea_pk_x, ea_pk_y) for the circuit's
 * 9 field elements.
 *
 * Parameters:
 *   proof_ptr         - Pointer to serialized Halo2 proof bytes.
 *   proof_len         - Length of the proof byte array.
 *   public_inputs_ptr - Pointer to 256 bytes (8 x 32-byte chunks).
 *   public_inputs_len - Length of the public inputs byte array (must be 256).
 *
 * Returns:
 *    0  on successful verification.
 *   -1  if inputs are invalid (null pointers or wrong lengths).
 *   -2  if the proof does not verify.
 *   -3  if there is an internal deserialization error (e.g. invalid ea_pk).
 */
int32_t zally_verify_vote_proof(
    const uint8_t* proof_ptr,
    size_t proof_len,
    const uint8_t* public_inputs_ptr,
    size_t public_inputs_len
);

#ifdef __cplusplus
}
#endif

#endif /* ZALLY_CIRCUITS_H */
