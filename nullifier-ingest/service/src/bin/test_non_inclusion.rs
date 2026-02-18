use std::env;
use std::path::Path;

use anyhow::Result;
use ff::PrimeField as _;
use pasta_curves::Fp;

use nullifier_service::file_store;
use nullifier_service::tree_db;

fn main() -> Result<()> {
    let data_dir = env::var("DATA_DIR").unwrap_or_else(|_| ".".to_string());
    let dir = Path::new(&data_dir);

    println!("Data directory: {}", dir.display());

    // ── 1. Build the nullifier tree ────────────────────────────────────
    println!("Building NullifierTree from flat file...");
    let tree = tree_db::tree_from_file(dir)?;
    println!(
        "  Tree built: {} ranges, root = 0x{}",
        tree.len(),
        hex::encode(tree.root().to_repr())
    );

    // ── 2. Load a raw nullifier for testing ────────────────────────────
    let all_nfs = file_store::load_all_nullifiers(dir)?;
    assert!(
        !all_nfs.is_empty(),
        "No nullifiers found in {}",
        dir.display()
    );
    let existing_nf = all_nfs[0];

    // ══════════════════════════════════════════════════════════════════════
    //  TEST 1: Non-existing nullifier  →  exclusion proof SHOULD succeed
    // ══════════════════════════════════════════════════════════════════════
    println!("\n── TEST 1: Non-inclusion proof for a NON-EXISTING value ──");

    let test_value = Fp::zero();
    println!("  Test value: 0x{}", hex::encode(test_value.to_repr()));

    let proof = tree
        .prove(test_value)
        .expect("BUG: Fp::zero() was not found in any gap range — unexpected");

    println!(
        "  Found in range: [0x{}..0x{}]",
        hex::encode(proof.low.to_repr()),
        hex::encode(proof.high.to_repr())
    );
    assert!(test_value >= proof.low && test_value <= proof.high);
    assert!(
        proof.verify(test_value),
        "Exclusion proof did not verify"
    );
    println!(
        "  Merkle path verified (position {})",
        proof.leaf_pos
    );
    println!("  PASS: Non-inclusion proof SUCCEEDED");

    // ══════════════════════════════════════════════════════════════════════
    //  TEST 2: Existing nullifier  →  exclusion proof SHOULD fail
    // ══════════════════════════════════════════════════════════════════════
    println!("\n── TEST 2: Non-inclusion proof for an EXISTING nullifier ──");

    println!(
        "  Existing nullifier: 0x{}",
        hex::encode(existing_nf.to_repr())
    );

    assert!(
        tree.prove(existing_nf).is_none(),
        "BUG: existing nullifier was found inside a gap range!"
    );
    println!("  PASS: Existing nullifier correctly NOT found in any gap range");

    // ══════════════════════════════════════════════════════════════════════
    //  TEST 3: Another non-existing value (middle of a later range)
    // ══════════════════════════════════════════════════════════════════════
    println!("\n── TEST 3: Non-inclusion proof for a value in a later gap range ──");

    let mid_range = tree.len() / 2;
    let [mid_low, _] = tree.ranges()[mid_range];
    let test_value_2 = mid_low + Fp::one();
    println!(
        "  Test value: 0x{} (low+1 of range {})",
        hex::encode(test_value_2.to_repr()),
        mid_range
    );

    let proof2 = tree
        .prove(test_value_2)
        .expect("BUG: test value in middle of a gap range was not found");

    assert!(test_value_2 >= proof2.low && test_value_2 <= proof2.high);
    assert!(
        proof2.verify(test_value_2),
        "Exclusion proof did not verify for range {}",
        mid_range
    );
    println!(
        "  PASS: Non-inclusion proof SUCCEEDED for range {}",
        mid_range
    );

    println!("\n== All tests passed ==");
    Ok(())
}
