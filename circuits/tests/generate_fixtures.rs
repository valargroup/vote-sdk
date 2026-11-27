//! Integration test that generates fixture files for Go and TypeScript tests.
//!
//! Run with: cargo test --release -- generate_fixtures --ignored --nocapture
//!
//! Generates:
//!   ffi/zkp/testdata/toy_valid_proof.bin      - valid Halo2 proof bytes
//!   ffi/zkp/testdata/toy_valid_input.bin      - correct public input (Fp, 32-byte LE)
//!   ffi/zkp/testdata/toy_wrong_input.bin      - wrong public input for negative tests
//!   ffi/zkp/testdata/vote_max_proposal.bin    - real ZKP #2 proof and public inputs for proposal 50
//!   ffi/zkp/testdata/proxy_delegation_*.bin   - real ZKP #4 proofs and public inputs (see
//!                                               `build_proxy_delegation_fixtures`)
//!   ffi/redpallas/testdata/valid_rk.bin       - 32-byte RedPallas verification key
//!   ffi/redpallas/testdata/valid_sighash.bin  - 32-byte sighash (message)
//!   ffi/redpallas/testdata/valid_sig.bin      - 64-byte valid RedPallas signature
//!   ffi/redpallas/testdata/wrong_sig.bin      - 64-byte signature over wrong message
//!
//! Delegation fixture (ZKP #1) is no longer generated here; the Rust E2E tests
//! build the delegation bundle inline via e2e_tests::setup::build_delegation_bundle_for_test.

use std::fs;
use std::path::Path;
use voting_crypto_deps::pasta_curves::group::ff::PrimeField;

use blake2b_simd::Params as Blake2bParams;
use voting_crypto_deps::reddsa::{orchard as reddsa_orchard, SigningKey, VerificationKey};

use shielded_vote_circuits::redpallas as rp;
use shielded_vote_circuits::toy;

/// Generate fixture files for Go and TypeScript tests.
///
/// Marked `#[ignore]` so it only runs when explicitly requested
/// (e.g., `cargo test --release -- generate_fixtures --ignored`).
/// This avoids regenerating fixtures on every `cargo test`.
#[test]
#[ignore]
fn generate_fixtures() {
    generate_halo2_fixtures();
    write_vote_max_proposal_fixture();
    write_proxy_delegation_fixtures();
    generate_redpallas_fixtures();
    generate_share_reveal_fixtures();
    println!("\nAll fixtures generated and validated successfully.");
}

/// Regenerate only the proposal-50 ZKP #2 fixture used by Go FFI and ante tests.
#[test]
#[ignore]
fn generate_vote_max_proposal_fixture() {
    write_vote_max_proposal_fixture();
}

/// Generate and verify a proposal-50 ZKP #2 proof in the normal CI test suite.
#[test]
fn vote_max_proposal_proves_and_verifies() {
    build_vote_max_proposal_fixture();
}

/// Regenerate only the ZKP #4 fixtures used by the Go FFI tests.
#[test]
#[ignore]
fn generate_proxy_delegation_fixtures() {
    write_proxy_delegation_fixtures();
}

/// Generate and verify the ZKP #4 fixture proofs in the normal CI test suite.
#[test]
fn proxy_delegation_fixtures_prove_and_verify() {
    build_proxy_delegation_fixtures();
}

