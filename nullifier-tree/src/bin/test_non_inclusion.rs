use std::env;

use anyhow::Result;
use ff::PrimeField as _;
use orchard::note::ExtractedNoteCommitment;
use orchard::vote::calculate_merkle_paths;
use pasta_curves::Fp;
use rusqlite::Connection;

use nullifier_tree::{build_nf_ranges, commit_ranges, find_range_for_value};

fn main() -> Result<()> {
    let db_path = env::var("DB_PATH").unwrap_or_else(|_| "nullifiers.db".to_string());

    println!("Opening database: {}", db_path);
    let connection = Connection::open(&db_path)?;

    // ── 1. Load raw nullifiers ──────────────────────────────────────────
    println!("Loading nullifiers...");
    let mut stmt = connection.prepare("SELECT nullifier FROM nullifiers")?;
    let rows = stmt.query_map([], |r| {
        let v = r.get::<_, [u8; 32]>(0)?;
        let v = Fp::from_repr(v).unwrap();
        Ok(v)
    })?;
    let mut raw_nfs: Vec<Fp> = rows.collect::<std::result::Result<Vec<_>, _>>()?;
    raw_nfs.sort();
    println!("  Loaded {} nullifiers", raw_nfs.len());

    // ── 2. Build gap ranges and commit to leaves ────────────────────────
    let ranges = build_nf_ranges(raw_nfs.iter().copied());
    let leaves = commit_ranges(&ranges);
    println!("  Built {} gap ranges ({} leaves)", ranges.len(), leaves.len());

    // ── 3. Compute Merkle root ──────────────────────────────────────────
    println!("Computing Merkle root over committed leaves...");
    let (root, _) = calculate_merkle_paths(0, &[], &leaves);
    println!("  Root: {:?}", hex::encode(root.to_repr()));

    // ══════════════════════════════════════════════════════════════════════
    //  TEST 1: Non-existing nullifier  →  exclusion proof SHOULD succeed
    // ══════════════════════════════════════════════════════════════════════
    println!("\n── TEST 1: Non-inclusion proof for a NON-EXISTING value ──");

    let test_value = Fp::zero();
    println!("  Test value: 0x{}", hex::encode(test_value.to_repr()));

    let range_idx = find_range_for_value(&ranges, test_value);
    match range_idx {
        Some(idx) => {
            let [low, high] = ranges[idx];
            println!(
                "  Found in range {}: [0x{}..0x{}]",
                idx,
                hex::encode(low.to_repr()),
                hex::encode(high.to_repr())
            );

            // One Merkle path per range — each leaf commits to (low, high).
            let pos = idx as u32;
            let (root2, paths) = calculate_merkle_paths(0, &[pos], &leaves);
            assert_eq!(root, root2, "Root mismatch between calls");

            let path = &paths[0];
            let mp = path.to_orchard_merkle_tree();
            let anchor = mp.root(
                ExtractedNoteCommitment::from_bytes(&path.value.to_repr()).unwrap(),
            );
            assert_eq!(
                root.to_repr(),
                anchor.to_bytes(),
                "Merkle path does not reconstruct to root",
            );
            println!(
                "  Merkle path verified (position {}, leaf 0x{})",
                path.position,
                hex::encode(path.value.to_repr())
            );

            assert!(test_value >= low && test_value <= high);

            println!("  PASS: Non-inclusion proof SUCCEEDED");
        }
        None => {
            panic!("BUG: Fp::zero() was not found in any gap range — unexpected");
        }
    }

    // ══════════════════════════════════════════════════════════════════════
    //  TEST 2: Existing nullifier  →  exclusion proof SHOULD fail
    // ══════════════════════════════════════════════════════════════════════
    println!("\n── TEST 2: Non-inclusion proof for an EXISTING nullifier ──");

    let existing_nf = raw_nfs[0];
    println!(
        "  Existing nullifier: 0x{}",
        hex::encode(existing_nf.to_repr())
    );

    let result = find_range_for_value(&ranges, existing_nf);
    assert!(
        result.is_none(),
        "BUG: existing nullifier was found inside a gap range!"
    );
    println!("  PASS: Existing nullifier correctly NOT found in any gap range");

    // ══════════════════════════════════════════════════════════════════════
    //  TEST 3: Another non-existing value (middle of a later range)
    // ══════════════════════════════════════════════════════════════════════
    println!("\n── TEST 3: Non-inclusion proof for a value in a later gap range ──");

    let mid_range = ranges.len() / 2;
    let [mid_low, _] = ranges[mid_range];
    let test_value_2 = mid_low + Fp::one();
    println!(
        "  Test value: 0x{} (low+1 of range {})",
        hex::encode(test_value_2.to_repr()),
        mid_range
    );

    let range_idx_2 = find_range_for_value(&ranges, test_value_2);
    match range_idx_2 {
        Some(idx) => {
            assert_eq!(idx, mid_range);
            let pos = idx as u32;
            let (root3, paths) = calculate_merkle_paths(0, &[pos], &leaves);
            assert_eq!(root, root3, "Root mismatch");

            let path = &paths[0];
            let mp = path.to_orchard_merkle_tree();
            let anchor = mp.root(
                ExtractedNoteCommitment::from_bytes(&path.value.to_repr()).unwrap(),
            );
            assert_eq!(root.to_repr(), anchor.to_bytes());

            let [low, high] = ranges[idx];
            assert!(test_value_2 >= low && test_value_2 <= high);
            println!(
                "  PASS: Non-inclusion proof SUCCEEDED for range {}",
                idx
            );
        }
        None => {
            panic!("BUG: test value in middle of a gap range was not found");
        }
    }

    println!("\n== All tests passed ==");
    Ok(())
}
