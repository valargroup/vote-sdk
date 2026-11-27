//! Test-only provers for the proxy delegation end-to-end test.
//!
//! Compiled only with the `test-provers` feature, which
//! `make circuits-test-provers` builds in its own target directory, so the
//! production library never carries them.
//! Each prover takes its witness and the 772-byte Merkle path that
//! `votetree.TreeHandle.Path` reads from the chain's tree, so its proof is
//! made against the tree the chain really has. ZKP3 needs no test prover: the
//! helper's `sv_generate_share_reveal` already takes such a path.
//!
//! Every function returns 0 on success, -1 for a null pointer or a wrong
//! length, -3 for an input that does not decode and -5 when building, proving
//! or signing fails, with the reason in `sv_last_error`. The spending key is
//! `SpendingKey::from_bytes(seed)` at address index 1, as the fixtures use.

use voting_circuits::proxy_delegation::{
    build_proxy_delegation_proof, create_proxy_delegation_proof, ShareLayout,
};
use voting_circuits::vote_proof::{
    build_vote_proof_from_delegation, derive_vote_authority_transition, EncryptedShareOutput,
};
use voting_circuits::{MAX_PROPOSAL_AUTHORITY, VOTE_COMM_TREE_DEPTH};
use voting_crypto_deps::orchard::keys::SpendingKey;
use voting_crypto_deps::pasta_curves::group::{ff::PrimeField, Curve, Group, GroupEncoding};
use voting_crypto_deps::pasta_curves::pallas;
use voting_crypto_deps::reddsa::{orchard::SpendAuth, SigningKey, VerificationKey};

use crate::ffi::set_ffi_error;
use crate::votetree::MERKLE_PATH_BYTES;

/// Zatoshi per ballot, the weight unit of a VAN.
const BALLOT_ZATOSHI: u64 = 12_500_000;

/// Length of one share record in a prover's output: C1 and C2 compressed, the
/// commitment blind and the share commitment, 32 bytes each, then the share's
/// plaintext as a little-endian u64.
pub const SHARE_RECORD_LEN: usize = 4 * 32 + 8;

/// Length of [`sv_test_prove_proxy_delegation`]'s output: van_nullifier,
/// r_vpk, vote_authority_note_new, the DC, the tree root, dc_slot_nullifier
/// and rsk, 32 bytes each, then 16 share records.
pub const PROXY_DELEGATION_OUT_LEN: usize = 7 * 32 + 16 * SHARE_RECORD_LEN;

/// Length of [`sv_test_prove_vote`]'s output: van_nullifier, r_vpk,
/// vote_authority_note_new, the vote commitment, the tree root and rsk, 32
/// bytes each, then 16 share records.
pub const VOTE_OUT_LEN: usize = 6 * 32 + 16 * SHARE_RECORD_LEN;

/// A failed step: its FFI return code and the message for `sv_last_error`.
type Failure = (i32, String);

/// Runs `f` behind a panic guard and maps its outcome to an FFI code, storing
/// any failure's message, prefixed with `name`, for `sv_last_error`.
fn run(name: &str, f: impl FnOnce() -> Result<(), Failure>) -> i32 {
    match std::panic::catch_unwind(std::panic::AssertUnwindSafe(f)) {
        Ok(Ok(())) => 0,
        Ok(Err((code, msg))) => {
            set_ffi_error(format!("{name}: {msg}"));
            code
        }
        Err(_) => {
            set_ffi_error(format!("{name}: internal panic"));
            -5
        }
    }
}

/// Copies the 32 bytes at `ptr`.
///
/// # Safety
/// `ptr` must be valid for 32 bytes.
unsafe fn read32(ptr: *const u8) -> [u8; 32] {
    std::ptr::read_unaligned(ptr as *const [u8; 32])
}

/// Decodes a canonical Pallas base field element.
fn base(bytes: [u8; 32], name: &str) -> Result<pallas::Base, Failure> {
    Option::from(pallas::Base::from_repr(bytes))
        .ok_or_else(|| (-3, format!("{name} is not a canonical Pallas Fp element")))
}

/// Decodes a compressed Pallas point that is not the identity.
fn point(bytes: [u8; 32], name: &str) -> Result<pallas::Affine, Failure> {
    let point: Option<pallas::Point> = pallas::Point::from_bytes(&bytes).into();
    match point {
        Some(point) if !bool::from(point.is_identity()) => Ok(point.to_affine()),
        _ => Err((
            -3,
            format!("{name} is not a compressed non-identity Pallas point"),
        )),
    }
}

/// Derives the spending key of the test identity `seed`.
fn spending_key(seed: [u8; 32]) -> Result<SpendingKey, Failure> {
    Option::from(SpendingKey::from_bytes(seed))
        .ok_or_else(|| (-3, "the key seed is not a valid spending key".to_owned()))
}

