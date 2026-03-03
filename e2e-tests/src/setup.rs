//! Build a real delegation bundle for E2E tests (ZKP #1 + RedPallas).
//!
//! Generates session params with vote_end_time = now + configured window and a canonical
//! vote_round_id, then builds the delegation bundle and RedPallas signature
//! so the test can create the session and delegate without fixture files.
//! The window defaults to 3 minutes and can be overridden with
//! ZALLY_E2E_VOTE_WINDOW_SECS. This timestamp is part of vote_round_id, so it must
//! be chosen before proof generation starts.

use crate::payloads::{DelegationBundlePayload, SetupRoundFields};
use blake2b_simd::Params as Blake2bParams;
use ff::{Field, PrimeField};
use incrementalmerkletree::{Hashable, Level};
use orchard::{
    keys::{FullViewingKey, Scope, SpendAuthorizingKey, SpendingKey},
    note::{ExtractedNoteCommitment, Note, Rho},
    tree::{MerkleHashOrchard, MerklePath},
    value::NoteValue,
    NOTE_COMMITMENT_TREE_DEPTH,
};
use pasta_curves::pallas;
use rand::rngs::OsRng;
use voting_circuits::{
    delegation::{
        builder::{build_delegation_bundle, RealNoteInput},
        imt::{ImtProvider, SpacedLeafImtProvider},
        prove::{create_delegation_proof, verify_delegation_proof},
    },
};

const DEFAULT_E2E_VOTE_WINDOW_SECS: u64 = 180;
const MIN_E2E_VOTE_WINDOW_SECS: u64 = 120;

fn vote_window_secs() -> u64 {
    std::env::var("ZALLY_E2E_VOTE_WINDOW_SECS")
        .ok()
        .and_then(|raw| raw.parse::<u64>().ok())
        .map(|secs| secs.max(MIN_E2E_VOTE_WINDOW_SECS))
        .unwrap_or(DEFAULT_E2E_VOTE_WINDOW_SECS)
}

/// Data from delegation that the vote proof builder needs.
pub struct VoteProofDelegationData {
    /// The spending key used during delegation.
    pub sk: SpendingKey,
    /// Blinding factor for the VAN (van_comm).
    pub van_comm_rand: pallas::Base,
    /// Vote round identifier as a Pallas field element.
    pub vote_round_id: pallas::Base,
    /// Sum of delegated note values.
    pub total_note_value: u64,
    /// The VAN leaf value (van_comm) appended to the commitment tree.
    pub van_comm: pallas::Base,
    /// The cmx_new output note commitment from delegation (not appended to VCT).
    pub cmx_new: pallas::Base,
}

/// Build delegation bundle and session fields for the E2E test.
/// vote_end_time = now + vote_window_secs() where the default window is 3 min
/// (override with ZALLY_E2E_VOTE_WINDOW_SECS, clamped to >= 120s).
/// The round must stay ACTIVE through all submissions, then expire for auto-tally.
/// Returns payload for MsgDelegateVote, session fields for MsgCreateVotingSession,
/// and private witness data for building ZKP #2 (vote proof).
///
/// If `sk_override` is Some, uses that SpendingKey (e.g. derived from a hotkey seed
/// via ZIP-32, for testing the librustvoting path). Otherwise generates a random key.
pub fn build_delegation_bundle_for_test(
    sk_override: Option<SpendingKey>,
) -> Result<
    (
        DelegationBundlePayload,
        SetupRoundFields,
        VoteProofDelegationData,
    ),
    Box<dyn std::error::Error + Send + Sync>,
