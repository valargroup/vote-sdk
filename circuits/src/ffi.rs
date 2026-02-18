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

    // Debug: dump all 12 field elements so we can compare with prover side.
    {
        fn bytes_to_hex(b: &[u8]) -> String {
            b.iter().map(|byte| format!("{:02x}", byte)).collect()
        }
        let names = [
            "nf_signed", "rk_x", "rk_y", "cmx_new", "van_comm",
            "vote_round_id", "nc_root", "nf_imt_root",
            "gov_null_1", "gov_null_2", "gov_null_3", "gov_null_4",
        ];
        eprintln!("[zkp1-verify] 12 public inputs (post-deser, hex LE):");
        for (i, (fe, name)) in public_inputs.iter().zip(names.iter()).enumerate() {
            let bytes: [u8; 32] = fe.to_repr();
            eprintln!("[zkp1-verify]   [{:>2}] {:<14} {}", i, name, bytes_to_hex(&bytes));
        }
        // Also dump raw input chunks for slot 4 (vote_round_id before wide reduction)
        let raw_vrid = chunk(4);
        eprintln!("[zkp1-verify] raw vote_round_id (slot 4, before wide reduction): {}", bytes_to_hex(&raw_vrid));
        eprintln!("[zkp1-verify] proof_len={}", proof_len);
    }

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

// ---------------------------------------------------------------------------
// Share Reveal proof generation (ZKP #3) — composite FFI function
// ---------------------------------------------------------------------------

