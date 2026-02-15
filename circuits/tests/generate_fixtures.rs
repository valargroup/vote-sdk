//! Integration test that generates fixture files for Go and TypeScript tests.
//!
//! Run with: cargo test --release -- generate_fixtures --ignored --nocapture
//!
//! Generates:
//!   crypto/zkp/testdata/toy_valid_proof.bin      - valid Halo2 proof bytes
//!   crypto/zkp/testdata/toy_valid_input.bin      - correct public input (Fp, 32-byte LE)
//!   crypto/zkp/testdata/toy_wrong_input.bin      - wrong public input for negative tests
//!   crypto/redpallas/testdata/valid_rk.bin       - 32-byte RedPallas verification key
//!   crypto/redpallas/testdata/valid_sighash.bin  - 32-byte sighash (message)
//!   crypto/redpallas/testdata/valid_sig.bin      - 64-byte valid RedPallas signature
//!   crypto/redpallas/testdata/wrong_sig.bin      - 64-byte signature over wrong message
//!   tests/api/fixtures/delegation_real.json       - full delegation proof fixture (ZKP #1)

use pasta_curves::group::ff::PrimeField;
use std::fs;
use std::path::Path;

use blake2b_simd::Params as Blake2bParams;
use rand::thread_rng;
use reddsa::{orchard as reddsa_orchard, SigningKey, VerificationKey};

use zally_circuits::toy;
use zally_circuits::redpallas as rp;

/// Generate fixture files for Go and TypeScript tests.
///
/// Marked `#[ignore]` so it only runs when explicitly requested
/// (e.g., `cargo test --release -- generate_fixtures --ignored`).
/// This avoids regenerating fixtures on every `cargo test`.
#[test]
#[ignore]
fn generate_fixtures() {
    generate_halo2_fixtures();
    generate_redpallas_fixtures();
    generate_delegation_fixtures();
    println!("\nAll fixtures generated and validated successfully.");
}

/// Generate Halo2 toy circuit proof fixtures.
fn generate_halo2_fixtures() {
    let testdata_dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("crypto/zkp/testdata");

    fs::create_dir_all(&testdata_dir).expect("failed to create testdata directory");

    // Generate a valid proof with known inputs: a=2, b=3, constant=7.
    // c = 7 * 2^2 * 3^2 = 7 * 4 * 9 = 252
    let (proof, c) = toy::create_toy_proof(2, 3);

    // Serialize the public input as 32-byte little-endian (Pallas Fp repr).
    let c_bytes = c.to_repr();

    // Write valid proof.
    let proof_path = testdata_dir.join("toy_valid_proof.bin");
    fs::write(&proof_path, &proof).expect("failed to write proof fixture");
    println!(
        "Wrote valid proof ({} bytes) to {}",
        proof.len(),
        proof_path.display()
    );

    // Write valid public input.
    let input_path = testdata_dir.join("toy_valid_input.bin");
    fs::write(&input_path, c_bytes.as_ref()).expect("failed to write input fixture");
    println!(
        "Wrote valid input ({} bytes) to {}",
        c_bytes.as_ref().len(),
        input_path.display()
    );

    // Write wrong public input (c = 999, which does not match any valid (a,b) for constant=7).
    use halo2_proofs::pasta::Fp;
    let wrong_c = Fp::from(999u64);
    let wrong_bytes = wrong_c.to_repr();
    let wrong_path = testdata_dir.join("toy_wrong_input.bin");
    fs::write(&wrong_path, wrong_bytes.as_ref()).expect("failed to write wrong input fixture");
    println!(
        "Wrote wrong input ({} bytes) to {}",
        wrong_bytes.as_ref().len(),
        wrong_path.display()
    );

    // Verify the generated proof works before committing the fixtures.
    assert!(
        toy::verify_toy(&proof, &c).is_ok(),
        "generated proof should verify against correct input"
    );
    assert!(
        toy::verify_toy(&proof, &wrong_c).is_err(),
        "generated proof should NOT verify against wrong input"
    );

    println!("Halo2 fixtures generated and validated.");
}

