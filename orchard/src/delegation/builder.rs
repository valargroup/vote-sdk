//! Multi-note delegation bundle builder.
//!
//! Orchestrates the creation of a complete delegation proof:
//! a single merged circuit proving all 16 conditions for up to 4 notes.
//! Handles padding unused note slots with zero-value notes that still carry
//! valid IMT non-membership proofs against the real tree root.

use alloc::vec::Vec;
use group::Curve;
use halo2_proofs::circuit::Value;
use pasta_curves::{arithmetic::CurveAffine, pallas};
use rand::RngCore;

use crate::{
    keys::{FullViewingKey, Scope, SpendValidatingKey},
    note::{commitment::ExtractedNoteCommitment, nullifier::Nullifier, Note, Rho},
    spec::NonIdentityPallasPoint,
    tree::MerklePath,
    value::NoteValue,
};

use super::{
    circuit::{self, gov_commitment_hash, rho_binding_hash, NoteSlotWitness},
    imt::{gov_null_hash, ImtProofData, ImtProvider, IMT_DEPTH},
};

/// Input for a single real note in the delegation.
#[derive(Debug)]
pub struct RealNoteInput {
    /// The note being delegated.
    pub note: Note,
    /// The note's full viewing key.
    pub fvk: FullViewingKey,
    /// Merkle authentication path for the note commitment.
    pub merkle_path: MerklePath,
    /// IMT non-membership proof for this note's nullifier.
    pub imt_proof: ImtProofData,
}

/// Complete delegation bundle: a single circuit proving all 16 conditions.
#[derive(Debug)]
pub struct DelegationBundle {
    /// The merged delegation circuit.
    pub circuit: circuit::Circuit,
    /// Public inputs (12 field elements).
    pub instance: circuit::Instance,
}

/// Errors from delegation bundle construction.
#[derive(Clone, Debug)]
pub enum DelegationBuildError {
    /// Must have 1–4 real notes.
    InvalidNoteCount(usize),
}

impl std::fmt::Display for DelegationBuildError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            DelegationBuildError::InvalidNoteCount(n) => {
                write!(f, "invalid note count: {} (expected 1–4)", n)
            }
        }
    }
}

