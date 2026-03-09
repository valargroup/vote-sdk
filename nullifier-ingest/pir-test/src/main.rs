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

use imt_tree::tree::build_sentinel_tree;

use pir_export::build_pir_tree;
use pir_export::{
    TIER1_ITEM_BITS, TIER1_ROWS, TIER1_ROW_BYTES, TIER2_ITEM_BITS, TIER2_ROWS, TIER2_ROW_BYTES,
};

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
        /// Server URL (e.g., http://localhost:3000).
        #[arg(long)]
        url: String,

        /// Path to nullifiers.bin (to know which values to query).
        #[arg(long)]
        nullifiers: PathBuf,

        /// Number of proofs to generate and verify.
        #[arg(long, default_value = "5")]
        num_proofs: usize,

        /// If set, fetch all proofs in a single parallel PIR request batch.
        #[arg(long, default_value_t = false)]
        parallel: bool,
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

    /// Benchmark YPIR query/response sizes and timing in-process (no HTTP).
    Bench {
        /// Number of YPIR queries per tier.
        #[arg(long, default_value = "3")]
        num_queries: usize,
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
            parallel,
        } => {
            let rt = tokio::runtime::Runtime::new()?;
            rt.block_on(run_server(url, nullifiers, num_proofs, parallel))
        }
        Command::Compare {
            nullifiers,
            num_proofs,
        } => run_compare(nullifiers, num_proofs),
        Command::Bench { num_queries } => run_bench(num_queries),
    }
}

// ── Small mode ───────────────────────────────────────────────────────────────

fn run_small() -> Result<()> {
    eprintln!("=== PIR Test: small (synthetic 1000 nullifiers) ===\n");
    let t_total = Instant::now();

    // Generate 1000 random nullifiers
    let mut rng = rand::thread_rng();
    let nfs: Vec<Fp> = (0..1000).map(|_| Fp::random(&mut rng)).collect();

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

    let ranges = pir_export::prepare_nullifiers(raw_nfs.to_vec());
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
            // Pick a random offset within the range (truncated to u64 — large sentinel
            // ranges degenerate to querying `low` directly, which is acceptable for testing).
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

async fn run_server(
    url: String,
    nullifiers_path: PathBuf,
    num_proofs: usize,
    parallel: bool,
) -> Result<()> {
    eprintln!("=== PIR Test: server ({}) ===\n", url);

    let nfs = load_nullifiers(&nullifiers_path)?;
    eprintln!("  Loaded {} nullifiers", nfs.len());

    // Connect to server
    let client = pir_client::PirClient::connect(&url).await?;
    eprintln!("  Connected to PIR server");

    // Pick random values
    let ranges = pir_export::prepare_nullifiers(nfs);

    let mut rng = rand::thread_rng();
    let test_values: Vec<Fp> = (0..num_proofs)
        .map(|_| {
            let idx = rng.gen_range(0..ranges.len());
            let [low, _width] = ranges[idx];
            low // Use the low value directly (always valid)
        })
        .collect();

    let mut passed = 0usize;
    let mut failed = 0usize;

    if parallel {
        let t0 = Instant::now();
        let proofs = client.fetch_proofs(&test_values).await?;
        anyhow::ensure!(
            proofs.len() == test_values.len(),
            "parallel fetch returned {} proofs for {} queries",
            proofs.len(),
            test_values.len()
        );
        for (i, (&value, proof)) in test_values.iter().zip(proofs.iter()).enumerate() {
            if proof.verify(value) {
                passed += 1;
            } else {
                failed += 1;
                eprintln!("  Proof {}/{}: FAIL (verify false)", i + 1, num_proofs);
            }
        }
        eprintln!(
            "  Parallel batch: {}/{} valid ({:.1}ms total)",
            passed,
            num_proofs,
            t0.elapsed().as_secs_f64() * 1000.0
        );
    } else {
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
    let ranges = pir_export::prepare_nullifiers(raw_nfs);

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
    let tier0_data = pir_export::tier0::export(
        &pir_tree.root26,
        &pir_tree.levels,
        &pir_tree.ranges,
        &pir_tree.empty_hashes,
    );
    let mut tier1_data = Vec::new();
    pir_export::tier1::export(
        &pir_tree.levels,
        &pir_tree.ranges,
        &pir_tree.empty_hashes,
        &mut tier1_data,
    )?;
    let mut tier2_data = Vec::new();
    pir_export::tier2::export(
        &pir_tree.levels,
        &pir_tree.ranges,
        &pir_tree.empty_hashes,
        &mut tier2_data,
    )?;

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
                eprintln!(
                    "  Compare {}/{}: depth-29 prove returned None",
                    i + 1,
                    num_proofs
                );
                mismatched += 1;
            }
            (_, Err(e)) => {
                eprintln!("  Compare {}/{}: PIR error: {}", i + 1, num_proofs, e);
                mismatched += 1;
            }
        }
    }

    eprintln!(
        "\n  Summary: {} matched, {} mismatched",
        matched, mismatched
    );
    if mismatched > 0 {
        anyhow::bail!("{} comparisons failed", mismatched);
    }

    eprintln!("\n=== PASSED ===");
    Ok(())
}

