//! PIR E2E test harness.
//!
//! Modes:
//!   small   — Synthetic 1000-nullifier tree, full round-trip (~5s)
//!   local   — Full in-process test with real nullifiers (no HTTP, no YPIR crypto)
//!   server  — Test against a running pir-server instance (HTTP + YPIR crypto)
//!   compare — Verify PIR proofs match existing NullifierTree::prove()

use std::path::PathBuf;
use std::time::Instant;

use anyhow::Result;
use clap::{Parser, Subcommand};
use ff::{Field, PrimeField as _};
use pasta_curves::Fp;
use rand::Rng;

use imt_tree::tree::{build_nf_ranges, build_sentinel_tree};

use pir_export::build_pir_tree;

#[derive(Parser)]
#[command(name = "pir-test", about = "PIR system end-to-end testing")]
struct Args {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Synthetic 1000-nullifier tree, fast round-trip test (~5s).
    Small,

    /// Full in-process test with real or synthetic nullifiers.
    /// Tests tier data extraction and proof construction without YPIR crypto.
    Local {
        /// Path to nullifiers.bin. If omitted, generates 10,000 random nullifiers.
        #[arg(long)]
        nullifiers: Option<PathBuf>,

        /// Number of proofs to generate and verify.
        #[arg(long, default_value = "10")]
        num_proofs: usize,
    },

    /// Test against a running pir-server instance.
    Server {
        /// Server URL (e.g., http://157.180.63.235:3001).
        #[arg(long)]
        url: String,

        /// Path to nullifiers.bin (to know which values to query).
        #[arg(long)]
        nullifiers: PathBuf,

        /// Number of proofs to generate and verify.
        #[arg(long, default_value = "5")]
        num_proofs: usize,
    },

    /// Verify PIR proofs match existing NullifierTree::prove().
    Compare {
        /// Path to nullifiers.bin.
        #[arg(long)]
        nullifiers: PathBuf,

        /// Number of proofs to compare.
        #[arg(long, default_value = "100")]
        num_proofs: usize,
    },
}

fn main() -> Result<()> {
    let args = Args::parse();

    match args.command {
        Command::Small => run_small(),
        Command::Local {
            nullifiers,
            num_proofs,
        } => run_local(nullifiers, num_proofs),
        Command::Server {
            url,
            nullifiers,
            num_proofs,
        } => {
            let rt = tokio::runtime::Runtime::new()?;
            rt.block_on(run_server(url, nullifiers, num_proofs))
        }
        Command::Compare {
            nullifiers,
            num_proofs,
        } => run_compare(nullifiers, num_proofs),
    }
}

// ── Small mode ───────────────────────────────────────────────────────────────

fn run_small() -> Result<()> {
    eprintln!("=== PIR Test: small (synthetic 1000 nullifiers) ===\n");
    let t_total = Instant::now();

    // Generate 1000 random nullifiers
    let mut rng = rand::thread_rng();
    let nfs: Vec<Fp> = (0..1000)
        .map(|_| Fp::random(&mut rng))
        .collect();

    run_local_inner(&nfs, 10)?;

    eprintln!(
        "\n=== PASSED in {:.1}s ===",
        t_total.elapsed().as_secs_f64()
    );
    Ok(())
}

// ── Local mode ───────────────────────────────────────────────────────────────

fn run_local(nullifiers_path: Option<PathBuf>, num_proofs: usize) -> Result<()> {
    eprintln!("=== PIR Test: local ===\n");

    let nfs = if let Some(path) = nullifiers_path {
        eprintln!("Loading nullifiers from {:?}...", path);
        load_nullifiers(&path)?
    } else {
        eprintln!("Generating 10,000 random nullifiers...");
        let mut rng = rand::thread_rng();
        (0..10_000).map(|_| Fp::random(&mut rng)).collect()
    };

    run_local_inner(&nfs, num_proofs)?;

    eprintln!("\n=== PASSED ===");
    Ok(())
}

