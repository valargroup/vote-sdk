//! C-compatible FFI functions for calling verification from Go via CGo.
//!
//! All functions use C calling conventions and return i32 status codes:
//!   0  = success
//!   -1 = invalid input (null pointer, wrong length, etc.)
//!   -2 = verification failed (proof/signature is invalid) / position out of range (tree path)
//!   -3 = internal error (deserialization, etc.)

use std::sync::OnceLock;

use halo2_proofs::pasta::{EqAffine, Fp};
use halo2_proofs::plonk::VerifyingKey;
use halo2_proofs::poly::commitment::Params;
use pasta_curves::group::ff::{FromUniformBytes, PrimeField};

use crate::delegation;
use crate::redpallas;
use crate::share_reveal;
use crate::toy;
use crate::vote_proof;
use crate::votetree;

/// Convert a 32-byte hash (e.g. Blake2b-256 output) to a canonical Pallas Fp
/// element via wide reduction.
///
/// Raw 32-byte hashes are frequently non-canonical (the Pallas modulus is
/// ~2^254, so ~75% of random 32-byte values exceed it). This function
/// zero-extends the input to 64 bytes and calls `from_uniform_bytes`, which
/// performs a modular reduction that always yields a valid, canonical Fp.
///
/// Used for `voting_round_id` (Blake2b-256 derived) in ZKP #1, #2, and #3.
///
/// TODO: Once we move vote round to a field element we can delete this.
fn hash_bytes_to_fp(bytes: [u8; 32]) -> pasta_curves::pallas::Base {
    let mut wide = [0u8; 64];
    wide[..32].copy_from_slice(&bytes);
    pasta_curves::pallas::Base::from_uniform_bytes(&wide)
}

/// Cached delegation circuit params and verifying key.
///
/// IPA params generation (K=14 → 16,384 group elements) and circuit keygen
/// are expensive (~10-30s on slow hardware). They are deterministic and
/// identical for every verification call, so we compute them once and reuse.
fn delegation_vk_cached() -> &'static (Params<EqAffine>, VerifyingKey<EqAffine>) {
    static CACHE: OnceLock<(Params<EqAffine>, VerifyingKey<EqAffine>)> = OnceLock::new();
    CACHE.get_or_init(|| {
        let params = delegation::delegation_params();
        let (_pk, vk) = delegation::delegation_proving_key(&params);
        (params, vk)
    })
}

/// Cached vote proof circuit params and verifying key.
///
/// Same caching pattern as delegation: K=14 params and circuit keygen are
/// computed once and reused for all subsequent verification calls.
fn vote_proof_vk_cached() -> &'static (Params<EqAffine>, VerifyingKey<EqAffine>) {
    static CACHE: OnceLock<(Params<EqAffine>, VerifyingKey<EqAffine>)> = OnceLock::new();
    CACHE.get_or_init(|| {
        let params = vote_proof::vote_proof_params();
        let (_pk, vk) = vote_proof::vote_proof_proving_key(&params);
        (params, vk)
    })
}

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

// ---------------------------------------------------------------------------
// Vote commitment tree — Poseidon Merkle root and path via FFI
// ---------------------------------------------------------------------------

