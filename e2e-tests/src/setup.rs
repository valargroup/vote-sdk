//! Build a real delegation bundle for E2E tests (ZKP #1 + RedPallas).
//!
//! Generates session params with vote_end_time = now + configured window and a canonical
//! vote_round_id, then builds the delegation bundle and RedPallas signature
//! so the test can create the session and delegate without fixture files.
//! The window defaults to 8 minutes and can be overridden with
//! ZALLY_E2E_VOTE_WINDOW_SECS. This timestamp is part of vote_round_id, so it must
//! be chosen before proof generation starts.

use crate::payloads::{
    DelegationBundlePayload, SetupRoundFields,
};
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
    NOTE_COMMITMENT_TREE_DEPTH,
};
use orchard::vote_proof::VOTE_COMM_TREE_DEPTH;
use pasta_curves::pallas;
use rand::rngs::OsRng;
use vote_commitment_tree::MemoryTreeServer;

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
    let enc_memo = [0x05u8; 64];

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
        enc_memo: enc_memo.to_vec(),
        van_cmx: van_cmx_bytes[..].to_vec(),
        gov_nullifiers: gov_null_bytes.iter().map(|b| b.to_vec()).collect(),
        proof,
    };

    let fields = SetupRoundFields {
        snapshot_height,
        snapshot_blockhash,
        proposals_hash,
        vote_end_time,
        nullifier_imt_root: nf_imt_root_repr,
        nc_root: nc_root_repr,
    };

    let vote_proof_data = VoteProofDelegationData {
        sk,
        van_comm_rand,
        vote_round_id,
        total_note_value: total_note_value,
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
    let mut tree = MemoryTreeServer::empty();
    tree.append(van_cmx).unwrap();
    tree.checkpoint(checkpoint_height).unwrap();

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
    let mut tree = MemoryTreeServer::empty();
    tree.append(van_cmx).unwrap(); // position 0
    tree.append(vote_authority_note_new).unwrap(); // position 1
    tree.append(vote_commitment).unwrap(); // position 2
    tree.checkpoint(checkpoint_height).unwrap();

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

// ---------------------------------------------------------------------------
// Multi-validator ceremony helpers
// ---------------------------------------------------------------------------

/// Info about a single validator in the multi-validator chain.
pub struct ValidatorInfo {
    /// Validator operator address (zvotevaloper1...).
    pub operator_address: String,
    /// Validator account address (zvote1...) — used as tx creator.
    pub account_address: String,
    /// 32-byte compressed Pallas public key.
    pub pallas_pk: [u8; 32],
    /// Path to the validator's home directory.
    pub home_dir: String,
}

/// Load multi-validator info from disk and the staking module.
///
/// Reads `pallas.pk` from each validator's home directory (`$HOME/.zallyd-val{1,2,3}/`)
/// and matches operator addresses from the staking module by validator moniker
/// (val1, val2, val3 as set during `init_multi.sh`).
pub fn load_multi_validator_info() -> Vec<ValidatorInfo> {
    use crate::api::get_validators_with_monikers;

    let home = std::env::var("HOME").expect("HOME env var must be set");
    let staking_vals =
        get_validators_with_monikers().expect("failed to query validators from staking module");

    eprintln!(
        "[E2E] Found {} validators in staking module:",
        staking_vals.len()
    );
    for (addr, moniker) in &staking_vals {
        eprintln!("[E2E]   {} (moniker: {})", addr, moniker);
    }

    let mut result = Vec::new();
    for i in 1..=3u32 {
        let home_dir = format!("{}/.zallyd-val{}", home, i);
        let moniker = format!("val{}", i);

        let (op_addr, _) = staking_vals
            .iter()
            .find(|(_, m)| m == &moniker)
            .unwrap_or_else(|| {
                panic!(
                    "no validator with moniker '{}' in staking module; found: {:?}",
                    moniker,
                    staking_vals.iter().map(|(_, m)| m).collect::<Vec<_>>()
                )
            });

        let pallas_pk_path = format!("{}/pallas.pk", home_dir);
        let pallas_pk_bytes = std::fs::read(&pallas_pk_path)
            .unwrap_or_else(|e| panic!("failed to read {}: {}", pallas_pk_path, e));
        assert_eq!(
            pallas_pk_bytes.len(),
            32,
            "pallas.pk must be 32 bytes, got {} at {}",
            pallas_pk_bytes.len(),
            pallas_pk_path
        );

        let mut pallas_pk = [0u8; 32];
        pallas_pk.copy_from_slice(&pallas_pk_bytes);

        let account_address = crate::api::key_account_address("validator", &home_dir)
            .unwrap_or_else(|| panic!("failed to get account address for val{} from keyring", i));

        eprintln!(
            "[E2E] Val{}: operator={}, account={}, pallas_pk={}",
            i,
            op_addr,
            account_address,
            hex::encode(pallas_pk)
        );

        result.push(ValidatorInfo {
            operator_address: op_addr.clone(),
            account_address,
            pallas_pk,
            home_dir,
        });
    }
    result
}

// ensure_ceremony_idle removed: per-round ceremony has no singleton state to reset.

/// Register all validators' Pallas keys in the global registry.
///
/// In the per-round ceremony model, Pallas keys are registered once globally
/// and reused across rounds. This is typically done during chain init via
/// MsgCreateValidatorWithPallasKey, but this function handles the case where
/// keys need to be registered explicitly (e.g., after init without Pallas keys).
///
/// Ignores "already registered" errors (idempotent).
pub fn register_pallas_keys_multi(validators: &[ValidatorInfo]) {
    use crate::api::{broadcast_cosmos_msg, CosmosTxConfig};
    use crate::payloads::register_pallas_key_payload;

    for (i, val) in validators.iter().enumerate() {
        eprintln!(
            "[E2E] Registering Pallas key for validator {} (account={})...",
            i + 1,
            val.account_address
        );
        let mut msg = register_pallas_key_payload(&val.account_address, &val.pallas_pk);
        msg["@type"] = serde_json::json!("/zvote.v1.MsgRegisterPallasKey");
        let config = CosmosTxConfig {
            key_name: "validator".to_string(),
            home_dir: val.home_dir.clone(),
            chain_id: "zvote-1".to_string(),
            node_url: std::env::var("ZALLY_NODE_URL")
                .unwrap_or_else(|_| "tcp://localhost:26157".to_string()),
        };
        match broadcast_cosmos_msg(&msg, &config) {
            Ok((status, json)) => {
                let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(-1);
                if status == 200 && code == 0 {
                    eprintln!("[E2E] Pallas key registered for val{}", i + 1);
                } else {
                    // Already registered is fine — log and continue.
                    eprintln!(
                        "[E2E] Pallas key registration for val{} returned code={} (may be already registered)",
                        i + 1, code
                    );
                }
            }
            Err(e) => eprintln!("[E2E] Pallas key registration for val{} failed: {} (may be already registered)", i + 1, e),
        }
        std::thread::sleep(std::time::Duration::from_millis(2000));
    }

    // Wait for the last registration to commit.
    std::thread::sleep(std::time::Duration::from_millis(6000));
    eprintln!("[E2E] Pallas key registration complete");
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
    use crate::api::{
        broadcast_cosmos_msg, default_cosmos_tx_config, key_account_address,
    };
    use crate::payloads::register_pallas_key_payload;

    let config = default_cosmos_tx_config();
    let validator_addr = key_account_address(&config.key_name, &config.home_dir)
        .unwrap_or_else(|| panic!(
            "failed to get account address for key '{}' from keyring at {}",
            config.key_name, config.home_dir
        ));

    // Read the chain validator's Pallas PK from disk.
    let pallas_pk_path = std::env::var("ZALLY_PALLAS_PK_PATH").unwrap_or_else(|_| {
        let home = std::env::var("HOME").expect("HOME env var must be set");
        format!("{}/.zallyd/pallas.pk", home)
    });
    let pallas_pk_bytes = std::fs::read(&pallas_pk_path)
        .unwrap_or_else(|e| panic!("failed to read Pallas PK from {}: {}", pallas_pk_path, e));
    assert_eq!(pallas_pk_bytes.len(), 32, "Pallas PK must be exactly 32 bytes");

    eprintln!("[E2E] Ensuring Pallas key registered for {}...", validator_addr);
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
                eprintln!("[E2E] Pallas key already registered (code={}), continuing", code);
            }
        }
        Err(e) => eprintln!("[E2E] Pallas key registration failed: {} (may be already registered)", e),
    }
}
