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
//!
//! Delegation fixture (ZKP #1) is no longer generated here; the Rust E2E tests
//! build the delegation bundle inline via e2e_tests::setup::build_delegation_bundle_for_test.

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

/// Canonical delegation sighash domain. Must match sdk/x/vote/types/sighash.go
/// and e2e-tests/src/setup.rs.
const DELEGATION_SIGHASH_DOMAIN: &[u8] = b"ZALLY_DELEGATION_SIGHASH_V0";

/// Build the canonical signable payload for the RedPallas test message used in
/// validate_redpallas_test.go: vote_round_id = 32×0x01, rk = given, rest zeros,
/// gov_nullifiers = 4×32 zero bytes.
fn canonical_delegation_payload_for_fixture(rk_bytes: &[u8; 32]) -> Vec<u8> {
    let mut out = Vec::with_capacity(
        DELEGATION_SIGHASH_DOMAIN.len() + 32 + 32 + 32 + 32 + 64 + 32 + 4 * 32,
    );
    out.extend_from_slice(DELEGATION_SIGHASH_DOMAIN);
    out.extend_from_slice(&[0x01u8; 32]); // testRoundID in Go
    out.extend_from_slice(rk_bytes);
    out.extend_from_slice(&[0u8; 32]); // signed_note_nullifier
    out.extend_from_slice(&[0u8; 32]); // cmx_new
    out.extend_from_slice(&[0u8; 64]); // enc_memo
    out.extend_from_slice(&[0u8; 32]); // van_comm
    for _ in 0..4 {
        out.extend_from_slice(&[0u8; 32]); // gov_nullifiers (4 slots)
    }
    out
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
    let rk_bytes: [u8; 32] = vk.into();

    // Sighash = Blake2b-256(canonical delegation payload). Must match the
    // message built in validate_redpallas_test.go so the chain's
    // ComputeDelegationSighash(msg) equals this value.
    let canonical = canonical_delegation_payload_for_fixture(&rk_bytes);
    let sighash_full = Blake2bParams::new().hash_length(32).hash(&canonical);
    let mut sighash = [0u8; 32];
    sighash.copy_from_slice(sighash_full.as_bytes());

    // Sign the sighash.
    let sig = sk.sign(&mut rng, &sighash);
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

    println!("RedPallas delegation fixtures generated and validated.");

    // --- CastVote fixtures ---
    generate_cast_vote_redpallas_fixtures(&testdata_dir, &mut rng);
}

/// Canonical cast vote sighash domain. Must match sdk/x/vote/types/sighash.go.
const CAST_VOTE_SIGHASH_DOMAIN: &[u8] = b"ZALLY_CAST_VOTE_SIGHASH_V0";

/// Build the canonical signable payload for the RedPallas CastVote test message
/// used in validate_redpallas_test.go: vote_round_id = 32×0x01, r_vpk = given,
/// rest zeros except proposal_id = 1 (LE, padded to 32) and anchor_height = 10 (LE, padded to 32).
fn canonical_cast_vote_payload_for_fixture(r_vpk_bytes: &[u8; 32]) -> Vec<u8> {
    let mut out = Vec::with_capacity(
        CAST_VOTE_SIGHASH_DOMAIN.len() + 7 * 32, // domain + 7 fields of 32 bytes each
    );
    out.extend_from_slice(CAST_VOTE_SIGHASH_DOMAIN);
    out.extend_from_slice(&[0x01u8; 32]); // vote_round_id (testRoundID in Go)
    out.extend_from_slice(r_vpk_bytes);    // r_vpk (compressed)
    out.extend_from_slice(&[0u8; 32]);     // van_nullifier
    out.extend_from_slice(&[0u8; 32]);     // vote_authority_note_new
    out.extend_from_slice(&[0u8; 32]);     // vote_commitment
    // proposal_id: 1 as 4 bytes LE, padded to 32 bytes
    let mut pid_buf = [0u8; 32];
    pid_buf[..4].copy_from_slice(&1u32.to_le_bytes());
    out.extend_from_slice(&pid_buf);
    // anchor_height: 10 as 8 bytes LE, padded to 32 bytes
    let mut ah_buf = [0u8; 32];
    ah_buf[..8].copy_from_slice(&10u64.to_le_bytes());
    out.extend_from_slice(&ah_buf);
    out
}

/// Generate RedPallas SpendAuth signature fixtures for CastVote.
fn generate_cast_vote_redpallas_fixtures(
    testdata_dir: &std::path::Path,
    rng: &mut (impl rand::RngCore + rand::CryptoRng),
) {
    // Generate a signing key and derive the verification key (r_vpk).
    let sk = SigningKey::<reddsa_orchard::SpendAuth>::new(&mut *rng);
    let vk = VerificationKey::from(&sk);
    let r_vpk_bytes: [u8; 32] = vk.into();

    // Sighash = Blake2b-256(canonical cast vote payload).
    let canonical = canonical_cast_vote_payload_for_fixture(&r_vpk_bytes);
    let sighash_full = Blake2bParams::new().hash_length(32).hash(&canonical);
    let mut sighash = [0u8; 32];
    sighash.copy_from_slice(sighash_full.as_bytes());

    // Sign the sighash.
    let sig = sk.sign(&mut *rng, &sighash);
    let sig_bytes: [u8; 64] = sig.into();

    // Write CastVote fixtures.
    let r_vpk_path = testdata_dir.join("cast_vote_r_vpk.bin");
    fs::write(&r_vpk_path, &r_vpk_bytes).expect("failed to write cast_vote r_vpk fixture");
    println!("Wrote cast_vote r_vpk ({} bytes) to {}", r_vpk_bytes.len(), r_vpk_path.display());

    let sighash_path = testdata_dir.join("cast_vote_sighash.bin");
    fs::write(&sighash_path, &sighash).expect("failed to write cast_vote sighash fixture");
    println!("Wrote cast_vote sighash ({} bytes) to {}", sighash.len(), sighash_path.display());

    let sig_path = testdata_dir.join("cast_vote_sig.bin");
    fs::write(&sig_path, &sig_bytes).expect("failed to write cast_vote sig fixture");
    println!("Wrote cast_vote sig ({} bytes) to {}", sig_bytes.len(), sig_path.display());

    // Verify the generated fixtures work.
    assert!(
        rp::verify_spend_auth_sig(&r_vpk_bytes, &sighash, &sig_bytes).is_ok(),
        "CastVote valid signature should verify"
    );

    println!("RedPallas CastVote fixtures generated and validated.");
}