/// Compute the Poseidon Merkle root of a vote commitment tree built from the
/// given leaves.
///
/// This is a **stateless** call: a fresh tree is constructed from the leaf
/// array, checkpointed, and the root is returned. This matches the current
/// Go keeper pattern (read all leaves from KV, compute root).
///
/// # Arguments
/// * `leaves_ptr`  - Pointer to a flat byte array of leaves. Each leaf is
///                   32 bytes (Pallas Fp, little-endian canonical repr).
///                   Total size: `leaf_count * 32`.
/// * `leaf_count`  - Number of leaves.
/// * `root_out`    - Pointer to a 32-byte output buffer for the root.
///
/// # Returns
/// * `0`  on success (root written to `root_out`).
/// * `-1` if inputs are invalid (null pointers).
/// * `-3` if a leaf contains a non-canonical field element encoding.
///
/// # Safety
/// Caller must ensure pointers are valid and buffers are correctly sized.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_root(
    leaves_ptr: *const u8,
    leaf_count: usize,
    root_out: *mut u8,
) -> i32 {
    // Validate pointers.
    if root_out.is_null() {
        return -1;
    }
    if leaf_count > 0 && leaves_ptr.is_null() {
        return -1;
    }

    // Build tree and compute root.
    match votetree::compute_root_from_raw(leaves_ptr, leaf_count) {
        Ok(root_bytes) => {
            std::ptr::copy_nonoverlapping(root_bytes.as_ptr(), root_out, 32);
            0
        }
        Err(votetree::FfiError::InvalidInput) => -1,
        Err(votetree::FfiError::Deserialization) => -3,
        Err(votetree::FfiError::PositionOutOfRange) => -2, // should not happen for root
    }
}

/// Compute the Poseidon Merkle authentication path for a leaf at `position`
/// in a vote commitment tree built from the given leaves.
///
/// The path is serialized as [`MERKLE_PATH_BYTES`] bytes:
/// - Bytes `[0..4)`:    position (`u32` LE)
/// - Remaining bytes:   auth path (TREE_DEPTH sibling hashes, 32 bytes each, leaf→root)
///
/// # Arguments
/// * `leaves_ptr`  - Pointer to a flat byte array of leaves (each 32 bytes LE Fp).
/// * `leaf_count`  - Number of leaves.
/// * `position`    - Leaf index for which to generate the path.
/// * `path_out`    - Pointer to a [`MERKLE_PATH_BYTES`]-byte output buffer.
///
/// # Returns
/// * `0`  on success (path written to `path_out`).
/// * `-1` if inputs are invalid (null pointers, zero leaves).
/// * `-2` if `position` is out of range (>= leaf_count).
/// * `-3` if a leaf contains a non-canonical field element encoding.
///
/// # Safety
/// Caller must ensure pointers are valid and buffers are correctly sized.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_path(
    leaves_ptr: *const u8,
    leaf_count: usize,
    position: u64,
    path_out: *mut u8,
) -> i32 {
    // Validate pointers.
    if leaves_ptr.is_null() || path_out.is_null() {
        return -1;
    }
    if leaf_count == 0 {
        return -1;
    }

    // Build tree and compute path.
    match votetree::compute_path_from_raw(leaves_ptr, leaf_count, position) {
        Ok(path_bytes) => {
            std::ptr::copy_nonoverlapping(path_bytes.as_ptr(), path_out, path_bytes.len());
            0
        }
        Err(votetree::FfiError::InvalidInput) => -1,
        Err(votetree::FfiError::PositionOutOfRange) => -2,
        Err(votetree::FfiError::Deserialization) => -3,
    }
}

// ---------------------------------------------------------------------------
// Delegation circuit (ZKP #1) — real Halo2 proof verification
// ---------------------------------------------------------------------------

