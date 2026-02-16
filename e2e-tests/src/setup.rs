//! Build a real delegation bundle for E2E tests (ZKP #1 + RedPallas).
//!
//! Generates session params with vote_end_time = now + configured window and a canonical
//! vote_round_id, then builds the delegation bundle and RedPallas signature
//! so the test can create the session and delegate without fixture files.
//! The window defaults to 8 minutes and can be overridden with
//! ZALLY_E2E_VOTE_WINDOW_SECS. This timestamp is part of vote_round_id, so it must
//! be chosen before proof generation starts.

use crate::payloads::{DelegationBundlePayload, SetupRoundFields};
use blake2b_simd::Params as Blake2bParams;
use ff::{Field, PrimeField};
use incrementalmerkletree::{Hashable, Level};
use orchard::{
    delegation::{
        builder::{build_delegation_bundle, RealNoteInput},
        imt::{ImtProvider, SpacedLeafImtProvider},
        prove::{create_delegation_proof, verify_delegation_proof},
    },
    keys::{FullViewingKey, Scope, SpendAuthorizingKey, SpendingKey},
    note::{ExtractedNoteCommitment, Note, Rho},
    tree::{MerkleHashOrchard, MerklePath},
    value::NoteValue,
    vote_proof::VOTE_COMM_TREE_DEPTH,
    NOTE_COMMITMENT_TREE_DEPTH,
};
use pasta_curves::pallas;
use rand::rngs::OsRng;
use vote_commitment_tree::TreeServer;

const DEFAULT_E2E_VOTE_WINDOW_SECS: u64 = 480;
const MIN_E2E_VOTE_WINDOW_SECS: u64 = 300;

fn vote_window_secs() -> u64 {
    std::env::var("ZALLY_E2E_VOTE_WINDOW_SECS")
        .ok()
        .and_then(|raw| raw.parse::<u64>().ok())
        .map(|secs| secs.max(MIN_E2E_VOTE_WINDOW_SECS))
        .unwrap_or(DEFAULT_E2E_VOTE_WINDOW_SECS)
}

/// Append exactly 32 bytes to `out` from `b` (pad with zeros if shorter).
fn extend_padded32(out: &mut Vec<u8>, b: &[u8]) {
    let mut buf = [0u8; 32];
    let n = b.len().min(32);
    buf[..n].copy_from_slice(&b[..n]);
    out.extend_from_slice(&buf);
}

/// Append exactly 64 bytes to `out` from `b` (pad with zeros if shorter).
fn extend_padded64(out: &mut Vec<u8>, b: &[u8]) {
    let mut buf = [0u8; 64];
    let n = b.len().min(64);
    buf[..n].copy_from_slice(&b[..n]);
    out.extend_from_slice(&buf);
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
    /// The cmx_new value appended to the commitment tree (sibling at position 0).
    pub cmx_new: pallas::Base,
}