> {
    let mut rng = OsRng;

    let sk = sk_override.unwrap_or_else(|| SpendingKey::random(&mut rng));
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let alpha = pallas::Scalar::random(&mut rng);
    let van_comm_rand = pallas::Base::random(&mut rng);

    // Two notes with mixed scopes to exercise both External and Internal paths.
    let note_value_ext = 8_000_000u64;
    let note_value_int = 7_000_000u64;
    let total_note_value = note_value_ext + note_value_int;

    let recipient_ext = fvk.address_at(0u32, Scope::External);
    let recipient_int = fvk.address_at(0u32, Scope::Internal);

    let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
    let note_ext = Note::new(
        recipient_ext,
        NoteValue::from_raw(note_value_ext),
        Rho::from_nf_old(dummy_parent.nullifier(&fvk)),
        &mut rng,
    );
    let (_, _, dummy_parent2) = Note::dummy(&mut rng, None);
    let note_int = Note::new(
        recipient_int,
        NoteValue::from_raw(note_value_int),
        Rho::from_nf_old(dummy_parent2.nullifier(&fvk)),
        &mut rng,
    );

    let empty_leaf = MerkleHashOrchard::empty_leaf();
    let cmx_ext = ExtractedNoteCommitment::from(note_ext.commitment());
    let cmx_int = ExtractedNoteCommitment::from(note_int.commitment());
    let leaves = [
        MerkleHashOrchard::from_cmx(&cmx_ext),
        MerkleHashOrchard::from_cmx(&cmx_int),
        empty_leaf,
        empty_leaf,
    ];
    let l1_0 = MerkleHashOrchard::combine(Level::from(0), &leaves[0], &leaves[1]);
    let l1_1 = MerkleHashOrchard::combine(Level::from(0), &leaves[2], &leaves[3]);
    let l2_0 = MerkleHashOrchard::combine(Level::from(1), &l1_0, &l1_1);
    let mut current = l2_0;
    for level in 2..NOTE_COMMITMENT_TREE_DEPTH {
        let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
        current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
    }
    let nc_root_bytes = current.to_bytes();
    let nc_root: pallas::Base = pallas::Base::from_repr(nc_root_bytes).unwrap();

    let imt = SpacedLeafImtProvider::new();
    let nf_imt_root = imt.root();

    // Note 0 (External) at position 0: sibling is leaves[1], parent sibling is l1_1
    let mut auth_path_0 = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];
    auth_path_0[0] = leaves[1];
    auth_path_0[1] = l1_1;
    for level in 2..NOTE_COMMITMENT_TREE_DEPTH {
        auth_path_0[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
    }
    let merkle_path_0 = MerklePath::from_parts(0u32, auth_path_0);

    let nf_ext = note_ext.nullifier(&fvk);
    let nf_ext_fp: pallas::Base = pallas::Base::from_repr(nf_ext.to_bytes()).unwrap();
    let imt_proof_0 = imt.non_membership_proof(nf_ext_fp)?;

    // Note 1 (Internal) at position 1: sibling is leaves[0], parent sibling is l1_1
    let mut auth_path_1 = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];
    auth_path_1[0] = leaves[0];
    auth_path_1[1] = l1_1;
    for level in 2..NOTE_COMMITMENT_TREE_DEPTH {
        auth_path_1[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
    }
    let merkle_path_1 = MerklePath::from_parts(1u32, auth_path_1);

    let nf_int = note_int.nullifier(&fvk);
    let nf_int_fp: pallas::Base = pallas::Base::from_repr(nf_int.to_bytes()).unwrap();
    let imt_proof_1 = imt.non_membership_proof(nf_int_fp)?;

    let note_inputs = vec![
        RealNoteInput {
            note: note_ext,
            fvk: fvk.clone(),
            merkle_path: merkle_path_0,
            imt_proof: imt_proof_0,
            scope: Scope::External,
        },
        RealNoteInput {
            note: note_int,
            fvk: fvk.clone(),
            merkle_path: merkle_path_1,
            imt_proof: imt_proof_1,
            scope: Scope::Internal,
        },
    ];

    let snapshot_blockhash = [0xAAu8; 32];
    let proposals_hash = [0xBBu8; 32];
    let vote_end_time: u64 = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs()
        + vote_window_secs();

    let nc_root_repr = nc_root.to_repr();
    let nf_imt_root_repr = nf_imt_root.to_repr();

    let snapshot_height: u64 = 42_000;
    let round_id_fields = SetupRoundFields {
        snapshot_height,
        snapshot_blockhash,
        proposals_hash,
        vote_end_time,
        nullifier_imt_root: nf_imt_root_repr,
        nc_root: nc_root_repr,
    };
    let round_id_bytes = crate::payloads::derive_round_id(&round_id_fields);
    let vote_round_id: pallas::Base =
        pallas::Base::from_repr(round_id_bytes).expect("Poseidon output must be canonical Fp");

    let bundle = build_delegation_bundle(
        note_inputs,
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        van_comm_rand,
        &imt,
        &mut rng,
        None,
    )
    .map_err(|e| format!("build_delegation_bundle: {}", e))?;

    let proof = create_delegation_proof(bundle.circuit, &bundle.instance);
    verify_delegation_proof(&proof, &bundle.instance)
        .map_err(|e| format!("verify_delegation_proof: {}", e))?;

    let ask = SpendAuthorizingKey::from(&sk);
    let rsk = ask.randomize(&alpha);

    let rk_bytes: [u8; 32] = bundle.instance.rk.clone().into();
    let nf_signed_bytes = bundle.instance.nf_signed.to_bytes();
    let cmx_new_bytes = bundle.instance.cmx_new.to_repr();
    let van_cmx_bytes = bundle.instance.van_comm.to_repr();
    let gov_null_bytes: Vec<[u8; 32]> = bundle
        .instance
        .gov_null
        .iter()
        .map(|g| g.to_repr())
        .collect();
    // Sighash: in production this is the ZIP-244 sighash extracted from the
    // governance PCZT. The e2e test builds the bundle directly (no PCZT), so
    // we use a deterministic 32-byte value. The chain only checks
    // len(sighash)==32 and verifies the RedPallas sig over it.
    let sighash = {
        let h = Blake2bParams::new()
            .hash_length(32)
            .personal(b"e2e-test-sighash")
            .hash(&rk_bytes);
        let mut buf = [0u8; 32];
        buf.copy_from_slice(h.as_bytes());
        buf
    };
    let sig = rsk.sign(&mut rng, &sighash);

    let sig_bytes: [u8; 64] = (&sig).into();

    let payload = DelegationBundlePayload {
        rk: rk_bytes.to_vec(),
        spend_auth_sig: sig_bytes.to_vec(),
        sighash: sighash.to_vec(),
        signed_note_nullifier: nf_signed_bytes.to_vec(),
        cmx_new: cmx_new_bytes[..].to_vec(),
        van_cmx: van_cmx_bytes[..].to_vec(),
        gov_nullifiers: gov_null_bytes.iter().map(|b| b.to_vec()).collect(),
        proof,
    };

    let vote_proof_data = VoteProofDelegationData {
        sk,
        van_comm_rand,
        vote_round_id,
        total_note_value,
        van_comm: bundle.instance.van_comm,
        cmx_new: bundle.instance.cmx_new,
    };

    Ok((payload, round_id_fields, vote_proof_data))
}

