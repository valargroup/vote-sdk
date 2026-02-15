//! C-compatible FFI functions for calling verification from Go via CGo.
//!
//! All functions use C calling conventions and return i32 status codes:
//!   0  = success
//!   -1 = invalid input (null pointer, wrong length, etc.)
//!   -2 = verification failed (proof/signature is invalid) / position out of range (tree path)
//!   -3 = internal error (deserialization, etc.)

use std::sync::OnceLock;

use pasta_curves::group::ff::PrimeField;
use halo2_proofs::pasta::{Fp, EqAffine};
use halo2_proofs::plonk::VerifyingKey;
use halo2_proofs::poly::commitment::Params;

use crate::toy;
use crate::redpallas;
use crate::votetree;
use crate::delegation;

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
///   [nf_signed, rk_compressed, cmx_new, gov_comm, vote_round_id,
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
    let deserialize_fp = |bytes: [u8; 32]| -> Option<pallas::Base> {
        pallas::Base::from_repr(bytes).into()
    };

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
    let cmx_new = match deserialize_fp(chunk(2)) { Some(f) => f, None => return -3 };
    let gov_comm = match deserialize_fp(chunk(3)) { Some(f) => f, None => return -3 };
    let vote_round_id = match deserialize_fp(chunk(4)) { Some(f) => f, None => return -3 };
    let nc_root = match deserialize_fp(chunk(5)) { Some(f) => f, None => return -3 };
    let nf_imt_root = match deserialize_fp(chunk(6)) { Some(f) => f, None => return -3 };
    let gov_null_1 = match deserialize_fp(chunk(7)) { Some(f) => f, None => return -3 };
    let gov_null_2 = match deserialize_fp(chunk(8)) { Some(f) => f, None => return -3 };
    let gov_null_3 = match deserialize_fp(chunk(9)) { Some(f) => f, None => return -3 };
    let gov_null_4 = match deserialize_fp(chunk(10)) { Some(f) => f, None => return -3 };

    // Build the 12-element public input vector (matches circuit instance order).
    let public_inputs = vec![
        nf_signed, rk_x, rk_y, cmx_new, gov_comm, vote_round_id,
        nc_root, nf_imt_root, gov_null_1, gov_null_2, gov_null_3, gov_null_4,
    ];

    // Run verification using cached params and VK.
    // First call initializes the cache (~10-30s); subsequent calls are fast.
    let (params, vk) = delegation_vk_cached();

    let strategy = halo2_proofs::plonk::SingleVerifier::new(params);
    let mut transcript = halo2_proofs::transcript::Blake2bRead::<
        _, halo2_proofs::pasta::EqAffine, halo2_proofs::transcript::Challenge255<_>,
    >::init(proof);

    match halo2_proofs::plonk::verify_proof(
        params, vk, strategy, &[&[&public_inputs]], &mut transcript,
    ) {
        Ok(()) => 0,
        Err(_) => -2,
    }
}