/// Generate a share reveal proof (ZKP #3) from raw inputs.
///
/// This composite function performs the entire crypto pipeline in a single
/// CGo call, avoiding the need to expose 6+ individual Poseidon/field-arithmetic
/// functions to Go:
///
/// 1. Decode Merkle auth path from serialized bytes
/// 2. Decode 8 compressed Pallas points (all_enc_shares), clear sign bits → x-coords
/// 3. Compute shares_hash and verify against expected value
/// 4. Convert round_id via wide reduction to canonical Fp
/// 5. Compute vote_commitment via Poseidon hash
/// 6. Derive share_nullifier via Poseidon hash
/// 7. Build share reveal circuit with all witnesses
/// 8. Generate Halo2 proof (CPU-intensive, ~30-60s in release mode)
///
/// # Arguments
/// * `merkle_path_ptr/len`       - 772-byte serialized Merkle path (from `zally_vote_tree_path`)
/// * `all_enc_shares_ptr/len`    - 256 bytes: 4 shares × (C1 + C2) × 32 bytes each
///                                 Order: C1_0, C2_0, C1_1, C2_1, C1_2, C2_2, C1_3, C2_3
/// * `share_index`               - Which of the 4 shares (0..3)
/// * `proposal_id`               - Proposal being voted on
/// * `vote_decision`             - Vote choice (0=support, 1=oppose, 2=skip)
/// * `round_id_ptr/len`          - 32-byte raw Blake2b-256 round ID
/// * `expected_shares_hash_ptr`  - 32-byte expected shares_hash (Fp, canonical LE)
/// * `proof_out/capacity/len_out` - Output buffer for the proof bytes
/// * `nullifier_out`             - 32-byte output buffer for share nullifier
/// * `tree_root_out`             - 32-byte output buffer for commitment tree root
///
/// # Returns
/// *  `0` on success (proof, nullifier, tree_root written to output buffers)
/// * `-1` invalid input (null pointers, wrong lengths)
/// * `-3` deserialization error (non-canonical Fp, invalid point)
/// * `-4` shares_hash mismatch (all_enc_shares don't match expected_shares_hash)
/// * `-5` proof generation failure
///
/// # Safety
/// Caller must ensure all pointers are valid and buffers are correctly sized.
#[no_mangle]
pub unsafe extern "C" fn zally_generate_share_reveal(
    merkle_path_ptr: *const u8,
    merkle_path_len: usize,
    all_enc_shares_ptr: *const u8,
    all_enc_shares_len: usize,
    share_index: u32,
    proposal_id: u32,
    vote_decision: u32,
    round_id_ptr: *const u8,
    round_id_len: usize,
    expected_shares_hash_ptr: *const u8,
    proof_out: *mut u8,
    proof_out_capacity: usize,
    proof_len_out: *mut usize,
    nullifier_out: *mut u8,
    tree_root_out: *mut u8,
) -> i32 {
    use pasta_curves::pallas;

    // --- Input validation ---
    if merkle_path_ptr.is_null()
        || all_enc_shares_ptr.is_null()
        || round_id_ptr.is_null()
        || expected_shares_hash_ptr.is_null()
        || proof_out.is_null()
        || proof_len_out.is_null()
        || nullifier_out.is_null()
        || tree_root_out.is_null()
    {
        return -1;
    }
    if merkle_path_len != votetree::MERKLE_PATH_BYTES {
        return -1;
    }
    // 4 shares × 2 points (C1+C2) × 32 bytes = 256
    if all_enc_shares_len != 256 {
        return -1;
    }
    if round_id_len != 32 {
        return -1;
    }
    if share_index > 3 {
        return -1;
    }

    // --- Step 1: Decode Merkle auth path ---
    let merkle_path_raw = std::slice::from_raw_parts(merkle_path_ptr, merkle_path_len);

    // Position is first 4 bytes (u32 LE).
    let position = u32::from_le_bytes([
        merkle_path_raw[0],
        merkle_path_raw[1],
        merkle_path_raw[2],
        merkle_path_raw[3],
    ]);

    // Auth path: TREE_DEPTH sibling hashes, 32 bytes each, starting at offset 4.
    const TREE_DEPTH: usize = vote_commitment_tree::TREE_DEPTH;
    let mut auth_path = [pallas::Base::zero(); TREE_DEPTH];
    for i in 0..TREE_DEPTH {
        let offset = 4 + i * 32;
        let mut bytes = [0u8; 32];
        bytes.copy_from_slice(&merkle_path_raw[offset..offset + 32]);
        match Option::from(pallas::Base::from_repr(bytes)) {
            Some(fp) => auth_path[i] = fp,
            None => return -3,
        }
    }

    // --- Step 2: Decode all 8 encrypted share x-coordinates ---
    // Layout: C1_0(32) C2_0(32) C1_1(32) C2_1(32) C1_2(32) C2_2(32) C1_3(32) C2_3(32)
    let enc_shares_raw = std::slice::from_raw_parts(all_enc_shares_ptr, all_enc_shares_len);

    let mut all_c1_x = [pallas::Base::zero(); 4];
    let mut all_c2_x = [pallas::Base::zero(); 4];

    for i in 0..4usize {
        let c1_offset = i * 64;
        let c2_offset = c1_offset + 32;

        let mut c1_bytes = [0u8; 32];
        c1_bytes.copy_from_slice(&enc_shares_raw[c1_offset..c1_offset + 32]);
        // Clear sign bit to get raw x-coordinate.
        c1_bytes[31] &= 0x7F;
        match Option::from(pallas::Base::from_repr(c1_bytes)) {
            Some(fp) => all_c1_x[i] = fp,
            None => return -3,
        }

        let mut c2_bytes = [0u8; 32];
        c2_bytes.copy_from_slice(&enc_shares_raw[c2_offset..c2_offset + 32]);
        c2_bytes[31] &= 0x7F;
        match Option::from(pallas::Base::from_repr(c2_bytes)) {
            Some(fp) => all_c2_x[i] = fp,
            None => return -3,
        }
    }

    // --- Step 3: Compute shares_hash and verify against expected ---
    let computed_shares_hash = orchard::vote_proof::shares_hash(all_c1_x, all_c2_x);

    let expected_hash_raw = std::slice::from_raw_parts(expected_shares_hash_ptr, 32);
    let mut expected_hash_bytes = [0u8; 32];
    expected_hash_bytes.copy_from_slice(expected_hash_raw);
    let expected_shares_hash = match Option::from(pallas::Base::from_repr(expected_hash_bytes)) {
        Some(fp) => fp,
        None => return -3,
    };
    if computed_shares_hash != expected_shares_hash {
        return -4;
    }

    // --- Step 4: Convert round_id to Fp via wide reduction ---
    let round_id_raw = std::slice::from_raw_parts(round_id_ptr, 32);
    let mut round_id_bytes = [0u8; 32];
    round_id_bytes.copy_from_slice(round_id_raw);
    let voting_round_id = hash_bytes_to_fp(round_id_bytes);

    // --- Step 5: Compute vote_commitment ---
    let proposal_id_fp = pallas::Base::from(u64::from(proposal_id));
    let vote_decision_fp = pallas::Base::from(u64::from(vote_decision));
    let _vote_commitment = vote_commitment_tree::vote_commitment_hash(
        computed_shares_hash,
        proposal_id_fp,
        vote_decision_fp,
    );

    // --- Step 6: Build circuit and instance ---
    // build_share_reveal internally computes the nullifier, tree root, and
    // populates all circuit witnesses.
    let bundle = share_reveal::builder::build_share_reveal(
        auth_path,
        position,
        all_c1_x,
        all_c2_x,
        share_index,
        proposal_id_fp,
        vote_decision_fp,
        voting_round_id,
    );

    // Extract the nullifier and tree root from the instance.
    let share_nullifier = bundle.instance.share_nullifier;
    let tree_root = bundle.instance.vote_comm_tree_root;

    // --- Step 7: Generate Halo2 proof ---
    // Uses cached params and proving key (~30-60s in release mode).
    let proof_bytes = std::panic::catch_unwind(|| {
        share_reveal::create_share_reveal_proof(bundle.circuit, &bundle.instance)
    });
    let proof_bytes = match proof_bytes {
        Ok(bytes) => bytes,
        Err(_) => return -5,
    };

    // --- Step 8: Write outputs ---
    if proof_bytes.len() > proof_out_capacity {
        return -5; // proof too large for output buffer
    }

    std::ptr::copy_nonoverlapping(proof_bytes.as_ptr(), proof_out, proof_bytes.len());
    *proof_len_out = proof_bytes.len();

    let nullifier_bytes = share_nullifier.to_repr();
    std::ptr::copy_nonoverlapping(nullifier_bytes.as_ptr(), nullifier_out, 32);

    let tree_root_bytes = tree_root.to_repr();
    std::ptr::copy_nonoverlapping(tree_root_bytes.as_ptr(), tree_root_out, 32);

    0
}

