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
 * Thread-local error reporting
 *
 * Every FFI function stores a human-readable description of the last
 * failure in a thread-local buffer before returning a non-zero code.
 * Call zally_last_error() immediately after a failed call to retrieve it;
 * the pointer is valid until the next FFI call on the same thread.
 * ----------------------------------------------------------------------- */

/*
 * Return a pointer to the last error message for the current thread.
 *
 * The returned pointer is owned by the library; the caller MUST NOT free it.
 * It remains valid until the next FFI call on this thread overwrites the
 * buffer. Copy the string (e.g. via C.GoString() in CGo) before making
 * another FFI call.
 *
 * Returns a pointer to an empty string (never NULL) when no error is set.
 */
const char* zally_last_error(void);

/*
 * Clear the thread-local error message.
 *
 * Optional housekeeping; all FFI functions overwrite the buffer on every
 * call so an explicit clear is rarely necessary.
 */
void zally_clear_error(void);

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
 * The public inputs are passed as a flat byte array of 12 x 32-byte
 * chunks (384 bytes total), in order:
 *   [nf_signed, rk_compressed, cmx_new, van_comm, vote_round_id,
 *    nc_root, nf_imt_root, gov_null_1, gov_null_2, gov_null_3, gov_null_4, gov_null_5]
 *
 * rk_compressed is a 32-byte compressed Pallas curve point. The FFI
 * decompresses it into (rk_x, rk_y) for the circuit's 13 field elements.
 *
 * Parameters:
 *   proof_ptr         - Pointer to serialized Halo2 proof bytes.
 *   proof_len         - Length of the proof byte array.
 *   public_inputs_ptr - Pointer to 384 bytes (12 x 32-byte chunks).
 *   public_inputs_len - Length of the public inputs byte array (must be 384).
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
 * The public inputs are passed as a flat byte array of 10 x 32-byte
 * chunks (320 bytes total), in order:
 *   [van_nullifier, r_vpk_x, r_vpk_y, vote_authority_note_new, vote_commitment,
 *    vote_comm_tree_root, anchor_height_le, proposal_id_le, voting_round_id, ea_pk_compressed]
 *
 * Condition 4 (Spend Authority) adds r_vpk at slots 1-2. Slot 9 (ea_pk_compressed)
 * is decompressed to (ea_pk_x, ea_pk_y) for the circuit's 11 field elements.
 *
 * Parameters:
 *   proof_ptr         - Pointer to serialized Halo2 proof bytes.
 *   proof_len         - Length of the proof byte array.
 *   public_inputs_ptr - Pointer to 320 bytes (10 x 32-byte chunks).
 *   public_inputs_len - Length of the public inputs byte array (must be 320).
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

/* -----------------------------------------------------------------------
 * Share Reveal circuit (ZKP #3) — real Halo2 proof verification
 * ----------------------------------------------------------------------- */

/*
 * Verify a real share reveal circuit proof (ZKP #3, 5 conditions, K=14).
 *
 * The public inputs are passed as a flat byte array of 7 x 32-byte
 * chunks (224 bytes total), in order:
 *   [share_nullifier, enc_share_c1_x, enc_share_c2_x, proposal_id,
 *    vote_decision, vote_comm_tree_root, voting_round_id]
 *
 * All values are plain Fp elements (32-byte LE canonical encoding).
 *
 * Parameters:
 *   proof_ptr         - Pointer to serialized Halo2 proof bytes.
 *   proof_len         - Length of the proof byte array.
 *   public_inputs_ptr - Pointer to 224 bytes (7 x 32-byte chunks).
 *   public_inputs_len - Length of the public inputs byte array (must be 224).
 *
 * Returns:
 *    0  on successful verification.
 *   -1  if inputs are invalid (null pointers or wrong lengths).
 *   -2  if the proof does not verify.
 *   -3  if there is an internal deserialization error.
 */
