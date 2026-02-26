//! Share Reveal bundle builder.
//!
//! Constructs the [`Circuit`] and [`Instance`] from high-level inputs
//! (Merkle path, share commitments, vote metadata). The builder computes
//! all derived values (shares_hash, vote_commitment, share_nullifier,
//! tree root) so the caller only provides raw witness data.

use halo2_proofs::circuit::Value;
use pasta_curves::pallas;

use crate::shared_primitives::shares_hash::shares_hash_from_comms;
use crate::vote_proof::{
    poseidon_hash_2, vote_commitment_hash as compute_vote_commitment_hash, VOTE_COMM_TREE_DEPTH,
};

use super::circuit::{share_nullifier_hash, Circuit, Instance};

/// Complete share reveal bundle: circuit + public inputs.
#[derive(Clone, Debug)]
pub struct ShareRevealBundle {
    /// The share reveal circuit with all witnesses populated.
    pub circuit: Circuit,
    /// Public inputs (7 field elements).
    pub instance: Instance,
}

/// Build a share reveal bundle from high-level inputs.
///
/// # Arguments
///
/// - `merkle_auth_path`: The 24 sibling hashes from the vote commitment tree.
/// - `merkle_position`: Leaf position in the vote commitment tree.
/// - `share_comms`: Pre-computed per-share Poseidon commitments
///   (`share_comm_i = Poseidon(blind_i, c1_i_x, c2_i_x)`).
/// - `primary_blind`: Blind factor for the revealed share (at `share_index`).
/// - `enc_c1_x`: X-coordinate of the revealed share's El Gamal C1.
/// - `enc_c2_x`: X-coordinate of the revealed share's El Gamal C2.
/// - `share_index`: Which of the 16 shares is being revealed (0..15).
/// - `proposal_id`: Proposal identifier (as a field element).
/// - `vote_decision`: The voter's choice (as a field element).
/// - `voting_round_id`: Voting round identifier (as a field element).
#[allow(clippy::too_many_arguments)]
pub fn build_share_reveal(
    merkle_auth_path: [pallas::Base; VOTE_COMM_TREE_DEPTH],
    merkle_position: u32,
    share_comms: [pallas::Base; 16],
    primary_blind: pallas::Base,
    enc_c1_x: pallas::Base,
    enc_c2_x: pallas::Base,
    share_index: u32,
    proposal_id: pallas::Base,
    vote_decision: pallas::Base,
    voting_round_id: pallas::Base,
) -> ShareRevealBundle {
    let shares_hash = shares_hash_from_comms(share_comms);

    let vote_commitment = compute_vote_commitment_hash(shares_hash, proposal_id, vote_decision);

    let vote_comm_tree_root = {
        let mut current = vote_commitment;
        for (i, sibling) in merkle_auth_path.iter().enumerate().take(VOTE_COMM_TREE_DEPTH) {
            let bit = (merkle_position >> i) & 1;
            let (left, right) = if bit == 0 {
                (current, *sibling)
            } else {
                (*sibling, current)
            };
            current = poseidon_hash_2(left, right);
        }
        current
    };

    let share_index_fp = pallas::Base::from(share_index as u64);
    let share_nullifier = share_nullifier_hash(
        vote_commitment,
        share_index_fp,
        enc_c1_x,
        enc_c2_x,
        voting_round_id,
    );

    let circuit = Circuit {
        vote_comm_tree_path: Value::known(merkle_auth_path),
        vote_comm_tree_position: Value::known(merkle_position),
        share_comms: share_comms.map(Value::known),
        primary_blind: Value::known(primary_blind),
        share_index: Value::known(share_index_fp),
        vote_commitment: Value::known(vote_commitment),
    };

    let instance = Instance::from_parts(
        share_nullifier,
        enc_c1_x,
        enc_c2_x,
        proposal_id,
        vote_decision,
        vote_comm_tree_root,
        voting_round_id,
    );

    ShareRevealBundle { circuit, instance }
}

#[cfg(test)]
mod tests {
    use super::*;
    use halo2_proofs::dev::MockProver;
    use pasta_curves::pallas;

    use crate::vote_proof::{elgamal_encrypt, share_commitment, spend_auth_g_affine};

    use super::super::circuit::K;

    #[test]
    fn test_builder_round_trip() {
        let ea_sk = pallas::Scalar::from(42u64);
        let g = pallas::Point::from(spend_auth_g_affine());
        let ea_pk = g * ea_sk;

        let shares: [u64; 16] = [625; 16];
        let randomness: [pallas::Base; 16] = core::array::from_fn(|i| {
            pallas::Base::from((i as u64 + 1) * 101)
        });
        let share_blinds: [pallas::Base; 16] = core::array::from_fn(|i| {
            pallas::Base::from(1001u64 + i as u64)
        });
        let mut c1_x = [pallas::Base::zero(); 16];
        let mut c2_x = [pallas::Base::zero(); 16];
        for i in 0..16 {
            let (c1, c2) = elgamal_encrypt(pallas::Base::from(shares[i]), randomness[i], ea_pk);
            c1_x[i] = c1;
            c2_x[i] = c2;
        }

        let share_comms: [pallas::Base; 16] = core::array::from_fn(|i| {
            share_commitment(share_blinds[i], c1_x[i], c2_x[i])
        });

        let mut empty_roots = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
        empty_roots[0] = poseidon_hash_2(pallas::Base::zero(), pallas::Base::zero());
        for i in 1..VOTE_COMM_TREE_DEPTH {
            empty_roots[i] = poseidon_hash_2(empty_roots[i - 1], empty_roots[i - 1]);
        }

        let share_idx: u32 = 2;
        let bundle = build_share_reveal(
            empty_roots,
            0,
            share_comms,
            share_blinds[share_idx as usize],
            c1_x[share_idx as usize],
            c2_x[share_idx as usize],
            share_idx,
            pallas::Base::from(3u64),
            pallas::Base::from(1u64),
            pallas::Base::from(999u64),
        );

        let prover = MockProver::run(K, &bundle.circuit, vec![bundle.instance.to_halo2_instance()])
            .unwrap();
        assert_eq!(prover.verify(), Ok(()));
    }
}
