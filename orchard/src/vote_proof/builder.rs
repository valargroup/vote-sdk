//! Vote proof builder (ZKP #2).
//!
//! Constructs a vote proof from delegation key material, a vote commitment
//! tree witness, and vote parameters. Lives inside the orchard crate to
//! access `pub(crate)` key internals.

use alloc::format;
use alloc::string::String;
use alloc::vec::Vec;

use ff::Field;
use group::{Curve, GroupEncoding};
use halo2_proofs::circuit::Value;
use pasta_curves::{arithmetic::CurveAffine, pallas};
use rand::RngCore;

use crate::keys::{FullViewingKey, Scope, SpendAuthorizingKey, SpendingKey};

use super::circuit::{
    base_to_scalar, elgamal_encrypt, shares_hash, van_integrity_hash, van_nullifier_hash,
    vote_commitment_hash, Circuit, Instance, VOTE_COMM_TREE_DEPTH,
};
use super::prove::create_vote_proof;

/// Maximum proposal authority bitmask for a fresh VAN (16 proposals).
const MAX_PROPOSAL_AUTHORITY: u64 = 65535; // 2^16 - 1

/// Result of building a vote proof.
#[derive(Debug)]
pub struct VoteProofBundle {
    /// Serialized Halo2 proof bytes.
    pub proof: Vec<u8>,
    /// Public inputs for the proof.
    pub instance: Instance,
}

/// Errors that can occur during vote proof construction.
#[derive(Debug)]
pub enum VoteProofBuildError {
    /// A share randomness value could not be converted to a scalar.
    InvalidRandomness(String),
    /// The total note value cannot be split into valid shares.
    InvalidShares(String),
}

impl core::fmt::Display for VoteProofBuildError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            VoteProofBuildError::InvalidRandomness(msg) => {
                write!(f, "invalid randomness: {}", msg)
            }
            VoteProofBuildError::InvalidShares(msg) => {
                write!(f, "invalid shares: {}", msg)
            }
        }
    }
}

/// Extract the voting spending key scalar from a SpendingKey.
///
/// This replicates the sign-correction logic from `SpendAuthorizingKey::from`:
/// `ask = PRF_expand(sk)`, then negate if the resulting ak has ỹ = 1.
fn extract_vsk(sk: &SpendingKey) -> pallas::Scalar {
    use super::circuit::spend_auth_g_affine;

    let ask_raw = SpendAuthorizingKey::derive_inner(sk);
    let g = pallas::Point::from(spend_auth_g_affine());
    let ak_point = (g * ask_raw).to_affine();
    let ak_bytes = ak_point.to_bytes();

    // If the sign bit of ak is 1, the real ask was negated.
    if (ak_bytes.as_ref()[31] >> 7) == 1 {
        -ask_raw
    } else {
        ask_raw
    }
}

/// Generate a valid El Gamal randomness value (must be < scalar field modulus).
fn random_valid_base_as_scalar(rng: &mut impl RngCore) -> pallas::Base {
    loop {
        let r = pallas::Base::random(&mut *rng);
        if base_to_scalar(r).is_some() {
            return r;
        }
    }
}