/// Decodes a `MERKLE_PATH_BYTES` Merkle path: the leaf position as a
/// little-endian u32, then the 24 siblings from the leaf level up.
fn decode_merkle_path(raw: &[u8]) -> Result<([pallas::Base; VOTE_COMM_TREE_DEPTH], u32), Failure> {
    let position = u32::from_le_bytes(raw[..4].try_into().expect("4 bytes"));
    let mut siblings = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
    for (level, sibling) in siblings.iter_mut().enumerate() {
        let start = 4 + level * 32;
        *sibling = base(
            raw[start..start + 32].try_into().expect("32 bytes"),
            "a Merkle path sibling",
        )?;
    }
    Ok((siblings, position))
}

/// Returns `rsk = vsk + alpha`, the key that signs for `r_vpk = [vsk + alpha] G`,
/// with `vsk` derived as voting-circuits' `extract_vsk` derives it, and checks
/// that it verifies as `r_vpk`.
fn randomized_key(
    sk: &SpendingKey,
    alpha: pallas::Scalar,
    r_vpk: &[u8; 32],
) -> Result<[u8; 32], Failure> {
    let vsk = voting_crypto_deps::spend_authorizing_scalar(sk);
    let rsk = (vsk + alpha).to_repr();
    let key = SigningKey::<SpendAuth>::try_from(rsk)
        .map_err(|err| (-5, format!("rsk is not a signing key: {err}")))?;
    if <[u8; 32]>::from(VerificationKey::from(&key)) != *r_vpk {
        return Err((-5, "rsk does not verify as r_vpk".to_owned()));
    }
    Ok(rsk)
}

/// Writes the 16 share records of [`SHARE_RECORD_LEN`] bytes to `out`.
fn write_shares(
    out: &mut [u8],
    shares: &[EncryptedShareOutput; 16],
    blinds: &[pallas::Base; 16],
    comms: &[pallas::Base; 16],
) {
    let (records, _) = out.as_chunks_mut::<SHARE_RECORD_LEN>();
    for (i, record) in records.iter_mut().enumerate() {
        record[..32].copy_from_slice(&shares[i].c1);
        record[32..64].copy_from_slice(&shares[i].c2);
        record[64..96].copy_from_slice(&blinds[i].to_repr());
        record[96..128].copy_from_slice(&comms[i].to_repr());
        record[128..].copy_from_slice(&shares[i].plaintext_value.to_le_bytes());
    }
}

/// Copies `proof` to the caller's buffer and stores its length.
///
/// # Safety
/// `out` must be valid for `capacity` bytes and `len_out` for one write.
unsafe fn write_proof(
    proof: &[u8],
    out: *mut u8,
    capacity: usize,
    len_out: *mut usize,
) -> Result<(), Failure> {
    if proof.len() > capacity {
        return Err((
            -5,
            format!("the {}-byte proof exceeds {capacity} bytes", proof.len()),
        ));
    }
    std::ptr::copy_nonoverlapping(proof.as_ptr(), out, proof.len());
    *len_out = proof.len();
    Ok(())
}

/// Writes the full-authority VAN commitment, the `van_cmx` a ZKP1
/// registration appends, of identity `key_seed` holding `van_ballots` ballots
/// with randomness `van_comm_rand` in round `round_id` (both canonical Fp).
///
/// # Safety
/// The three inputs must be valid for 32 bytes and `van_out` for 32 writes.
#[no_mangle]
pub unsafe extern "C" fn sv_test_van_commitment(
    key_seed: *const u8,
    van_ballots: u64,
    van_comm_rand: *const u8,
    round_id: *const u8,
    van_out: *mut u8,
) -> i32 {
    if key_seed.is_null() || van_comm_rand.is_null() || round_id.is_null() || van_out.is_null() {
        set_ffi_error("sv_test_van_commitment: null pointer");
        return -1;
    }
    run("sv_test_van_commitment", || {
        let sk = spending_key(read32(key_seed))?;
        let van = derive_vote_authority_transition(
            &sk,
            1,
            van_ballots * BALLOT_ZATOSHI,
            base(read32(van_comm_rand), "van_comm_rand")?,
            base(read32(round_id), "round_id")?,
            1,
            MAX_PROPOSAL_AUTHORITY,
        )
        .map_err(|err| (-5, err.to_string()))?
        .vote_authority_note_old;
        std::ptr::copy_nonoverlapping(van.to_repr().as_ptr(), van_out, 32);
        Ok(())
    })
}