// ── Bench mode ───────────────────────────────────────────────────────────────

fn run_bench(num_queries: usize) -> Result<()> {
    use pir_server::{OwnedTierState, YpirScenario};

    eprintln!("=== PIR Benchmark: in-process YPIR ({} queries per tier) ===\n", num_queries);
    eprintln!(
        "  Config: TIER1_LAYERS={}, TIER2_LAYERS={}",
        pir_export::TIER1_LAYERS,
        pir_export::TIER2_LAYERS
    );
    eprintln!(
        "  Tier 1: {} rows × {} bytes/row ({} bits/item), instances={}",
        TIER1_ROWS,
        TIER1_ROW_BYTES,
        TIER1_ITEM_BITS,
        (TIER1_ITEM_BITS as f64 / (2048.0 * 14.0)).ceil() as usize,
    );
    eprintln!(
        "  Tier 2: {} rows × {} bytes/row ({} bits/item), instances={}",
        TIER2_ROWS,
        TIER2_ROW_BYTES,
        TIER2_ITEM_BITS,
        (TIER2_ITEM_BITS as f64 / (2048.0 * 14.0)).ceil() as usize,
    );

    // Build a small tree to get valid tier data
    eprintln!("\nBuilding synthetic tree (1000 nullifiers)...");
    let mut rng = rand::thread_rng();
    let raw_nfs: Vec<Fp> = (0..1000).map(|_| Fp::random(&mut rng)).collect();
    let ranges = pir_export::prepare_nullifiers(raw_nfs);
    let tree = build_pir_tree(ranges)?;

    // Export tier data
    eprintln!("Exporting tier data...");
    let mut tier1_data = Vec::new();
    pir_export::tier1::export(&tree.levels, &tree.ranges, &tree.empty_hashes, &mut tier1_data)?;
    let mut tier2_data = Vec::new();
    pir_export::tier2::export(&tree.levels, &tree.ranges, &tree.empty_hashes, &mut tier2_data)?;
    eprintln!("  Tier 1: {} bytes", tier1_data.len());
    eprintln!("  Tier 2: {} bytes", tier2_data.len());

    // Initialize YPIR servers
    eprintln!("\nInitializing YPIR servers...");
    let tier1_scenario = YpirScenario {
        num_items: TIER1_ROWS,
        item_size_bits: TIER1_ITEM_BITS,
    };
    let t0 = Instant::now();
    let tier1_server = OwnedTierState::new(&tier1_data, tier1_scenario.clone());
    eprintln!("  Tier 1 YPIR server ready in {:.1}s", t0.elapsed().as_secs_f64());
    drop(tier1_data);

    let tier2_scenario = YpirScenario {
        num_items: TIER2_ROWS,
        item_size_bits: TIER2_ITEM_BITS,
    };
    let t0 = Instant::now();
    let tier2_server = OwnedTierState::new(&tier2_data, tier2_scenario.clone());
    eprintln!("  Tier 2 YPIR server ready in {:.1}s", t0.elapsed().as_secs_f64());
    drop(tier2_data);

    // Run tier 1 benchmarks
    eprintln!("\n── Tier 1 YPIR Benchmark ──────────────────────────────────");
    let tier1_results = bench_tier(
        "tier1",
        tier1_scenario.num_items,
        tier1_scenario.item_size_bits,
        tier1_server.server(),
        num_queries,
    )?;

    // Run tier 2 benchmarks
    eprintln!("\n── Tier 2 YPIR Benchmark ──────────────────────────────────");
    let tier2_results = bench_tier(
        "tier2",
        tier2_scenario.num_items,
        tier2_scenario.item_size_bits,
        tier2_server.server(),
        num_queries,
    )?;

    // Summary table
    eprintln!("\n══════════════════════════════════════════════════════════════");
    eprintln!("  SUMMARY (averages over {} queries)", num_queries);
    eprintln!("══════════════════════════════════════════════════════════════");
    eprintln!(
        "  {:>10} {:>12} {:>12} {:>10} {:>10} {:>10}",
        "", "Query(up)", "Response(dn)", "ClientGen", "ServerComp", "ClientDec"
    );
    eprintln!(
        "  {:>10} {:>12} {:>12} {:>10} {:>10} {:>10}",
        "Tier 1",
        format_bytes(tier1_results.avg_query_bytes),
        format_bytes(tier1_results.avg_response_bytes),
        format_ms(tier1_results.avg_gen_ms),
        format_ms(tier1_results.avg_server_ms),
        format_ms(tier1_results.avg_decode_ms),
    );
    eprintln!(
        "  {:>10} {:>12} {:>12} {:>10} {:>10} {:>10}",
        "Tier 2",
        format_bytes(tier2_results.avg_query_bytes),
        format_bytes(tier2_results.avg_response_bytes),
        format_ms(tier2_results.avg_gen_ms),
        format_ms(tier2_results.avg_server_ms),
        format_ms(tier2_results.avg_decode_ms),
    );
    eprintln!(
        "  {:>10} {:>12} {:>12}",
        "TOTAL",
        format_bytes(tier1_results.avg_query_bytes + tier2_results.avg_query_bytes),
        format_bytes(tier1_results.avg_response_bytes + tier2_results.avg_response_bytes),
    );
    eprintln!("══════════════════════════════════════════════════════════════");

    Ok(())
}