/// Build a real vote proof (ZKP #2) from delegation key material.
///
/// This function constructs the full vote proof circuit, computes all
/// public inputs, and generates a Halo2 proof.
///
/// # Arguments
///
/// * `sk` - The SpendingKey used during delegation (ZKP #1).
/// * `address_index` - The diversifier index of the output recipient
///   address used in delegation (typically 1).
/// * `total_note_value` - Sum of delegated note values (e.g. 15_000_000).
/// * `gov_comm_rand` - The blinding factor used for the VAN in delegation.
/// * `voting_round_id` - The vote round identifier (Pallas base field element).
/// * `vote_comm_tree_path` - Merkle authentication path (24 siblings) for
///   the VAN in the vote commitment tree.
/// * `vote_comm_tree_position` - Leaf position of the VAN in the tree.
/// * `anchor_height` - The block height at which the tree was snapshotted
///   (must match the on-chain commitment tree root).
/// * `proposal_id` - Which proposal to vote on (0-indexed, must be < 16).
/// * `vote_decision` - The voter's choice.
/// * `ea_pk` - Election authority public key (Pallas affine point from session).
/// * `rng` - Random number generator for El Gamal encryption randomness.
///
/// **Expensive**: K=14 proof generation takes ~30-60 seconds in release mode.
#[allow(clippy::too_many_arguments)]
pub fn build_vote_proof_from_delegation(
    sk: &SpendingKey,
    address_index: u32,
    total_note_value: u64,
    gov_comm_rand: pallas::Base,
    voting_round_id: pallas::Base,
    vote_comm_tree_path: [pallas::Base; VOTE_COMM_TREE_DEPTH],
    vote_comm_tree_position: u32,
    anchor_height: u32,
    proposal_id: u64,
    vote_decision: u64,
    ea_pk: pallas::Affine,
    rng: &mut impl RngCore,
) -> Result<VoteProofBundle, VoteProofBuildError> {
    // ---- Key derivation (matches delegation's key hierarchy) ----

    let vsk = extract_vsk(sk);
    let fvk: FullViewingKey = sk.into();
    let vsk_nk = fvk.nk().inner();
    let rivk_v = fvk.rivk(Scope::External).inner();

    let address = fvk.address_at(address_index, Scope::External);
    let vpk_g_d_affine = address.g_d().to_affine();
    let vpk_pk_d_affine = address.pk_d().inner().to_affine();

    let vpk_g_d_x = *vpk_g_d_affine.coordinates().unwrap().x();
    let vpk_pk_d_x = *vpk_pk_d_affine.coordinates().unwrap().x();

    // ---- Fast key-chain consistency checks (instant, no circuit) ----
    {
        use super::circuit::spend_auth_g_affine;
        use core::iter;
        use group::ff::PrimeFieldBits;
        use halo2_gadgets::sinsemilla::primitives::CommitDomain;
        use crate::constants::{fixed_bases::COMMIT_IVK_PERSONALIZATION, L_ORCHARD_BASE};

        // Check 1: [vsk] * SpendAuthG must match the ak from the FullViewingKey.
        let ak_from_vsk = (pallas::Point::from(spend_auth_g_affine()) * vsk).to_affine();
        let fvk_bytes = fvk.to_bytes();
        let ak_from_fvk_bytes: [u8; 32] = fvk_bytes[0..32].try_into().unwrap();
        let ak_from_fvk: pallas::Affine = {
            let opt: Option<pallas::Point> = pallas::Point::from_bytes(&ak_from_fvk_bytes).into();
            opt.expect("ak from fvk must be a valid point").to_affine()
        };
        assert_eq!(
            ak_from_vsk, ak_from_fvk,
            "extract_vsk bug: [vsk]*SpendAuthG != ak from FullViewingKey"
        );

        // Check 2: CommitIvk(ak_x, nk, rivk) must produce an ivk where [ivk]*g_d == pk_d.
        let ak_x = *ak_from_vsk.coordinates().unwrap().x();
        let domain = CommitDomain::new(COMMIT_IVK_PERSONALIZATION);
        let ivk = domain
            .short_commit(
                iter::empty()
                    .chain(ak_x.to_le_bits().iter().by_vals().take(L_ORCHARD_BASE))
                    .chain(vsk_nk.to_le_bits().iter().by_vals().take(L_ORCHARD_BASE)),
                &rivk_v,
            )
            .expect("CommitIvk must not produce bottom");
        let ivk_scalar = super::circuit::base_to_scalar(ivk)
            .expect("ivk must be convertible to scalar");
        let pk_d_derived = (pallas::Point::from(vpk_g_d_affine) * ivk_scalar).to_affine();
        assert_eq!(
            pk_d_derived, vpk_pk_d_affine,
            "CommitIvk chain mismatch: [ivk]*g_d != pk_d from address"
        );

        std::eprintln!("[BUILDER] key-chain consistency checks passed");
    }

    // ---- Proposal authority ----

    let proposal_authority_old = pallas::Base::from(MAX_PROPOSAL_AUTHORITY);
    let one_shifted = pallas::Base::from(1u64 << proposal_id);
    let proposal_authority_new = proposal_authority_old - one_shifted;

    // ---- VAN integrity hashes ----

    let total_note_value_base = pallas::Base::from(total_note_value);

    let vote_authority_note_old = van_integrity_hash(
        vpk_g_d_x,
        vpk_pk_d_x,
        total_note_value_base,
        voting_round_id,
        proposal_authority_old,
        gov_comm_rand,
    );

    let van_nullifier =
        van_nullifier_hash(vsk_nk, voting_round_id, vote_authority_note_old);

    let vote_authority_note_new = van_integrity_hash(
        vpk_g_d_x,
        vpk_pk_d_x,
        total_note_value_base,
        voting_round_id,
        proposal_authority_new,
        gov_comm_rand,
    );

    // ---- Shares (split total_note_value into 4 parts) ----
    // Each share must be in [0, 2^30) for the range check.

    let quarter = total_note_value / 4;
    let remainder = total_note_value - quarter * 3;
    let shares_u64: [u64; 4] = [quarter, quarter, quarter, remainder];

    // Verify all shares are in range
    for (i, &s) in shares_u64.iter().enumerate() {
        if s >= (1u64 << 30) {
            return Err(VoteProofBuildError::InvalidShares(format!(
                "share {} = {} exceeds 2^30",
                i, s
            )));
        }
    }

    let shares_base: [pallas::Base; 4] = [
        pallas::Base::from(shares_u64[0]),
        pallas::Base::from(shares_u64[1]),
        pallas::Base::from(shares_u64[2]),
        pallas::Base::from(shares_u64[3]),
    ];

    // ---- El Gamal encryption of shares ----

    let ea_pk_point = pallas::Point::from(ea_pk);
    let ea_pk_x = *ea_pk.coordinates().unwrap().x();
    let ea_pk_y = *ea_pk.coordinates().unwrap().y();

    let mut enc_c1_x = [pallas::Base::zero(); 4];
    let mut enc_c2_x = [pallas::Base::zero(); 4];
    let mut share_randomness = [pallas::Base::zero(); 4];

    for i in 0..4 {
        let r = random_valid_base_as_scalar(rng);
        share_randomness[i] = r;
        let (c1, c2) = elgamal_encrypt(shares_base[i], r, ea_pk_point);
        enc_c1_x[i] = c1;
        enc_c2_x[i] = c2;
    }

    let shares_hash_val = shares_hash(enc_c1_x, enc_c2_x);

    // ---- Vote commitment ----

    let proposal_id_base = pallas::Base::from(proposal_id);
    let vote_decision_base = pallas::Base::from(vote_decision);
    let vote_commitment =
        vote_commitment_hash(shares_hash_val, proposal_id_base, vote_decision_base);

    // ---- Vote commitment tree root (from auth path) ----
    // Recompute the root from the leaf + auth path to set as public input.

    let vote_comm_tree_root = {
        use super::circuit::poseidon_hash_2;

        let mut current = vote_authority_note_old;
        for level in 0..VOTE_COMM_TREE_DEPTH {
            let sibling = vote_comm_tree_path[level];
            if vote_comm_tree_position & (1 << level) == 0 {
                current = poseidon_hash_2(current, sibling);
            } else {
                current = poseidon_hash_2(sibling, current);
            }
        }
        current
    };

    // ---- Build circuit ----

    let mut circuit = Circuit::with_van_witnesses(
        Value::known(vote_comm_tree_path),
        Value::known(vote_comm_tree_position),
        Value::known(vpk_g_d_affine),
        Value::known(vpk_pk_d_affine),
        Value::known(total_note_value_base),
        Value::known(proposal_authority_old),
        Value::known(gov_comm_rand),
        Value::known(vote_authority_note_old),
        Value::known(vsk),
        Value::known(rivk_v),
        Value::known(vsk_nk),
    );
    circuit.one_shifted = Value::known(one_shifted);
    circuit.shares = [
        Value::known(shares_base[0]),
        Value::known(shares_base[1]),
        Value::known(shares_base[2]),
        Value::known(shares_base[3]),
    ];
    circuit.enc_share_c1_x = enc_c1_x.map(Value::known);
    circuit.enc_share_c2_x = enc_c2_x.map(Value::known);
    circuit.share_randomness = share_randomness.map(Value::known);
    circuit.ea_pk = Value::known(ea_pk);
    circuit.vote_decision = Value::known(vote_decision_base);

    // ---- Build instance (public inputs) ----

    let anchor_height_base = pallas::Base::from(u64::from(anchor_height));
    let instance = Instance::from_parts(
        van_nullifier,
        vote_authority_note_new,
        vote_commitment,
        vote_comm_tree_root,
        anchor_height_base,
        proposal_id_base,
        voting_round_id,
        ea_pk_x,
        ea_pk_y,
    );

    // ---- MockProver check ----

    {
        use halo2_proofs::dev::MockProver;
        let mock_circuit = circuit.clone();
        let prover = MockProver::run(
            super::circuit::K,
            &mock_circuit,
            vec![instance.to_halo2_instance()],
        )
        .expect("MockProver::run should not fail");

        if let Err(failures) = prover.verify() {
            return Err(VoteProofBuildError::InvalidShares(format!(
                "circuit constraints not satisfied: {} failure(s): {:?}",
                failures.len(),
                failures,
            )));
        }
        std::eprintln!("[BUILDER] MockProver passed");
    }

    // ---- Generate proof ----

    let proof = create_vote_proof(circuit, &instance);

    Ok(VoteProofBundle { proof, instance })
}