/// Verify a real delegation circuit proof (ZKP #1).
///
/// The public inputs are passed as a flat byte array of 11 × 32-byte
/// chunks (352 bytes total), in the order:
///   [nf_signed, rk_compressed, cmx_new, van_comm, vote_round_id,
///    nc_root, nf_imt_root, gov_null_1, gov_null_2, gov_null_3, gov_null_4]
///
/// The `rk_compressed` is a 32-byte compressed Pallas curve point. The FFI
/// decompresses it into (rk_x, rk_y) to produce the 12 field elements that
/// the circuit expects.
///
/// # Arguments
/// * `proof_ptr`         - Pointer to the serialized Halo2 proof bytes.
/// * `proof_len`         - Length of the proof byte slice.
/// * `public_inputs_ptr` - Pointer to 352 bytes (11 × 32-byte chunks).
/// * `public_inputs_len` - Length of the public inputs byte slice (must be 352).
///
/// # Returns
/// * `0`  on successful verification.
/// * `-1` if inputs are invalid (null pointers or wrong lengths).
/// * `-2` if the proof does not verify.
/// * `-3` if there is an internal deserialization error (e.g. invalid rk point).
///
/// # Safety
/// Caller must ensure the pointers are valid and the lengths are correct.
#[no_mangle]
pub unsafe extern "C" fn zally_verify_delegation_proof(
    proof_ptr: *const u8,
    proof_len: usize,
    public_inputs_ptr: *const u8,
    public_inputs_len: usize,
) -> i32 {
    use group::Curve;
    use pasta_curves::{arithmetic::CurveAffine, group::GroupEncoding, pallas};

    // Validate pointers and lengths.
    if proof_ptr.is_null() || public_inputs_ptr.is_null() {
        return -1;
    }
    if public_inputs_len != 11 * 32 {
        return -1;
    }
    if proof_len == 0 {
        return -1;
    }

    // Reconstruct slices from raw pointers.
    let proof = std::slice::from_raw_parts(proof_ptr, proof_len);
    let raw = std::slice::from_raw_parts(public_inputs_ptr, public_inputs_len);

    // Helper: extract a 32-byte chunk.
    let chunk = |i: usize| -> [u8; 32] {
        let mut buf = [0u8; 32];
        buf.copy_from_slice(&raw[i * 32..(i + 1) * 32]);
        buf
    };

    // Deserialize each chunk as a Pallas Fp, except rk which is a compressed point.
    let deserialize_fp =
        |bytes: [u8; 32]| -> Option<pallas::Base> { pallas::Base::from_repr(bytes).into() };

    // Slot 0: nf_signed
    let nf_signed = match deserialize_fp(chunk(0)) {
        Some(f) => f,
        None => return -3,
    };

    // Slot 1: rk (compressed Pallas point) — decompress to (x, y).
    let rk_bytes = chunk(1);
    let rk_point: pallas::Point = match pallas::Point::from_bytes(&rk_bytes).into() {
        Some(p) => p,
        None => return -3,
    };
    let rk_affine = rk_point.to_affine();
    let rk_coords: Option<pasta_curves::arithmetic::Coordinates<pallas::Affine>> =
        rk_affine.coordinates().into();
    let rk_coords = match rk_coords {
        Some(c) => c,
        None => return -3, // identity point
    };
    let rk_x: pallas::Base = *rk_coords.x();
    let rk_y: pallas::Base = *rk_coords.y();

    // Slots 2–10: the remaining 9 field elements.
    let cmx_new = match deserialize_fp(chunk(2)) {
        Some(f) => f,
        None => return -3,
    };
    let van_comm = match deserialize_fp(chunk(3)) {
        Some(f) => f,
        None => return -3,
    };
    // vote_round_id is a Blake2b-256 hash and may be non-canonical as a raw
    // Fp encoding. Use wide reduction to get a canonical field element.
    //
    // TODO: Once we move vote round to a field element we can use deserialize_fp directly.
    let vote_round_id = hash_bytes_to_fp(chunk(4));
    let nc_root = match deserialize_fp(chunk(5)) {
        Some(f) => f,
        None => return -3,
    };
    let nf_imt_root = match deserialize_fp(chunk(6)) {
        Some(f) => f,
        None => return -3,
    };
    let gov_null_1 = match deserialize_fp(chunk(7)) {
        Some(f) => f,
        None => return -3,
    };
    let gov_null_2 = match deserialize_fp(chunk(8)) {
        Some(f) => f,
        None => return -3,
    };
    let gov_null_3 = match deserialize_fp(chunk(9)) {
        Some(f) => f,
        None => return -3,
    };
    let gov_null_4 = match deserialize_fp(chunk(10)) {
        Some(f) => f,
        None => return -3,
    };

    // Build the 12-element public input vector (matches circuit instance order).
    let public_inputs = vec![
        nf_signed,
        rk_x,
        rk_y,
        cmx_new,
        van_comm,
        vote_round_id,
        nc_root,
        nf_imt_root,
        gov_null_1,
        gov_null_2,
        gov_null_3,
        gov_null_4,
    ];

    // Run verification using cached params and VK.
    // First call initializes the cache (~10-30s); subsequent calls are fast.
    let (params, vk) = delegation_vk_cached();

    let strategy = halo2_proofs::plonk::SingleVerifier::new(params);
    let mut transcript = halo2_proofs::transcript::Blake2bRead::<
        _,
        halo2_proofs::pasta::EqAffine,
        halo2_proofs::transcript::Challenge255<_>,
    >::init(proof);

    match halo2_proofs::plonk::verify_proof(
        params,
        vk,
        strategy,
        &[&[&public_inputs]],
        &mut transcript,
    ) {
        Ok(()) => 0,
        Err(_) => -2,
    }
}