int32_t zally_verify_share_reveal_proof(
    const uint8_t* proof_ptr,
    size_t proof_len,
    const uint8_t* public_inputs_ptr,
    size_t public_inputs_len
);

/* -----------------------------------------------------------------------
 * Share Reveal proof generation (ZKP #3) — composite function
 * ----------------------------------------------------------------------- */

/*
 * Generate a share reveal proof (ZKP #3) in a single call.
 *
 * Performs the entire crypto pipeline: decode inputs, compute shares_hash,
 * verify consistency, derive nullifier, build circuit, generate Halo2 proof.
 *
 * Parameters:
 *   merkle_path_ptr       - Pointer to 772-byte serialized Merkle path
 *                           (from zally_vote_tree_path: 4 bytes position + 24*32 siblings).
 *   merkle_path_len       - Length (must be 772).
 *   all_enc_shares_ptr    - Pointer to 1024 bytes: 16 shares x (C1 + C2) x 32 bytes.
 *                           Order: C1_0, C2_0, C1_1, C2_1, ..., C1_15, C2_15.
 *   all_enc_shares_len    - Length (must be 1024).
 *   share_index           - Which of the 16 shares (0..15).
 *   proposal_id           - Proposal being voted on.
 *   vote_decision         - Vote choice.
 *   round_id_ptr          - Pointer to 32-byte raw Blake2b-256 round ID.
 *   round_id_len          - Length (must be 32).
 *   expected_shares_hash_ptr - Pointer to 32-byte expected shares_hash (Fp LE).
 *   proof_out             - Output buffer for proof bytes.
 *   proof_out_capacity    - Size of proof_out buffer (recommend 8192).
 *   proof_len_out         - On success, receives actual proof length.
 *   nullifier_out         - 32-byte output buffer for share nullifier.
 *   tree_root_out         - 32-byte output buffer for commitment tree root.
 *
 * Returns:
 *    0  on success.
 *   -1  invalid input (null pointers, wrong lengths).
 *   -3  deserialization error (non-canonical Fp).
 *   -4  shares_hash mismatch.
 *   -5  proof generation failure.
 */
int32_t zally_generate_share_reveal(
    const uint8_t* merkle_path_ptr,
    size_t merkle_path_len,
    const uint8_t* all_enc_shares_ptr,
    size_t all_enc_shares_len,
    const uint8_t* share_blinds_ptr,
    size_t share_blinds_len,
    uint32_t share_index,
    uint32_t proposal_id,
    uint32_t vote_decision,
    const uint8_t* round_id_ptr,
    size_t round_id_len,
    const uint8_t* expected_shares_hash_ptr,
    uint8_t* proof_out,
    size_t proof_out_capacity,
    size_t* proof_len_out,
    uint8_t* nullifier_out,
    uint8_t* tree_root_out
);

/* -----------------------------------------------------------------------
 * Orchard note commitment tree root extraction
 * ----------------------------------------------------------------------- */

/*
 * Compute the Orchard nc_root from a hex-encoded frontier string.
 *
 * The orchardTree field from lightwalletd's GetTreeState response is a
 * hex-encoded serialized CommitmentTree. This function hex-decodes it,
 * parses the frontier, and computes the Sinsemilla-based Merkle root.
 *
 * Parameters:
 *   hex_ptr  - Pointer to the hex-encoded orchard frontier string (ASCII).
 *   hex_len  - Length of the hex string (bytes/characters).
 *   root_out - Pointer to a 32-byte output buffer for the root.
 *
 * Returns:
 *    0  on success (root written to root_out).
 *   -1  if inputs are invalid (null pointers, zero length).
 *   -3  if the hex string or frontier data is invalid.
 */
int32_t zally_extract_nc_root(
    const uint8_t* hex_ptr,
    size_t hex_len,
    uint8_t* root_out
);

#ifdef __cplusplus
}
#endif

#endif /* ZALLY_CIRCUITS_H */