// ---------------------------------------------------------------------------
// Multi-delegation bundle builder (shared NC tree, parallel proof gen)
// ---------------------------------------------------------------------------

/// Build a note commitment Merkle tree from an arbitrary number of leaves.
///
/// Returns `(nc_root, levels)` where `levels[k]` contains all nodes at height k
/// in the subtree (padded to next power of two). The nc_root is the full-depth
/// root after folding through empty siblings up to `NOTE_COMMITMENT_TREE_DEPTH`.
fn build_shared_nc_tree(cmxs: &[MerkleHashOrchard]) -> (pallas::Base, Vec<Vec<MerkleHashOrchard>>) {
    let n = cmxs.len();
    assert!(n > 0, "need at least one leaf");

    let p = n.next_power_of_two();
    let subtree_levels = p.trailing_zeros() as usize; // log2(p)

    // Level 0: leaves, padded to power-of-two with empty leaves.
    let empty_leaf = MerkleHashOrchard::empty_leaf();
    let mut leaves = vec![empty_leaf; p];
    leaves[..n].copy_from_slice(cmxs);

    let mut levels: Vec<Vec<MerkleHashOrchard>> = Vec::with_capacity(subtree_levels + 1);
    levels.push(leaves);

    // Build internal subtree levels bottom-up.
    for k in 0..subtree_levels {
        let prev = &levels[k];
        let mut next_level = Vec::with_capacity(prev.len() / 2);
        for i in 0..prev.len() / 2 {
            next_level.push(MerkleHashOrchard::combine(
                Level::from(k as u8),
                &prev[2 * i],
                &prev[2 * i + 1],
            ));
        }
        levels.push(next_level);
    }

    // Fold subtree root through empty siblings for the remaining levels.
    let mut current = levels[subtree_levels][0];
    for level in subtree_levels..NOTE_COMMITMENT_TREE_DEPTH {
        let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
        current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
    }

    (current.inner(), levels)
}

