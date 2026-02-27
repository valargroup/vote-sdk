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
 * Vote commitment tree — stateful handle (incremental per-block appends)
 * ----------------------------------------------------------------------- */

/* Opaque type for the stateful vote commitment tree handle. */
typedef struct ZallyTreeHandle ZallyTreeHandle;

/*
 * Free a tree handle previously created by zally_vote_tree_create.
 *
 * Parameters:
 *   handle - Pointer returned by zally_vote_tree_create.
 */
void zally_vote_tree_free(ZallyTreeHandle* handle);

/*
 * Append a batch of leaves to a stateful tree handle.
 *
 * Parameters:
 *   handle     - Pointer returned by zally_vote_tree_create_with_kv.
 *   leaves_ptr - Pointer to flat byte array of leaves (each 32 bytes LE Fp).
 *   leaf_count - Number of leaves.
 *
 * Returns:
 *    0  on success.
 *   -1  if handle is null, or leaf_count > 0 and leaves_ptr is null.
 *   -3  if a leaf contains a non-canonical field element encoding.
 *   -4  if the KV store or ShardTree returned a storage error.
 */
int32_t zally_vote_tree_append_batch(
    ZallyTreeHandle* handle,
    const uint8_t* leaves_ptr,
    size_t leaf_count
);

/*
 * Append count leaves starting at cursor directly from the Cosmos KV store.
 *
 * Reads each leaf from key 0x02 || (cursor+i as uint64 big-endian) via the
 * KV callbacks registered at handle creation. This is the optimised delta-
 * append path: one CGO call regardless of batch size, no Go-side allocation.
 *
 * Parameters:
 *   handle - Pointer returned by zally_vote_tree_create_with_kv.
 *   cursor - Index of the first leaf to append (current treeCursor in Go).
 *   count  - Number of leaves to append (nextIndex - treeCursor).
 *
 * Returns:
 *    0  on success.
 *   -1  if handle is null.
 *   -4  if a leaf is missing/malformed or the KV store returned an error.
 */
int32_t zally_vote_tree_append_from_kv(
    ZallyTreeHandle* handle,
    uint64_t cursor,
    uint64_t count
);

/*
 * Snapshot the current tree state at height (block height).
 *
 * Must be called after appending all leaves for a block so that
 * root_stateful and path_stateful queries work for that height.
 *
 * Parameters:
 *   handle - Pointer returned by zally_vote_tree_create_with_kv.
 *   height - Block height to associate with this checkpoint.
 *
 * Returns:
 *    0  on success.
 *   -1  if handle is null.
 *   -4  if the KV store returned a storage error during the checkpoint write.
 */
int32_t zally_vote_tree_checkpoint(ZallyTreeHandle* handle, uint32_t height);

/*
 * Return the 32-byte Merkle root at the latest checkpoint.
 *
 * Parameters:
 *   handle   - Pointer returned by zally_vote_tree_create.
 *   root_out - Pointer to a 32-byte output buffer.
 *
 * Returns:
 *    0  on success (root written to root_out).
 *   -1  if handle or root_out is null.
 */
int32_t zally_vote_tree_root_stateful(
    const ZallyTreeHandle* handle,
    uint8_t* root_out
);


/*
 * Return the number of leaves appended to the stateful handle so far.
 *
 * Parameters:
 *   handle - Pointer returned by zally_vote_tree_create.
 *
 * Returns 0 if handle is null.
 */
uint64_t zally_vote_tree_size(const ZallyTreeHandle* handle);

/*
 * Compute the Poseidon Merkle authentication path using the stateful handle.
 *
 * Parameters:
 *   handle   - Pointer returned by zally_vote_tree_create.
 *   position - Leaf index for which to generate the path.
 *   height   - Checkpoint height to use as anchor.
 *   path_out - Pointer to a 772-byte output buffer.
 *
 * Returns:
 *    0  on success (path written to path_out).
 *   -1  if handle or path_out is null.
 *   -2  if position is out of range or height has no checkpoint.
 */
int32_t zally_vote_tree_path_stateful(
    const ZallyTreeHandle* handle,
    uint64_t position,
    uint32_t height,
    uint8_t* path_out
);

/* -----------------------------------------------------------------------
 * Stateful vote tree — KV-backed handle creation
 * ----------------------------------------------------------------------- */

/*
 * C function pointer types for the Go KV store callbacks.
 * ctx is a stable pointer to the Go KvStoreProxy (updated each block by Go).
 *
 * get_fn:         reads a value; writes C-malloc'd buffer to *out_val / *out_val_len.
 *                 Returns 0 (found), 1 (not found), -1 (error).
 * set_fn:         writes a key-value pair. Returns 0 on success.
 * delete_fn:      deletes a key. Returns 0 on success.
 * iter_create_fn: creates an iterator over prefix; reverse=1 for descending.
 *                 Returns opaque handle or NULL on error.
 * iter_next_fn:   advances iterator; writes C-malloc'd key+val to out pointers.
 *                 Returns 0 (valid), 1 (exhausted), -1 (error).
 * iter_free_fn:   closes and frees an iterator handle.
 * free_buf_fn:    frees a C-malloc'd buffer returned by get_fn or iter_next_fn.
 */