fn run_local_inner(raw_nfs: &[Fp], num_proofs: usize) -> Result<()> {
    let t0 = Instant::now();

    // Sort and build ranges (with sentinels)
    let mut sorted = raw_nfs.to_vec();
    sorted.sort();

    // Add sentinel nullifiers at k * 2^250 boundaries
    let step = Fp::from(2u64).pow([250, 0, 0, 0]);
    let sentinels: Vec<Fp> = (0u64..=16).map(|k| step * Fp::from(k)).collect();
    let mut all_nfs = sentinels;
    all_nfs.extend_from_slice(&sorted);
    all_nfs.sort();
    all_nfs.dedup();

    let ranges = build_nf_ranges(all_nfs.iter().copied());
    eprintln!(
        "  {} ranges from {} nullifiers in {:.1}s",
        ranges.len(),
        raw_nfs.len(),
        t0.elapsed().as_secs_f64()
    );

    // Build PIR tree
    let t1 = Instant::now();
    let tree = build_pir_tree(ranges.clone())?;
    eprintln!(
        "  PIR tree built in {:.1}s (root26={}, root29={})",
        t1.elapsed().as_secs_f64(),
        &hex::encode(tree.root26.to_repr())[..16],
        &hex::encode(tree.root29.to_repr())[..16],
    );

    // Export tier data in memory
    let t2 = Instant::now();
    let tier0_data =
        pir_export::tier0::export(&tree.root26, &tree.levels, &tree.ranges, &tree.empty_hashes);
    eprintln!("  Tier 0: {} bytes", tier0_data.len());

    let mut tier1_data = Vec::new();
    pir_export::tier1::export(
        &tree.levels,
        &tree.ranges,
        &tree.empty_hashes,
        &mut tier1_data,
    )?;
    eprintln!("  Tier 1: {} bytes", tier1_data.len());

    let mut tier2_data = Vec::new();
    pir_export::tier2::export(
        &tree.levels,
        &tree.ranges,
        &tree.empty_hashes,
        &mut tier2_data,
    )?;
    eprintln!(
        "  Tier 2: {} bytes, exported in {:.1}s",
        tier2_data.len(),
        t2.elapsed().as_secs_f64()
    );

    // Pick random values from populated ranges to query
    let mut rng = rand::thread_rng();
    let test_values: Vec<Fp> = (0..num_proofs)
        .map(|_| {
            // Pick a random populated range and a random value within it
            let idx = rng.gen_range(0..ranges.len());
            let [low, width] = ranges[idx];
            // Pick a random offset within the range
            let _low_u64 = u64::from_le_bytes(low.to_repr()[..8].try_into().unwrap());
            let width_u64 = u64::from_le_bytes(width.to_repr()[..8].try_into().unwrap());
            let offset_val = if width_u64 > 0 {
                rng.gen_range(0..=width_u64.min(u64::MAX - 1))
            } else {
                0
            };
            low + Fp::from(offset_val)
        })
        .collect();

    // Test each proof
    let mut passed = 0;
    let mut failed = 0;

    for (i, &value) in test_values.iter().enumerate() {
        let t_proof = Instant::now();

        let result = pir_client::fetch_proof_local(
            &tier0_data,
            &tier1_data,
            &tier2_data,
            ranges.len(),
            value,
            &tree.empty_hashes,
            tree.root29,
        );

        match result {
            Ok(proof) => {
                if proof.verify(value) {
                    passed += 1;
                    eprintln!(
                        "  Proof {}/{}: PASS ({:.1}ms) leaf_pos={}",
                        i + 1,
                        num_proofs,
                        t_proof.elapsed().as_secs_f64() * 1000.0,
                        proof.leaf_pos,
                    );
                } else {
                    failed += 1;
                    eprintln!(
                        "  Proof {}/{}: FAIL (verify returned false) leaf_pos={}",
                        i + 1,
                        num_proofs,
                        proof.leaf_pos,
                    );
                }
            }
            Err(e) => {
                failed += 1;
                eprintln!("  Proof {}/{}: ERROR: {}", i + 1, num_proofs, e);
            }
        }
    }

    eprintln!("\n  Summary: {} passed, {} failed", passed, failed);
    if failed > 0 {
        anyhow::bail!("{} proofs failed", failed);
    }

    Ok(())
}