/// Build an authentication path for a leaf at `position` in the shared NC tree.
///
/// Uses concrete siblings from `levels` for the subtree portion, and empty
/// roots for levels above the subtree.
fn build_nc_auth_path(
    position: u32,
    levels: &[Vec<MerkleHashOrchard>],
    subtree_levels: usize,
) -> [MerkleHashOrchard; NOTE_COMMITMENT_TREE_DEPTH] {
    let mut auth_path = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];

    for k in 0..subtree_levels {
        let sibling_idx = ((position as usize) >> k) ^ 1;
        auth_path[k] = levels[k][sibling_idx];
    }

    for (k, elem) in auth_path
        .iter_mut()
        .enumerate()
        .take(NOTE_COMMITMENT_TREE_DEPTH)
        .skip(subtree_levels)
    {
        *elem = MerkleHashOrchard::empty_root(Level::from(k as u8));
    }

    auth_path
}

/// Build N delegation bundles sharing a common note commitment tree and round fields.
///
/// All N delegations prove against the same `nc_root` (shared 2N-leaf tree,
/// 2 notes per delegation: external + internal) and the same `vote_round_id`.
/// Proof generation is parallelized across threads (~30-60s wall time per proof,
/// but N proofs run concurrently).
///
/// Returns `(bundles, round_fields)` where `bundles[i] = (payload, vote_proof_data)`.
pub fn build_multi_delegation_bundles(
    count: usize,
) -> Result<
    (
        Vec<(DelegationBundlePayload, VoteProofDelegationData)>,
        SetupRoundFields,
    ),
    Box<dyn std::error::Error + Send + Sync>,