/// Build test data for `zally_generate_share_reveal` FFI round-trip tests.
///
/// Uses synthetic x-coordinates (small Fp values) as stand-ins for actual
/// encrypted share x-coordinates. Since canonical Pallas Fp elements never
/// have the sign bit set (modulus < 2^255), the FFI's sign-bit clearing is
/// a no-op and the round-trip is clean.
///
/// Returns (merkle_path, all_enc_shares_flat, share_index, proposal_id,
///          vote_decision, round_id, shares_hash).
pub fn build_share_reveal_test_data()
    -> (Vec<u8>, [u8; 256], u32, u32, u32, [u8; 32], [u8; 32])
{
    use pasta_curves::group::ff::PrimeField;
    use pasta_curves::pallas;

    let proposal_id: u32 = 3;
    let vote_decision: u32 = 1;
    let round_id = [0u8; 32]; // simple zero round ID

    // Synthetic x-coordinates for encrypted shares.
    let mut all_c1_x = [pallas::Base::zero(); 4];
    let mut all_c2_x = [pallas::Base::zero(); 4];
    for i in 0..4u64 {
        all_c1_x[i as usize] = pallas::Base::from(100 + i);
        all_c2_x[i as usize] = pallas::Base::from(200 + i);
    }

    // Compute shares_hash.
    let shares_hash_fp = orchard::vote_proof::shares_hash(all_c1_x, all_c2_x);
    let shares_hash_bytes: [u8; 32] = shares_hash_fp.to_repr();

    // Compute vote_commitment.
    let proposal_id_fp = pallas::Base::from(u64::from(proposal_id));
    let vote_decision_fp = pallas::Base::from(u64::from(vote_decision));
    let vote_commitment = vote_commitment_tree::vote_commitment_hash(
        shares_hash_fp,
        proposal_id_fp,
        vote_decision_fp,
    );

    // Build single-leaf Merkle tree with vote_commitment as the leaf.
    let vc_bytes = vote_commitment.to_repr();
    let mut path_buf = [0u8; votetree::MERKLE_PATH_BYTES];
    unsafe {
        let rc = zally_vote_tree_path(
            vc_bytes.as_ptr(),
            1, // leaf_count
            0, // position
            path_buf.as_mut_ptr(),
        );
        assert_eq!(rc, 0, "zally_vote_tree_path failed");
    }

    // Flatten enc_shares: C1_0(32) C2_0(32) C1_1(32) C2_1(32) ...
    let mut enc_shares_flat = [0u8; 256];
    for i in 0..4 {
        let c1_bytes = all_c1_x[i].to_repr();
        let c2_bytes = all_c2_x[i].to_repr();
        enc_shares_flat[i * 64..i * 64 + 32].copy_from_slice(&c1_bytes);
        enc_shares_flat[i * 64 + 32..i * 64 + 64].copy_from_slice(&c2_bytes);
    }

    (
        path_buf.to_vec(),
        enc_shares_flat,
        0,
        proposal_id,
        vote_decision,
        round_id,
        shares_hash_bytes,
    )
}

// ---------------------------------------------------------------------------
// nc_root extraction — Sinsemilla-based Orchard commitment tree root
// ---------------------------------------------------------------------------

