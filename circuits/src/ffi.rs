//! C-compatible FFI functions for calling verification from Go via CGo.
//!
//! All functions use C calling conventions and return i32 status codes:
//!   0  = success
//!   -1 = invalid input (null pointer, wrong length, etc.)
//!   -2 = verification failed (proof/signature is invalid) / position out of range (tree path)
//!   -3 = internal error (deserialization, etc.)
//!
//! On any non-zero return, a human-readable description is stored in a
//! thread-local buffer. Call `zally_last_error()` immediately after a
//! failing call to retrieve the message before the next FFI call clears it.

use std::ffi::CString;
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

// ---------------------------------------------------------------------------
// Thread-local last-error store
// ---------------------------------------------------------------------------

// Each thread keeps its own CString so that the pointer returned by
// `zally_last_error()` is stable until the next FFI call on that thread.
thread_local! {
    static LAST_ERROR: std::cell::RefCell<CString> =
        std::cell::RefCell::new(CString::new("").unwrap());
}

/// Store a human-readable error message in the thread-local buffer.
///
/// Called internally by every FFI function before returning a non-zero code.
/// The message is retrievable via `zally_last_error()`.
fn set_ffi_error(msg: impl AsRef<str>) {
    let s = msg.as_ref();
    let cstr = CString::new(s)
        .unwrap_or_else(|_| CString::new("<error message contained NUL byte>").unwrap());
    LAST_ERROR.with(|cell| *cell.borrow_mut() = cstr);
}

/// Return a pointer to the last error message for the current thread.
///
/// The returned pointer points into a thread-local buffer. It is valid until
/// the next FFI call on this thread (which may overwrite the buffer). Copy
/// the string immediately — e.g. via `C.GoString()` in CGo — before making
/// another FFI call.
///
/// Returns a pointer to an empty string (never NULL) when no error has been set.
///
/// The caller MUST NOT free the returned pointer.
#[no_mangle]
pub extern "C" fn zally_last_error() -> *const std::os::raw::c_char {
    LAST_ERROR.with(|cell| cell.borrow().as_ptr())
}