/// Builds and proves one ZKP4 delegation of `delegated_ballots` of a
/// full-authority VAN's `van_ballots` to delegate `delegate_index`, as
/// voting-circuits' `build_proxy_delegation_proof` documents its arguments,
/// with `alpha_v = alpha` and the single-share layout if `single_share`. The
/// VAN is the leaf at the path's position; for a later DC of a batch, pass the
/// single-leaf path of its predecessor's successor VAN.
///
/// Writes the proof to `proof_out` and [`PROXY_DELEGATION_OUT_LEN`] bytes to
/// `out`, laid out as that constant documents.
///
/// # Safety
/// `key_seed`, `van_comm_rand`, `round_id`, `ea_pk` and `dc_seed` must be valid
/// for 32 bytes, `merkle_path` for `merkle_path_len`, `proof_out` for
/// `proof_out_capacity`, `proof_len_out` for one write and `out` for `out_len`.
#[no_mangle]
pub unsafe extern "C" fn sv_test_prove_proxy_delegation(
    key_seed: *const u8,
    van_ballots: u64,
    van_comm_rand: *const u8,
    round_id: *const u8,
    merkle_path: *const u8,
    merkle_path_len: usize,
    anchor_height: u64,
    ea_pk: *const u8,
    alpha: u64,
    delegate_index: u32,
    delegated_ballots: u64,
    dc_slot: u8,
    dc_seed: *const u8,
    single_share: bool,
    proof_out: *mut u8,
    proof_out_capacity: usize,
    proof_len_out: *mut usize,
    out: *mut u8,
    out_len: usize,
) -> i32 {
    if key_seed.is_null()
        || van_comm_rand.is_null()
        || round_id.is_null()
        || merkle_path.is_null()
        || ea_pk.is_null()
        || dc_seed.is_null()
        || proof_out.is_null()
        || proof_len_out.is_null()
        || out.is_null()
        || merkle_path_len != MERKLE_PATH_BYTES
        || out_len != PROXY_DELEGATION_OUT_LEN
    {
        set_ffi_error("sv_test_prove_proxy_delegation: null pointer or wrong length");
        return -1;
    }
    run("sv_test_prove_proxy_delegation", || {
        let sk = spending_key(read32(key_seed))?;
        let (path, position) =
            decode_merkle_path(std::slice::from_raw_parts(merkle_path, merkle_path_len))?;
        let anchor_height = u32::try_from(anchor_height)
            .map_err(|_| (-1, format!("anchor height {anchor_height} exceeds u32")))?;
        let alpha = pallas::Scalar::from(alpha);
        let bundle = build_proxy_delegation_proof(
            &sk,
            1,
            van_ballots,
            base(read32(van_comm_rand), "van_comm_rand")?,
            base(read32(round_id), "round_id")?,
            path,
            position,
            anchor_height,
            point(read32(ea_pk), "ea_pk")?,
            alpha,
            delegate_index,
            delegated_ballots,
            dc_slot,
            read32(dc_seed),
            if single_share {
                ShareLayout::SingleShare
            } else {
                ShareLayout::Standard
            },
        )
        .map_err(|err| (-5, err.to_string()))?;
        let rsk = randomized_key(&sk, alpha, &bundle.r_vpk_bytes)?;
        let proof = create_proxy_delegation_proof(bundle.circuit.clone(), &bundle.instance)
            .map_err(|err| (-5, format!("proving failed: {err}")))?;
        write_proof(&proof, proof_out, proof_out_capacity, proof_len_out)?;

        let out = std::slice::from_raw_parts_mut(out, out_len);
        let instance = &bundle.instance;
        for (slot, value) in [
            instance.van_nullifier.to_repr(),
            bundle.r_vpk_bytes,
            instance.vote_authority_note_new.to_repr(),
            instance.delegation_commitment.to_repr(),
            instance.vote_comm_tree_root.to_repr(),
            instance.dc_slot_nullifier.to_repr(),
            rsk,
        ]
        .iter()
        .enumerate()
        {
            out[slot * 32..(slot + 1) * 32].copy_from_slice(value);
        }
        write_shares(
            &mut out[7 * 32..],
            &bundle.encrypted_shares,
            &bundle.share_blinds,
            &bundle.share_comms,
        );
        Ok(())
    })
}

