//! Integration test: prove non-membership with a real NullifierTree and verify
//! through the delegation circuit using MockProver.
//!
//! This bridges `imt_tree::NullifierTree` to orchard's `ImtProvider` trait,
//! then uses `build_delegation_bundle()` + `MockProver` to verify the proof
//! end-to-end in the ZK circuit.

use ff::{Field, PrimeField};
use halo2_proofs::dev::MockProver;
use incrementalmerkletree::{Hashable, Level};
use pasta_curves::pallas;
use rand::rngs::OsRng;

use imt_tree::{build_sentinel_tree, NullifierTree};

use orchard::{
    delegation::{
        builder::{build_delegation_bundle, RealNoteInput},
        imt::{ImtProofData as OrchardImtProofData, ImtProvider},
    },
    keys::{FullViewingKey, Scope, SpendingKey},
    note::{ExtractedNoteCommitment, Note, Nullifier, Rho},
    tree::{MerkleHashOrchard, MerklePath},
    value::NoteValue,
    NOTE_COMMITMENT_TREE_DEPTH,
};

// ── Adapter: bridges NullifierTree to orchard's ImtProvider trait ──────

/// Wraps a `NullifierTree` to implement `ImtProvider`.
struct NullifierTreeAdapter<'a>(&'a NullifierTree);

impl ImtProvider for NullifierTreeAdapter<'_> {
    fn root(&self) -> pallas::Base {
        // NullifierTree::root() returns pasta_curves::Fp = pallas::Base.
        self.0.root()
    }

    fn non_membership_proof(&self, nf: pallas::Base) -> OrchardImtProofData {
        let proof = self.0.prove(nf).expect("nullifier should be in a gap range");
        OrchardImtProofData {
            root: proof.root,
            low: proof.low,
            high: proof.high,
            leaf_pos: proof.leaf_pos,
            path: proof.path,
        }
    }
}

// ── Helpers ────────────────────────────────────────────────────────────

/// Convert a `MerkleHashOrchard` to `pallas::Base` via byte roundtrip.
///
/// `MerkleHashOrchard::inner()` is pub(crate), so we go through bytes.
fn merkle_hash_to_base(h: MerkleHashOrchard) -> pallas::Base {
    pallas::Base::from_repr(h.to_bytes()).unwrap()
}

/// Convert a `Nullifier` to `pallas::Base` via byte roundtrip.
fn nullifier_to_base(nf: Nullifier) -> pallas::Base {
    pallas::Base::from_repr(nf.to_bytes()).unwrap()
}

/// Merged circuit K value (must match orchard's delegation circuit).
const K: u32 = 14;

/// Build a note commitment tree with up to 4 notes, returning
/// `(inputs, nc_root)` suitable for `build_delegation_bundle`.
///
/// Follows the same pattern as `make_real_note_inputs` in orchard's builder tests.
fn make_real_note_inputs(
    fvk: &FullViewingKey,
    values: &[u64],
    imt_provider: &impl ImtProvider,
    rng: &mut impl rand::RngCore,
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
    // nc_root is the full 32-level Orchard note commitment tree root.
    let mut current = l2_0;
    for level in 2..NOTE_COMMITMENT_TREE_DEPTH {
        let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
        current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
    }
    let nc_root = merkle_hash_to_base(current);

    // Build Merkle paths and RealNoteInputs.
    // MerklePath requires NOTE_COMMITMENT_TREE_DEPTH (32) elements.
    let l1 = [l1_0, l1_1];
    let mut inputs = Vec::with_capacity(n);
    for (i, note) in notes.into_iter().enumerate() {
        let mut auth_path = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];
        // Level 0: sibling leaf in the same pair.
        auth_path[0] = leaves[i ^ 1];
        // Level 1: sibling pair hash.
        auth_path[1] = l1[1 - (i >> 1)];
        // Levels 2..31: empty subtree roots.
        for level in 2..NOTE_COMMITMENT_TREE_DEPTH {
            auth_path[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
        }
        let merkle_path = MerklePath::from_parts(i as u32, auth_path);

        let real_nf = note.nullifier(fvk);
        let nf_base = nullifier_to_base(real_nf);
        let imt_proof = imt_provider.non_membership_proof(nf_base);

        inputs.push(RealNoteInput {
            note,
            fvk: fvk.clone(),
            merkle_path,
            imt_proof,
        });
    }

    (inputs, nc_root)
}

// ── Tests ──────────────────────────────────────────────────────────────

/// End-to-end test: build a sentinel `NullifierTree`, adapt it as an `ImtProvider`,
/// construct a delegation bundle with a single real note, and verify the merged
/// circuit with `MockProver`.
#[test]
fn imt_proof_from_nullifier_tree_verifies_in_circuit() {
    let mut rng = OsRng;

    // 1. Build the sentinel nullifier tree with a couple of extra nullifiers.
    let tree = build_sentinel_tree(&[
        pallas::Base::from(12345u64),
        pallas::Base::from(67890u64),
    ])
    .unwrap();
    let adapter = NullifierTreeAdapter(&tree);

    // 2. Create keys.
    let sk = SpendingKey::random(&mut rng);
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let vote_round_id = pallas::Base::random(&mut rng);
    let gov_comm_rand = pallas::Base::random(&mut rng);
    let alpha = pallas::Scalar::random(&mut rng);

    // 3. Build a single real note with value >= 12,500,000 (the min weight).
    let (inputs, nc_root) = make_real_note_inputs(&fvk, &[13_000_000], &adapter, &mut rng);

    // 4. Build the delegation bundle.
    let bundle = build_delegation_bundle(
        inputs,
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        gov_comm_rand,
        &adapter,
        &mut rng,
    )
    .expect("build_delegation_bundle should succeed");

    // 5. Verify with MockProver.
    let pi = bundle.instance.to_halo2_instance();
    let prover = MockProver::run(K, &bundle.circuit, vec![pi]).unwrap();
    assert_eq!(
        prover.verify(),
        Ok(()),
        "delegation circuit with real NullifierTree IMT proofs should verify"
    );
}

/// Same test with 4 real notes.
#[test]
fn four_notes_with_nullifier_tree_verify_in_circuit() {
    let mut rng = OsRng;

    let tree = build_sentinel_tree(&[
        pallas::Base::from(111u64),
        pallas::Base::from(222u64),
        pallas::Base::from(333u64),
    ])
    .unwrap();
    let adapter = NullifierTreeAdapter(&tree);

    let sk = SpendingKey::random(&mut rng);
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let vote_round_id = pallas::Base::random(&mut rng);
    let gov_comm_rand = pallas::Base::random(&mut rng);
    let alpha = pallas::Scalar::random(&mut rng);

    // 4 notes x 3,200,000 = 12,800,000 >= 12,500,000.
    let (inputs, nc_root) = make_real_note_inputs(
        &fvk,
        &[3_200_000, 3_200_000, 3_200_000, 3_200_000],
        &adapter,
        &mut rng,
    );

    let bundle = build_delegation_bundle(
        inputs,
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        gov_comm_rand,
        &adapter,
        &mut rng,
    )
    .expect("build_delegation_bundle should succeed");

    let pi = bundle.instance.to_halo2_instance();
    let prover = MockProver::run(K, &bundle.circuit, vec![pi]).unwrap();
    assert_eq!(
        prover.verify(),
        Ok(()),
        "4-note delegation circuit with real NullifierTree IMT proofs should verify"
    );
}
