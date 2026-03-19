//! Metrics collection for the voter throughput stress test.
//!
//! Tracks per-submission latency, computes TPS (transactions per second),
//! percentiles, and writes results to JSON + human-readable summary.

use std::collections::HashMap;
use std::path::Path;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use serde::Serialize;

/// A single submission measurement.
#[derive(Clone, Debug)]
pub struct Sample {
    pub phase: String,
    pub timestamp: Instant,
    pub latency: Duration,
    pub http_status: u16,
    pub success: bool,
}

/// Collects samples from concurrent workers.
pub struct MetricsCollector {
    start: Instant,
    samples: Mutex<Vec<Sample>>,
}

impl MetricsCollector {
    pub fn new() -> Self {
        Self {
            start: Instant::now(),
            samples: Mutex::new(Vec::new()),
        }
    }

    pub fn record(&self, sample: Sample) {
        self.samples.lock().unwrap().push(sample);
    }

    pub fn snapshot(&self) -> Vec<Sample> {
        self.samples.lock().unwrap().clone()
    }

    pub fn wall_time(&self) -> Duration {
        self.start.elapsed()
    }
}

// ---------------------------------------------------------------------------
// Aggregate metrics
// ---------------------------------------------------------------------------

#[derive(Serialize, Clone, Debug)]
pub struct PhaseMetrics {
    pub phase: String,
    pub total_submitted: usize,
    pub succeeded: usize,
    pub failed: usize,
    pub success_rate: f64,
    pub wall_time_secs: f64,
    pub tps_sustained: f64,
    pub latency_p50_ms: f64,
    pub latency_p95_ms: f64,
    pub latency_p99_ms: f64,
    pub latency_max_ms: f64,
    pub latency_min_ms: f64,
}

#[derive(Serialize, Clone, Debug)]
pub struct AggregateMetrics {
    pub phases: Vec<PhaseMetrics>,
    pub total_wall_time_secs: f64,
    pub overall_tps: f64,
    pub overall_success_rate: f64,
}

/// Compute aggregate metrics from collected samples.
pub fn compute_aggregate(samples: &[Sample], total_wall_time: Duration) -> AggregateMetrics {
    let mut by_phase: HashMap<String, Vec<&Sample>> = HashMap::new();
    for s in samples {
        by_phase.entry(s.phase.clone()).or_default().push(s);
    }

    let mut phases: Vec<PhaseMetrics> = Vec::new();
    let phase_order = ["delegation", "cast_vote", "share_enqueue"];

    for phase_name in &phase_order {
        if let Some(phase_samples) = by_phase.get(*phase_name) {
            phases.push(compute_phase_metrics(phase_name, phase_samples));
        }
    }

    // Include any phases not in the standard order.
    for (name, phase_samples) in &by_phase {
        if !phase_order.contains(&name.as_str()) {
            phases.push(compute_phase_metrics(name, phase_samples));
        }
    }

    let total_submitted: usize = phases.iter().map(|p| p.total_submitted).sum();
    let total_succeeded: usize = phases.iter().map(|p| p.succeeded).sum();
    let total_wall_secs = total_wall_time.as_secs_f64();

    AggregateMetrics {
        phases,
        total_wall_time_secs: total_wall_secs,
        overall_tps: if total_wall_secs > 0.0 {
            total_submitted as f64 / total_wall_secs
        } else {
            0.0
        },
        overall_success_rate: if total_submitted > 0 {
            total_succeeded as f64 / total_submitted as f64
        } else {
            0.0
        },
    }
}

fn compute_phase_metrics(phase: &str, samples: &[&Sample]) -> PhaseMetrics {
    let total = samples.len();
    let succeeded = samples.iter().filter(|s| s.success).count();
    let failed = total - succeeded;

    let mut latencies_ms: Vec<f64> = samples.iter().map(|s| s.latency.as_secs_f64() * 1000.0).collect();
    latencies_ms.sort_by(|a, b| a.partial_cmp(b).unwrap());

    let wall_time = if total > 1 {
        let first = samples.iter().map(|s| s.timestamp).min().unwrap();
        let last = samples.iter().map(|s| s.timestamp + s.latency).max().unwrap();
        last.duration_since(first).as_secs_f64()
    } else {
        latencies_ms.first().copied().unwrap_or(0.0) / 1000.0
    };

    PhaseMetrics {
        phase: phase.to_string(),
        total_submitted: total,
        succeeded,
        failed,
        success_rate: if total > 0 { succeeded as f64 / total as f64 } else { 0.0 },
        wall_time_secs: wall_time,
        tps_sustained: if wall_time > 0.0 { total as f64 / wall_time } else { 0.0 },
        latency_p50_ms: percentile(&latencies_ms, 50.0),
        latency_p95_ms: percentile(&latencies_ms, 95.0),
        latency_p99_ms: percentile(&latencies_ms, 99.0),
        latency_max_ms: latencies_ms.last().copied().unwrap_or(0.0),
        latency_min_ms: latencies_ms.first().copied().unwrap_or(0.0),
    }
}

fn percentile(sorted: &[f64], p: f64) -> f64 {
    if sorted.is_empty() {
        return 0.0;
    }
    let idx = (p / 100.0 * (sorted.len() - 1) as f64).round() as usize;
    sorted[idx.min(sorted.len() - 1)]
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

/// Write metrics JSON and human-readable summary to the given directory.
pub fn write_report(metrics: &AggregateMetrics, dir: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;

    let json_path = dir.join("metrics.json");
    std::fs::write(&json_path, serde_json::to_string_pretty(metrics).unwrap())?;

    let summary = format_summary(metrics);
    let summary_path = dir.join("summary.md");
    std::fs::write(&summary_path, &summary)?;

    eprintln!("Metrics written to {}", json_path.display());
    eprintln!("Summary written to {}", summary_path.display());
    Ok(())
}

fn format_summary(m: &AggregateMetrics) -> String {
    let mut s = String::new();
    s.push_str("# Voter Throughput Stress Test Results\n\n");
    s.push_str(&format!(
        "**Overall sampled success rate:** {:.1}%\n\n",
        m.overall_success_rate * 100.0
    ));
    s.push_str(
        "_This summary is descriptive only; the benchmark's actual pass/fail result is determined by the test assertions._\n\n",
    );

    s.push_str(&format!(
        "- Total wall time: {:.1}s\n",
        m.total_wall_time_secs
    ));
    s.push_str(&format!("- Overall TPS: {:.1}\n\n", m.overall_tps));

    s.push_str("## Per-Phase Breakdown\n\n");
    s.push_str("| Phase | Submitted | Succeeded | Failed | Success% | TPS | p50 ms | p95 ms | p99 ms | max ms |\n");
    s.push_str("|-------|-----------|-----------|--------|----------|-----|--------|--------|--------|--------|\n");

    for p in &m.phases {
        s.push_str(&format!(
            "| {} | {} | {} | {} | {:.1}% | {:.1} | {:.0} | {:.0} | {:.0} | {:.0} |\n",
            p.phase,
            p.total_submitted,
            p.succeeded,
            p.failed,
            p.success_rate * 100.0,
            p.tps_sustained,
            p.latency_p50_ms,
            p.latency_p95_ms,
            p.latency_p99_ms,
            p.latency_max_ms,
        ));
    }

    s
}