/// Generate Halo2 toy circuit proof fixtures.
fn generate_halo2_fixtures() {
    let testdata_dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("ffi/zkp/testdata");

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
    fs::write(&input_path, &c_bytes[..]).expect("failed to write input fixture");
    println!(
        "Wrote valid input ({} bytes) to {}",
        c_bytes.len(),
        input_path.display()
    );

    // Write wrong public input (c = 999, which does not match any valid (a,b) for constant=7).
    use voting_crypto_deps::halo2_proofs::pasta::Fp;
    let wrong_c = Fp::from(999u64);
    let wrong_bytes = wrong_c.to_repr();
    let wrong_path = testdata_dir.join("toy_wrong_input.bin");
    fs::write(&wrong_path, &wrong_bytes[..]).expect("failed to write wrong input fixture");
    println!(
        "Wrote wrong input ({} bytes) to {}",
        wrong_bytes.len(),
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
        .join("ffi/redpallas/testdata");

    fs::create_dir_all(&testdata_dir).expect("failed to create redpallas testdata directory");

    let mut rng = voting_crypto_deps::rand::rngs::OsRng;

    // Generate a signing key and derive the verification key (rk).
    let sk = SigningKey::<reddsa_orchard::SpendAuth>::new(&mut rng);
    let vk = VerificationKey::from(&sk);
    let rk_bytes: [u8; 32] = vk.into();

    // Sighash: arbitrary 32 bytes. The chain only checks len==32 and verifies
    // the RedPallas sig over it. In production this is the ZIP-244 sighash
    // extracted from the governance PCZT.
    let sighash = [0x42u8; 32];

    // Sign the sighash.
    let sig = sk.sign(&mut rng, &sighash);
    let sig_bytes: [u8; 64] = sig.into();

    // Write valid rk.
    let rk_path = testdata_dir.join("valid_rk.bin");
    fs::write(&rk_path, &rk_bytes).expect("failed to write rk fixture");
    println!(
        "Wrote valid rk ({} bytes) to {}",
        rk_bytes.len(),
        rk_path.display()
    );

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
                let b1 = if i + 1 < bytes.len() {
                    bytes[i + 1] as u32
                } else {
                    0
                };
                let b2 = if i + 2 < bytes.len() {
                    bytes[i + 2] as u32
                } else {
                    0
                };
                let triple = (b0 << 16) | (b1 << 8) | b2;
                cursor
                    .write_all(&[alphabet[((triple >> 18) & 0x3F) as usize]])
                    .unwrap();
                cursor
                    .write_all(&[alphabet[((triple >> 12) & 0x3F) as usize]])
                    .unwrap();
                if i + 1 < bytes.len() {
                    cursor
                        .write_all(&[alphabet[((triple >> 6) & 0x3F) as usize]])
                        .unwrap();
                } else {
                    cursor.write_all(b"=").unwrap();
                }
                if i + 2 < bytes.len() {
                    cursor
                        .write_all(&[alphabet[(triple & 0x3F) as usize]])
                        .unwrap();
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
    generate_cast_vote_redpallas_fixtures(&testdata_dir);
}

/// Canonical cast vote sighash domain. Must match sdk/x/vote/types/sighash.go.
const CAST_VOTE_SIGHASH_DOMAIN: &[u8] = b"SVOTE_CAST_VOTE_SIGHASH_V0";

/// Build the canonical signable payload for the RedPallas CastVote test message
/// used in validate_redpallas_test.go: vote_round_id = 32×0x01, r_vpk = given,
/// rest zeros except proposal_id = 1 (LE, padded to 32) and anchor_height = 10 (LE, padded to 32).
fn canonical_cast_vote_payload_for_fixture(r_vpk_bytes: &[u8; 32]) -> Vec<u8> {
    let mut out = Vec::with_capacity(
        CAST_VOTE_SIGHASH_DOMAIN.len() + 7 * 32, // domain + 7 fields of 32 bytes each
    );
    out.extend_from_slice(CAST_VOTE_SIGHASH_DOMAIN);
    out.extend_from_slice(&[0x01u8; 32]); // vote_round_id (testRoundID in Go)
    out.extend_from_slice(r_vpk_bytes); // r_vpk (compressed)
    out.extend_from_slice(&[0u8; 32]); // van_nullifier
    out.extend_from_slice(&[0u8; 32]); // vote_authority_note_new
    out.extend_from_slice(&[0u8; 32]); // vote_commitment
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
fn generate_cast_vote_redpallas_fixtures(testdata_dir: &std::path::Path) {
    let mut rng = voting_crypto_deps::rand::rngs::OsRng;

    // Generate a signing key and derive the verification key (r_vpk).
    let sk = SigningKey::<reddsa_orchard::SpendAuth>::new(&mut rng);
    let vk = VerificationKey::from(&sk);
    let r_vpk_bytes: [u8; 32] = vk.into();

    // Sighash = Blake2b-256(canonical cast vote payload).
    let canonical = canonical_cast_vote_payload_for_fixture(&r_vpk_bytes);
    let sighash_full = Blake2bParams::new().hash_length(32).hash(&canonical);
    let mut sighash = [0u8; 32];
    sighash.copy_from_slice(sighash_full.as_bytes());

    // Sign the sighash.
    let sig = sk.sign(&mut rng, &sighash);
    let sig_bytes: [u8; 64] = sig.into();

    // Write CastVote fixtures.
    let r_vpk_path = testdata_dir.join("cast_vote_r_vpk.bin");
    fs::write(&r_vpk_path, &r_vpk_bytes).expect("failed to write cast_vote r_vpk fixture");
    println!(
        "Wrote cast_vote r_vpk ({} bytes) to {}",
        r_vpk_bytes.len(),
        r_vpk_path.display()
    );

    let sighash_path = testdata_dir.join("cast_vote_sighash.bin");
    fs::write(&sighash_path, &sighash).expect("failed to write cast_vote sighash fixture");
    println!(
        "Wrote cast_vote sighash ({} bytes) to {}",
        sighash.len(),
        sighash_path.display()
    );

    let sig_path = testdata_dir.join("cast_vote_sig.bin");
    fs::write(&sig_path, &sig_bytes).expect("failed to write cast_vote sig fixture");
    println!(
        "Wrote cast_vote sig ({} bytes) to {}",
        sig_bytes.len(),
        sig_path.display()
    );

    // Verify the generated fixtures work.
    assert!(
        rp::verify_spend_auth_sig(&r_vpk_bytes, &sighash, &sig_bytes).is_ok(),
        "CastVote valid signature should verify"
    );

    println!("RedPallas CastVote fixtures generated and validated.");
}

/// Generate a real ZKP #2 proof at the maximum proposal ID.
///
/// Binary layout:
///   magic[8] || proof_len(u32 LE) || proof || van_nullifier[32] ||
///   r_vpk[32] || vote_authority_note_new[32] || vote_commitment[32] ||
///   vote_comm_tree_root[32] || anchor_height(u64 LE) || proposal_id(u32 LE) ||
///   voting_round_id[32] || ea_pk[32]
fn build_vote_max_proposal_fixture() -> Vec<u8> {
    use voting_circuits::vote_proof::{build_vote_proof_from_delegation, verify_vote_proof};
    use voting_crypto_deps::orchard::keys::SpendingKey;
    use voting_crypto_deps::pasta_curves::{
        group::{Curve, GroupEncoding},
        pallas,
    };

    const MAGIC: &[u8; 8] = b"SVZKP2M1";
    const ANCHOR_HEIGHT: u64 = 123;
    const PROPOSAL_ID: u32 = 50;

    let sk = SpendingKey::from_bytes([0x42; 32]).expect("valid test spending key");
    let ea_pk = (voting_circuits::spend_auth_g_affine() * pallas::Scalar::from(42u64)).to_affine();
    let voting_round_id = pallas::Base::from(0xCAFE_u64);
    let bundle = build_vote_proof_from_delegation(
        &sk,
        1,
        12_500_000,
        pallas::Base::from(0xDEAD_u64),
        voting_round_id,
        [pallas::Base::zero(); voting_circuits::VOTE_COMM_TREE_DEPTH],
        0,
        ANCHOR_HEIGHT as u32,
        u64::from(PROPOSAL_ID),
        1,
        ea_pk,
        pallas::Scalar::from(7u64),
        voting_circuits::MAX_PROPOSAL_AUTHORITY,
        true,
    )
    .expect("proposal-50 vote proof should build");
    verify_vote_proof(&bundle.proof, &bundle.instance)
        .expect("proposal-50 vote proof should verify");

    let mut fixture = Vec::with_capacity(12 + bundle.proof.len() + 236);
    fixture.extend_from_slice(MAGIC);
    fixture.extend_from_slice(&(bundle.proof.len() as u32).to_le_bytes());
    fixture.extend_from_slice(&bundle.proof);
    fixture.extend_from_slice(&bundle.instance.van_nullifier.to_repr());
    fixture.extend_from_slice(&bundle.r_vpk_bytes);
    fixture.extend_from_slice(&bundle.instance.vote_authority_note_new.to_repr());
    fixture.extend_from_slice(&bundle.instance.vote_commitment.to_repr());
    fixture.extend_from_slice(&bundle.instance.vote_comm_tree_root.to_repr());
    fixture.extend_from_slice(&ANCHOR_HEIGHT.to_le_bytes());
    fixture.extend_from_slice(&PROPOSAL_ID.to_le_bytes());
    fixture.extend_from_slice(&bundle.instance.voting_round_id.to_repr());
    fixture.extend_from_slice(ea_pk.to_bytes().as_ref());

    fixture
}

fn write_vote_max_proposal_fixture() {
    let fixture = build_vote_max_proposal_fixture();
    let fixture_path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("ffi/zkp/testdata/vote_max_proposal.bin");
    fs::write(&fixture_path, &fixture).expect("failed to write proposal-50 vote proof fixture");
    println!(
        "Wrote proposal-50 vote proof fixture ({} bytes) to {}",
        fixture.len(),
        fixture_path.display()
    );
}

/// Generate share reveal (ZKP #3) proof generation fixture for the Go round-trip test.
///
/// Writes all inputs needed by `halo2.GenerateShareRevealProof()` to a single
/// binary file so the Go test can call generate + verify without needing Poseidon
/// in Go.
///
/// Format (1488 bytes total):
///   [0..772)       merkle_path (772 bytes)
///   [772..1284)    share_comms (512 bytes: 16 × 32-byte commitments)
///   [1284..1316)   primary_blind (32 bytes)
///   [1316..1348)   enc_c1 (32 bytes, compressed Pallas point — C1)
///   [1348..1380)   enc_c2 (32 bytes, compressed Pallas point — C2)
///   [1380..1384)   share_index (u32 LE)
///   [1384..1388)   proposal_id (u32 LE)
///   [1388..1392)   vote_decision (u32 LE)
///   [1392..1424)   round_id (32 bytes)
///   [1424..1488)   enc_share (64 bytes: C1 compressed || C2 compressed Pallas points)
fn generate_share_reveal_fixtures() {
    use shielded_vote_circuits::ffi::build_share_reveal_test_data;

    let testdata_dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("ffi/zkp/testdata");

    fs::create_dir_all(&testdata_dir).expect("failed to create testdata directory");

    let (
        merkle_path,
        share_comms,
        primary_blind,
        enc_c1_compressed,
        enc_c2_compressed,
        enc_share,
        share_index,
        proposal_id,
        vote_decision,
        round_id,
    ) = build_share_reveal_test_data();

    let mut fixture: Vec<u8> = Vec::with_capacity(1488);
    fixture.extend_from_slice(&merkle_path); // 772 bytes
    fixture.extend_from_slice(&share_comms); // 512 bytes
    fixture.extend_from_slice(&primary_blind); // 32 bytes
    fixture.extend_from_slice(&enc_c1_compressed); // 32 bytes
    fixture.extend_from_slice(&enc_c2_compressed); // 32 bytes
    fixture.extend_from_slice(&share_index.to_le_bytes()); // 4 bytes
    fixture.extend_from_slice(&proposal_id.to_le_bytes()); // 4 bytes
    fixture.extend_from_slice(&vote_decision.to_le_bytes()); // 4 bytes
    fixture.extend_from_slice(&round_id); // 32 bytes
    fixture.extend_from_slice(&enc_share); // 64 bytes
    assert_eq!(fixture.len(), 1488);

    let fixture_path = testdata_dir.join("share_reveal_inputs.bin");
    fs::write(&fixture_path, &fixture).expect("failed to write share reveal fixture");
    println!(
        "Wrote share reveal inputs ({} bytes) to {}",
        fixture.len(),
        fixture_path.display()
    );

    println!("Share reveal fixtures generated.");
}

// ---------------------------------------------------------------------------
// Proxy delegation (ZKP #4) fixtures
// ---------------------------------------------------------------------------

/// Magic prefix of the ZKP #4 fixtures.
const PROXY_DELEGATION_MAGIC: &[u8; 8] = b"SVZKP4D1";

/// Zatoshi per ballot, the weight unit of a VAN.
const BALLOT_ZATOSHI: u64 = 12_500_000;

/// The batch anchor height of the anchored and single-share fixtures.
const PROXY_BATCH_ANCHOR_HEIGHT: u64 = 1_234;

/// The inputs of one fixture delegation. The spending key, address index 1,
/// round and election authority key are those of [`proxy_fixture_context`].
struct DelegationSpec {
    /// The spent VAN's weight and randomness; the VAN has full authority, as
    /// ZKP #1 and ZKP #4 leave it.
    van_ballots: u64,
    van_comm_rand: u64,
    /// Leaves around the spent VAN in the chain's vote commitment tree. With
    /// none, the root is the chain's `SingleLeafRoot` of the VAN.
    leaves_before: Vec<u64>,
    leaves_after: Vec<u64>,
    anchor_height: u64,
    delegate_index: u32,
    delegated_ballots: u64,
    dc_slot: u8,
    dc_seed: [u8; 32],
    layout: voting_circuits::proxy_delegation::ShareLayout,
    alpha_v: u64,
}

/// A fixture delegation, proved.
struct ProvedDelegation {
    bundle: voting_circuits::proxy_delegation::ProxyDelegationBundle,
    proof: Vec<u8>,
    anchor_height: u64,
    ea_pk: [u8; 32],
    spent_van: voting_crypto_deps::pasta_curves::pallas::Base,
}

/// The spending key, round and election authority key of the ZKP #4 fixtures.
fn proxy_fixture_context() -> (
    voting_crypto_deps::orchard::keys::SpendingKey,
    voting_crypto_deps::pasta_curves::pallas::Base,
    voting_crypto_deps::pasta_curves::pallas::Affine,
) {
    use voting_crypto_deps::pasta_curves::{group::Curve, pallas};

    (
        voting_crypto_deps::orchard::keys::SpendingKey::from_bytes([0x42; 32])
            .expect("valid test spending key"),
        pallas::Base::from(0xCAFE_u64),
        (voting_circuits::spend_auth_g_affine() * pallas::Scalar::from(42u64)).to_affine(),
    )
}

/// Builds and proves `spec`, placing the spent VAN in the chain's vote
/// commitment tree (`vote-commitment-tree`, as the chain builds it) and
/// checking that the circuit's root is that tree's root.
fn prove_proxy_delegation(spec: &DelegationSpec) -> ProvedDelegation {
    use shielded_vote_circuits::votetree;
    use voting_circuits::proxy_delegation::{
        build_proxy_delegation_proof, create_proxy_delegation_proof,
    };
    use voting_circuits::vote_proof::derive_vote_authority_transition;
    use voting_crypto_deps::pasta_curves::{group::GroupEncoding, pallas};

    let (sk, voting_round_id, ea_pk) = proxy_fixture_context();
    let van_comm_rand = pallas::Base::from(spec.van_comm_rand);
    // ZKP #2's VAN derivation, for any proposal, gives the full-authority VAN.
    let spent_van = derive_vote_authority_transition(
        &sk,
        1,
        spec.van_ballots * BALLOT_ZATOSHI,
        van_comm_rand,
        voting_round_id,
        1,
        voting_circuits::MAX_PROPOSAL_AUTHORITY,
    )
    .expect("full-authority VAN")
    .vote_authority_note_old;

    let leaves: Vec<u8> = spec
        .leaves_before
        .iter()
        .map(|&leaf| pallas::Base::from(leaf))
        .chain([spent_van])
        .chain(
            spec.leaves_after
                .iter()
                .map(|&leaf| pallas::Base::from(leaf)),
        )
        .flat_map(|leaf| leaf.to_repr())
        .collect();
    let (leaf_count, position) = (leaves.len() / 32, spec.leaves_before.len());
    let (path_bytes, root) = unsafe {
        (
            votetree::compute_path_from_raw(leaves.as_ptr(), leaf_count, position as u64)
                .expect("tree path"),
            votetree::compute_root_from_raw(leaves.as_ptr(), leaf_count).expect("tree root"),
        )
    };
    // The path is a 4-byte position, then one 32-byte sibling per level.
    let path = core::array::from_fn(|level| {
        let sibling = &path_bytes[4 + level * 32..4 + (level + 1) * 32];
        Option::from(pallas::Base::from_repr(sibling.try_into().unwrap()))
            .expect("canonical sibling")
    });

    let bundle = build_proxy_delegation_proof(
        &sk,
        1,
        spec.van_ballots,
        van_comm_rand,
        voting_round_id,
        path,
        position as u32,
        spec.anchor_height as u32,
        ea_pk,
        pallas::Scalar::from(spec.alpha_v),
        spec.delegate_index,
        spec.delegated_ballots,
        spec.dc_slot,
        spec.dc_seed,
        spec.layout,
    )
    .expect("valid proxy delegation inputs");
    assert_eq!(
        bundle.instance.vote_comm_tree_root.to_repr(),
        root,
        "the circuit's root must be the chain tree's root"
    );
    let proof = create_proxy_delegation_proof(bundle.circuit.clone(), &bundle.instance)
        .expect("proxy delegation proof");
    ProvedDelegation {
        bundle,
        proof,
        anchor_height: spec.anchor_height,
        ea_pk: ea_pk.to_bytes(),
        spent_van,
    }
}

/// The 288-byte `sv_verify_proxy_delegation_proof` public inputs of `d`.
fn proxy_delegation_wire(d: &ProvedDelegation) -> Vec<u8> {
    let instance = &d.bundle.instance;
    let mut anchor = [0u8; 32];
    anchor[..8].copy_from_slice(&d.anchor_height.to_le_bytes());
    [
        instance.van_nullifier.to_repr(),
        d.bundle.r_vpk_bytes,
        instance.vote_authority_note_new.to_repr(),
        instance.delegation_commitment.to_repr(),
        instance.vote_comm_tree_root.to_repr(),
        anchor,
        instance.voting_round_id.to_repr(),
        d.ea_pk,
        instance.dc_slot_nullifier.to_repr(),
    ]
    .concat()
}

/// Checks `d` through the FFI: its proof verifies, and fails once its
/// commitment changes.
fn check_proxy_delegation_ffi(d: &ProvedDelegation) {
    use shielded_vote_circuits::ffi::sv_verify_proxy_delegation_proof;

    let verify = |wire: &[u8]| unsafe {
        sv_verify_proxy_delegation_proof(d.proof.as_ptr(), d.proof.len(), wire.as_ptr(), wire.len())
    };
    let mut wire = proxy_delegation_wire(d);
    assert_eq!(verify(&wire), 0, "the FFI must accept the fixture proof");
    wire[3 * 32] ^= 1;
    assert_eq!(verify(&wire), -2, "a changed commitment must fail");
}

/// Builds the ZKP #4 fixtures as `(file name, bytes)`, each proved and
/// checked through `sv_verify_proxy_delegation_proof`:
/// - `proxy_delegation_anchored.bin`: a batch's first DC, spending a VAN at
///   position 2 of a four-leaf tree, at anchor height 1,234.
/// - `proxy_delegation_registration.bin`: a DC after a registration, at anchor
///   0 against `SingleLeafRoot` of the registration's VAN.
/// - `proxy_delegation_single_share.bin`: the anchored batch's second DC, in
///   the single-share layout, spending the first DC's successor VAN against
///   its `SingleLeafRoot` at the batch's anchor height.
///
/// Binary layout, the FFI's slots with an 8-byte anchor height, then the VAN
/// the proof spends, which is not a public input:
///   magic[8] || proof_len(u32 LE) || proof || van_nullifier[32] ||
///   r_vpk[32] || vote_authority_note_new[32] || proxy_delegation_commitment[32] ||
///   vote_comm_tree_root[32] || anchor_height(u64 LE) || voting_round_id[32] ||
///   ea_pk[32] || dc_slot_nullifier[32] || spent_van[32]
fn build_proxy_delegation_fixtures() -> Vec<(&'static str, Vec<u8>)> {
    use voting_circuits::proxy_delegation::ShareLayout;

    let anchored = prove_proxy_delegation(&DelegationSpec {
        van_ballots: 10_000,
        van_comm_rand: 0xDEAD,
        leaves_before: vec![1_001, 1_002],
        leaves_after: vec![1_004],
        anchor_height: PROXY_BATCH_ANCHOR_HEIGHT,
        delegate_index: 2,
        delegated_ballots: 4_800,
        dc_slot: 0,
        dc_seed: [0xA1; 32],
        layout: ShareLayout::Standard,
        alpha_v: 7,
    });
    let registration = prove_proxy_delegation(&DelegationSpec {
        van_ballots: 25_000,
        van_comm_rand: 0xBEEF,
        leaves_before: vec![],
        leaves_after: vec![],
        anchor_height: 0,
        delegate_index: 1,
        delegated_ballots: 12_345,
        dc_slot: 0,
        dc_seed: [0xB2; 32],
        layout: ShareLayout::Standard,
        alpha_v: 8,
    });
    let single_share = prove_proxy_delegation(&DelegationSpec {
        van_ballots: 10_000 - 4_800,
        van_comm_rand: 0xDEAD,
        leaves_before: vec![],
        leaves_after: vec![],
        anchor_height: PROXY_BATCH_ANCHOR_HEIGHT,
        delegate_index: 5,
        delegated_ballots: 1_000,
        dc_slot: 1,
        dc_seed: [0xC3; 32],
        layout: ShareLayout::SingleShare,
        alpha_v: 9,
    });
    assert_eq!(
        single_share.spent_van, anchored.bundle.instance.vote_authority_note_new,
        "the second DC must spend the first DC's successor VAN"
    );

    [
        ("proxy_delegation_anchored.bin", anchored),
        ("proxy_delegation_registration.bin", registration),
        ("proxy_delegation_single_share.bin", single_share),
    ]
    .into_iter()
    .map(|(name, d)| {
        check_proxy_delegation_ffi(&d);
        let wire = proxy_delegation_wire(&d);
        let fixture = [
            PROXY_DELEGATION_MAGIC.as_slice(),
            &(d.proof.len() as u32).to_le_bytes(),
            &d.proof,
            &wire[..5 * 32],
            &d.anchor_height.to_le_bytes(),
            &wire[6 * 32..],
            &d.spent_van.to_repr(),
        ]
        .concat();
        (name, fixture)
    })
    .collect()
}

/// Write the ZKP #4 fixtures to `ffi/zkp/testdata/`.
fn write_proxy_delegation_fixtures() {
    let testdata_dir = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("ffi/zkp/testdata");
    for (name, fixture) in build_proxy_delegation_fixtures() {
        let path = testdata_dir.join(name);
        fs::write(&path, &fixture).expect("failed to write proxy delegation fixture");
        println!(
            "Wrote proxy delegation fixture ({} bytes) to {}",
            fixture.len(),
            path.display()
        );
    }
}