> {
    assert!(count > 0, "need at least one delegation");

    let note_value_ext = 8_000_000u64;
    let note_value_int = 7_000_000u64;
    let total_note_value = note_value_ext + note_value_int;

    // ---- Generate N spending keys and 2N notes ----
    let mut rng = OsRng;
    let mut per_delegation_data: Vec<(SpendingKey, Note, Note, FullViewingKey)> =
        Vec::with_capacity(count);
    let mut all_cmxs: Vec<MerkleHashOrchard> = Vec::with_capacity(2 * count);

    for _ in 0..count {
        let sk = SpendingKey::random(&mut rng);
        let fvk: FullViewingKey = (&sk).into();
        let recipient_ext = fvk.address_at(0u32, Scope::External);
        let recipient_int = fvk.address_at(0u32, Scope::Internal);

        let (_, _, dp1) = Note::dummy(&mut rng, None);
        let note_ext = Note::new(
            recipient_ext,
            NoteValue::from_raw(note_value_ext),
            Rho::from_nf_old(dp1.nullifier(&fvk)),
            &mut rng,
        );
        let (_, _, dp2) = Note::dummy(&mut rng, None);
        let note_int = Note::new(
            recipient_int,
            NoteValue::from_raw(note_value_int),
            Rho::from_nf_old(dp2.nullifier(&fvk)),
            &mut rng,
        );

        all_cmxs.push(MerkleHashOrchard::from_cmx(&ExtractedNoteCommitment::from(
            note_ext.commitment(),
        )));
        all_cmxs.push(MerkleHashOrchard::from_cmx(&ExtractedNoteCommitment::from(
            note_int.commitment(),
        )));

        per_delegation_data.push((sk, note_ext, note_int, fvk));
    }

    // ---- Build shared NC tree (2N leaves -> shared nc_root) ----
    let (nc_root, levels) = build_shared_nc_tree(&all_cmxs);
    let subtree_levels = all_cmxs.len().next_power_of_two().trailing_zeros() as usize;

    // ---- Shared round fields ----
    let imt = SpacedLeafImtProvider::new();
    let nf_imt_root = imt.root();

    let snapshot_blockhash = [0xAAu8; 32];
    let proposals_hash = [0xBBu8; 32];
    let vote_end_time: u64 = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs()
        + vote_window_secs();

    let snapshot_height: u64 = 42_000;
    let round_fields = SetupRoundFields {
        snapshot_height,
        snapshot_blockhash,
        proposals_hash,
        vote_end_time,
        nullifier_imt_root: nf_imt_root.to_repr(),
        nc_root: nc_root.to_repr(),
    };
    let round_id_bytes = crate::payloads::derive_round_id(&round_fields);
    let vote_round_id: pallas::Base =
        pallas::Base::from_repr(round_id_bytes).expect("Poseidon output must be canonical Fp");

    // ---- Prepare per-delegation inputs (auth paths + IMT proofs for real notes) ----
    struct DelegationInput {
        sk: SpendingKey,
        fvk: FullViewingKey,
        note_ext: Note,
        note_int: Note,
        ext_pos: u32,
        int_pos: u32,
        auth_path_ext: [MerkleHashOrchard; NOTE_COMMITMENT_TREE_DEPTH],
        auth_path_int: [MerkleHashOrchard; NOTE_COMMITMENT_TREE_DEPTH],
        imt_proof_ext: voting_circuits::delegation::imt::ImtProofData,
        imt_proof_int: voting_circuits::delegation::imt::ImtProofData,
    }

    let mut inputs: Vec<DelegationInput> = Vec::with_capacity(count);
    for (i, (sk, note_ext, note_int, fvk)) in per_delegation_data.into_iter().enumerate() {
        let ext_pos = (2 * i) as u32;
        let int_pos = (2 * i + 1) as u32;

        let auth_path_ext = build_nc_auth_path(ext_pos, &levels, subtree_levels);
        let auth_path_int = build_nc_auth_path(int_pos, &levels, subtree_levels);

        let nf_ext_fp: pallas::Base = pallas::Base::from_repr(note_ext.nullifier(&fvk).to_bytes())
            .expect("note nullifier must be canonical Fp");
        let imt_proof_ext = imt.non_membership_proof(nf_ext_fp)?;

        let nf_int_fp: pallas::Base = pallas::Base::from_repr(note_int.nullifier(&fvk).to_bytes())
            .expect("note nullifier must be canonical Fp");
        let imt_proof_int = imt.non_membership_proof(nf_int_fp)?;

        inputs.push(DelegationInput {
            sk,
            fvk,
            note_ext,
            note_int,
            ext_pos,
            int_pos,
            auth_path_ext,
            auth_path_int,
            imt_proof_ext,
            imt_proof_int,
        });
    }

    // ---- Parallel proof generation ----
    eprintln!(
        "[multi-deleg] spawning {} threads for parallel proof generation...",
        count
    );
    let handles: Vec<_> = inputs
        .into_iter()
        .enumerate()
        .map(|(i, input)| {
            std::thread::spawn(move || -> Result<
                (DelegationBundlePayload, VoteProofDelegationData),
                Box<dyn std::error::Error + Send + Sync>,
            > {
                let mut rng = OsRng;
                let imt = SpacedLeafImtProvider::new();
                let alpha = pallas::Scalar::random(&mut rng);
                let van_comm_rand = pallas::Base::random(&mut rng);
                let output_recipient = input.fvk.address_at(1u32, Scope::External);

                let note_inputs = vec![
                    RealNoteInput {
                        note: input.note_ext,
                        fvk: input.fvk.clone(),
                        merkle_path: MerklePath::from_parts(input.ext_pos, input.auth_path_ext),
                        imt_proof: input.imt_proof_ext,
                        scope: Scope::External,
                    },
                    RealNoteInput {
                        note: input.note_int,
                        fvk: input.fvk.clone(),
                        merkle_path: MerklePath::from_parts(input.int_pos, input.auth_path_int),
                        imt_proof: input.imt_proof_int,
                        scope: Scope::Internal,
                    },
                ];

                eprintln!("[multi-deleg] delegation {} building bundle + proof...", i);
                let bundle = build_delegation_bundle(
                    note_inputs,
                    &input.fvk,
                    alpha,
                    output_recipient,
                    vote_round_id,
                    nc_root,
                    van_comm_rand,
                    &imt,
                    &mut rng,
                    None,
                )
                .map_err(|e| format!("delegation {}: build_delegation_bundle: {}", i, e))?;

                let proof = create_delegation_proof(bundle.circuit, &bundle.instance);
                verify_delegation_proof(&proof, &bundle.instance)
                    .map_err(|e| format!("delegation {}: verify_delegation_proof: {}", i, e))?;
                eprintln!("[multi-deleg] delegation {} proof verified", i);

                // RedPallas signature
                let ask = SpendAuthorizingKey::from(&input.sk);
                let rsk = ask.randomize(&alpha);
                let rk_bytes: [u8; 32] = bundle.instance.rk.clone().into();
                let sighash = {
                    let h = Blake2bParams::new()
                        .hash_length(32)
                        .personal(b"e2e-test-sighash")
                        .hash(&rk_bytes);
                    let mut buf = [0u8; 32];
                    buf.copy_from_slice(h.as_bytes());
                    buf
                };
                let sig = rsk.sign(&mut rng, &sighash);
                let sig_bytes: [u8; 64] = (&sig).into();

                let nf_signed_bytes = bundle.instance.nf_signed.to_bytes();
                let cmx_new_bytes = bundle.instance.cmx_new.to_repr();
                let van_cmx_bytes = bundle.instance.van_comm.to_repr();
                let gov_null_bytes: Vec<[u8; 32]> =
                    bundle.instance.gov_null.iter().map(|g| g.to_repr()).collect();

                let payload = DelegationBundlePayload {
                    rk: rk_bytes.to_vec(),
                    spend_auth_sig: sig_bytes.to_vec(),
                    sighash: sighash.to_vec(),
                    signed_note_nullifier: nf_signed_bytes.to_vec(),
                    cmx_new: cmx_new_bytes[..].to_vec(),
                    van_cmx: van_cmx_bytes[..].to_vec(),
                    gov_nullifiers: gov_null_bytes.iter().map(|b| b.to_vec()).collect(),
                    proof,
                };

                let vote_proof_data = VoteProofDelegationData {
                    sk: input.sk,
                    van_comm_rand,
                    vote_round_id,
                    total_note_value,
                    van_comm: bundle.instance.van_comm,
                    cmx_new: bundle.instance.cmx_new,
                };

                Ok((payload, vote_proof_data))
            })
        })
        .collect();

    let mut results = Vec::with_capacity(count);
    for (i, handle) in handles.into_iter().enumerate() {
        let result = handle
            .join()
            .map_err(|_| format!("delegation {} thread panicked", i))?;
        results.push(result?);
    }

    eprintln!("[multi-deleg] all {} bundles built and verified", count);
    Ok((results, round_fields))
}