typedef int32_t (*ZallyKvGetFn)(void* ctx, const uint8_t* key, size_t key_len, uint8_t** out_val, size_t* out_val_len);
typedef int32_t (*ZallyKvSetFn)(void* ctx, const uint8_t* key, size_t key_len, const uint8_t* val, size_t val_len);
typedef int32_t (*ZallyKvDeleteFn)(void* ctx, const uint8_t* key, size_t key_len);
typedef void*   (*ZallyKvIterCreateFn)(void* ctx, const uint8_t* prefix, size_t prefix_len, uint8_t reverse);
typedef int32_t (*ZallyKvIterNextFn)(void* iter, uint8_t** out_key, size_t* out_key_len, uint8_t** out_val, size_t* out_val_len);
typedef void    (*ZallyKvIterFreeFn)(void* iter);
typedef void    (*ZallyKvFreeBufFn)(uint8_t* ptr, size_t len);

/*
 * Create a KV-backed stateful tree handle.
 *
 * Shards, the cap, and checkpoints are read/written directly through the Go
 * KV callbacks. ShardTree lazily loads only the data it accesses (O(1) cold
 * start).
 *
 * next_position: CommitmentTreeState.NextIndex from KV (0 on first boot).
 * ctx:           pointer to a stable Go KvStoreProxy; updated each block.
 *
 * Returns a non-null pointer on success; free with zally_vote_tree_free.
 */
ZallyTreeHandle* zally_vote_tree_create_with_kv(
    void*              ctx,
    ZallyKvGetFn       get_fn,
    ZallyKvSetFn       set_fn,
    ZallyKvDeleteFn    delete_fn,
    ZallyKvIterCreateFn iter_create_fn,
    ZallyKvIterNextFn  iter_next_fn,
    ZallyKvIterFreeFn  iter_free_fn,
    ZallyKvFreeBufFn   free_buf_fn,
    uint64_t           next_position
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
 * Performs the entire crypto pipeline: decode inputs, compute shares_hash
 * from share_comms, derive nullifier, build circuit, generate Halo2 proof.
 *
 * Parameters:
 *   merkle_path_ptr       - Pointer to 772-byte serialized Merkle path
 *                           (from zally_vote_tree_path_stateful: 4 bytes position + 24*32 siblings).
 *   merkle_path_len       - Length (must be 772).
 *   share_comms_ptr       - Pointer to 512 bytes: 16 share commitments x 32 bytes (Fp LE).
 *   share_comms_len       - Length (must be 512).
 *   primary_blind_ptr     - Pointer to 32-byte blind factor for the revealed share (Fp LE).
 *   enc_c1_x_ptr          - Pointer to 32-byte x-coord of revealed share's C1 (compressed, sign cleared).
 *   enc_c2_x_ptr          - Pointer to 32-byte x-coord of revealed share's C2 (compressed, sign cleared).
 *   share_index           - Which of the 16 shares (0..15).
 *   proposal_id           - Proposal being voted on.
 *   vote_decision         - Vote choice.
 *   round_id_ptr          - Pointer to 32-byte round ID (canonical Pallas Fp).
 *   round_id_len          - Length (must be 32).
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
 *   -5  proof generation failure.
 */
int32_t zally_generate_share_reveal(
    const uint8_t* merkle_path_ptr,
    size_t merkle_path_len,
    const uint8_t* share_comms_ptr,
    size_t share_comms_len,
    const uint8_t* primary_blind_ptr,
    const uint8_t* enc_c1_x_ptr,
    const uint8_t* enc_c2_x_ptr,
    uint32_t share_index,
    uint32_t proposal_id,
    uint32_t vote_decision,
    const uint8_t* round_id_ptr,
    size_t round_id_len,
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

/* -----------------------------------------------------------------------
 * Round ID derivation (Poseidon)
 * ----------------------------------------------------------------------- */

/*
 * Derive vote_round_id from session fields via Poseidon hash.
 *
 * Encodes the 6 inputs into 8 Fp elements and hashes with
 * Poseidon::<ConstantLength<8>> (P128Pow5T3). The output is a canonical
 * 32-byte Pallas Fp element.
 *
 * Parameters:
 *   snapshot_height      - Block height for the snapshot.
 *   snapshot_blockhash   - Pointer to 32-byte block hash.
 *   proposals_hash       - Pointer to 32-byte proposals hash.
 *   vote_end_time        - Unix timestamp when voting ends.
 *   nullifier_imt_root   - Pointer to 32-byte canonical Fp (IMT root).
 *   nc_root              - Pointer to 32-byte canonical Fp (NC root).
 *   round_id_out         - Pointer to 32-byte output buffer.
 *
 * Returns:
 *    0  on success (round_id written to round_id_out).
 *   -1  if any pointer is null.
 *   -3  if nullifier_imt_root or nc_root is not a canonical Pallas Fp.
 */
int32_t zally_derive_round_id(
    uint64_t snapshot_height,
    const uint8_t* snapshot_blockhash,
    const uint8_t* proposals_hash,
    uint64_t vote_end_time,
    const uint8_t* nullifier_imt_root,
    const uint8_t* nc_root,
    uint8_t* round_id_out
);

#ifdef __cplusplus
}
#endif

#endif /* ZALLY_CIRCUITS_H */