/// Build delegation bundle and session fields for the E2E test.
/// vote_end_time = now + vote_window_secs() where the default window is 8 min
/// (override with ZALLY_E2E_VOTE_WINDOW_SECS, clamped to >= 300s).
/// The round must stay ACTIVE through all submissions, then expire for auto-tally.
/// Returns payload for MsgDelegateVote, session fields for MsgCreateVotingSession,
/// and private witness data for building ZKP #2 (vote proof).
///
/// If `sk_override` is Some, uses that SpendingKey (e.g. derived from a hotkey seed
/// via ZIP-32, for testing the librustvoting path). Otherwise generates a random key.
pub fn build_delegation_bundle_for_test(
    sk_override: Option<SpendingKey>,
) -> Result<(DelegationBundlePayload, SetupRoundFields, VoteProofDelegationData), Box<dyn std::error::Error + Send + Sync>>
{
    let mut rng = OsRng;

    let sk = sk_override.unwrap_or_else(|| SpendingKey::random(&mut rng));
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let alpha = pallas::Scalar::random(&mut rng);
    let van_comm_rand = pallas::Base::random(&mut rng);

    let note_value = 15_000_000u64;
    let recipient = fvk.address_at(0u32, Scope::External);
    let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
    let note = Note::new(
        recipient,
        NoteValue::from_raw(note_value),
        Rho::from_nf_old(dummy_parent.nullifier(&fvk)),
        &mut rng,
    );

    let empty_leaf = MerkleHashOrchard::empty_leaf();
    let cmx = ExtractedNoteCommitment::from(note.commitment());
    let leaves = [
        MerkleHashOrchard::from_cmx(&cmx),
        empty_leaf,
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

    let mut auth_path = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];
    auth_path[0] = leaves[1];
    auth_path[1] = l1_1;
    for level in 2..NOTE_COMMITMENT_TREE_DEPTH {
        auth_path[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
    }
    let merkle_path = MerklePath::from_parts(0u32, auth_path);

    let imt = SpacedLeafImtProvider::new();
    let nf_imt_root = imt.root();
    let real_nf = note.nullifier(&fvk);
    let nf_fp: pallas::Base = pallas::Base::from_repr(real_nf.to_bytes()).unwrap();
    let imt_proof = imt.non_membership_proof(nf_fp)?;

    let note_input = RealNoteInput {
        note,
        fvk: fvk.clone(),
        merkle_path,
        imt_proof,
    };

    let snapshot_blockhash = [0xAAu8; 32];
    let proposals_hash = [0xBBu8; 32];
    let vote_end_time: u64 = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs()
        + vote_window_secs();

    let nc_root_repr = nc_root.to_repr();
    let nf_imt_root_repr = nf_imt_root.to_repr();

    let mut snapshot_height: u64 = 42_000;
    let vote_round_id: pallas::Base;
    loop {
        let mut data = Vec::with_capacity(8 + 32 + 32 + 8 + 32 + 32);
        data.extend_from_slice(&snapshot_height.to_be_bytes());
        data.extend_from_slice(&snapshot_blockhash);
        data.extend_from_slice(&proposals_hash);
        data.extend_from_slice(&vote_end_time.to_be_bytes());
        data.extend_from_slice(nf_imt_root_repr.as_ref());
        data.extend_from_slice(nc_root_repr.as_ref());

        let hash = Blake2bParams::new().hash_length(32).hash(&data);
        let mut repr = [0u8; 32];
        repr.copy_from_slice(hash.as_bytes());

        if let Some(fp) = pallas::Base::from_repr(repr).into() {
            vote_round_id = fp;
            break;
        }
        snapshot_height += 1;
    }

    let bundle = build_delegation_bundle(
        vec![note_input],
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        van_comm_rand,
        &imt,
        &mut rng,
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
    let vote_round_id_repr = bundle.instance.vote_round_id.to_repr();
    let van_cmx_bytes = bundle.instance.van_comm.to_repr();
    let gov_null_bytes: Vec<[u8; 32]> = bundle
        .instance
        .gov_null
        .iter()
        .map(|g| g.to_repr())
        .collect();
    let enc_memo = [0x05u8; 64];

    // Canonical sighash: Blake2b-256(domain || vote_round_id || rk || ...).
    // Must match sdk/x/vote/types/sighash.go ComputeDelegationSighash.
    const SIGHASH_DOMAIN: &[u8] = b"ZALLY_DELEGATION_SIGHASH_V0";
    let mut canonical =
        Vec::with_capacity(SIGHASH_DOMAIN.len() + 32 + 32 + 32 + 32 + 64 + 32 + 4 * 32);
    canonical.extend_from_slice(SIGHASH_DOMAIN);
    extend_padded32(&mut canonical, vote_round_id_repr.as_ref());
    canonical.extend_from_slice(&rk_bytes);
    extend_padded32(&mut canonical, &nf_signed_bytes);
    canonical.extend_from_slice(cmx_new_bytes.as_ref());
    extend_padded64(&mut canonical, &enc_memo);
    extend_padded32(&mut canonical, van_cmx_bytes.as_ref());
    for i in 0..4 {
        if i < gov_null_bytes.len() {
            canonical.extend_from_slice(&gov_null_bytes[i]);
        } else {
            canonical.extend_from_slice(&[0u8; 32]);
        }
    }
    let sighash_full = Blake2bParams::new().hash_length(32).hash(&canonical);
    let mut sighash = [0u8; 32];
    sighash.copy_from_slice(sighash_full.as_bytes());
    let sig = rsk.sign(&mut rng, &sighash);

    let sig_bytes: [u8; 64] = (&sig).into();

    let payload = DelegationBundlePayload {
        rk: rk_bytes.to_vec(),
        spend_auth_sig: sig_bytes.to_vec(),
        sighash: sighash.to_vec(),
        signed_note_nullifier: nf_signed_bytes.to_vec(),
        cmx_new: cmx_new_bytes.as_ref().to_vec(),
        enc_memo: enc_memo.to_vec(),
        van_cmx: van_cmx_bytes.as_ref().to_vec(),
        gov_nullifiers: gov_null_bytes.iter().map(|b| b.to_vec()).collect(),
        proof,
    };

    let fields = SetupRoundFields {
        snapshot_height,
        snapshot_blockhash,
        proposals_hash,
        vote_end_time,
        nullifier_imt_root: nf_imt_root_repr.as_ref().try_into().unwrap(),
        nc_root: nc_root_repr.as_ref().try_into().unwrap(),
    };

    let vote_proof_data = VoteProofDelegationData {
        sk,
        van_comm_rand,
        vote_round_id,
        total_note_value: note_value,
        van_comm: bundle.instance.van_comm,
        cmx_new: bundle.instance.cmx_new,
    };

    Ok((payload, fields, vote_proof_data))
}

/// Build a vote commitment tree locally with the single van_cmx leaf from
/// delegation (at position 0) and return the Merkle authentication path.
/// cmx_new is NOT added to the tree — no subsequent proof references it.
///
/// The `checkpoint_height` should be the on-chain anchor height at which
/// the delegation block was committed.
///
/// Returns `(auth_path, position, root)` suitable for the vote proof builder.
pub fn build_van_merkle_witness(
    van_cmx: pallas::Base,
    checkpoint_height: u32,
) -> ([pallas::Base; VOTE_COMM_TREE_DEPTH], u32, pallas::Base) {
    let mut tree = TreeServer::empty();
    tree.append(van_cmx);
    tree.checkpoint(checkpoint_height);

    let root = tree
        .root_at_height(checkpoint_height)
        .expect("checkpoint should exist");

    let path = tree
        .path(0, checkpoint_height)
        .expect("VAN at position 0 should have a valid path");

    // Convert MerkleHashVote siblings to pallas::Base for the circuit.
    let auth_path_hashes = path.auth_path();
    let mut auth_path = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
    for (i, hash) in auth_path_hashes.iter().enumerate() {
        auth_path[i] = hash.inner();
    }

    let position = path.position();

    // Sanity: verify the path produces the expected root.
    assert!(
        path.verify(van_cmx, root),
        "merkle path verification failed for VAN at position 0"
    );

    (auth_path, position, root)
}

/// Build a vote commitment tree locally with all 3 leaves from delegation + cast
/// (van_cmx at 0, vote_authority_note_new at 1, vote_commitment at 2) and return
/// the Merkle authentication path for vote_commitment at position 2.
///
/// Returns `(auth_path, position, root)` suitable for the share reveal builder (ZKP #3).
pub fn build_vote_commitment_merkle_witness(
    van_cmx: pallas::Base,
    vote_authority_note_new: pallas::Base,
    vote_commitment: pallas::Base,
    checkpoint_height: u32,
) -> ([pallas::Base; VOTE_COMM_TREE_DEPTH], u32, pallas::Base) {
    let mut tree = TreeServer::empty();
    tree.append(van_cmx); // position 0
    tree.append(vote_authority_note_new); // position 1
    tree.append(vote_commitment); // position 2
    tree.checkpoint(checkpoint_height);

    let root = tree
        .root_at_height(checkpoint_height)
        .expect("checkpoint should exist");

    let path = tree
        .path(2, checkpoint_height)
        .expect("vote_commitment at position 2 should have a valid path");

    // Convert MerkleHashVote siblings to pallas::Base for the circuit.
    let auth_path_hashes = path.auth_path();
    let mut auth_path = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
    for (i, hash) in auth_path_hashes.iter().enumerate() {
        auth_path[i] = hash.inner();
    }

    let position = path.position();

    // Sanity: verify the path produces the expected root.
    assert!(
        path.verify(vote_commitment, root),
        "merkle path verification failed for vote_commitment at position 2"
    );

    (auth_path, position, root)
}