/// Generate RedPallas SpendAuth signature fixtures.
fn generate_redpallas_fixtures() {
    let testdata_dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("crypto/redpallas/testdata");

    fs::create_dir_all(&testdata_dir).expect("failed to create redpallas testdata directory");

    let mut rng = thread_rng();

    // Generate a signing key and derive the verification key (rk).
    let sk = SigningKey::<reddsa_orchard::SpendAuth>::new(&mut rng);
    let vk = VerificationKey::from(&sk);

    // The sighash is Blake2b-256("ZALLY_SIGHASH_V0"). It must match the value
    // sent by the client as msg.sighash and the REAL_SIGHASH constant in
    // tests/api/src/helpers.ts.
    let sighash_full = Blake2bParams::new()
        .hash_length(32)
        .hash(b"ZALLY_SIGHASH_V0");
    let mut sighash = [0u8; 32];
    sighash.copy_from_slice(sighash_full.as_bytes());

    // Sign the sighash.
    let sig = sk.sign(&mut rng, &sighash);

    // Serialize to byte arrays.
    let rk_bytes: [u8; 32] = vk.into();
    let sig_bytes: [u8; 64] = sig.into();

    // Write valid rk.
    let rk_path = testdata_dir.join("valid_rk.bin");
    fs::write(&rk_path, &rk_bytes).expect("failed to write rk fixture");
    println!("Wrote valid rk ({} bytes) to {}", rk_bytes.len(), rk_path.display());

    // Write valid sighash.
    let sighash_path = testdata_dir.join("valid_sighash.bin");
    fs::write(&sighash_path, &sighash).expect("failed to write sighash fixture");
    println!(
        "Wrote valid sighash ({} bytes) to {}",
        sighash.len(),
        sighash_path.display()
    );

    // Write valid signature.
    let sig_path = testdata_dir.join("valid_sig.bin");
    fs::write(&sig_path, &sig_bytes).expect("failed to write sig fixture");
    println!(
        "Wrote valid sig ({} bytes) to {}",
        sig_bytes.len(),
        sig_path.display()
    );

    // Generate a wrong signature: sign a different message.
    let wrong_msg: [u8; 32] = [0xff; 32];
    let wrong_sig = sk.sign(&mut rng, &wrong_msg);
    let wrong_sig_bytes: [u8; 64] = wrong_sig.into();

    let wrong_sig_path = testdata_dir.join("wrong_sig.bin");
    fs::write(&wrong_sig_path, &wrong_sig_bytes).expect("failed to write wrong sig fixture");
    println!(
        "Wrote wrong sig ({} bytes) to {}",
        wrong_sig_bytes.len(),
        wrong_sig_path.display()
    );

    // Verify the generated fixtures work before committing.
    assert!(
        rp::verify_spend_auth_sig(&rk_bytes, &sighash, &sig_bytes).is_ok(),
        "valid signature should verify"
    );
    assert!(
        rp::verify_spend_auth_sig(&rk_bytes, &sighash, &wrong_sig_bytes).is_err(),
        "wrong signature should NOT verify"
    );

    // Print base64-encoded values for embedding in TypeScript API tests.
    use std::io::Write;
    let b64 = |bytes: &[u8]| {
        use std::io::Cursor;
        let mut buf = Vec::new();
        {
            let mut cursor = Cursor::new(&mut buf);
            // Simple base64 encoding (standard alphabet, no padding stripping).
            let alphabet = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
            let mut i = 0;
            while i < bytes.len() {
                let b0 = bytes[i] as u32;
                let b1 = if i + 1 < bytes.len() { bytes[i + 1] as u32 } else { 0 };
                let b2 = if i + 2 < bytes.len() { bytes[i + 2] as u32 } else { 0 };
                let triple = (b0 << 16) | (b1 << 8) | b2;
                cursor.write_all(&[alphabet[((triple >> 18) & 0x3F) as usize]]).unwrap();
                cursor.write_all(&[alphabet[((triple >> 12) & 0x3F) as usize]]).unwrap();
                if i + 1 < bytes.len() {
                    cursor.write_all(&[alphabet[((triple >> 6) & 0x3F) as usize]]).unwrap();
                } else {
                    cursor.write_all(b"=").unwrap();
                }
                if i + 2 < bytes.len() {
                    cursor.write_all(&[alphabet[(triple & 0x3F) as usize]]).unwrap();
                } else {
                    cursor.write_all(b"=").unwrap();
                }
                i += 3;
            }
        }
        String::from_utf8(buf).unwrap()
    };
    println!("\n--- Base64 values for TypeScript tests ---");
    println!("REAL_RK  = \"{}\"", b64(&rk_bytes));
    println!("REAL_SIG = \"{}\"", b64(&sig_bytes));
    println!("-------------------------------------------");

    println!("RedPallas fixtures generated and validated.");
}