// ---------------------------------------------------------------------------
// Single-validator ceremony helpers
// ---------------------------------------------------------------------------

/// Ensure the chain validator's Pallas key is registered in the global registry.
///
/// In the per-round ceremony model, Pallas keys are registered once globally
/// (usually during chain init via MsgCreateValidatorWithPallasKey). This
/// function handles the case where the key wasn't registered during init.
///
/// Idempotent: if the key is already registered, the tx will be rejected by
/// the keeper and the error is ignored.
pub fn ensure_pallas_key_registered() {
    use crate::api::{broadcast_cosmos_msg, default_cosmos_tx_config, key_account_address};
    use crate::payloads::register_pallas_key_payload;

    let config = default_cosmos_tx_config();
    let validator_addr =
        key_account_address(&config.key_name, &config.home_dir).unwrap_or_else(|| {
            panic!(
                "failed to get account address for key '{}' from keyring at {}",
                config.key_name, config.home_dir
            )
        });

    // Read the chain validator's Pallas PK from disk.
    let pallas_pk_path = std::env::var("ZALLY_PALLAS_PK_PATH").unwrap_or_else(|_| {
        let home = std::env::var("HOME").expect("HOME env var must be set");
        format!("{}/.zallyd/pallas.pk", home)
    });
    let pallas_pk_bytes = std::fs::read(&pallas_pk_path)
        .unwrap_or_else(|e| panic!("failed to read Pallas PK from {}: {}", pallas_pk_path, e));
    assert_eq!(
        pallas_pk_bytes.len(),
        32,
        "Pallas PK must be exactly 32 bytes"
    );

    eprintln!(
        "[E2E] Ensuring Pallas key registered for {}...",
        validator_addr
    );
    let mut msg = register_pallas_key_payload(&validator_addr, &pallas_pk_bytes);
    msg["@type"] = serde_json::json!("/zvote.v1.MsgRegisterPallasKey");
    match broadcast_cosmos_msg(&msg, &config) {
        Ok((status, json)) => {
            let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1);
            if status == 200 && code == 0 {
                eprintln!("[E2E] Pallas key registered ✓");
                // Wait one block for state to commit.
                std::thread::sleep(std::time::Duration::from_millis(6000));
            } else {
                eprintln!(
                    "[E2E] Pallas key already registered (code={}), continuing",
                    code
                );
            }
        }
        Err(e) => eprintln!(
            "[E2E] Pallas key registration failed: {} (may be already registered)",
            e
        ),
    }
}