/// Build a complete delegation bundle with 1–4 real notes and padding.
///
/// # Arguments
///
/// - `real_notes`: 1–4 real notes with their keys, Merkle paths, and IMT proofs.
/// - `fvk`: The delegator's full viewing key (shared across all real notes).
/// - `alpha`: Spend auth randomizer for the keystone signature.
/// - `output_recipient`: Address of the voting hotkey (output note recipient).
/// - `vote_round_id`: Voting round identifier.
/// - `nc_root`: Note commitment tree root (shared anchor).
/// - `gov_comm_rand`: Blinding factor for the governance commitment.
/// - `imt_provider`: Provider for padded-note IMT non-membership proofs.
/// - `rng`: Random number generator.
#[allow(clippy::too_many_arguments)]
pub fn build_delegation_bundle(
    real_notes: Vec<RealNoteInput>,
    fvk: &FullViewingKey,
    alpha: pallas::Scalar,
    output_recipient: crate::Address,
    vote_round_id: pallas::Base,
    nc_root: pallas::Base,
    gov_comm_rand: pallas::Base,
    imt_provider: &impl ImtProvider,
    rng: &mut impl RngCore,
) -> Result<DelegationBundle, DelegationBuildError> {
    // The circuit supports 1–4 real notes; reject empty or oversized bundles.
    let n_real = real_notes.len();
    if n_real == 0 || n_real > 4 {
        return Err(DelegationBuildError::InvalidNoteCount(n_real));
    }

    // Snapshot the IMT root — all per-note non-membership proofs must be against this root.
    let nf_imt_root = imt_provider.root();

    // Derive key material.
    let nk_val = fvk.nk().inner();
    let ak: SpendValidatingKey = fvk.clone().into();

    // Collect per-note data.
    let mut note_slots = Vec::with_capacity(4);
    let mut cmx_values = Vec::with_capacity(4);
    let mut v_values = Vec::with_capacity(4);
    let mut gov_nulls = Vec::with_capacity(4);

    // Process real notes: derive psi/rcm from rseed, compute the note commitment,
    // real nullifier, and gov nullifier, then pack everything into a NoteSlotWitness.
    for input in &real_notes {
        let note = &input.note;
        let rho = note.rho();
        let psi = note.rseed().psi(&rho);
        let rcm = note.rseed().rcm(&rho);
        let cm = note.commitment();
        let cmx = ExtractedNoteCommitment::from(cm.clone()).inner();
        let v_raw = note.value().inner();
        let recipient = note.recipient();

        // Condition 12: real nullifier for IMT non-membership.
        let real_nf = note.nullifier(fvk);
        // Condition 14: governance nullifier = Poseidon(nk, domain_tag, vote_round_id, real_nf).
        let gov_null = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        let slot = NoteSlotWitness {
            g_d: Value::known(recipient.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&recipient.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(note.value()),
            rho: Value::known(rho.into_inner()),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(input.merkle_path.auth_path()[..IMT_DEPTH].try_into().unwrap()),
            pos: Value::known(input.merkle_path.position()),
            is_note_real: Value::known(true),
            imt_low: Value::known(input.imt_proof.low),
            imt_high: Value::known(input.imt_proof.high),
            imt_leaf_pos: Value::known(input.imt_proof.leaf_pos),
            imt_path: Value::known(input.imt_proof.path),
        };

        note_slots.push(slot);
        cmx_values.push(cmx);
        v_values.push(v_raw);
        gov_nulls.push(gov_null);
    }

    // Pad remaining slots to 4 with zero-value dummy notes (§1.3.5).
    // Padded notes use random rho/psi/rcm, v=0, and is_note_real=false.
    // The circuit still runs all constraints uniformly; condition 10 (Merkle path)
    // and condition 15 (v=0) are gated by is_note_real.
    for i in n_real..4 {
        // Use a high diversifier index to avoid collision with real notes.
        let pad_addr = fvk.address_at((1000 + i) as u32, Scope::External);
        let (_, _, dummy) = Note::dummy(&mut *rng, None);
        let pad_note = Note::new(
            pad_addr,
            NoteValue::zero(),
            Rho::from_nf_old(dummy.nullifier(fvk)),
            &mut *rng,
        );

        let rho = pad_note.rho();
        let psi = pad_note.rseed().psi(&rho);
        let rcm = pad_note.rseed().rcm(&rho);
        let cm = pad_note.commitment();
        let cmx = ExtractedNoteCommitment::from(cm.clone()).inner();

        let real_nf = pad_note.nullifier(fvk);
        let gov_null = gov_null_hash(nk_val, vote_round_id, real_nf.0);

        // Get IMT non-membership proof for this padded note's nullifier.
        let imt_proof = imt_provider.non_membership_proof(real_nf.0);

        // Merkle path: zeros (condition 10 is skipped for padded notes).
        let merkle_path = MerklePath::dummy(&mut *rng);

        let slot = NoteSlotWitness {
            g_d: Value::known(pad_addr.g_d()),
            pk_d: Value::known(
                NonIdentityPallasPoint::from_bytes(&pad_addr.pk_d().to_bytes()).unwrap(),
            ),
            v: Value::known(NoteValue::zero()),
            rho: Value::known(rho.into_inner()),
            psi: Value::known(psi),
            rcm: Value::known(rcm),
            cm: Value::known(cm),
            path: Value::known(merkle_path.auth_path()[..IMT_DEPTH].try_into().unwrap()),
            pos: Value::known(merkle_path.position()),
            is_note_real: Value::known(false),
            imt_low: Value::known(imt_proof.low),
            imt_high: Value::known(imt_proof.high),
            imt_leaf_pos: Value::known(imt_proof.leaf_pos),
            imt_path: Value::known(imt_proof.path),
        };

        note_slots.push(slot);
        cmx_values.push(cmx);
        v_values.push(0);
        gov_nulls.push(gov_null);
    }

    let notes: [NoteSlotWitness; 4] = note_slots.try_into().unwrap_or_else(|_| unreachable!());

    // Condition 7: gov commitment integrity.
    // gov_comm = Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, v_total,
    //                     vote_round_id, MAX_PROPOSAL_AUTHORITY, gov_comm_rand)
    // Extract the output address as two x-coordinates (vpk representation).
    let v_total = pallas::Base::from(v_values.iter().sum::<u64>());

    let g_d_new_x = *output_recipient
        .g_d()
        .to_affine()
        .coordinates()
        .unwrap()
        .x();
    let pk_d_new_x = *output_recipient
        .pk_d()
        .inner()
        .to_affine()
        .coordinates()
        .unwrap()
        .x();

    let gov_comm = gov_commitment_hash(g_d_new_x, pk_d_new_x, v_total, vote_round_id, gov_comm_rand);

    // Condition 3: rho binding.
    // rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id)
    // Binds the keystone note to the exact notes being delegated.
    let rho = rho_binding_hash(
        cmx_values[0],
        cmx_values[1],
        cmx_values[2],
        cmx_values[3],
        gov_comm,
        vote_round_id,
    );

    // Construct the keystone (signed) note (§1.3.4).
    // This is a zero-value dummy note whose rho is bound to the delegation via condition 3.
    let sender_address = fvk.address_at(0u32, Scope::External);
    let signed_note = Note::new(
        sender_address,
        NoteValue::zero(),
        Rho::from_nf_old(Nullifier(rho)),
        &mut *rng,
    );

    // Condition 2: nullifier integrity — nf_signed is a public input.
    let nf_signed = signed_note.nullifier(fvk);

    // Condition 6: output note commitment integrity.
    // The output note is sent to the voting hotkey address with rho = nf_signed.
    let output_note = Note::new(
        output_recipient,
        NoteValue::zero(),
        Rho::from_nf_old(nf_signed),
        &mut *rng,
    );
    let cmx_new = ExtractedNoteCommitment::from(output_note.commitment()).inner();

    // Condition 4: spend authority — rk is the randomized spend key.
    let rk = ak.randomize(&alpha);

    // Assemble the circuit (private witnesses) and instance (public inputs).
    // The caller runs keygen + create_proof on the circuit, then submits
    // the proof + instance to the vote chain. The verifier only needs
    // the instance, proof, and verification key.
    let circuit = circuit::Circuit::from_note_unchecked(fvk, &signed_note, alpha)
        .with_output_note(&output_note)
        .with_notes(notes)
        .with_gov_comm_rand(gov_comm_rand);

    let instance = circuit::Instance::from_parts(
        nf_signed,
        rk,
        cmx_new,
        gov_comm,
        vote_round_id,
        nc_root,
        nf_imt_root,
        [gov_nulls[0], gov_nulls[1], gov_nulls[2], gov_nulls[3]],
    );

    Ok(DelegationBundle { circuit, instance })
}

// ================================================================
// Test-only
// ================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        constants::MERKLE_DEPTH_ORCHARD,
        delegation::imt::{SpacedLeafImtProvider, IMT_DEPTH},
        keys::{FullViewingKey, Scope, SpendingKey},
        note::{commitment::ExtractedNoteCommitment, Note, Rho},
        tree::{MerkleHashOrchard, MerklePath},
        value::NoteValue,
    };
    use ff::Field;
    use halo2_proofs::dev::MockProver;
    use incrementalmerkletree::{Hashable, Level};
    use pasta_curves::pallas;
    use rand::rngs::OsRng;

    /// Merged circuit K value.
    const K: u32 = 14;

    /// Helper: create 1–4 real note inputs with a shared Merkle tree and anchor.
    ///
    /// Notes are placed at positions 0..n in the commitment tree. Returns
    /// `(inputs, nc_root)` where `nc_root` is the shared anchor.
    fn make_real_note_inputs(
        fvk: &FullViewingKey,
        values: &[u64],
        imt_provider: &impl ImtProvider,
        rng: &mut impl RngCore,
    ) -> (Vec<RealNoteInput>, pallas::Base) {
        let n = values.len();
        assert!(n >= 1 && n <= 4);

        // Create notes.
        let mut notes = Vec::with_capacity(n);
        for &v in values {
            let recipient = fvk.address_at(0u32, Scope::External);
            let note_value = NoteValue::from_raw(v);
            let (_, _, dummy_parent) = Note::dummy(&mut *rng, None);
            let note = Note::new(
                recipient,
                note_value,
                Rho::from_nf_old(dummy_parent.nullifier(fvk)),
                &mut *rng,
            );
            notes.push(note);
        }

        // Extract leaf hashes, padding to 4 with empty leaves.
        let empty_leaf = MerkleHashOrchard::empty_leaf();
        let mut leaves = [empty_leaf; 4];
        for (i, note) in notes.iter().enumerate() {
            let cmx = ExtractedNoteCommitment::from(note.commitment());
            leaves[i] = MerkleHashOrchard::from_cmx(&cmx);
        }

        // Build the bottom two levels of the shared tree.
        let l1_0 = MerkleHashOrchard::combine(Level::from(0), &leaves[0], &leaves[1]);
        let l1_1 = MerkleHashOrchard::combine(Level::from(0), &leaves[2], &leaves[3]);
        let l2_0 = MerkleHashOrchard::combine(Level::from(1), &l1_0, &l1_1);

        // Hash up through the remaining levels with empty subtree siblings.
        // The delegation circuit uses IMT_DEPTH (29) levels for the note commitment
        // Merkle path, so nc_root must be the level-29 root.
        let mut current = l2_0;
        for level in 2..IMT_DEPTH {
            let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
            current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
        }
        let nc_root = current.inner();

        // Build Merkle paths and RealNoteInputs.
        // MerklePath requires MERKLE_DEPTH_ORCHARD (32) elements, but the
        // delegation builder only uses the first IMT_DEPTH (29).
        let l1 = [l1_0, l1_1];
        let mut inputs = Vec::with_capacity(n);
        for (i, note) in notes.into_iter().enumerate() {
            let mut auth_path = [MerkleHashOrchard::empty_leaf(); MERKLE_DEPTH_ORCHARD];
            // Level 0: sibling leaf in the same pair.
            auth_path[0] = leaves[i ^ 1];
            // Level 1: sibling pair hash.
            auth_path[1] = l1[1 - (i >> 1)];
            // Levels 2..IMT_DEPTH-1: empty subtree roots.
            for level in 2..IMT_DEPTH {
                auth_path[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
            }
            // Levels IMT_DEPTH..MERKLE_DEPTH_ORCHARD: unused by delegation circuit.
            let merkle_path = MerklePath::from_parts(i as u32, auth_path);

            let real_nf = note.nullifier(fvk);
            let imt_proof = imt_provider.non_membership_proof(real_nf.0);

            inputs.push(RealNoteInput {
                note,
                fvk: fvk.clone(),
                merkle_path,
                imt_proof,
            });
        }

        (inputs, nc_root)
    }

    /// Helper: build a bundle from values and verify the merged circuit with MockProver.
    fn build_and_verify(values: &[u64]) -> DelegationBundle {
        let mut rng = OsRng;
        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let output_recipient = fvk.address_at(1u32, Scope::External);
        let vote_round_id = pallas::Base::random(&mut rng);
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let alpha = pallas::Scalar::random(&mut rng);

        let imt = SpacedLeafImtProvider::new();
        let (inputs, nc_root) = make_real_note_inputs(&fvk, values, &imt, &mut rng);

        let bundle = build_delegation_bundle(
            inputs,
            &fvk,
            alpha,
            output_recipient,
            vote_round_id,
            nc_root,
            gov_comm_rand,
            &imt,
            &mut rng,
        )
        .unwrap();

        // Verify merged circuit.
        let pi = bundle.instance.to_halo2_instance();
        let prover = MockProver::run(K, &bundle.circuit, vec![pi]).unwrap();
        assert_eq!(prover.verify(), Ok(()), "merged circuit failed");

        bundle
    }

    #[test]
    fn test_single_real_note() {
        build_and_verify(&[13_000_000]);
    }

    #[test]
    fn test_four_real_notes() {
        // 3,200,000 x 4 = 12,800,000 >= 12,500,000.
        build_and_verify(&[3_200_000, 3_200_000, 3_200_000, 3_200_000]);
    }

    #[test]
    fn test_two_real_notes() {
        build_and_verify(&[7_000_000, 7_000_000]);
    }

    #[test]
    fn test_min_weight_boundary() {
        // v_total = 12,500,000 exactly. Should pass.
        build_and_verify(&[12_500_000]);
    }

    #[test]
    fn test_below_min_weight() {
        // v_total < 12,500,000. Circuit should fail.
        let mut rng = OsRng;
        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let output_recipient = fvk.address_at(1u32, Scope::External);
        let vote_round_id = pallas::Base::random(&mut rng);
        let gov_comm_rand = pallas::Base::random(&mut rng);
        let alpha = pallas::Scalar::random(&mut rng);

        let imt = SpacedLeafImtProvider::new();
        let (inputs, nc_root) = make_real_note_inputs(&fvk, &[12_499_999], &imt, &mut rng);

        let bundle = build_delegation_bundle(
            inputs,
            &fvk,
            alpha,
            output_recipient,
            vote_round_id,
            nc_root,
            gov_comm_rand,
            &imt,
            &mut rng,
        )
        .unwrap();

        let pi = bundle.instance.to_halo2_instance();
        let prover = MockProver::run(K, &bundle.circuit, vec![pi]).unwrap();
        assert!(prover.verify().is_err(), "below min weight should fail");
    }

    #[test]
    fn test_zero_notes_error() {
        let mut rng = OsRng;
        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let output_recipient = fvk.address_at(1u32, Scope::External);
        let imt = SpacedLeafImtProvider::new();

        let result = build_delegation_bundle(
            vec![],
            &fvk,
            pallas::Scalar::random(&mut rng),
            output_recipient,
            pallas::Base::random(&mut rng),
            pallas::Base::random(&mut rng),
            pallas::Base::random(&mut rng),
            &imt,
            &mut rng,
        );

        assert!(matches!(
            result,
            Err(DelegationBuildError::InvalidNoteCount(0))
        ));
    }

    #[test]
    fn test_five_notes_error() {
        let mut rng = OsRng;
        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let output_recipient = fvk.address_at(1u32, Scope::External);
        let imt = SpacedLeafImtProvider::new();

        let (inputs, _) = make_real_note_inputs(
            &fvk,
            &[3_000_000, 3_000_000, 3_000_000, 3_000_000],
            &imt,
            &mut rng,
        );
        // Add a 5th note by extending.
        let mut inputs = inputs;
        let (extra, _) = make_real_note_inputs(&fvk, &[3_000_000], &imt, &mut rng);
        inputs.extend(extra);

        let result = build_delegation_bundle(
            inputs,
            &fvk,
            pallas::Scalar::random(&mut rng),
            output_recipient,
            pallas::Base::random(&mut rng),
            pallas::Base::random(&mut rng),
            pallas::Base::random(&mut rng),
            &imt,
            &mut rng,
        );

        assert!(matches!(
            result,
            Err(DelegationBuildError::InvalidNoteCount(5))
        ));
    }
}