// ---------------------------------------------------------------------------
// Vote proof circuit (ZKP #2) — real Halo2 proof verification
// ---------------------------------------------------------------------------

/// Verify a real vote proof circuit proof (ZKP #2).
///
/// The public inputs are passed as a flat byte array of 10 × 32-byte
/// chunks (320 bytes total), in the order:
///   [van_nullifier, r_vpk_x, r_vpk_y, vote_authority_note_new, vote_commitment,
///    vote_comm_tree_root, anchor_height_le, proposal_id_le, voting_round_id, ea_pk_compressed]
///
/// Condition 4 (Spend Authority) adds r_vpk = vsk.ak + [alpha_v]*G; r_vpk_x and r_vpk_y
/// are public inputs at slots 1 and 2. Slot 9 (ea_pk) is decompressed to (ea_pk_x, ea_pk_y)
/// for the circuit's 11 field elements.
///
/// # Arguments
/// * `proof_ptr`         - Pointer to the serialized Halo2 proof bytes.
/// * `proof_len`         - Length of the proof byte slice.
/// * `public_inputs_ptr` - Pointer to 320 bytes (10 × 32-byte chunks).
/// * `public_inputs_len` - Length of the public inputs byte slice (must be 320).
///
/// # Returns
/// * `0`  on successful verification.
/// * `-1` if inputs are invalid (null pointers or wrong lengths).
/// * `-2` if the proof does not verify.
/// * `-3` if there is an internal deserialization error (e.g. invalid ea_pk).
///
/// # Safety
/// Caller must ensure the pointers are valid and the lengths are correct.
#[no_mangle]
pub unsafe extern "C" fn zally_verify_vote_proof(
    proof_ptr: *const u8,
    proof_len: usize,
    public_inputs_ptr: *const u8,
    public_inputs_len: usize,
) -> i32 {
    use group::Curve;
    use pasta_curves::{arithmetic::CurveAffine, group::GroupEncoding, pallas};

    const NUM_CHUNKS: usize = 10;
    const EXPECTED_LEN: usize = NUM_CHUNKS * 32;

    // Validate pointers and lengths.
    if proof_ptr.is_null() || public_inputs_ptr.is_null() {
        return -1;
    }
    if public_inputs_len != EXPECTED_LEN {
        return -1;
    }
    if proof_len == 0 {
        return -1;
    }

    // Reconstruct slices from raw pointers.
    let proof = std::slice::from_raw_parts(proof_ptr, proof_len);
    let raw = std::slice::from_raw_parts(public_inputs_ptr, public_inputs_len);

    // Helper: extract a 32-byte chunk.
    let chunk = |i: usize| -> [u8; 32] {
        let mut buf = [0u8; 32];
        buf.copy_from_slice(&raw[i * 32..(i + 1) * 32]);
        buf
    };

    let deserialize_fp =
        |bytes: [u8; 32]| -> Option<pallas::Base> { pallas::Base::from_repr(bytes).into() };

    // Slot 0: van_nullifier (Fp)
    let van_nullifier = match deserialize_fp(chunk(0)) {
        Some(f) => f,
        None => return -3,
    };

    // Slots 1–2: r_vpk_x, r_vpk_y (condition 4: Spend Authority)
    let r_vpk_x = match deserialize_fp(chunk(1)) {
        Some(f) => f,
        None => return -3,
    };
    let r_vpk_y = match deserialize_fp(chunk(2)) {
        Some(f) => f,
        None => return -3,
    };

    // Slot 3: vote_authority_note_new (Fp)
    let vote_authority_note_new = match deserialize_fp(chunk(3)) {
        Some(f) => f,
        None => return -3,
    };

    // Slot 4: vote_commitment (Fp)
    let vote_commitment = match deserialize_fp(chunk(4)) {
        Some(f) => f,
        None => return -3,
    };

    // Slot 5: vote_comm_tree_root (Fp)
    let vote_comm_tree_root = match deserialize_fp(chunk(5)) {
        Some(f) => f,
        None => return -3,
    };

    // Slot 6: anchor_height (uint64 LE zero-padded to 32 bytes → Fp)
    let anchor_height_bytes = chunk(6);
    let anchor_height_u64 = u64::from_le_bytes(anchor_height_bytes[..8].try_into().unwrap());
    let vote_comm_tree_anchor_height = pallas::Base::from(anchor_height_u64);

    // Slot 7: proposal_id (uint32 LE zero-padded to 32 bytes → Fp)
    let proposal_id_bytes = chunk(7);
    let proposal_id_u32 = u32::from_le_bytes(proposal_id_bytes[..4].try_into().unwrap());
    let proposal_id = pallas::Base::from(u64::from(proposal_id_u32));

    // Slot 8: voting_round_id (Blake2b-256 hash — use wide reduction like ZKP #1 and #3)
    //
    // TODO: Once we move vote round to a field element we can use deserialize_fp directly.
    let voting_round_id = hash_bytes_to_fp(chunk(8));

    // Slot 9: ea_pk (compressed Pallas point) — decompress to (x, y).
    let ea_pk_bytes = chunk(9);
    let ea_pk_point: pallas::Point = match pallas::Point::from_bytes(&ea_pk_bytes).into() {
        Some(p) => p,
        None => return -3,
    };
    let ea_pk_affine = ea_pk_point.to_affine();
    let ea_pk_coords: Option<pasta_curves::arithmetic::Coordinates<pallas::Affine>> =
        ea_pk_affine.coordinates().into();
    let ea_pk_coords = match ea_pk_coords {
        Some(c) => c,
        None => return -3, // identity point
    };
    let ea_pk_x: pallas::Base = *ea_pk_coords.x();
    let ea_pk_y: pallas::Base = *ea_pk_coords.y();

    // Build the 11-element public input vector (matches circuit instance order).
    let public_inputs = vec![
        van_nullifier,
        r_vpk_x,
        r_vpk_y,
        vote_authority_note_new,
        vote_commitment,
        vote_comm_tree_root,
        vote_comm_tree_anchor_height,
        proposal_id,
        voting_round_id,
        ea_pk_x,
        ea_pk_y,
    ];

    // Run verification using cached params and VK.
    let (params, vk) = vote_proof_vk_cached();

    let strategy = halo2_proofs::plonk::SingleVerifier::new(params);
    let mut transcript = halo2_proofs::transcript::Blake2bRead::<
        _,
        halo2_proofs::pasta::EqAffine,
        halo2_proofs::transcript::Challenge255<_>,
    >::init(proof);

    match halo2_proofs::plonk::verify_proof(
        params,
        vk,
        strategy,
        &[&[&public_inputs]],
        &mut transcript,
    ) {
        Ok(()) => 0,
        Err(_) => -2,
    }
}