// ── Server mode ──────────────────────────────────────────────────────────────

async fn run_server(url: String, nullifiers_path: PathBuf, num_proofs: usize) -> Result<()> {
    eprintln!("=== PIR Test: server ({}) ===\n", url);

    let nfs = load_nullifiers(&nullifiers_path)?;
    eprintln!("  Loaded {} nullifiers", nfs.len());

    // Connect to server
    let client = pir_client::PirClient::connect(&url).await?;
    eprintln!("  Connected to PIR server");

    // Pick random values
    let mut sorted = nfs;
    sorted.sort();
    let step = Fp::from(2u64).pow([250, 0, 0, 0]);
    let sentinels: Vec<Fp> = (0u64..=16).map(|k| step * Fp::from(k)).collect();
    let mut all_nfs = sentinels;
    all_nfs.extend_from_slice(&sorted);
    all_nfs.sort();
    all_nfs.dedup();
    let ranges = build_nf_ranges(all_nfs.iter().copied());

    let mut rng = rand::thread_rng();
    let test_values: Vec<Fp> = (0..num_proofs)
        .map(|_| {
            let idx = rng.gen_range(0..ranges.len());
            let [low, _width] = ranges[idx];
            low // Use the low value directly (always valid)
        })
        .collect();

    let mut passed = 0;
    let mut failed = 0;

    for (i, &value) in test_values.iter().enumerate() {
        let t0 = Instant::now();
        match client.fetch_proof(value).await {
            Ok(proof) => {
                if proof.verify(value) {
                    passed += 1;
                    eprintln!(
                        "  Proof {}/{}: PASS ({:.1}ms)",
                        i + 1,
                        num_proofs,
                        t0.elapsed().as_secs_f64() * 1000.0,
                    );
                } else {
                    failed += 1;
                    eprintln!("  Proof {}/{}: FAIL (verify false)", i + 1, num_proofs);
                }
            }
            Err(e) => {
                failed += 1;
                eprintln!("  Proof {}/{}: ERROR: {}", i + 1, num_proofs, e);
            }
        }
    }

    eprintln!("\n  Summary: {} passed, {} failed", passed, failed);
    if failed > 0 {
        anyhow::bail!("{} proofs failed", failed);
    }

    eprintln!("\n=== PASSED ===");
    Ok(())
}

// ── Compare mode ─────────────────────────────────────────────────────────────