/// Builds and proves one ZKP2 vote for `vote_decision` on `proposal_id` by a
/// VAN holding `van_ballots` ballots and `proposal_authority`, as
/// voting-circuits' `build_vote_proof_from_delegation` documents its
/// arguments, with `alpha_v = alpha` and the standard share layout. The VAN is
/// the leaf at the path's position; for a later vote of a 0x06 batch, pass the
/// single-leaf path of its predecessor's new VAN.
///
/// Writes the proof to `proof_out` and [`VOTE_OUT_LEN`] bytes to `out`, laid
/// out as that constant documents.
///
/// # Safety
/// `key_seed`, `van_comm_rand`, `round_id` and `ea_pk` must be valid for 32
/// bytes, `merkle_path` for `merkle_path_len`, `proof_out` for
/// `proof_out_capacity`, `proof_len_out` for one write and `out` for `out_len`.
#[no_mangle]
pub unsafe extern "C" fn sv_test_prove_vote(
    key_seed: *const u8,
    van_ballots: u64,
    van_comm_rand: *const u8,
    round_id: *const u8,
    merkle_path: *const u8,
    merkle_path_len: usize,
    anchor_height: u64,
    proposal_id: u32,
    vote_decision: u32,
    proposal_authority: u64,
    ea_pk: *const u8,
    alpha: u64,
    proof_out: *mut u8,
    proof_out_capacity: usize,
    proof_len_out: *mut usize,
    out: *mut u8,
    out_len: usize,
) -> i32 {
    if key_seed.is_null()
        || van_comm_rand.is_null()
        || round_id.is_null()
        || merkle_path.is_null()
        || ea_pk.is_null()
        || proof_out.is_null()
        || proof_len_out.is_null()
        || out.is_null()
        || merkle_path_len != MERKLE_PATH_BYTES
        || out_len != VOTE_OUT_LEN
    {
        set_ffi_error("sv_test_prove_vote: null pointer or wrong length");
        return -1;
    }
    run("sv_test_prove_vote", || {
        let sk = spending_key(read32(key_seed))?;
        let (path, position) =
            decode_merkle_path(std::slice::from_raw_parts(merkle_path, merkle_path_len))?;
        let anchor_height = u32::try_from(anchor_height)
            .map_err(|_| (-1, format!("anchor height {anchor_height} exceeds u32")))?;
        let alpha = pallas::Scalar::from(alpha);
        let bundle = build_vote_proof_from_delegation(
            &sk,
            1,
            van_ballots * BALLOT_ZATOSHI,
            base(read32(van_comm_rand), "van_comm_rand")?,
            base(read32(round_id), "round_id")?,
            path,
            position,
            anchor_height,
            u64::from(proposal_id),
            u64::from(vote_decision),
            point(read32(ea_pk), "ea_pk")?,
            alpha,
            proposal_authority,
            false,
        )
        .map_err(|err| (-5, err.to_string()))?;
        let rsk = randomized_key(&sk, alpha, &bundle.r_vpk_bytes)?;
        write_proof(&bundle.proof, proof_out, proof_out_capacity, proof_len_out)?;

        let out = std::slice::from_raw_parts_mut(out, out_len);
        let instance = &bundle.instance;
        for (slot, value) in [
            instance.van_nullifier.to_repr(),
            bundle.r_vpk_bytes,
            instance.vote_authority_note_new.to_repr(),
            instance.vote_commitment.to_repr(),
            instance.vote_comm_tree_root.to_repr(),
            rsk,
        ]
        .iter()
        .enumerate()
        {
            out[slot * 32..(slot + 1) * 32].copy_from_slice(value);
        }
        write_shares(
            &mut out[6 * 32..],
            &bundle.encrypted_shares,
            &bundle.share_blinds,
            &bundle.share_comms,
        );
        Ok(())
    })
}

/// Signs `msg` with the RedPallas SpendAuth key whose scalar is `sk` (32
/// bytes, canonical), and writes its verification key to `vk_out` and the
/// 64-byte signature to `sig_out`. A prover's rsk signs for its r_vpk.
///
/// # Safety
/// `sk` must be valid for 32 bytes, `msg` for `msg_len` (it may be null when
/// `msg_len` is 0), `vk_out` for 32 writes and `sig_out` for 64.
#[no_mangle]
pub unsafe extern "C" fn sv_test_redpallas_sign(
    sk: *const u8,
    msg: *const u8,
    msg_len: usize,
    vk_out: *mut u8,
    sig_out: *mut u8,
) -> i32 {
    if sk.is_null() || (msg.is_null() && msg_len > 0) || vk_out.is_null() || sig_out.is_null() {
        set_ffi_error("sv_test_redpallas_sign: null pointer");
        return -1;
    }
    run("sv_test_redpallas_sign", || {
        let key = SigningKey::<SpendAuth>::try_from(read32(sk))
            .map_err(|err| (-3, format!("sk is not a signing key: {err}")))?;
        let msg = if msg_len == 0 {
            &[]
        } else {
            std::slice::from_raw_parts(msg, msg_len)
        };
        let sig: [u8; 64] = key.sign(voting_crypto_deps::rand::rngs::OsRng, msg).into();
        let vk: [u8; 32] = VerificationKey::from(&key).into();
        std::ptr::copy_nonoverlapping(vk.as_ptr(), vk_out, 32);
        std::ptr::copy_nonoverlapping(sig.as_ptr(), sig_out, 64);
        Ok(())
    })
}