// ---------------------------------------------------------------------------
// Share Reveal circuit (ZKP #3) — real Halo2 proof verification
// ---------------------------------------------------------------------------

/// Cached share reveal circuit params and verifying key.
///
/// Same caching pattern as delegation_vk_cached().
fn share_reveal_vk_cached() -> &'static (Params<EqAffine>, VerifyingKey<EqAffine>) {
    static CACHE: OnceLock<(Params<EqAffine>, VerifyingKey<EqAffine>)> = OnceLock::new();
    CACHE.get_or_init(|| {
        let params = share_reveal::share_reveal_params();
        let (_pk, vk) = share_reveal::share_reveal_proving_key(&params);
        (params, vk)
    })
}

/// Verify a real share reveal circuit proof (ZKP #3).
///
/// The public inputs are passed as a flat byte array of 7 × 32-byte
/// chunks (224 bytes total), in order:
///   [share_nullifier, enc_share_c1_x, enc_share_c2_x, proposal_id,
///    vote_decision, vote_comm_tree_root, voting_round_id]
///
/// All values are plain Fp elements (32-byte LE canonical encoding).
/// No compressed point decompression needed (unlike delegation's rk).
///
/// # Arguments
/// * `proof_ptr`         - Pointer to the serialized Halo2 proof bytes.
/// * `proof_len`         - Length of the proof byte slice.
/// * `public_inputs_ptr` - Pointer to 224 bytes (7 × 32-byte chunks).
/// * `public_inputs_len` - Length of the public inputs byte slice (must be 224).
///
/// # Returns
/// * `0`  on successful verification.
/// * `-1` if inputs are invalid (null pointers or wrong lengths).
/// * `-2` if the proof does not verify.
/// * `-3` if there is an internal deserialization error.
///
/// # Safety
/// Caller must ensure the pointers are valid and the lengths are correct.
#[no_mangle]
pub unsafe extern "C" fn zally_verify_share_reveal_proof(
    proof_ptr: *const u8,
    proof_len: usize,
    public_inputs_ptr: *const u8,
    public_inputs_len: usize,
) -> i32 {
    use pasta_curves::pallas;

    const NUM_PUBLIC_INPUTS: usize = 7;
    const EXPECTED_BYTES: usize = NUM_PUBLIC_INPUTS * 32;

    // Validate pointers and lengths.
    if proof_ptr.is_null() || public_inputs_ptr.is_null() {
        return -1;
    }
    if public_inputs_len != EXPECTED_BYTES {
        return -1;
    }
    if proof_len == 0 {
        return -1;
    }

    // Reconstruct slices from raw pointers.
    let proof = std::slice::from_raw_parts(proof_ptr, proof_len);
    let raw = std::slice::from_raw_parts(public_inputs_ptr, public_inputs_len);

    // Helper: extract a 32-byte chunk by index.
    let chunk = |i: usize| -> [u8; 32] {
        let mut buf = [0u8; 32];
        buf.copy_from_slice(&raw[i * 32..(i + 1) * 32]);
        buf
    };
    let deserialize_fp =
        |bytes: [u8; 32]| -> Option<pallas::Base> { pallas::Base::from_repr(bytes).into() };

    // Deserialize each 32-byte chunk as a Pallas Fp element.
    // Slot 6 (voting_round_id) uses wide reduction because it is a
    // Blake2b-256 hash that may be non-canonical as a raw Fp encoding.
    let mut public_inputs: Vec<pallas::Base> = Vec::with_capacity(NUM_PUBLIC_INPUTS);
    for i in 0..NUM_PUBLIC_INPUTS {
        if i == 6 {
            // voting_round_id: wide reduction for Blake2b-256 output.
            public_inputs.push(hash_bytes_to_fp(chunk(i)));
        } else {
            match deserialize_fp(chunk(i)) {
                Some(f) => public_inputs.push(f),
                None => return -3,
            }
        }
    }

    // Run verification using cached params and VK.
    let (params, vk) = share_reveal_vk_cached();

    let strategy = halo2_proofs::plonk::SingleVerifier::new(params);
    let mut transcript = halo2_proofs::transcript::Blake2bRead::<
        _,
        halo2_proofs::pasta::EqAffine,
        halo2_proofs::transcript::Challenge255<_>,
    >::init(proof);

    match halo2_proofs::plonk::verify_proof(
        params,
        vk,
        strategy,
        &[&[&public_inputs]],
        &mut transcript,
    ) {
        Ok(()) => 0,
        Err(_) => -2,
    }
}