/// Compute the Orchard note commitment tree root from a hex-encoded frontier.
///
/// Lightwalletd's TreeState contains an `orchardTree` field: a hex string
/// encoding a serialized `CommitmentTree<MerkleHashOrchard, 32>`. Go can
/// fetch this via gRPC but cannot compute the Sinsemilla-based root.
/// This FFI function bridges that gap.
///
/// # Arguments
/// * `hex_ptr` - Pointer to the hex-encoded orchard frontier string (ASCII).
/// * `hex_len` - Length of the hex string (in bytes/characters).
/// * `root_out` - Pointer to a 32-byte output buffer for the root.
///
/// # Returns
/// *  `0` on success (root written to root_out).
/// * `-1` if inputs are invalid (null pointers, zero length).
/// * `-3` if the hex string is invalid or the frontier cannot be parsed.
///
/// # Safety
/// Caller must ensure pointers are valid and root_out has room for 32 bytes.
#[no_mangle]
pub unsafe extern "C" fn zally_extract_nc_root(
    hex_ptr: *const u8,
    hex_len: usize,
    root_out: *mut u8,
) -> i32 {
    if hex_ptr.is_null() || root_out.is_null() || hex_len == 0 {
        return -1;
    }

    let hex_bytes = std::slice::from_raw_parts(hex_ptr, hex_len);
    let hex_str = match std::str::from_utf8(hex_bytes) {
        Ok(s) => s,
        Err(_) => return -3,
    };

    match crate::nc_root::compute_nc_root(hex_str) {
        Ok(root) => {
            std::ptr::copy_nonoverlapping(root.as_ptr(), root_out, 32);
            0
        }
        Err(_) => -3,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    /// Full round-trip test: generate a share reveal proof and verify it via FFI.
    ///
    /// This test runs real Halo2 proving (~30-60s in release mode).
    /// Run with: `cargo test --release -p zally-circuits test_generate_share_reveal -- --ignored`
    #[test]
    #[ignore]
    fn test_generate_share_reveal() {
        let (
            merkle_path,
            enc_shares,
            share_index,
            proposal_id,
            vote_decision,
            round_id,
            shares_hash,
        ) = build_share_reveal_test_data();

        let mut proof_buf = [0u8; 8192];
        let mut proof_len: usize = 0;
        let mut nullifier = [0u8; 32];
        let mut tree_root = [0u8; 32];

        let rc = unsafe {
            zally_generate_share_reveal(
                merkle_path.as_ptr(),
                merkle_path.len(),
                enc_shares.as_ptr(),
                enc_shares.len(),
                share_index,
                proposal_id,
                vote_decision,
                round_id.as_ptr(),
                round_id.len(),
                shares_hash.as_ptr(),
                proof_buf.as_mut_ptr(),
                proof_buf.len(),
                &mut proof_len,
                nullifier.as_mut_ptr(),
                tree_root.as_mut_ptr(),
            )
        };

        assert_eq!(rc, 0, "generate returned error code {}", rc);
        assert!(proof_len > 0, "proof should not be empty");

        let proof = &proof_buf[..proof_len];

        // Build public inputs for the verifier: 7 × 32-byte chunks.
        // [share_nullifier, enc_share_c1_x, enc_share_c2_x, proposal_id,
        //  vote_decision, vote_comm_tree_root, voting_round_id]
        let mut public_inputs = [0u8; 7 * 32];

        // Slot 0: share_nullifier
        public_inputs[0..32].copy_from_slice(&nullifier);

        // Slot 1: enc_share_c1_x (for share_index=0 → enc_shares[0..32])
        let idx = share_index as usize;
        public_inputs[32..64].copy_from_slice(&enc_shares[idx * 64..idx * 64 + 32]);
        public_inputs[63] &= 0x7F; // clear sign bit

        // Slot 2: enc_share_c2_x
        public_inputs[64..96].copy_from_slice(&enc_shares[idx * 64 + 32..idx * 64 + 64]);
        public_inputs[95] &= 0x7F;

        // Slot 3: proposal_id as Fp (small u32 → first 4 bytes LE)
        public_inputs[96..100].copy_from_slice(&proposal_id.to_le_bytes());

        // Slot 4: vote_decision as Fp
        public_inputs[128..132].copy_from_slice(&vote_decision.to_le_bytes());

        // Slot 5: vote_comm_tree_root
        public_inputs[160..192].copy_from_slice(&tree_root);

        // Slot 6: voting_round_id (raw bytes; verifier applies wide reduction)
        public_inputs[192..224].copy_from_slice(&round_id);

        let verify_rc = unsafe {
            zally_verify_share_reveal_proof(
                proof.as_ptr(),
                proof.len(),
                public_inputs.as_ptr(),
                public_inputs.len(),
            )
        };

        assert_eq!(verify_rc, 0, "verification failed with code {}", verify_rc);
    }
}
