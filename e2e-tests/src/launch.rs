//! Launch-validation planning, expected-result math, gate evaluation, and report rendering.
//!
//! The long-running network harness should write observations in the same shape
//! as `RunObservation`; this module keeps deterministic wallet/tally/share math
//! and the human-readable report independent from the network driver.

use rand::{rngs::StdRng, Rng, SeedableRng};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::BTreeMap;

pub const BALLOT_DIVISOR: u64 = 12_500_000;
pub const NORMAL_SHARE_COUNT: u64 = 16;
pub const LAST_MOMENT_SHARE_COUNT: u64 = 1;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct RunSpec {
    pub run_name: String,
    pub seed: u64,
    pub primary_api_url: String,
    #[serde(default, skip_serializing)]
    pub helper_api_token: Option<String>,
    pub output_dir: Option<String>,
    pub snapshot_height: u64,
    pub voter_count: usize,
    pub round_duration_secs: u64,
    pub validator_count: u32,
    pub helper_target_count: u32,
    pub proposals: Vec<ProposalSpec>,
    pub wallet_mix: WalletMix,
    pub timing: TimingSpec,
    pub vote_servers: Vec<ServiceEndpoint>,
    pub chaos: Vec<ChaosStep>,
}

impl Default for RunSpec {
    fn default() -> Self {
        Self {
            run_name: "shielded-vote-launch-validation".to_string(),
            seed: 7_777,
            primary_api_url: String::new(),
            helper_api_token: None,
            output_dir: None,
            snapshot_height: 3_317_500,
            voter_count: 1_000,
            round_duration_secs: 4 * 60 * 60,
            validator_count: 5,
            helper_target_count: 3,
            proposals: vec![
                ProposalSpec {
                    id: 1,
                    title: "Launch Validation Binary Proposal".to_string(),
                    options: vec!["Support".to_string(), "Oppose".to_string()],
                    decision_weights: vec![60, 40],
                },
                ProposalSpec {
                    id: 2,
                    title: "Launch Validation Multi-Option Proposal".to_string(),
                    options: vec![
                        "Option A".to_string(),
                        "Option B".to_string(),
                        "Abstain".to_string(),
                    ],
                    decision_weights: vec![50, 30, 20],
                },
            ],
            wallet_mix: WalletMix::default(),
            timing: TimingSpec::default(),
            vote_servers: Vec::new(),
            chaos: vec![
                ChaosStep {
                    id: "validator-down-10m".to_string(),
                    at_offset_secs: 45 * 60,
                    target: "val3".to_string(),
                    action: ChaosAction::Stop,
                    duration_secs: Some(10 * 60),
                    description: "Stop one non-primary validator/helper for ten minutes."
                        .to_string(),
                },
                ChaosStep {
                    id: "helper-down-through-close".to_string(),
                    at_offset_secs: 2 * 60 * 60,
                    target: "val4".to_string(),
                    action: ChaosAction::StopUntilVoteEnd,
                    duration_secs: None,
                    description:
                        "Stop another validator/helper after queued shares have been accepted."
                            .to_string(),
                },
            ],
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ProposalSpec {
    pub id: u32,
    pub title: String,
    pub options: Vec<String>,
    /// Relative integer weights used to deterministically assign synthetic
    /// voter choices. If omitted or invalid, choices are assigned uniformly.
    #[serde(default)]
    pub decision_weights: Vec<u32>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ServiceEndpoint {
    pub label: String,
    pub url: String,
    #[serde(default)]
    pub ssh_host: Option<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct WalletMix {
    pub dust_only_pct: u8,
    pub exact_threshold_pct: u8,
    pub five_note_pct: u8,
    pub multi_bundle_pct: u8,
    pub whale_pct: u8,
}

impl Default for WalletMix {
    fn default() -> Self {
        Self {
            dust_only_pct: 5,
            exact_threshold_pct: 15,
            five_note_pct: 45,
            multi_bundle_pct: 25,
            whale_pct: 10,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
pub struct TimingSpec {
    pub early_burst_pct: u8,
    pub mid_burst_pct: u8,
    pub last_moment_pct: u8,
}

impl Default for TimingSpec {
    fn default() -> Self {
        Self {
            early_burst_pct: 45,
            mid_burst_pct: 35,
            last_moment_pct: 20,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ChaosAction {
    Stop,
    Restart,
    StopUntilVoteEnd,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ChaosStep {
    pub id: String,
    pub at_offset_secs: u64,
    pub target: String,
    pub action: ChaosAction,
    #[serde(default)]
    pub duration_secs: Option<u64>,
    #[serde(default)]
    pub description: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
#[serde(rename_all = "snake_case")]
pub enum WalletTier {
    DustOnly,
    ExactThreshold,
    FiveNote,
    MultiBundle,
    Whale,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum TimingCohort {
    EarlyBurst,
    MidBurst,
    LastMoment,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct SyntheticWallet {
    pub id: usize,
    pub tier: WalletTier,
    pub timing: TimingCohort,
    pub notes: Vec<NotePlan>,
    pub bundles: Vec<NoteBundle>,
    pub eligible_weight: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct NotePlan {
    pub position: u32,
    pub value_zatoshi: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct NoteBundle {
    pub notes: Vec<NotePlan>,
    pub raw_total_zatoshi: u64,
    pub eligible_weight_zatoshi: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ExpectedModel {
    pub voter_count: usize,
    pub eligible_wallets: usize,
    pub dust_wallets: usize,
    pub surviving_bundles: u64,
    pub vote_commitments: u64,
    pub expected_tree_delta: u64,
    pub expected_unique_shares: u64,
    pub expected_helper_acceptances: u64,
    pub expected_tally: Vec<ExpectedTallyEntry>,
    pub planned_votes: Vec<PlannedVote>,
    pub tier_counts: BTreeMap<WalletTier, usize>,
    pub timing_counts: BTreeMap<String, usize>,
    pub wallets: Vec<SyntheticWallet>,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct ExpectedTallyEntry {
    pub proposal_id: u32,
    pub decision: u32,
    pub option_label: String,
    pub total_value_zatoshi: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct PlannedVote {
    pub wallet_id: usize,
    pub wallet_tier: WalletTier,
    pub bundle_index: usize,
    pub bundle_global_index: u64,
    pub proposal_id: u32,
    pub proposal_title: String,
    pub decision: u32,
    pub option_label: String,
    pub timing: TimingCohort,
    pub submit_offset_secs: u64,
    pub eligible_weight_zatoshi: u64,
    pub expected_unique_shares: u64,
    pub note_positions: Vec<u32>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct RunObservation {
    pub round_id: Option<String>,
    pub status: Option<String>,
    pub tally_timed_out: bool,
    pub validator_count: Option<u32>,
    pub commitment_tree_delta: Option<u64>,
    pub unique_shares_revealed: Option<u64>,
    pub tally: Vec<ObservedTallyEntry>,
    pub helper_queues: Vec<ObservedHelperQueue>,
    pub phase_metrics: Vec<ObservedPhaseMetric>,
    pub chaos_events: Vec<ObservedChaosEvent>,
    pub errors: Vec<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ObservedTallyEntry {
    pub proposal_id: u32,
    pub decision: u32,
    pub total_value_zatoshi: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ObservedHelperQueue {
    pub server: String,
    pub total: u64,
    pub pending: u64,
    pub submitted: u64,
    pub failed: u64,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ObservedPhaseMetric {
    pub phase: String,
    pub total: u64,
    pub succeeded: u64,
    pub failed: u64,
    pub p50_ms: Option<f64>,
    pub p95_ms: Option<f64>,
    pub p99_ms: Option<f64>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ObservedChaosEvent {
    pub id: String,
    pub target: String,
    pub started_at_offset_secs: u64,
    pub ended_at_offset_secs: Option<u64>,
    pub result: String,
}

#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum GateStatus {
    Pass,
    Fail,
    Pending,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct GateResult {
    pub name: String,
    pub status: GateStatus,
    pub expected: String,
    pub actual: String,
    pub details: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct LaunchReport {
    pub spec: RunSpec,
    pub expected: ExpectedModel,
    pub observation: Option<RunObservation>,
    pub gates: Vec<GateResult>,
    pub overall_status: GateStatus,
}

pub fn build_launch_report(spec: RunSpec, observation: Option<RunObservation>) -> LaunchReport {
    let expected = generate_expected_model(&spec);
    let gates = evaluate_gates(&spec, &expected, observation.as_ref());
    let overall_status = summarize_gates(&gates);
    LaunchReport {
        spec,
        expected,
        observation,
        gates,
        overall_status,
    }
}

pub fn simulated_observation(spec: &RunSpec, expected: &ExpectedModel) -> RunObservation {
    RunObservation {
        round_id: Some(format!("simulated-{:016x}", spec.seed)),
        status: Some("FINALIZED".to_string()),
        tally_timed_out: false,
        validator_count: Some(spec.validator_count),
        commitment_tree_delta: Some(expected.expected_tree_delta),
        unique_shares_revealed: Some(expected.expected_unique_shares),
        tally: expected
            .expected_tally
            .iter()
            .map(|entry| ObservedTallyEntry {
                proposal_id: entry.proposal_id,
                decision: entry.decision,
                total_value_zatoshi: entry.total_value_zatoshi,
            })
            .collect(),
        helper_queues: spec
            .vote_servers
            .iter()
            .map(|server| ObservedHelperQueue {
                server: server.label.clone(),
                total: expected.expected_helper_acceptances,
                pending: 0,
                submitted: expected.expected_unique_shares,
                failed: 0,
            })
            .collect(),
        phase_metrics: Vec::new(),
        chaos_events: spec
            .chaos
            .iter()
            .map(|step| ObservedChaosEvent {
                id: step.id.clone(),
                target: step.target.clone(),
                started_at_offset_secs: step.at_offset_secs,
                ended_at_offset_secs: step.duration_secs.map(|d| step.at_offset_secs + d),
                result: "simulated".to_string(),
            })
            .collect(),
        errors: Vec::new(),
    }
}

pub fn collect_observation(
    spec: &RunSpec,
    round_id_hex: &str,
) -> Result<RunObservation, Box<dyn std::error::Error>> {
    let primary_api_url = resolve_primary_api_url(spec)?;
    let helper_token = spec
        .helper_api_token
        .clone()
        .or_else(|| std::env::var("HELPER_API_TOKEN").ok())
        .filter(|s| !s.trim().is_empty());

    let mut obs = RunObservation {
        round_id: Some(round_id_hex.to_string()),
        ..RunObservation::default()
    };
    let mut errors = Vec::new();
    let planned_down_targets = spec
        .chaos
        .iter()
        .filter(|step| matches!(step.action, ChaosAction::StopUntilVoteEnd))
        .map(|step| step.target.as_str())
        .collect::<Vec<_>>();

    match get_json_from(
        &primary_api_url,
        &format!("/shielded-vote/v1/round/{round_id_hex}"),
        None,
    ) {
        Ok(json) => {
            if let Some(round) = json.get("round") {
                obs.status = round
                    .get("status")
                    .and_then(session_status_label)
                    .or_else(|| Some("unknown".to_string()));
                obs.tally_timed_out = round
                    .get("tally_timed_out")
                    .or_else(|| round.get("tallyTimedOut"))
                    .and_then(|v| v.as_bool())
                    .unwrap_or(false);
                obs.validator_count = count_round_validators(round);
            }
        }
        Err(err) => errors.push(format!("round query failed: {err}")),
    }

    match get_json_from(
        &primary_api_url,
        &format!("/shielded-vote/v1/commitment-tree/{round_id_hex}/latest"),
        None,
    ) {
        Ok(json) => {
            obs.commitment_tree_delta = json
                .get("tree")
                .and_then(|tree| tree.get("next_index").or_else(|| tree.get("nextIndex")))
                .and_then(json_u64);
        }
        Err(err) => errors.push(format!("commitment-tree query failed: {err}")),
    }

    match get_json_from(
        &primary_api_url,
        &format!("/shielded-vote/v1/tally-results/{round_id_hex}"),
        None,
    ) {
        Ok(json) => {
            obs.tally = parse_tally_results(&json);
        }
        Err(err) => errors.push(format!("tally-results query failed: {err}")),
    }

    match query_vote_summary_unique_shares(&primary_api_url, round_id_hex) {
        Ok(Some(shares)) => obs.unique_shares_revealed = Some(shares),
        Ok(None) => {}
        Err(_) => {}
    }

    for server in &spec.vote_servers {
        match get_json_from(
            &server.url,
            "/shielded-vote/v1/queue-status",
            helper_token.as_deref(),
        ) {
            Ok(json) => {
                if let Some(queue) = json.get(round_id_hex) {
                    obs.helper_queues.push(ObservedHelperQueue {
                        server: server.label.clone(),
                        total: queue.get("total").and_then(json_u64).unwrap_or(0),
                        pending: queue.get("pending").and_then(json_u64).unwrap_or(0),
                        submitted: queue.get("submitted").and_then(json_u64).unwrap_or(0),
                        failed: queue.get("failed").and_then(json_u64).unwrap_or(0),
                    });
                }
            }
            Err(err) => {
                let planned_down = planned_down_targets.iter().any(|target| {
                    *target == server.label || server.ssh_host.as_deref() == Some(*target)
                });
                if !planned_down {
                    errors.push(format!("{} queue-status failed: {err}", server.label));
                }
            }
        }
    }

    obs.errors = errors;
    Ok(obs)
}

pub fn generate_expected_model(spec: &RunSpec) -> ExpectedModel {
    let mut rng = StdRng::seed_from_u64(spec.seed);
    let mut wallets = Vec::with_capacity(spec.voter_count);
    let mut tally: BTreeMap<(u32, u32), u64> = BTreeMap::new();
    let mut tier_counts: BTreeMap<WalletTier, usize> = BTreeMap::new();
    let mut timing_counts: BTreeMap<String, usize> = BTreeMap::new();
    let mut eligible_wallets = 0usize;
    let mut dust_wallets = 0usize;
    let mut surviving_bundles = 0u64;
    let mut vote_commitments = 0u64;
    let mut expected_unique_shares = 0u64;
    let mut planned_votes = Vec::new();

    for id in 0..spec.voter_count {
        let tier = tier_for_index(id, spec.voter_count, &spec.wallet_mix);
        let timing = timing_for_index(id, spec.voter_count, &spec.timing);
        let notes = note_plan_for_tier(id, &tier);
        let bundles = chunk_notes(&notes);
        let eligible_weight = bundles.iter().map(|b| b.eligible_weight_zatoshi).sum();

        *tier_counts.entry(tier.clone()).or_default() += 1;
        *timing_counts.entry(format!("{:?}", timing)).or_default() += 1;

        if bundles.is_empty() {
            dust_wallets += 1;
        } else {
            eligible_wallets += 1;
        }

        for (bundle_index, bundle) in bundles.iter().enumerate() {
            let bundle_global_index = surviving_bundles;
            surviving_bundles += 1;
            for (proposal_index, proposal) in spec.proposals.iter().enumerate() {
                vote_commitments += 1;
                let decision = choose_decision(&mut rng, proposal);
                let share_count = match timing {
                    TimingCohort::LastMoment => LAST_MOMENT_SHARE_COUNT,
                    TimingCohort::EarlyBurst | TimingCohort::MidBurst => NORMAL_SHARE_COUNT,
                };
                *tally.entry((proposal.id, decision)).or_default() +=
                    bundle.eligible_weight_zatoshi;
                expected_unique_shares += share_count;
                planned_votes.push(PlannedVote {
                    wallet_id: id,
                    wallet_tier: tier.clone(),
                    bundle_index,
                    bundle_global_index,
                    proposal_id: proposal.id,
                    proposal_title: proposal.title.clone(),
                    decision,
                    option_label: proposal
                        .options
                        .get(decision as usize)
                        .cloned()
                        .unwrap_or_else(|| format!("option-{decision}")),
                    timing: timing.clone(),
                    submit_offset_secs: submit_offset_secs(
                        id,
                        bundle_index,
                        proposal_index,
                        &timing,
                        spec.round_duration_secs,
                    ),
                    eligible_weight_zatoshi: bundle.eligible_weight_zatoshi,
                    expected_unique_shares: share_count,
                    note_positions: bundle.notes.iter().map(|n| n.position).collect(),
                });
            }
        }

        wallets.push(SyntheticWallet {
            id,
            tier,
            timing,
            notes,
            bundles,
            eligible_weight,
        });
    }

    let mut expected_tally = Vec::new();
    for proposal in &spec.proposals {
        for (decision, label) in proposal.options.iter().enumerate() {
            expected_tally.push(ExpectedTallyEntry {
                proposal_id: proposal.id,
                decision: decision as u32,
                option_label: label.clone(),
                total_value_zatoshi: *tally.get(&(proposal.id, decision as u32)).unwrap_or(&0),
            });
        }
    }

    ExpectedModel {
        voter_count: spec.voter_count,
        eligible_wallets,
        dust_wallets,
        surviving_bundles,
        vote_commitments,
        expected_tree_delta: surviving_bundles + (2 * vote_commitments),
        expected_unique_shares,
        expected_helper_acceptances: expected_unique_shares * u64::from(spec.helper_target_count),
        expected_tally,
        planned_votes,
        tier_counts,
        timing_counts,
        wallets,
    }
}

pub fn chunk_notes(notes: &[NotePlan]) -> Vec<NoteBundle> {
    let mut sorted = notes.to_vec();
    sorted.sort_by(|a, b| {
        b.value_zatoshi
            .cmp(&a.value_zatoshi)
            .then_with(|| a.position.cmp(&b.position))
    });

    let mut candidate_bundles = Vec::new();
    for chunk in sorted.chunks(5) {
        let raw_total_zatoshi = chunk.iter().map(|n| n.value_zatoshi).sum::<u64>();
        if raw_total_zatoshi < BALLOT_DIVISOR {
            continue;
        }
        let mut bundle_notes = chunk.to_vec();
        bundle_notes.sort_by_key(|n| n.position);
        candidate_bundles.push(NoteBundle {
            notes: bundle_notes,
            raw_total_zatoshi,
            eligible_weight_zatoshi: (raw_total_zatoshi / BALLOT_DIVISOR) * BALLOT_DIVISOR,
        });
    }

    candidate_bundles.sort_by(|a, b| {
        b.raw_total_zatoshi
            .cmp(&a.raw_total_zatoshi)
            .then_with(|| min_position(&a.notes).cmp(&min_position(&b.notes)))
    });
    candidate_bundles
}

fn min_position(notes: &[NotePlan]) -> u32 {
    notes.iter().map(|n| n.position).min().unwrap_or(u32::MAX)
}

fn tier_for_index(index: usize, total: usize, mix: &WalletMix) -> WalletTier {
    let pct = percentile_index(index, total);
    let dust = mix.dust_only_pct;
    let exact = dust.saturating_add(mix.exact_threshold_pct);
    let five = exact.saturating_add(mix.five_note_pct);
    let multi = five.saturating_add(mix.multi_bundle_pct);
    if pct < dust {
        WalletTier::DustOnly
    } else if pct < exact {
        WalletTier::ExactThreshold
    } else if pct < five {
        WalletTier::FiveNote
    } else if pct < multi {
        WalletTier::MultiBundle
    } else {
        WalletTier::Whale
    }
}

fn timing_for_index(index: usize, total: usize, timing: &TimingSpec) -> TimingCohort {
    let pct = percentile_index(index, total);
    let early = timing.early_burst_pct;
    let mid = early.saturating_add(timing.mid_burst_pct);
    if pct < early {
        TimingCohort::EarlyBurst
    } else if pct < mid {
        TimingCohort::MidBurst
    } else {
        TimingCohort::LastMoment
    }
}

fn submit_offset_secs(
    wallet_id: usize,
    bundle_index: usize,
    proposal_index: usize,
    timing: &TimingCohort,
    round_duration_secs: u64,
) -> u64 {
    if round_duration_secs <= 1 {
        return 0;
    }

    let jitter =
        ((wallet_id as u64 * 37) + (bundle_index as u64 * 13) + (proposal_index as u64 * 7))
            % round_duration_secs;

    match timing {
        TimingCohort::EarlyBurst => {
            let window = (round_duration_secs / 8).max(1).min(20 * 60);
            jitter % window
        }
        TimingCohort::MidBurst => {
            let window = (round_duration_secs / 6).max(1).min(45 * 60);
            let start = round_duration_secs
                .saturating_div(2)
                .saturating_sub(window / 2);
            (start + (jitter % window)).min(round_duration_secs - 1)
        }
        TimingCohort::LastMoment => {
            let window = round_duration_secs.min(2 * 60).max(1);
            round_duration_secs.saturating_sub(1 + (jitter % window))
        }
    }
}

fn percentile_index(index: usize, total: usize) -> u8 {
    if total == 0 {
        return 0;
    }
    ((index * 100) / total).min(99) as u8
}

fn note_plan_for_tier(wallet_id: usize, tier: &WalletTier) -> Vec<NotePlan> {
    let base = (wallet_id as u32) * 32;
    let values = match tier {
        WalletTier::DustOnly => vec![1_000_000, 2_000_000, 3_000_000],
        WalletTier::ExactThreshold => vec![BALLOT_DIVISOR],
        WalletTier::FiveNote => vec![4_000_000, 3_000_000, 2_500_000, 2_000_000, 1_000_000],
        WalletTier::MultiBundle => vec![
            20_000_000, 15_000_000, 12_500_000, 10_000_000, 5_000_000, 12_500_000, 8_000_000,
            5_000_000, 3_000_000, 2_000_000,
        ],
        WalletTier::Whale => vec![
            125_000_000,
            100_000_000,
            75_000_000,
            50_000_000,
            25_000_000,
            40_000_000,
            35_000_000,
            30_000_000,
            25_000_000,
            20_000_000,
            18_000_000,
            16_000_000,
            14_000_000,
            12_500_000,
            10_000_000,
        ],
    };
    values
        .into_iter()
        .enumerate()
        .map(|(offset, value_zatoshi)| NotePlan {
            position: base + offset as u32,
            value_zatoshi,
        })
        .collect()
}

fn choose_decision(rng: &mut StdRng, proposal: &ProposalSpec) -> u32 {
    let option_count = proposal.options.len().max(1);
    if proposal.decision_weights.len() != option_count
        || proposal.decision_weights.iter().all(|w| *w == 0)
    {
        return rng.gen_range(0..option_count) as u32;
    }
    let total_weight = proposal
        .decision_weights
        .iter()
        .map(|w| u64::from(*w))
        .sum::<u64>();
    let mut ticket = rng.gen_range(0..total_weight);
    for (idx, weight) in proposal.decision_weights.iter().enumerate() {
        let weight = u64::from(*weight);
        if ticket < weight {
            return idx as u32;
        }
        ticket -= weight;
    }
    (option_count - 1) as u32
}

fn resolve_primary_api_url(spec: &RunSpec) -> Result<String, Box<dyn std::error::Error>> {
    if !spec.primary_api_url.trim().is_empty() {
        return Ok(spec
            .primary_api_url
            .trim()
            .trim_end_matches('/')
            .to_string());
    }
    if let Some(first) = spec.vote_servers.first() {
        return Ok(first.url.trim().trim_end_matches('/').to_string());
    }
    Err("primary_api_url is required when vote_servers is empty".into())
}

fn get_json_from(
    base_url: &str,
    path: &str,
    helper_token: Option<&str>,
) -> Result<Value, Box<dyn std::error::Error>> {
    let url = format!("{}{}", base_url.trim_end_matches('/'), path);
    let mut request = reqwest::blocking::Client::builder()
        .timeout(std::time::Duration::from_secs(20))
        .build()?
        .get(url);
    if let Some(token) = helper_token {
        request = request.header("X-Helper-Token", token);
    }
    let response = request.send()?;
    let status = response.status();
    if !status.is_success() {
        return Err(format!("HTTP {status}").into());
    }
    Ok(response.json()?)
}

fn json_u64(value: &Value) -> Option<u64> {
    value
        .as_u64()
        .or_else(|| value.as_str().and_then(|s| s.parse().ok()))
}

fn session_status_label(value: &Value) -> Option<String> {
    if let Some(s) = value.as_str() {
        return Some(s.to_ascii_uppercase());
    }
    match value.as_i64()? {
        1 => Some("ACTIVE".to_string()),
        2 => Some("TALLYING".to_string()),
        3 => Some("FINALIZED".to_string()),
        4 => Some("PENDING".to_string()),
        5 => Some("CEREMONY_FAILED".to_string()),
        _ => Some("UNSPECIFIED".to_string()),
    }
}

fn count_round_validators(round: &Value) -> Option<u32> {
    [
        "validators",
        "ceremony_validators",
        "ceremonyValidators",
        "pallas_keys",
        "pallasKeys",
    ]
    .into_iter()
    .find_map(|field| {
        round
            .get(field)
            .and_then(|v| v.as_array())
            .map(|items| items.len() as u32)
    })
}

fn parse_tally_results(json: &Value) -> Vec<ObservedTallyEntry> {
    json.get("results")
        .and_then(|v| v.as_array())
        .into_iter()
        .flatten()
        .filter_map(|entry| {
            Some(ObservedTallyEntry {
                proposal_id: entry
                    .get("proposal_id")
                    .or_else(|| entry.get("proposalId"))
                    .and_then(json_u64)? as u32,
                decision: entry
                    .get("vote_decision")
                    .or_else(|| entry.get("voteDecision"))
                    .and_then(json_u64)? as u32,
                total_value_zatoshi: parse_tally_value_zatoshi(entry)?,
            })
        })
        .collect()
}

fn parse_tally_value_zatoshi(entry: &Value) -> Option<u64> {
    if let Some(value) = entry
        .get("total_value_zatoshi")
        .or_else(|| entry.get("totalValueZatoshi"))
        .and_then(json_u64)
    {
        return Some(value);
    }

    // Current chain responses expose decrypted ballot counts in total_value.
    // The launch gates compare ZEC/zatoshi, so convert by the same divisor
    // used by ZKP #1/#2. If the API grows an explicit zatoshi field, prefer it
    // above and avoid this conversion.
    entry
        .get("total_value")
        .or_else(|| entry.get("totalValue"))
        .and_then(json_u64)
        .and_then(|ballots| ballots.checked_mul(BALLOT_DIVISOR))
}

fn query_vote_summary_unique_shares(
    primary_api_url: &str,
    round_id_hex: &str,
) -> Result<Option<u64>, Box<dyn std::error::Error>> {
    let paths = [
        format!("/shielded-vote/v1/vote-summary/{round_id_hex}"),
        format!("/shielded-vote/v1/summary/{round_id_hex}"),
    ];
    let mut last_err = None;
    for path in paths {
        match get_json_from(primary_api_url, &path, None) {
            Ok(json) => return Ok(parse_vote_summary_unique_shares(&json)),
            Err(err) => last_err = Some(err),
        }
    }
    Err(last_err.unwrap_or_else(|| "vote-summary unavailable".into()))
}

fn parse_vote_summary_unique_shares(json: &Value) -> Option<u64> {
    let proposals = json
        .get("proposals")
        .or_else(|| json.get("summary").and_then(|s| s.get("proposals")))?
        .as_array()?;
    let mut total = 0u64;
    for proposal in proposals {
        for option in proposal.get("options")?.as_array()? {
            total += option
                .get("ballot_count")
                .or_else(|| option.get("ballotCount"))
                .and_then(json_u64)
                .unwrap_or(0);
        }
    }
    Some(total)
}

pub fn evaluate_gates(
    spec: &RunSpec,
    expected: &ExpectedModel,
    observation: Option<&RunObservation>,
) -> Vec<GateResult> {
    let Some(obs) = observation else {
        return vec![GateResult {
            name: "run observation".to_string(),
            status: GateStatus::Pending,
            expected: "final run observations".to_string(),
            actual: "not provided".to_string(),
            details: "Generate observations from the loadtest network to evaluate strict gates."
                .to_string(),
        }];
    };

    let mut gates = Vec::new();
    let status = obs.status.clone().unwrap_or_else(|| "unknown".to_string());
    gates.push(GateResult {
        name: "round finalized".to_string(),
        status: if status.eq_ignore_ascii_case("FINALIZED") {
            GateStatus::Pass
        } else {
            GateStatus::Fail
        },
        expected: "FINALIZED".to_string(),
        actual: status,
        details: "Round must reach finalization.".to_string(),
    });

    gates.push(GateResult {
        name: "no tally timeout".to_string(),
        status: if obs.tally_timed_out {
            GateStatus::Fail
        } else {
            GateStatus::Pass
        },
        expected: "false".to_string(),
        actual: obs.tally_timed_out.to_string(),
        details: "A timeout finalization is not launch-success evidence.".to_string(),
    });

    gates.push(GateResult {
        name: "validator count".to_string(),
        status: compare_optional_u64(
            obs.validator_count.map(u64::from),
            u64::from(spec.validator_count),
        ),
        expected: spec.validator_count.to_string(),
        actual: obs
            .validator_count
            .map(|v| v.to_string())
            .unwrap_or_else(|| "missing".to_string()),
        details: "The launch gate targets the planned validator count.".to_string(),
    });

    gates.push(GateResult {
        name: "commitment tree delta".to_string(),
        status: compare_optional_u64(obs.commitment_tree_delta, expected.expected_tree_delta),
        expected: expected.expected_tree_delta.to_string(),
        actual: obs
            .commitment_tree_delta
            .map(|v| v.to_string())
            .unwrap_or_else(|| "missing".to_string()),
        details: "Expected one delegation leaf per surviving bundle and two leaves per cast vote."
            .to_string(),
    });

    gates.push(GateResult {
        name: "unique shares revealed".to_string(),
        status: compare_optional_u64(obs.unique_shares_revealed, expected.expected_unique_shares),
        expected: expected.expected_unique_shares.to_string(),
        actual: obs
            .unique_shares_revealed
            .map(|v| v.to_string())
            .unwrap_or_else(|| "missing".to_string()),
        details: "Every expected unique share must be revealed exactly once by the helper fleet."
            .to_string(),
    });

    let observed_tally = tally_map(&obs.tally);
    for entry in &expected.expected_tally {
        let actual = observed_tally
            .get(&(entry.proposal_id, entry.decision))
            .copied();
        gates.push(GateResult {
            name: format!("tally p{} d{}", entry.proposal_id, entry.decision),
            status: compare_optional_u64(actual, entry.total_value_zatoshi),
            expected: entry.total_value_zatoshi.to_string(),
            actual: actual
                .map(|v| v.to_string())
                .unwrap_or_else(|| "missing".to_string()),
            details: entry.option_label.clone(),
        });
    }

    let planned_down_targets = spec
        .chaos
        .iter()
        .filter(|step| matches!(step.action, ChaosAction::StopUntilVoteEnd))
        .map(|step| step.target.as_str())
        .collect::<Vec<_>>();
    let unexpected_helper_failures: u64 = obs
        .helper_queues
        .iter()
        .filter(|q| {
            !planned_down_targets
                .iter()
                .any(|target| *target == q.server)
        })
        .map(|q| q.failed)
        .sum();
    gates.push(GateResult {
        name: "helper permanent failures".to_string(),
        status: if unexpected_helper_failures == 0 {
            GateStatus::Pass
        } else {
            GateStatus::Fail
        },
        expected: "0".to_string(),
        actual: unexpected_helper_failures.to_string(),
        details: "Planned down-server backlog should be recorded separately from permanent helper failures.".to_string(),
    });

    gates.push(GateResult {
        name: "unexpected errors".to_string(),
        status: if obs.errors.is_empty() {
            GateStatus::Pass
        } else {
            GateStatus::Fail
        },
        expected: "0".to_string(),
        actual: obs.errors.len().to_string(),
        details: obs.errors.join("; "),
    });

    gates
}

fn compare_optional_u64(actual: Option<u64>, expected: u64) -> GateStatus {
    match actual {
        Some(actual) if actual == expected => GateStatus::Pass,
        Some(_) => GateStatus::Fail,
        None => GateStatus::Pending,
    }
}

fn tally_map(entries: &[ObservedTallyEntry]) -> BTreeMap<(u32, u32), u64> {
    entries
        .iter()
        .map(|entry| {
            (
                (entry.proposal_id, entry.decision),
                entry.total_value_zatoshi,
            )
        })
        .collect()
}

fn summarize_gates(gates: &[GateResult]) -> GateStatus {
    if gates.iter().any(|g| g.status == GateStatus::Fail) {
        GateStatus::Fail
    } else if gates.iter().any(|g| g.status == GateStatus::Pending) {
        GateStatus::Pending
    } else {
        GateStatus::Pass
    }
}

pub fn render_summary_markdown(report: &LaunchReport) -> String {
    let mut out = String::new();
    out.push_str("# Shielded Vote Launch Validation\n\n");
    out.push_str(&format!("**Status:** {:?}\n\n", report.overall_status));
    out.push_str(&format!("- Run: `{}`\n", report.spec.run_name));
    out.push_str(&format!("- Voters: `{}`\n", report.expected.voter_count));
    out.push_str(&format!(
        "- Validators: `{}`\n",
        report.spec.validator_count
    ));
    out.push_str(&format!(
        "- Eligible wallets: `{}`\n",
        report.expected.eligible_wallets
    ));
    out.push_str(&format!(
        "- Surviving bundles: `{}`\n",
        report.expected.surviving_bundles
    ));
    out.push_str(&format!(
        "- Expected tree delta: `{}`\n",
        report.expected.expected_tree_delta
    ));
    out.push_str(&format!(
        "- Expected unique shares: `{}`\n\n",
        report.expected.expected_unique_shares
    ));

    out.push_str("## Gates\n\n");
    out.push_str("| Gate | Status | Expected | Actual |\n");
    out.push_str("|---|---:|---:|---:|\n");
    for gate in &report.gates {
        out.push_str(&format!(
            "| {} | {:?} | {} | {} |\n",
            gate.name, gate.status, gate.expected, gate.actual
        ));
    }
    out
}

pub fn render_report_html(report: &LaunchReport) -> String {
    let data = serde_json::to_string_pretty(report).expect("report serializes");
    let escaped_data = data.replace("</", "<\\/");
    format!(
        r#"<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>Shielded Vote Launch Validation</title>
<style>
:root {{ color-scheme: light; --bg:#f7f8fa; --panel:#fff; --text:#17202a; --muted:#667085; --border:#d9dee7; --ok:#087443; --bad:#b42318; --wait:#8a5a00; --accent:#1f6feb; }}
* {{ box-sizing:border-box; }}
body {{ margin:0; background:var(--bg); color:var(--text); font:14px/1.45 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }}
header {{ background:#111827; color:#fff; padding:24px 28px; }}
main {{ padding:20px 28px 40px; max-width:1400px; margin:0 auto; }}
h1 {{ margin:0 0 8px; font-size:26px; letter-spacing:0; }}
h2 {{ margin:22px 0 10px; font-size:18px; }}
.subtle {{ color:var(--muted); }}
.hero {{ display:grid; grid-template-columns:repeat(5,minmax(140px,1fr)); gap:12px; margin-top:16px; }}
.metric, section {{ background:var(--panel); border:1px solid var(--border); border-radius:8px; padding:14px; }}
.metric strong {{ display:block; font-size:22px; margin-top:4px; }}
.status-pass {{ color:var(--ok); }}
.status-fail {{ color:var(--bad); }}
.status-pending {{ color:var(--wait); }}
.tabs {{ display:flex; gap:8px; flex-wrap:wrap; margin:20px 0; }}
.tabs button {{ border:1px solid var(--border); background:#fff; color:var(--text); border-radius:6px; padding:8px 10px; cursor:pointer; }}
.tabs button.active {{ border-color:var(--accent); color:var(--accent); font-weight:700; }}
.panel {{ display:none; }}
.panel.active {{ display:block; }}
.toolbar {{ display:flex; gap:10px; align-items:center; margin:8px 0 12px; }}
input, select {{ border:1px solid var(--border); border-radius:6px; padding:7px 8px; min-height:34px; background:#fff; }}
table {{ width:100%; border-collapse:collapse; background:#fff; border:1px solid var(--border); border-radius:8px; overflow:hidden; }}
th, td {{ padding:9px 10px; border-bottom:1px solid var(--border); text-align:left; white-space:nowrap; }}
th {{ background:#f0f3f8; cursor:pointer; user-select:none; font-size:12px; text-transform:uppercase; color:#344054; }}
tr:last-child td {{ border-bottom:0; }}
code, pre {{ font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; }}
pre {{ overflow:auto; background:#0b1020; color:#d8e1ff; padding:14px; border-radius:8px; max-height:640px; }}
.grid2 {{ display:grid; grid-template-columns:1fr 1fr; gap:14px; }}
.timeline {{ display:flex; gap:8px; align-items:flex-end; min-height:130px; padding:12px; border:1px solid var(--border); border-radius:8px; background:#fff; overflow-x:auto; }}
.bar {{ min-width:34px; background:#d6e4ff; border:1px solid #9bbcff; border-radius:5px 5px 0 0; position:relative; }}
.bar span {{ position:absolute; bottom:-24px; left:0; font-size:11px; color:var(--muted); }}
@media (max-width:900px) {{ .hero, .grid2 {{ grid-template-columns:1fr; }} th,td {{ white-space:normal; }} }}
</style>
</head>
<body>
<header>
  <h1>Shielded Vote Launch Validation</h1>
  <div id="run-meta"></div>
  <div class="hero" id="hero"></div>
</header>
<main>
  <div class="tabs" id="tabs"></div>
  <section class="panel active" id="overview"></section>
  <section class="panel" id="tally"></section>
  <section class="panel" id="shares"></section>
  <section class="panel" id="latency"></section>
  <section class="panel" id="chaos"></section>
  <section class="panel" id="raw"><pre id="raw-json"></pre></section>
</main>
<script id="run-data" type="application/json">{escaped_data}</script>
<script>
const report = JSON.parse(document.getElementById('run-data').textContent);
const tabs = ['overview','tally','shares','latency','chaos','raw'];
const statusClass = s => s === 'Pass' || s === 'pass' ? 'status-pass' : s === 'Fail' || s === 'fail' ? 'status-fail' : 'status-pending';
const zec = z => (Number(z || 0) / 100000000).toLocaleString(undefined, {{maximumFractionDigits: 8}});
const expectedZatoshi = report.expected.expected_tally.reduce((s,e)=>s+Number(e.total_value_zatoshi),0);
const actualZatoshi = (report.observation?.tally || []).reduce((s,e)=>s+Number(e.total_value_zatoshi),0);
const observedShares = report.observation?.unique_shares_revealed;
document.getElementById('run-meta').innerHTML = `<span class="subtle">Run</span> <code>${{report.spec.run_name}}</code> <span class="subtle">Seed</span> <code>${{report.spec.seed}}</code>`;
document.getElementById('hero').innerHTML = [
  ['Status', report.overall_status],
  ['Round', report.observation?.round_id || 'pending'],
  ['Expected / Actual ZEC', `${{zec(expectedZatoshi)}} / ${{report.observation ? zec(actualZatoshi) : 'pending'}}`],
  ['Expected / Actual Shares', `${{report.expected.expected_unique_shares.toLocaleString()}} / ${{observedShares == null ? 'pending' : Number(observedShares).toLocaleString()}}`],
  ['Tree Delta', report.expected.expected_tree_delta.toLocaleString()]
].map(([k,v],i)=>`<div class="metric"><span class="subtle">${{k}}</span><strong class="${{i===0?statusClass(v):''}}">${{v}}</strong></div>`).join('');
document.getElementById('tabs').innerHTML = tabs.map(t=>`<button data-tab="${{t}}" class="${{t==='overview'?'active':''}}">${{t[0].toUpperCase()+t.slice(1)}}</button>`).join('');
document.getElementById('tabs').onclick = e => {{
  const tab = e.target?.dataset?.tab; if (!tab) return;
  document.querySelectorAll('.tabs button').forEach(b=>b.classList.toggle('active', b.dataset.tab===tab));
  document.querySelectorAll('.panel').forEach(p=>p.classList.toggle('active', p.id===tab));
}};
function table(rows, cols) {{
  return `<table><thead><tr>${{cols.map(c=>`<th data-key="${{c.key}}">${{c.label}}</th>`).join('')}}</tr></thead><tbody>${{rows.map(r=>`<tr>${{cols.map(c=>`<td>${{c.render?c.render(r):r[c.key]??''}}</td>`).join('')}}</tr>`).join('')}}</tbody></table>`;
}}
function installSort(root) {{
  root.querySelectorAll('th').forEach(th => th.onclick = () => {{
    const table = th.closest('table'); const idx = [...th.parentNode.children].indexOf(th);
    const rows = [...table.tBodies[0].rows].sort((a,b)=>a.cells[idx].textContent.localeCompare(b.cells[idx].textContent, undefined, {{numeric:true}}));
    table.tBodies[0].append(...rows);
  }});
}}
document.getElementById('overview').innerHTML = `<h2>Strict Gates</h2>` + table(report.gates, [
  {{key:'name', label:'Gate'}}, {{key:'status', label:'Status', render:r=>`<span class="${{statusClass(r.status)}}">${{r.status}}</span>`}},
  {{key:'expected', label:'Expected'}}, {{key:'actual', label:'Actual'}}, {{key:'details', label:'Details'}}
]) + `<h2>Wallet Mix</h2>` + table(Object.entries(report.expected.tier_counts).map(([tier,count])=>({{tier,count}})), [{{key:'tier',label:'Tier'}},{{key:'count',label:'Wallets'}}]);
document.getElementById('tally').innerHTML = `<div class="toolbar"><input id="tally-filter" placeholder="Filter proposal or option"></div><div id="tally-table"></div>`;
function renderTally() {{
  const q = document.getElementById('tally-filter').value.toLowerCase();
  const obs = new Map((report.observation?.tally||[]).map(e=>[`${{e.proposal_id}}:${{e.decision}}`, e.total_value_zatoshi]));
  const rows = report.expected.expected_tally.map(e=>({{...e, expected_zec:zec(e.total_value_zatoshi), actual_zec: obs.has(`${{e.proposal_id}}:${{e.decision}}`) ? zec(obs.get(`${{e.proposal_id}}:${{e.decision}}`)) : 'missing'}}))
    .filter(e => `${{e.proposal_id}} ${{e.decision}} ${{e.option_label}}`.toLowerCase().includes(q));
  document.getElementById('tally-table').innerHTML = table(rows, [
    {{key:'proposal_id',label:'Proposal'}},{{key:'decision',label:'Decision'}},{{key:'option_label',label:'Option'}},{{key:'expected_zec',label:'Expected ZEC'}},{{key:'actual_zec',label:'Actual ZEC'}}
  ]);
  installSort(document.getElementById('tally-table'));
}}
document.getElementById('tally-filter').oninput = renderTally; renderTally();
document.getElementById('shares').innerHTML = `<h2>Expected Shares</h2>` + table([
  {{name:'Unique share nullifiers', value:report.expected.expected_unique_shares.toLocaleString()}},
  {{name:'Observed unique shares', value:observedShares == null ? 'pending' : Number(observedShares).toLocaleString()}},
  {{name:'Helper acceptances', value:report.expected.expected_helper_acceptances.toLocaleString()}},
  {{name:'Vote commitments', value:report.expected.vote_commitments.toLocaleString()}},
  {{name:'Surviving bundles', value:report.expected.surviving_bundles.toLocaleString()}}
], [{{key:'name',label:'Metric'}},{{key:'value',label:'Value'}}]) + `<h2>Observed Helper Queues</h2>` + table(report.observation?.helper_queues||[], [
  {{key:'server',label:'Server'}},{{key:'total',label:'Total'}},{{key:'pending',label:'Pending'}},{{key:'submitted',label:'Submitted'}},{{key:'failed',label:'Failed'}}
]);
document.getElementById('latency').innerHTML = `<h2>Phase Metrics</h2>` + table(report.observation?.phase_metrics||[], [
  {{key:'phase',label:'Phase'}},{{key:'total',label:'Total'}},{{key:'succeeded',label:'Succeeded'}},{{key:'failed',label:'Failed'}},{{key:'p50_ms',label:'p50 ms'}},{{key:'p95_ms',label:'p95 ms'}},{{key:'p99_ms',label:'p99 ms'}}
]);
document.getElementById('chaos').innerHTML = `<div class="timeline">${{report.spec.chaos.map(c=>`<div class="bar" style="height:${{30 + Math.min(90, c.at_offset_secs / report.spec.round_duration_secs * 120)}}px"><span>${{c.id}}</span></div>`).join('')}}</div><h2>Planned Chaos</h2>` + table(report.spec.chaos, [
  {{key:'id',label:'ID'}},{{key:'target',label:'Target'}},{{key:'action',label:'Action'}},{{key:'at_offset_secs',label:'Offset sec'}},{{key:'duration_secs',label:'Duration sec'}},{{key:'description',label:'Description'}}
]) + `<h2>Observed Chaos</h2>` + table(report.observation?.chaos_events||[], [
  {{key:'id',label:'ID'}},{{key:'target',label:'Target'}},{{key:'started_at_offset_secs',label:'Start'}},{{key:'ended_at_offset_secs',label:'End'}},{{key:'result',label:'Result'}}
]);
document.getElementById('raw-json').textContent = JSON.stringify(report, null, 2);
document.querySelectorAll('section').forEach(installSort);
</script>
</body>
</html>"#
    )
}