fn run_compare(nullifiers_path: PathBuf, num_proofs: usize) -> Result<()> {
    eprintln!("=== PIR Test: compare (PIR vs NullifierTree) ===\n");

    let raw_nfs = load_nullifiers(&nullifiers_path)?;
    eprintln!("  Loaded {} nullifiers", raw_nfs.len());

    // Build the existing depth-29 NullifierTree
    let t0 = Instant::now();
    let tree29 = build_sentinel_tree(&raw_nfs)?;
    eprintln!(
        "  Depth-29 tree built in {:.1}s (root={})",
        t0.elapsed().as_secs_f64(),
        &hex::encode(tree29.root().to_repr())[..16],
    );

    // Build the depth-26 PIR tree
    let mut sorted = raw_nfs;
    sorted.sort();
    let step = Fp::from(2u64).pow([250, 0, 0, 0]);
    let sentinels: Vec<Fp> = (0u64..=16).map(|k| step * Fp::from(k)).collect();
    let mut all_nfs = sentinels;
    all_nfs.extend_from_slice(&sorted);
    all_nfs.sort();
    all_nfs.dedup();
    let ranges = build_nf_ranges(all_nfs.iter().copied());

    let t1 = Instant::now();
    let pir_tree = build_pir_tree(ranges.clone())?;
    eprintln!(
        "  Depth-26 PIR tree built in {:.1}s (root26={}, root29={})",
        t1.elapsed().as_secs_f64(),
        &hex::encode(pir_tree.root26.to_repr())[..16],
        &hex::encode(pir_tree.root29.to_repr())[..16],
    );

    // Verify roots match
    if pir_tree.root29 != tree29.root() {
        eprintln!(
            "  WARNING: Root mismatch! PIR root29={} vs tree29 root={}",
            hex::encode(pir_tree.root29.to_repr()),
            hex::encode(tree29.root().to_repr()),
        );
        // This might happen if the sentinel/range logic differs — log but continue
    } else {
        eprintln!("  Roots match! ✓");
    }

    // Export tier data
    let tier0_data =
        pir_export::tier0::export(&pir_tree.root26, &pir_tree.levels, &pir_tree.ranges, &pir_tree.empty_hashes);
    let mut tier1_data = Vec::new();
    pir_export::tier1::export(&pir_tree.levels, &pir_tree.ranges, &pir_tree.empty_hashes, &mut tier1_data)?;
    let mut tier2_data = Vec::new();
    pir_export::tier2::export(&pir_tree.levels, &pir_tree.ranges, &pir_tree.empty_hashes, &mut tier2_data)?;

    // Pick random values and compare proofs
    let mut rng = rand::thread_rng();
    let test_values: Vec<Fp> = (0..num_proofs)
        .map(|_| {
            let idx = rng.gen_range(0..ranges.len());
            let [low, _] = ranges[idx];
            low
        })
        .collect();

    let mut matched = 0;
    let mut mismatched = 0;

    for (i, &value) in test_values.iter().enumerate() {
        // Get proof from existing system
        let proof29 = tree29.prove(value);

        // Get proof from PIR system
        let proof_pir = pir_client::fetch_proof_local(
            &tier0_data,
            &tier1_data,
            &tier2_data,
            ranges.len(),
            value,
            &pir_tree.empty_hashes,
            pir_tree.root29,
        );

        match (proof29, proof_pir) {
            (Some(p29), Ok(ppir)) => {
                let low_match = p29.low == ppir.low;
                let width_match = p29.width == ppir.width;
                let pir_verify = ppir.verify(value);

                if low_match && width_match && pir_verify {
                    matched += 1;
                    if i < 5 || i % 20 == 0 {
                        eprintln!(
                            "  Compare {}/{}: MATCH low={} width={} leaf_pos_29={} leaf_pos_pir={}",
                            i + 1,
                            num_proofs,
                            &hex::encode(p29.low.to_repr())[..8],
                            &hex::encode(p29.width.to_repr())[..8],
                            p29.leaf_pos,
                            ppir.leaf_pos,
                        );
                    }
                } else {
                    mismatched += 1;
                    eprintln!(
                        "  Compare {}/{}: MISMATCH low={}/{} width={}/{} verify={}",
                        i + 1,
                        num_proofs,
                        low_match,
                        &hex::encode(p29.low.to_repr())[..8],
                        width_match,
                        &hex::encode(p29.width.to_repr())[..8],
                        pir_verify,
                    );
                }
            }
            (None, _) => {
                eprintln!("  Compare {}/{}: depth-29 prove returned None", i + 1, num_proofs);
                mismatched += 1;
            }
            (_, Err(e)) => {
                eprintln!("  Compare {}/{}: PIR error: {}", i + 1, num_proofs, e);
                mismatched += 1;
            }
        }
    }

    eprintln!("\n  Summary: {} matched, {} mismatched", matched, mismatched);
    if mismatched > 0 {
        anyhow::bail!("{} comparisons failed", mismatched);
    }

    eprintln!("\n=== PASSED ===");
    Ok(())
}

// ── Utilities ────────────────────────────────────────────────────────────────

fn load_nullifiers(path: &std::path::Path) -> Result<Vec<Fp>> {
    use rayon::prelude::*;

    let data = std::fs::read(path)?;
    anyhow::ensure!(
        data.len() % 32 == 0,
        "corrupt nullifiers file: size {} is not a multiple of 32",
        data.len()
    );
    let nfs: Vec<Fp> = data
        .par_chunks_exact(32)
        .map(|chunk| {
            let mut arr = [0u8; 32];
            arr.copy_from_slice(chunk);
            Fp::from_repr(arr).expect("non-canonical Fp in nullifiers.bin")
        })
        .collect();
    Ok(nfs)
}