// ---------------------------------------------------------------------------
// Real delegation proof fixture (ZKP #1)
// ---------------------------------------------------------------------------

/// Generate a self-consistent delegation proof fixture for e2e tests.
///
/// This creates a complete "world" (spending key, notes, Merkle tree, IMT,
/// session params) and generates a real Halo2 proof + matching RedPallas
/// signature. The output JSON file contains everything the TypeScript test
/// needs to construct MsgCreateVotingSession and MsgDelegateVote.
fn generate_delegation_fixtures() {
    use ff::Field;
    use incrementalmerkletree::{Hashable, Level};
    use pasta_curves::pallas;
    use rand::rngs::OsRng;

    use orchard::{
        NOTE_COMMITMENT_TREE_DEPTH,
        delegation::{
            builder::{build_delegation_bundle, RealNoteInput},
            imt::{ImtProvider, SpacedLeafImtProvider},
            prove::{create_delegation_proof, verify_delegation_proof},
        },
        keys::{FullViewingKey, Scope, SpendAuthorizingKey, SpendingKey},
        note::{ExtractedNoteCommitment, Note, Rho},
        tree::{MerkleHashOrchard, MerklePath},
        value::NoteValue,
    };

    println!("\n--- Generating delegation fixtures (this may take a few minutes in release mode) ---");

    let mut rng = OsRng;

    // =====================================================================
    // 1. Key material
    // =====================================================================
    let sk = SpendingKey::random(&mut rng);
    let fvk: FullViewingKey = (&sk).into();
    let output_recipient = fvk.address_at(1u32, Scope::External);
    let alpha = pallas::Scalar::random(&mut rng);
    let gov_comm_rand = pallas::Base::random(&mut rng);

    // =====================================================================
    // 2. Build a real note with value >= MIN_WEIGHT (12,500,000)
    // =====================================================================
    let note_value = 15_000_000u64; // safely above MIN_WEIGHT
    let recipient = fvk.address_at(0u32, Scope::External);
    let (_, _, dummy_parent) = Note::dummy(&mut rng, None);
    let note = Note::new(
        recipient,
        NoteValue::from_raw(note_value),
        Rho::from_nf_old(dummy_parent.nullifier(&fvk)),
        &mut rng,
    );

    // =====================================================================
    // 3. Build the Sinsemilla Merkle tree (nc_root)
    //    Place the note at position 0 with 3 empty siblings.
    // =====================================================================
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
    // Convert the root hash to a pallas::Base field element.
    let nc_root_bytes = current.to_bytes();
    let nc_root: pallas::Base = pallas::Base::from_repr(nc_root_bytes).unwrap();

    // Build the Merkle path for the note at position 0.
    let mut auth_path = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];
    auth_path[0] = leaves[1]; // sibling at level 0
    auth_path[1] = l1_1; // sibling at level 1
    for level in 2..NOTE_COMMITMENT_TREE_DEPTH {
        auth_path[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
    }
    let merkle_path = MerklePath::from_parts(0u32, auth_path);

    // =====================================================================
    // 4. Build the IMT (nf_imt_root)
    // =====================================================================
    let imt = SpacedLeafImtProvider::new();
    let nf_imt_root = imt.root();

    let real_nf = note.nullifier(&fvk);
    // Convert nullifier to pallas::Base for IMT non-membership proof.
    let nf_fp: pallas::Base = pallas::Base::from_repr(real_nf.to_bytes()).unwrap();
    let imt_proof = imt.non_membership_proof(nf_fp);

    let note_input = RealNoteInput {
        note,
        fvk: fvk.clone(),
        merkle_path,
        imt_proof,
    };

    // =====================================================================
    // 5. Find session params where vote_round_id is a canonical Pallas Fp
    //    vote_round_id = Blake2b-256(snapshot_height_BE || snapshot_blockhash ||
    //                                 proposals_hash || vote_end_time_BE ||
    //                                 nullifier_imt_root || nc_root)
    // =====================================================================
    let snapshot_blockhash = [0xAAu8; 32];
    let proposals_hash = [0xBBu8; 32];
    // vote_end_time is set to ~2.5 minutes from now so the E2E test can
    // observe the ACTIVE → TALLYING transition without a long idle wait.
    // Run `make fixtures && make test-api` in quick succession.
    let vote_end_time: u64 = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs()
        + 150;

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

        let hash = Blake2bParams::new()
            .hash_length(32)
            .hash(&data);
        let mut repr = [0u8; 32];
        repr.copy_from_slice(hash.as_bytes());

        if let Some(fp) = pallas::Base::from_repr(repr).into() {
            vote_round_id = fp;
            println!("Found canonical vote_round_id at snapshot_height={}", snapshot_height);
            break;
        }
        snapshot_height += 1;
    }

    // =====================================================================
    // 6. Build the delegation bundle (circuit + instance)
    // =====================================================================
    let bundle = build_delegation_bundle(
        vec![note_input],
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        gov_comm_rand,
        &imt,
        &mut rng,
    )
    .expect("build_delegation_bundle should succeed");

    println!("Delegation bundle built. Generating proof (K=14, this takes ~30-60s)...");

    // =====================================================================
    // 7. Generate the real proof
    // =====================================================================
    let proof = create_delegation_proof(bundle.circuit, &bundle.instance);
    println!("Proof generated ({} bytes).", proof.len());

    // Verify the proof roundtrips.
    verify_delegation_proof(&proof, &bundle.instance)
        .expect("generated delegation proof should verify");
    println!("Proof verification succeeded.");

    // =====================================================================
    // 8. Generate the RedPallas signature with the same (ask, alpha)
    // =====================================================================
    let ask = SpendAuthorizingKey::from(&sk);
    let rsk = ask.randomize(&alpha);

    let sighash_full = Blake2bParams::new()
        .hash_length(32)
        .hash(b"ZALLY_SIGHASH_V0");
    let mut sighash = [0u8; 32];
    sighash.copy_from_slice(sighash_full.as_bytes());

    let sig = rsk.sign(&mut rng, &sighash);

    // Serialize rk as 32 compressed bytes.
    let rk = bundle.instance.rk.clone();
    let rk_bytes: [u8; 32] = rk.into();

    // Serialize sig as 64 bytes.
    let sig_bytes: [u8; 64] = (&sig).into();

    // Verify the signature matches.
    rp::verify_spend_auth_sig(&rk_bytes, &sighash, &sig_bytes)
        .expect("generated RedPallas signature should verify with delegation rk");
    println!("RedPallas signature verification succeeded (matching rk).");

    // =====================================================================
    // 9. Serialize the instance fields
    // =====================================================================
    let nf_signed_bytes = bundle.instance.nf_signed.to_bytes();
    let cmx_new_bytes = bundle.instance.cmx_new.to_repr();
    let gov_comm_bytes = bundle.instance.gov_comm.to_repr();
    let vote_round_id_repr = bundle.instance.vote_round_id.to_repr();
    let gov_null_bytes: Vec<[u8; 32]> = bundle.instance.gov_null.iter()
        .map(|g| g.to_repr())
        .collect();

    // =====================================================================
    // 10. Build the JSON fixture
    // =====================================================================
    use std::io::Write;
    let b64 = |bytes: &[u8]| -> String {
        use std::io::Cursor;
        let mut buf = Vec::new();
        {
            let mut cursor = Cursor::new(&mut buf);
            let alphabet = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
            let mut i = 0;
            while i < bytes.len() {
                let b0 = bytes[i] as u32;
                let b1 = if i + 1 < bytes.len() { bytes[i + 1] as u32 } else { 0 };
                let b2 = if i + 2 < bytes.len() { bytes[i + 2] as u32 } else { 0 };
                let triple = (b0 << 16) | (b1 << 8) | b2;
                cursor.write_all(&[alphabet[((triple >> 18) & 0x3F) as usize]]).unwrap();
                cursor.write_all(&[alphabet[((triple >> 12) & 0x3F) as usize]]).unwrap();
                if i + 1 < bytes.len() {
                    cursor.write_all(&[alphabet[((triple >> 6) & 0x3F) as usize]]).unwrap();
                } else {
                    cursor.write_all(b"=").unwrap();
                }
                if i + 2 < bytes.len() {
                    cursor.write_all(&[alphabet[(triple & 0x3F) as usize]]).unwrap();
                } else {
                    cursor.write_all(b"=").unwrap();
                }
                i += 3;
            }
        }
        String::from_utf8(buf).unwrap()
    };

    // Dummy enc_memo (64 bytes of 0x05, matching current TS test).
    let enc_memo = [0x05u8; 64];

    let json = serde_json::json!({
        "proof": b64(&proof),
        "rk": b64(&rk_bytes),
        "spend_auth_sig": b64(&sig_bytes),
        "sighash": b64(&sighash),
        "signed_note_nullifier": b64(nf_signed_bytes.as_ref()),
        "cmx_new": b64(cmx_new_bytes.as_ref()),
        "enc_memo": b64(&enc_memo),
        "gov_comm": b64(gov_comm_bytes.as_ref()),
        "gov_nullifiers": [
            b64(gov_null_bytes[0].as_ref()),
            b64(gov_null_bytes[1].as_ref()),
            b64(gov_null_bytes[2].as_ref()),
            b64(gov_null_bytes[3].as_ref()),
        ],
        "vote_round_id": b64(vote_round_id_repr.as_ref()),
        "session": {
            "snapshot_height": snapshot_height,
            "snapshot_blockhash": b64(&snapshot_blockhash),
            "proposals_hash": b64(&proposals_hash),
            "vote_end_time": vote_end_time,
            "nullifier_imt_root": b64(nf_imt_root_repr.as_ref()),
            "nc_root": b64(nc_root_repr.as_ref()),
        },
    });

    // Write the fixture to tests/api/fixtures/.
    let fixtures_dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("tests/api/fixtures");
    fs::create_dir_all(&fixtures_dir).expect("failed to create fixtures directory");

    let fixture_path = fixtures_dir.join("delegation_real.json");
    let json_str = serde_json::to_string_pretty(&json).unwrap();
    fs::write(&fixture_path, &json_str).expect("failed to write delegation fixture");
    println!(
        "Wrote delegation fixture ({} bytes) to {}",
        json_str.len(),
        fixture_path.display()
    );

    // Also print key values for debugging.
    println!("\n--- Delegation fixture summary ---");
    println!("snapshot_height: {}", snapshot_height);
    println!("proof size: {} bytes", proof.len());
    println!("rk: {}", b64(&rk_bytes));
    println!("nf_signed: {}", b64(nf_signed_bytes.as_ref()));
    println!("cmx_new: {}", b64(cmx_new_bytes.as_ref()));
    println!("gov_comm: {}", b64(gov_comm_bytes.as_ref()));
    println!("vote_round_id: {}", b64(vote_round_id_repr.as_ref()));
    println!("nc_root: {}", b64(nc_root_repr.as_ref()));
    println!("nf_imt_root: {}", b64(nf_imt_root_repr.as_ref()));
    println!("-----------------------------------");

    println!("Delegation fixtures generated and validated.");
}