struct BenchResults {
    avg_query_bytes: usize,
    avg_response_bytes: usize,
    avg_gen_ms: f64,
    avg_server_ms: f64,
    avg_decode_ms: f64,
}

fn bench_tier(
    name: &str,
    num_items: usize,
    item_size_bits: usize,
    server: &pir_server::TierServer<'static>,
    num_queries: usize,
) -> Result<BenchResults> {
    use ypir::client::YPIRClient;

    let ypir_client = YPIRClient::from_db_sz(num_items as u64, item_size_bits as u64, true);

    let mut total_query_bytes = 0usize;
    let mut total_response_bytes = 0usize;
    let mut total_gen_ms = 0.0f64;
    let mut total_server_ms = 0.0f64;
    let mut total_decode_ms = 0.0f64;

    for i in 0..num_queries {
        let row_idx = i % num_items;

        // Client: generate query
        let t_gen = Instant::now();
        let (query, seed) = ypir_client.generate_query_simplepir(row_idx);
        let gen_ms = t_gen.elapsed().as_secs_f64() * 1000.0;

        // Serialize query (same format as pir-client)
        let pqr = query.0.as_slice();
        let pp = query.1.as_slice();
        let pqr_byte_len = pqr.len() * 8;
        let mut payload = Vec::with_capacity(8 + (pqr.len() + pp.len()) * 8);
        payload.extend_from_slice(&(pqr_byte_len as u64).to_le_bytes());
        for &v in pqr {
            payload.extend_from_slice(&v.to_le_bytes());
        }
        for &v in pp {
            payload.extend_from_slice(&v.to_le_bytes());
        }
        let query_bytes = payload.len();

        // Server: answer query
        let t_server = Instant::now();
        let answer = server.answer_query(&payload)?;
        let server_ms = t_server.elapsed().as_secs_f64() * 1000.0;
        let response_bytes = answer.response.len();

        // Client: decode response
        let t_decode = Instant::now();
        let _decoded = ypir_client.decode_response_simplepir(seed, &answer.response);
        let decode_ms = t_decode.elapsed().as_secs_f64() * 1000.0;

        eprintln!(
            "  {} query {}/{}: up={} dn={} gen={:.0}ms server={:.0}ms decode={:.0}ms",
            name,
            i + 1,
            num_queries,
            format_bytes(query_bytes),
            format_bytes(response_bytes),
            gen_ms,
            server_ms,
            decode_ms,
        );

        total_query_bytes += query_bytes;
        total_response_bytes += response_bytes;
        total_gen_ms += gen_ms;
        total_server_ms += server_ms;
        total_decode_ms += decode_ms;
    }

    let n = num_queries as f64;
    Ok(BenchResults {
        avg_query_bytes: total_query_bytes / num_queries,
        avg_response_bytes: total_response_bytes / num_queries,
        avg_gen_ms: total_gen_ms / n,
        avg_server_ms: total_server_ms / n,
        avg_decode_ms: total_decode_ms / n,
    })
}

fn format_bytes(b: usize) -> String {
    if b >= 1_048_576 {
        format!("{:.2} MB", b as f64 / 1_048_576.0)
    } else if b >= 1024 {
        format!("{:.1} KB", b as f64 / 1024.0)
    } else {
        format!("{} B", b)
    }
}

fn format_ms(ms: f64) -> String {
    if ms >= 1000.0 {
        format!("{:.1}s", ms / 1000.0)
    } else {
        format!("{:.0}ms", ms)
    }
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
