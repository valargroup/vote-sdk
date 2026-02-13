use std::env;

use anyhow::Result;
use ff::PrimeField as _;
use pasta_curves::Fp;
use rusqlite::Connection;

use nullifier_service::tree_db;

fn main() -> Result<()> {
    let db_path = env::var("DB_PATH").unwrap_or_else(|_| "nullifiers.db".to_string());

    println!("Opening database: {}", db_path);
    let connection = Connection::open(&db_path)?;

    // ── 1. Build the nullifier tree ────────────────────────────────────
    println!("Building NullifierTree from database...");
    let tree = tree_db::tree_from_db(&connection)?;
    println!(
        "  Tree built: {} ranges, root = 0x{}",
        tree.len(),
        hex::encode(tree.root().to_repr())
    );

    let root = tree.root();

    // ── 2. Load a raw nullifier for testing ────────────────────────────
    let mut stmt = connection.prepare("SELECT nullifier FROM nullifiers LIMIT 1")?;
    let existing_nf: Fp = stmt.query_row([], |r| {
        let v = r.get::<_, [u8; 32]>(0)?;
        Ok(Fp::from_repr(v).unwrap())
    })?;

    // ══════════════════════════════════════════════════════════════════════
    //  TEST 1: Non-existing nullifier  →  exclusion proof SHOULD succeed
    // ══════════════════════════════════════════════════════════════════════
    println!("\n── TEST 1: Non-inclusion proof for a NON-EXISTING value ──");

    let test_value = Fp::zero();
    println!("  Test value: 0x{}", hex::encode(test_value.to_repr()));

    let proof = tree
        .prove(test_value)
        .expect("BUG: Fp::zero() was not found in any gap range — unexpected");

    let [low, high] = proof.range;
    println!(
        "  Found in range: [0x{}..0x{}]",
        hex::encode(low.to_repr()),
        hex::encode(high.to_repr())
    );
    assert!(test_value >= low && test_value <= high);
    assert!(
        proof.verify(test_value, root),
        "Exclusion proof did not verify"
    );
    println!(
        "  Merkle path verified (position {}, leaf 0x{})",
        proof.position,
        hex::encode(proof.leaf.to_repr())
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

    let [low2, high2] = proof2.range;
    assert!(test_value_2 >= low2 && test_value_2 <= high2);
    assert!(
        proof2.verify(test_value_2, root),
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