/// Clear the thread-local error message.
///
/// Optional housekeeping; all FFI functions overwrite the buffer on entry
/// so an explicit clear is rarely needed.
#[no_mangle]
pub extern "C" fn zally_clear_error() {
    LAST_ERROR.with(|cell| {
        *cell.borrow_mut() = CString::new("").unwrap();
    });
}

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
        None => {
            set_ffi_error("toy: public input is not a canonical Pallas Fp element");
            return -3;
        }
    };

    // Run verification.
    match toy::verify_toy(proof, &fp) {
        Ok(()) => 0,
        Err(e) => {
            set_ffi_error(format!("toy: verify_proof failed: {:?}", e));
            -2
        }
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
// Delegation circuit (ZKP #1) — real Halo2 proof verification
// ---------------------------------------------------------------------------

/// Verify a real delegation circuit proof (ZKP #1).
///
/// The public inputs are passed as a flat byte array of 12 × 32-byte
/// chunks (384 bytes total), in the order:
///   [nf_signed, rk_compressed, cmx_new, van_comm, vote_round_id,
///    nc_root, nf_imt_root, gov_null_1, gov_null_2, gov_null_3, gov_null_4, gov_null_5]
///
/// The `rk_compressed` is a 32-byte compressed Pallas curve point. The FFI
/// decompresses it into (rk_x, rk_y) to produce the 13 field elements that
/// the circuit expects.
///
/// # Arguments
/// * `proof_ptr`         - Pointer to the serialized Halo2 proof bytes.
/// * `proof_len`         - Length of the proof byte slice.
/// * `public_inputs_ptr` - Pointer to 384 bytes (12 × 32-byte chunks).
/// * `public_inputs_len` - Length of the public inputs byte slice (must be 384).
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
    if public_inputs_len != 12 * 32 {
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
        None => {
            set_ffi_error("delegation: slot 0 (nf_signed) is not a canonical Pallas Fp element");
            return -3;
        }
    };

    // Slot 1: rk (compressed Pallas point) — decompress to (x, y).
    let rk_bytes = chunk(1);
    let rk_point: pallas::Point = match pallas::Point::from_bytes(&rk_bytes).into() {
        Some(p) => p,
        None => {
            set_ffi_error(format!(
                "delegation: slot 1 (rk) is not a valid compressed Pallas point: {:02x?}",
                &rk_bytes[..4]
            ));
            return -3;
        }
    };
    let rk_affine = rk_point.to_affine();
    let rk_coords: Option<pasta_curves::arithmetic::Coordinates<pallas::Affine>> =
        rk_affine.coordinates().into();
    let rk_coords = match rk_coords {
        Some(c) => c,
        None => {
            set_ffi_error("delegation: slot 1 (rk) decompressed to the identity point");
            return -3;
        }
    };
    let rk_x: pallas::Base = *rk_coords.x();
    let rk_y: pallas::Base = *rk_coords.y();

    // Slots 2–11: the remaining 10 field elements.
    let cmx_new = match deserialize_fp(chunk(2)) {
        Some(f) => f,
        None => {
            set_ffi_error("delegation: slot 2 (cmx_new) is not a canonical Pallas Fp element");
            return -3;
        }
    };
    let van_comm = match deserialize_fp(chunk(3)) {
        Some(f) => f,
        None => {
            set_ffi_error("delegation: slot 3 (van_comm) is not a canonical Pallas Fp element");
            return -3;
        }
    };
    // vote_round_id is a Blake2b-256 hash and may be non-canonical as a raw
    // Fp encoding. Use wide reduction to get a canonical field element.
    //
    // TODO: Once we move vote round to a field element we can use deserialize_fp directly.
    let vote_round_id = hash_bytes_to_fp(chunk(4));
    let nc_root = match deserialize_fp(chunk(5)) {
        Some(f) => f,
        None => {
            set_ffi_error("delegation: slot 5 (nc_root) is not a canonical Pallas Fp element");
            return -3;
        }
    };
    let nf_imt_root = match deserialize_fp(chunk(6)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "delegation: slot 6 (nf_imt_root) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };
    let gov_null_1 = match deserialize_fp(chunk(7)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "delegation: slot 7 (gov_null_1) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };
    let gov_null_2 = match deserialize_fp(chunk(8)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "delegation: slot 8 (gov_null_2) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };
    let gov_null_3 = match deserialize_fp(chunk(9)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "delegation: slot 9 (gov_null_3) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };
    let gov_null_4 = match deserialize_fp(chunk(10)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "delegation: slot 10 (gov_null_4) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };
    let gov_null_5 = match deserialize_fp(chunk(11)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "delegation: slot 11 (gov_null_5) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };

    // Build the 13-element public input vector (matches circuit instance order).
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
        gov_null_5,
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
        Err(e) => {
            set_ffi_error(format!("delegation: verify_proof failed: {:?}", e));
            -2
        }
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
        None => {
            set_ffi_error(
                "vote: slot 0 (van_nullifier) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };

    // Slots 1–2: r_vpk_x, r_vpk_y (condition 4: Spend Authority)
    let r_vpk_x = match deserialize_fp(chunk(1)) {
        Some(f) => f,
        None => {
            set_ffi_error("vote: slot 1 (r_vpk_x) is not a canonical Pallas Fp element");
            return -3;
        }
    };
    let r_vpk_y = match deserialize_fp(chunk(2)) {
        Some(f) => f,
        None => {
            set_ffi_error("vote: slot 2 (r_vpk_y) is not a canonical Pallas Fp element");
            return -3;
        }
    };

    // Slot 3: vote_authority_note_new (Fp)
    let vote_authority_note_new = match deserialize_fp(chunk(3)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "vote: slot 3 (vote_authority_note_new) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };

    // Slot 4: vote_commitment (Fp)
    let vote_commitment = match deserialize_fp(chunk(4)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "vote: slot 4 (vote_commitment) is not a canonical Pallas Fp element",
            );
            return -3;
        }
    };

    // Slot 5: vote_comm_tree_root (Fp)
    let vote_comm_tree_root = match deserialize_fp(chunk(5)) {
        Some(f) => f,
        None => {
            set_ffi_error(
                "vote: slot 5 (vote_comm_tree_root) is not a canonical Pallas Fp element",
            );
            return -3;
        }
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
        None => {
            set_ffi_error(format!(
                "vote: slot 9 (ea_pk) is not a valid compressed Pallas point: {:02x?}",
                &ea_pk_bytes[..4]
            ));
            return -3;
        }
    };
    let ea_pk_affine = ea_pk_point.to_affine();
    let ea_pk_coords: Option<pasta_curves::arithmetic::Coordinates<pallas::Affine>> =
        ea_pk_affine.coordinates().into();
    let ea_pk_coords = match ea_pk_coords {
        Some(c) => c,
        None => {
            set_ffi_error("vote: slot 9 (ea_pk) decompressed to the identity point");
            return -3;
        }
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
        Err(e) => {
            set_ffi_error(format!("vote: verify_proof failed: {:?}", e));
            -2
        }
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

    const SLOT_NAMES: [&str; 7] = [
        "share_nullifier",
        "enc_share_c1_x",
        "enc_share_c2_x",
        "proposal_id",
        "vote_decision",
        "vote_comm_tree_root",
        "voting_round_id",
    ];

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
                None => {
                    set_ffi_error(format!(
                        "share_reveal: slot {} ({}) is not a canonical Pallas Fp element",
                        i, SLOT_NAMES[i]
                    ));
                    return -3;
                }
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
        Err(e) => {
            set_ffi_error(format!("share_reveal: verify_proof failed: {:?}", e));
            -2
        }
    }
}

// ---------------------------------------------------------------------------
// Share Reveal proof generation (ZKP #3) — composite FFI function
// ---------------------------------------------------------------------------

/// Generate a share reveal proof (ZKP #3) from raw inputs.
///
/// This composite function performs the entire crypto pipeline in a single
/// CGo call:
///
/// 1. Decode Merkle auth path from serialized bytes
/// 2. Decode 16 share commitments (public inputs to the circuit)
/// 3. Decode primary blind and revealed share coordinates
/// 4. Convert round_id via wide reduction to canonical Fp
/// 5. Build share reveal circuit with all witnesses + public share_comms
/// 6. Generate Halo2 proof (CPU-intensive, ~30-60s in release mode)
///
/// # Arguments
/// * `merkle_path_ptr/len`   - 772-byte serialized Merkle path
/// * `share_comms_ptr/len`   - 512 bytes: 16 × 32-byte Poseidon commitments
/// * `primary_blind_ptr`     - 32-byte blind factor for the revealed share
/// * `enc_c1_x_ptr`          - 32-byte x-coord of revealed share's C1 (compressed)
/// * `enc_c2_x_ptr`          - 32-byte x-coord of revealed share's C2 (compressed)
/// * `share_index`           - Which of the 16 shares (0..15)
/// * `proposal_id`           - Proposal being voted on
/// * `vote_decision`         - Vote choice (0=support, 1=oppose, 2=skip)
/// * `round_id_ptr/len`      - 32-byte raw Blake2b-256 round ID
/// * `proof_out/capacity/len_out` - Output buffer for the proof bytes
/// * `nullifier_out`         - 32-byte output buffer for share nullifier
/// * `tree_root_out`         - 32-byte output buffer for commitment tree root
///
/// # Returns
/// *  `0` on success (proof, nullifier, tree_root written to output buffers)
/// * `-1` invalid input (null pointers, wrong lengths)
/// * `-3` deserialization error (non-canonical Fp)
/// * `-5` proof generation failure
///
/// # Safety
/// Caller must ensure all pointers are valid and buffers are correctly sized.
#[no_mangle]
pub unsafe extern "C" fn zally_generate_share_reveal(
    merkle_path_ptr: *const u8,
    merkle_path_len: usize,
    share_comms_ptr: *const u8,
    share_comms_len: usize,
    primary_blind_ptr: *const u8,
    enc_c1_x_ptr: *const u8,
    enc_c2_x_ptr: *const u8,
    share_index: u32,
    proposal_id: u32,
    vote_decision: u32,
    round_id_ptr: *const u8,
    round_id_len: usize,
    proof_out: *mut u8,
    proof_out_capacity: usize,
    proof_len_out: *mut usize,
    nullifier_out: *mut u8,
    tree_root_out: *mut u8,
) -> i32 {
    use pasta_curves::pallas;

    // --- Input validation ---
    if merkle_path_ptr.is_null()
        || share_comms_ptr.is_null()
        || primary_blind_ptr.is_null()
        || enc_c1_x_ptr.is_null()
        || enc_c2_x_ptr.is_null()
        || round_id_ptr.is_null()
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
    // 16 share commitments × 32 bytes = 512
    if share_comms_len != 512 {
        return -1;
    }
    if round_id_len != 32 {
        return -1;
    }
    if share_index > 15 {
        return -1;
    }

    // --- Step 1: Decode Merkle auth path ---
    let merkle_path_raw = std::slice::from_raw_parts(merkle_path_ptr, merkle_path_len);

    let position = u32::from_le_bytes([
        merkle_path_raw[0],
        merkle_path_raw[1],
        merkle_path_raw[2],
        merkle_path_raw[3],
    ]);

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

    // --- Step 2: Decode share commitments ---
    let share_comms_raw = std::slice::from_raw_parts(share_comms_ptr, share_comms_len);
    let mut share_comms = [pallas::Base::zero(); 16];
    for i in 0..16usize {
        let offset = i * 32;
        let mut bytes = [0u8; 32];
        bytes.copy_from_slice(&share_comms_raw[offset..offset + 32]);
        match Option::from(pallas::Base::from_repr(bytes)) {
            Some(fp) => share_comms[i] = fp,
            None => return -3,
        }
    }

    // --- Step 3: Decode primary blind ---
    let blind_raw = std::slice::from_raw_parts(primary_blind_ptr, 32);
    let mut blind_bytes = [0u8; 32];
    blind_bytes.copy_from_slice(blind_raw);
    let primary_blind = match Option::from(pallas::Base::from_repr(blind_bytes)) {
        Some(fp) => fp,
        None => return -3,
    };

    // --- Step 4: Decode revealed share coordinates ---
    let c1_raw = std::slice::from_raw_parts(enc_c1_x_ptr, 32);
    let mut c1_bytes = [0u8; 32];
    c1_bytes.copy_from_slice(c1_raw);
    c1_bytes[31] &= 0x7F; // clear sign bit
    let enc_c1_x = match Option::from(pallas::Base::from_repr(c1_bytes)) {
        Some(fp) => fp,
        None => return -3,
    };

    let c2_raw = std::slice::from_raw_parts(enc_c2_x_ptr, 32);
    let mut c2_bytes = [0u8; 32];
    c2_bytes.copy_from_slice(c2_raw);
    c2_bytes[31] &= 0x7F;
    let enc_c2_x = match Option::from(pallas::Base::from_repr(c2_bytes)) {
        Some(fp) => fp,
        None => return -3,
    };

    // --- Step 5: Convert round_id to Fp via wide reduction ---
    let round_id_raw = std::slice::from_raw_parts(round_id_ptr, 32);
    let mut round_id_bytes = [0u8; 32];
    round_id_bytes.copy_from_slice(round_id_raw);
    let voting_round_id = hash_bytes_to_fp(round_id_bytes);

    // --- Step 6: Build circuit and instance ---
    let proposal_id_fp = pallas::Base::from(u64::from(proposal_id));
    let vote_decision_fp = pallas::Base::from(u64::from(vote_decision));

    let bundle = share_reveal::builder::build_share_reveal(
        auth_path,
        position,
        share_comms,
        primary_blind,
        enc_c1_x,
        enc_c2_x,
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
/// Returns (merkle_path, share_comms_flat, primary_blind, enc_c1_x, enc_c2_x,
///          share_index, proposal_id, vote_decision, round_id).
pub fn build_share_reveal_test_data()
    -> (Vec<u8>, [u8; 512], [u8; 32], [u8; 32], [u8; 32], u32, u32, u32, [u8; 32])
{
    use pasta_curves::group::ff::PrimeField;
    use pasta_curves::pallas;

    let proposal_id: u32 = 3;
    let vote_decision: u32 = 1;
    let share_index: u32 = 0;
    let round_id = [0u8; 32];

    // Synthetic blind factors.
    let share_blinds: [pallas::Base; 16] = core::array::from_fn(|i| {
        pallas::Base::from(1001u64 + i as u64)
    });

    // Synthetic x-coordinates for encrypted shares.
    let mut all_c1_x = [pallas::Base::zero(); 16];
    let mut all_c2_x = [pallas::Base::zero(); 16];
    for i in 0..16u64 {
        all_c1_x[i as usize] = pallas::Base::from(100 + i);
        all_c2_x[i as usize] = pallas::Base::from(200 + i);
    }

    // Compute share commitments and shares_hash.
    let share_comms: [pallas::Base; 16] = core::array::from_fn(|i| {
        voting_circuits::vote_proof::share_commitment(share_blinds[i], all_c1_x[i], all_c2_x[i])
    });
    let shares_hash_fp = voting_circuits::shares_hash::shares_hash_from_comms(share_comms);

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
    let path_vec = unsafe {
        votetree::compute_path_from_raw(vc_bytes.as_ptr(), 1, 0)
            .expect("path for single-leaf tree must succeed")
    };
    assert_eq!(path_vec.len(), votetree::MERKLE_PATH_BYTES);
    let mut path_buf = [0u8; votetree::MERKLE_PATH_BYTES];
    path_buf.copy_from_slice(&path_vec);

    // Flatten share_comms: 16 × 32 bytes = 512 bytes.
    let mut comms_flat = [0u8; 512];
    for i in 0..16 {
        let bytes = share_comms[i].to_repr();
        comms_flat[i * 32..(i + 1) * 32].copy_from_slice(&bytes);
    }

    let primary_blind_bytes: [u8; 32] = share_blinds[share_index as usize].to_repr();
    let enc_c1_x_bytes: [u8; 32] = all_c1_x[share_index as usize].to_repr();
    let enc_c2_x_bytes: [u8; 32] = all_c2_x[share_index as usize].to_repr();

    (
        path_buf.to_vec(),
        comms_flat,
        primary_blind_bytes,
        enc_c1_x_bytes,
        enc_c2_x_bytes,
        share_index,
        proposal_id,
        vote_decision,
        round_id,
    )
}

// ---------------------------------------------------------------------------
// Vote commitment tree — stateful handle (incremental per-block appends)
// ---------------------------------------------------------------------------

/// C function pointer types for the KV store callbacks.
pub type ZallyKvGetFn = unsafe extern "C" fn(
    ctx: *mut std::os::raw::c_void,
    key: *const u8,
    key_len: usize,
    out_val: *mut *mut u8,
    out_val_len: *mut usize,
) -> i32;

pub type ZallyKvSetFn = unsafe extern "C" fn(
    ctx: *mut std::os::raw::c_void,
    key: *const u8,
    key_len: usize,
    val: *const u8,
    val_len: usize,
) -> i32;

pub type ZallyKvDeleteFn = unsafe extern "C" fn(
    ctx: *mut std::os::raw::c_void,
    key: *const u8,
    key_len: usize,
) -> i32;

pub type ZallyKvIterCreateFn = unsafe extern "C" fn(
    ctx: *mut std::os::raw::c_void,
    prefix: *const u8,
    prefix_len: usize,
    reverse: u8,
) -> *mut std::os::raw::c_void;

pub type ZallyKvIterNextFn = unsafe extern "C" fn(
    iter: *mut std::os::raw::c_void,
    out_key: *mut *mut u8,
    out_key_len: *mut usize,
    out_val: *mut *mut u8,
    out_val_len: *mut usize,
) -> i32;

pub type ZallyKvIterFreeFn =
    unsafe extern "C" fn(iter: *mut std::os::raw::c_void);

pub type ZallyKvFreeBufFn = unsafe extern "C" fn(ptr: *mut u8, len: usize);

/// Create a KV-backed stateful vote commitment tree handle.
///
/// The handle reads and writes shards, the cap, and checkpoints directly
/// through the provided Go KV callbacks. No leaf replay on cold start —
/// `ShardTree` lazily loads only the data it needs.
///
/// `next_position` must be `CommitmentTreeState.NextIndex` (0 on first boot).
///
/// The caller owns the returned pointer and must free it with
/// [`zally_vote_tree_free`].
///
/// # Safety
/// All function pointers must remain valid for the lifetime of the handle.
/// `ctx` must point to a stable Go `KvStoreProxy`; it is updated each block
/// by Go before any tree call.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_create_with_kv(
    ctx: *mut std::os::raw::c_void,
    get_fn: ZallyKvGetFn,
    set_fn: ZallyKvSetFn,
    delete_fn: ZallyKvDeleteFn,
    iter_create_fn: ZallyKvIterCreateFn,
    iter_next_fn: ZallyKvIterNextFn,
    iter_free_fn: ZallyKvIterFreeFn,
    free_buf_fn: ZallyKvFreeBufFn,
    next_position: u64,
) -> *mut votetree::TreeHandle {
    use vote_commitment_tree::kv_shard_store::KvCallbacks;
    let cb = KvCallbacks {
        ctx,
        get: get_fn,
        set: set_fn,
        delete: delete_fn,
        iter_create: iter_create_fn,
        iter_next: iter_next_fn,
        iter_free: iter_free_fn,
        free_buf: free_buf_fn,
    };
    let handle = votetree::TreeHandle::new_with_kv(cb, next_position);
    Box::into_raw(handle)
}

/// Free a tree handle previously created by [`zally_vote_tree_create`].
///
/// # Safety
/// `handle` must be a pointer returned by [`zally_vote_tree_create`] and
/// must not have been freed before.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_free(handle: *mut votetree::TreeHandle) {
    if !handle.is_null() {
        drop(Box::from_raw(handle));
    }
}

/// Append a batch of leaves to a stateful tree handle.
///
/// # Arguments
/// * `handle`      - Pointer returned by [`zally_vote_tree_create_with_kv`].
/// * `leaves_ptr`  - Pointer to a flat byte array of leaves (each 32 bytes LE Fp).
/// * `leaf_count`  - Number of leaves.
///
/// # Returns
/// * `0`  on success.
/// * `-1` if `handle` is null, or `leaf_count > 0` and `leaves_ptr` is null.
/// * `-3` if a leaf contains a non-canonical field element encoding.
/// * `-4` if the KV store or ShardTree returned a storage error.
///
/// # Safety
/// Caller must ensure `handle` is valid and `leaves_ptr` is valid for
/// `leaf_count * 32` bytes.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_append_batch(
    handle: *mut votetree::TreeHandle,
    leaves_ptr: *const u8,
    leaf_count: usize,
) -> i32 {
    if handle.is_null() {
        return -1;
    }
    let h = &mut *handle;
    match h.append_batch_raw(leaves_ptr, leaf_count) {
        Ok(()) => 0,
        Err(votetree::FfiError::InvalidInput) => -1,
        Err(votetree::FfiError::PositionOutOfRange) => -2,
        Err(votetree::FfiError::Deserialization) => -3,
        Err(votetree::FfiError::Storage) => -4,
    }
}

/// Append `count` leaves starting at `cursor` directly from the Cosmos KV
/// store, skipping the Go-side leaf fetch loop.
///
/// Each leaf is read from key `0x02 || cursor+i as u64 BE` (the
/// `CommitmentLeafKey` format from `types/keys.go`). This eliminates the
/// `newLeaves` allocation and per-leaf KV read loop previously performed in
/// Go's `ensureTreeLoaded`.
///
/// # Arguments
/// * `handle` - Pointer returned by [`zally_vote_tree_create_with_kv`].
/// * `cursor` - Index of the first leaf to append (= current `treeCursor`).
/// * `count`  - Number of leaves to append (= `nextIndex - treeCursor`).
///
/// # Returns
/// * `0`  on success.
/// * `-1` if `handle` is null.
/// * `-4` if a leaf is missing, malformed, or the KV store returned an error.
///
/// # Safety
/// `handle` must be a valid pointer returned by [`zally_vote_tree_create_with_kv`].
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_append_from_kv(
    handle: *mut votetree::TreeHandle,
    cursor: u64,
    count: u64,
) -> i32 {
    if handle.is_null() {
        return -1;
    }
    match (*handle).append_from_kv(cursor, count) {
        Ok(()) => 0,
        Err(_) => -4,
    }
}

/// Snapshot the current tree state at `height` (block height).
///
/// Must be called after appending all leaves for a block so that
/// `root_stateful` and `path_stateful` queries work for that height.
///
/// # Returns
/// * `0`  on success.
/// * `-1` if `handle` is null.
/// * `-4` if the checkpoint failed (non-monotonic height or KV storage error).
///
/// # Safety
/// `handle` must be a valid pointer returned by [`zally_vote_tree_create_with_kv`].
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_checkpoint(
    handle: *mut votetree::TreeHandle,
    height: u32,
) -> i32 {
    if handle.is_null() {
        return -1;
    }
    match (*handle).checkpoint(height) {
        Ok(()) => 0,
        Err(_) => -4,
    }
}

/// Return the 32-byte Merkle root at the latest checkpoint.
///
/// # Arguments
/// * `handle`   - Pointer returned by [`zally_vote_tree_create`].
/// * `root_out` - Pointer to a 32-byte output buffer.
///
/// # Returns
/// * `0`  on success (root written to `root_out`).
/// * `-1` if `handle` or `root_out` is null.
///
/// # Safety
/// Caller must ensure `handle` is valid and `root_out` has room for 32 bytes.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_root_stateful(
    handle: *const votetree::TreeHandle,
    root_out: *mut u8,
) -> i32 {
    if handle.is_null() || root_out.is_null() {
        return -1;
    }
    let root = (*handle).root();
    std::ptr::copy_nonoverlapping(root.as_ptr(), root_out, 32);
    0
}


/// Return the number of leaves appended to the stateful handle so far.
///
/// # Safety
/// `handle` must be a valid pointer returned by [`zally_vote_tree_create`].
/// Returns 0 for a null pointer.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_size(handle: *const votetree::TreeHandle) -> u64 {
    if handle.is_null() {
        return 0;
    }
    (*handle).size()
}

/// Compute the Poseidon Merkle authentication path for `position` at `height`
/// using the stateful tree handle.
///
/// # Arguments
/// * `handle`   - Pointer returned by [`zally_vote_tree_create`].
/// * `position` - Leaf index for which to generate the path.
/// * `height`   - Checkpoint height to use as anchor.
/// * `path_out` - Pointer to a [`MERKLE_PATH_BYTES`]-byte output buffer.
///
/// # Returns
/// * `0`  on success (path written to `path_out`).
/// * `-1` if `handle` or `path_out` is null.
/// * `-2` if `position` is out of range or `height` has no checkpoint.
///
/// # Safety
/// Caller must ensure `handle` is valid and `path_out` has room for
/// [`votetree::MERKLE_PATH_BYTES`] bytes.
#[no_mangle]
pub unsafe extern "C" fn zally_vote_tree_path_stateful(
    handle: *const votetree::TreeHandle,
    position: u64,
    height: u32,
    path_out: *mut u8,
) -> i32 {
    if handle.is_null() || path_out.is_null() {
        return -1;
    }
    match (*handle).path(position, height) {
        Some(bytes) => {
            std::ptr::copy_nonoverlapping(bytes.as_ptr(), path_out, bytes.len());
            0
        }
        None => -2,
    }
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
            share_comms,
            primary_blind,
            enc_c1_x,
            enc_c2_x,
            share_index,
            proposal_id,
            vote_decision,
            round_id,
        ) = build_share_reveal_test_data();

        let mut proof_buf = [0u8; 8192];
        let mut proof_len: usize = 0;
        let mut nullifier = [0u8; 32];
        let mut tree_root = [0u8; 32];

        let rc = unsafe {
            zally_generate_share_reveal(
                merkle_path.as_ptr(),
                merkle_path.len(),
                share_comms.as_ptr(),
                share_comms.len(),
                primary_blind.as_ptr(),
                enc_c1_x.as_ptr(),
                enc_c2_x.as_ptr(),
                share_index,
                proposal_id,
                vote_decision,
                round_id.as_ptr(),
                round_id.len(),
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

        // Slot 1: enc_share_c1_x
        public_inputs[32..64].copy_from_slice(&enc_c1_x);
        public_inputs[63] &= 0x7F; // clear sign bit

        // Slot 2: enc_share_c2_x
        public_inputs[64..96].copy_from_slice(&enc_c2_x);
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
