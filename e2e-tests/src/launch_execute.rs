//! Live launch-validation executor.
//!
//! This module deliberately keeps side effects out of `launch`: it creates the
//! round, submits protocol-correct delegation/cast/share payloads, runs
//! SSH-controlled outages, and returns observations for the report renderer.

use crate::api::{
    broadcast_cosmos_msg, commitment_tree_latest, commitment_tree_next_index,
    default_cosmos_tx_config, get_round_ea_pk, import_first_vote_manager_key, key_account_address,
    wait_for_create_round_id, wait_for_round_status, CosmosTxConfig, FIRST_VOTE_MANAGER_KEY_NAME,
    SESSION_STATUS_ACTIVE, SESSION_STATUS_FINALIZED,
};
use crate::launch::{
    collect_observation, generate_expected_model, ExpectedModel, ObservedChaosEvent,
    ObservedPhaseMetric, PlannedVote, ProposalSpec, RunObservation, RunSpec, ServiceEndpoint,
    TimingCohort,
};
use crate::metrics::{compute_aggregate, MetricsCollector, Sample};
use crate::payloads::{self, DelegationBundlePayload, SetupRoundFields};
use crate::setup::{prepare_launch_delegation_bundles, VoteProofDelegationData};
use base64::{engine::general_purpose::STANDARD as B64, Engine};
use ff::{Field, PrimeField};
use group::GroupEncoding;
use pasta_curves::pallas;
use rand::rngs::OsRng;
use serde_json::{json, Value};
use std::process::Command;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use voting_circuits::vote_proof::{
    builder::build_vote_proof_from_delegation, circuit::VOTE_COMM_TREE_DEPTH,
};

type DynError = Box<dyn std::error::Error + Send + Sync>;

const MAX_PROPOSAL_AUTHORITY: u64 = 65_535;

#[derive(Clone, Debug)]
pub struct ExecutionOptions {
    pub dry_run: bool,
    pub skip_chaos: bool,
    pub setup_buffer_secs: u64,
    pub active_timeout_secs: u64,
    pub phase_timeout_secs: u64,
    pub finalization_timeout_secs: u64,
    pub share_reveal_window_secs: u64,
    pub cast_proof_threads: usize,
}

impl Default for ExecutionOptions {
    fn default() -> Self {
        Self {
            dry_run: false,
            skip_chaos: false,
            setup_buffer_secs: env_u64("LAUNCH_SETUP_BUFFER_SECS", 60 * 60),
            active_timeout_secs: env_u64("LAUNCH_ACTIVE_TIMEOUT_SECS", 10 * 60),
            phase_timeout_secs: env_u64("LAUNCH_PHASE_TIMEOUT_SECS", 45 * 60),
            finalization_timeout_secs: env_u64("LAUNCH_FINALIZATION_TIMEOUT_SECS", 45 * 60),
            share_reveal_window_secs: env_u64("LAUNCH_SHARE_REVEAL_WINDOW_SECS", 10 * 60),
            cast_proof_threads: env_usize("LAUNCH_CAST_PROOF_THREADS", 4),
        }
    }
}

#[derive(Clone, Debug)]
pub struct ExecutionPlan {
    pub bundle_count: usize,
    pub vote_count: usize,
    pub expected_tree_delta: u64,
    pub vote_end_time: u64,
    pub vote_start_time: u64,
    pub helper_acceptances_planned: u64,
}

pub fn dry_run_execution_plan(spec: &RunSpec, options: &ExecutionOptions) -> ExecutionPlan {
    let expected = generate_expected_model(spec);
    let vote_end_time = now_unix() + spec.round_duration_secs + options.setup_buffer_secs;
    execution_plan(spec, &expected, vote_end_time)
}

pub fn execute_launch_run(
    spec: &RunSpec,
    options: &ExecutionOptions,
) -> Result<RunObservation, DynError> {
    let primary_api_url = resolve_primary_api_url(spec)?;
    std::env::set_var("SVOTE_API_URL", &primary_api_url);
    std::env::set_var("HELPER_SERVER_URL", &primary_api_url);

    let expected = generate_expected_model(spec);
    let vote_end_time = now_unix() + spec.round_duration_secs + options.setup_buffer_secs;
    let plan = execution_plan(spec, &expected, vote_end_time);
    eprintln!(
        "[launch] plan: {} bundles, {} cast votes, tree delta {}, vote window {}s, setup buffer {}s",
        plan.bundle_count,
        plan.vote_count,
        plan.expected_tree_delta,
        spec.round_duration_secs,
        options.setup_buffer_secs
    );

    let note_values_by_bundle = launch_bundle_note_values(&expected);
    if options.dry_run {
        return Ok(RunObservation {
            errors: vec![format!(
                "dry-run execution plan only: {} bundles, {} cast votes",
                plan.bundle_count, plan.vote_count
            )],
            ..RunObservation::default()
        });
    }

    let collector = Arc::new(MetricsCollector::new());
    let http = HttpSubmitter::new(spec);

    eprintln!("[launch] preparing synthetic launch notes and ZKP #1 witnesses...");
    let (prepared, round_fields) = prepare_launch_delegation_bundles(
        &note_values_by_bundle,
        Some(vote_end_time),
        spec.snapshot_height,
    )?;

    eprintln!("[launch] creating live voting round...");
    let (vote_manager_address, vm_config) = vote_manager_tx_config()?;
    let mut create_body =
        create_voting_session_payload_from_spec(&vote_manager_address, &round_fields, spec);
    create_body["@type"] = json!("/svote.v1.MsgCreateVotingSession");
    let (status, create_json) = broadcast_cosmos_msg(&create_body, &vm_config)?;
    ensure_cosmos_success(status, &create_json, "create voting session")?;
    let round_id_hex = wait_for_create_round_id(&create_json)?;
    let round_id = hex::decode(&round_id_hex)?;
    eprintln!("[launch] round created: {round_id_hex}");

    let chaos_events = if options.skip_chaos {
        Arc::new(Mutex::new(Vec::new()))
    } else {
        spawn_chaos_schedule(spec, plan.vote_start_time, plan.vote_end_time)
    };

    eprintln!("[launch] waiting for round ACTIVE...");
    wait_for_round_status(
        &round_id_hex,
        SESSION_STATUS_ACTIVE,
        options.active_timeout_secs * 1000,
        2_000,
    )?;

    eprintln!("[launch] building delegation proofs against emitted round id...");
    let delegation_bundles = prepared.build_for_round_id(&round_id)?;
    if delegation_bundles.len() != plan.bundle_count {
        return Err(format!(
            "prepared {} delegation bundles, expected {}",
            delegation_bundles.len(),
            plan.bundle_count
        )
        .into());
    }

    eprintln!("[launch] submitting delegations sequentially...");
    submit_delegations(
        &http,
        &primary_api_url,
        &round_id,
        &delegation_bundles,
        &collector,
    )?;
    wait_for_tree_size(
        &round_id_hex,
        expected.surviving_bundles,
        Duration::from_secs(options.phase_timeout_secs),
    )?;
    let (anchor_height, anchor_root, tree_size) =
        commitment_tree_latest(&round_id_hex).ok_or("query tree after delegations failed")?;
    if tree_size != expected.surviving_bundles {
        return Err(format!(
            "tree size after delegations was {tree_size}, expected {}",
            expected.surviving_bundles
        )
        .into());
    }
    eprintln!(
        "[launch] delegation anchor height {anchor_height}, root {}...",
        anchor_root.chars().take(20).collect::<String>()
    );

    let delegation_data = delegation_bundles
        .into_iter()
        .map(|(_, vote_data)| vote_data)
        .collect::<Vec<_>>();

    let ea_pk = round_ea_pk(&round_id_hex)?;
    let mut stage_state = CastStageState::new(&delegation_data, anchor_height as u32)?;
    stage_state.assert_root_matches(&anchor_root)?;
    let round_id_fp = round_id_fp(&round_id)?;

    for proposal in &spec.proposals {
        let planned = expected
            .planned_votes
            .iter()
            .filter(|vote| vote.proposal_id == proposal.id)
            .cloned()
            .collect::<Vec<_>>();
        if planned.is_empty() {
            continue;
        }
        eprintln!(
            "[launch] proposal {}: generating {} cast proofs...",
            proposal.id,
            planned.len()
        );
        let generated = generate_cast_stage(
            &planned,
            proposal,
            &delegation_data,
            &stage_state,
            round_id_fp,
            ea_pk,
            options.cast_proof_threads,
            plan.vote_end_time,
            options.share_reveal_window_secs,
            &collector,
        )?;

        eprintln!(
            "[launch] proposal {}: submitting {} cast votes and helper shares...",
            proposal.id,
            generated.len()
        );
        submit_cast_stage(
            &http,
            &primary_api_url,
            spec,
            &generated,
            &round_id_hex,
            plan.vote_start_time,
            &collector,
        )?;

        stage_state.apply_stage(&generated)?;
        let expected_next_index = expected.surviving_bundles + 2 * stage_state.cast_count;
        wait_for_tree_size(
            &round_id_hex,
            expected_next_index,
            Duration::from_secs(options.phase_timeout_secs),
        )?;
        let (height, root, next_index) =
            commitment_tree_latest(&round_id_hex).ok_or("query tree after cast stage failed")?;
        if next_index != expected_next_index {
            return Err(format!(
                "tree size after proposal {} was {next_index}, expected {expected_next_index}",
                proposal.id
            )
            .into());
        }
        stage_state.checkpoint(height as u32)?;
        stage_state.assert_root_matches(&root)?;
        eprintln!(
            "[launch] proposal {} committed; tree next_index={}, anchor height={}",
            proposal.id, next_index, height
        );
    }

    eprintln!("[launch] all planned votes submitted; waiting for vote end/finalization...");
    sleep_until(plan.vote_end_time);
    if let Err(err) = wait_for_round_status(
        &round_id_hex,
        SESSION_STATUS_FINALIZED,
        options.finalization_timeout_secs * 1000,
        5_000,
    ) {
        eprintln!("[launch] finalization wait failed: {err}");
    }

    let mut observation =
        collect_observation(spec, &round_id_hex).map_err(|err| err.to_string())?;
    observation.phase_metrics = observed_phase_metrics(&collector);
    observation.chaos_events = match Arc::try_unwrap(chaos_events) {
        Ok(mutex) => mutex.into_inner().unwrap_or_default(),
        Err(arc) => arc.lock().unwrap().clone(),
    };
    Ok(observation)
}

pub fn launch_bundle_note_values(expected: &ExpectedModel) -> Vec<Vec<u64>> {
    let mut out = Vec::with_capacity(expected.surviving_bundles as usize);
    for wallet in &expected.wallets {
        for bundle in &wallet.bundles {
            out.push(bundle.notes.iter().map(|note| note.value_zatoshi).collect());
        }
    }
    out
}

pub fn helper_targets_for_share(
    server_count: usize,
    target_count: usize,
    bundle_global_index: u64,
    proposal_id: u32,
    share_index: u32,
) -> Vec<usize> {
    if server_count == 0 || target_count == 0 {
        return Vec::new();
    }
    let target_count = target_count.min(server_count);
    let start = (bundle_global_index + u64::from(proposal_id) + u64::from(share_index)) as usize
        % server_count;
    (0..target_count)
        .map(|offset| (start + offset) % server_count)
        .collect()
}

pub fn planned_share_submit_at(
    vote: &PlannedVote,
    vote_end_time: u64,
    reveal_window_secs: u64,
) -> u64 {
    if matches!(vote.timing, TimingCohort::LastMoment) {
        return 0;
    }
    let window = reveal_window_secs.max(1);
    let jitter = (vote.bundle_global_index
        + u64::from(vote.proposal_id) * 17
        + u64::from(vote.decision) * 31)
        % window;
    vote_end_time.saturating_sub(1 + jitter)
}

fn execution_plan(spec: &RunSpec, expected: &ExpectedModel, vote_end_time: u64) -> ExecutionPlan {
    ExecutionPlan {
        bundle_count: expected.surviving_bundles as usize,
        vote_count: expected.vote_commitments as usize,
        expected_tree_delta: expected.expected_tree_delta,
        vote_end_time,
        vote_start_time: vote_end_time.saturating_sub(spec.round_duration_secs),
        helper_acceptances_planned: expected.expected_helper_acceptances,
    }
}

fn create_voting_session_payload_from_spec(
    creator: &str,
    fields: &SetupRoundFields,
    spec: &RunSpec,
) -> Value {
    let proposals = spec
        .proposals
        .iter()
        .map(|proposal| {
            json!({
                "id": proposal.id,
                "title": proposal.title,
                "description": format!("{} synthetic launch validation proposal", spec.run_name),
                "options": proposal.options.iter().enumerate().map(|(index, label)| {
                    json!({ "index": index, "label": label })
                }).collect::<Vec<_>>(),
            })
        })
        .collect::<Vec<_>>();

    json!({
        "creator": creator,
        "snapshot_height": fields.snapshot_height,
        "snapshot_blockhash": B64.encode(fields.snapshot_blockhash),
        "proposals_hash": B64.encode(fields.proposals_hash),
        "vote_end_time": fields.vote_end_time,
        "nullifier_imt_root": B64.encode(fields.nullifier_imt_root),
        "nc_root": B64.encode(fields.nc_root),
        "proposals": proposals,
        "title": spec.run_name,
        "description": "Synthetic Shielded Vote launch validation run",
        "discussion_url": "",
    })
}

fn vote_manager_tx_config() -> Result<(String, CosmosTxConfig), DynError> {
    let base = default_cosmos_tx_config();
    let key_name = std::env::var("SVOTE_TX_KEY_NAME")
        .unwrap_or_else(|_| FIRST_VOTE_MANAGER_KEY_NAME.to_string());
    let address = if let Ok(address) = std::env::var("SVOTE_VOTE_MANAGER_ADDRESS") {
        if address.trim().is_empty() {
            return Err("SVOTE_VOTE_MANAGER_ADDRESS is empty".into());
        }
        address
    } else if key_name == FIRST_VOTE_MANAGER_KEY_NAME && std::env::var("VM_PRIVKEYS").is_ok() {
        import_first_vote_manager_key(&base.home_dir)
    } else {
        key_account_address(&key_name, &base.home_dir).ok_or_else(|| {
            format!(
                "failed to resolve vote manager key '{key_name}' in keyring {}; set VM_PRIVKEYS or SVOTE_VOTE_MANAGER_ADDRESS/SVOTE_TX_KEY_NAME",
                base.home_dir
            )
        })?
    };

    Ok((
        address,
        CosmosTxConfig {
            key_name,
            home_dir: base.home_dir,
            chain_id: base.chain_id,
            node_url: base.node_url,
        },
    ))
}

fn submit_delegations(
    http: &HttpSubmitter,
    primary_api_url: &str,
    round_id: &[u8],
    delegation_bundles: &[(DelegationBundlePayload, VoteProofDelegationData)],
    collector: &Arc<MetricsCollector>,
) -> Result<(), DynError> {
    let total = delegation_bundles.len();
    let mut succeeded = 0usize;
    for (idx, (bundle, _)) in delegation_bundles.iter().enumerate() {
        let payload = payloads::delegate_vote_payload(round_id, bundle);
        let start = Instant::now();
        let result = http.post_json(primary_api_url, "/shielded-vote/v1/delegate-vote", &payload);
        let latency = start.elapsed();
        let (status, success) = result_success(result, "delegation", idx)?;
        collector.record(Sample {
            phase: "delegation".to_string(),
            timestamp: start,
            latency,
            http_status: status,
            success,
        });
        if success {
            succeeded += 1;
        }
        if (idx + 1) % 25 == 0 || idx + 1 == total {
            eprintln!("[delegation] {}/{} submitted", idx + 1, total);
        }
    }
    if succeeded != total {
        return Err(format!("delegation submission succeeded {succeeded}/{total}").into());
    }
    Ok(())
}

#[derive(Clone)]
struct CastBundle {
    planned: PlannedVote,
    cast_payload: Value,
    share_payloads: Vec<Value>,
    vote_authority_note_new: pallas::Base,
    vote_commitment: pallas::Base,
    vc_tree_position: u64,
}

#[derive(Clone)]
struct CastInput {
    planned: PlannedVote,
    tree_path: [pallas::Base; VOTE_COMM_TREE_DEPTH],
    tree_position: u32,
    anchor_height: u32,
    proposal_authority: u64,
    vc_tree_position: u64,
    submit_at: u64,
}

struct CastStageState {
    tree: vote_commitment_tree::MemoryTreeServer,
    current_positions: Vec<u32>,
    proposal_authority: Vec<u64>,
    anchor_height: u32,
    cast_count: u64,
}

impl CastStageState {
    fn new(
        delegation_data: &[VoteProofDelegationData],
        anchor_height: u32,
    ) -> Result<Self, DynError> {
        let mut tree = vote_commitment_tree::MemoryTreeServer::empty();
        for vote_data in delegation_data {
            tree.append(vote_data.van_comm)?;
        }
        tree.checkpoint(anchor_height)?;
        Ok(Self {
            tree,
            current_positions: (0..delegation_data.len()).map(|i| i as u32).collect(),
            proposal_authority: vec![MAX_PROPOSAL_AUTHORITY; delegation_data.len()],
            anchor_height,
            cast_count: 0,
        })
    }

    fn cast_input_for(
        &self,
        planned: &PlannedVote,
        stage_offset: usize,
        vote_end_time: u64,
        reveal_window_secs: u64,
    ) -> Result<CastInput, DynError> {
        let bundle_idx = planned.bundle_global_index as usize;
        let tree_position = *self
            .current_positions
            .get(bundle_idx)
            .ok_or("planned vote references missing bundle index")?;
        let path = self
            .tree
            .path(u64::from(tree_position), self.anchor_height)
            .ok_or_else(|| {
                format!(
                    "no commitment tree path for bundle {} at position {} height {}",
                    bundle_idx, tree_position, self.anchor_height
                )
            })?;
        let tree_path = path.auth_path().map(|h| h.inner());
        let van_new_position = self.tree.size() + (2 * stage_offset as u64);
        Ok(CastInput {
            planned: planned.clone(),
            tree_path,
            tree_position,
            anchor_height: self.anchor_height,
            proposal_authority: self.proposal_authority[bundle_idx],
            vc_tree_position: van_new_position + 1,
            submit_at: planned_share_submit_at(planned, vote_end_time, reveal_window_secs),
        })
    }

    fn apply_stage(&mut self, generated: &[CastBundle]) -> Result<(), DynError> {
        for bundle in generated {
            let bundle_idx = bundle.planned.bundle_global_index as usize;
            let expected_van_new_position = self.tree.size();
            self.tree.append(bundle.vote_authority_note_new)?;
            self.tree.append(bundle.vote_commitment)?;
            self.current_positions[bundle_idx] = expected_van_new_position as u32;
            self.proposal_authority[bundle_idx] &= !(1u64 << bundle.planned.proposal_id);
            self.cast_count += 1;
        }
        Ok(())
    }

    fn checkpoint(&mut self, height: u32) -> Result<(), DynError> {
        self.tree.checkpoint(height)?;
        self.anchor_height = height;
        Ok(())
    }

    fn assert_root_matches(&self, root_b64: &str) -> Result<(), DynError> {
        let local = B64.encode(self.tree.root().to_repr());
        if local != root_b64 {
            return Err(format!(
                "local commitment tree root {} did not match on-chain {}",
                local, root_b64
            )
            .into());
        }
        Ok(())
    }
}

#[allow(clippy::too_many_arguments)]
fn generate_cast_stage(
    planned_votes: &[PlannedVote],
    proposal: &ProposalSpec,
    delegation_data: &[VoteProofDelegationData],
    stage_state: &CastStageState,
    round_id: pallas::Base,
    ea_pk: pallas::Affine,
    proof_threads: usize,
    vote_end_time: u64,
    share_reveal_window_secs: u64,
    collector: &Arc<MetricsCollector>,
) -> Result<Vec<CastBundle>, DynError> {
    let inputs = planned_votes
        .iter()
        .enumerate()
        .map(|(stage_offset, planned)| {
            stage_state.cast_input_for(
                planned,
                stage_offset,
                vote_end_time,
                share_reveal_window_secs,
            )
        })
        .collect::<Result<Vec<_>, _>>()?;

    let proof_threads = proof_threads.max(1);
    let mut remaining = inputs.into_iter().enumerate().collect::<Vec<_>>();
    let mut generated = Vec::with_capacity(remaining.len());
    let total = remaining.len();
    while !remaining.is_empty() {
        let chunk_size = proof_threads.min(remaining.len());
        let chunk = remaining.drain(..chunk_size).collect::<Vec<_>>();
        let handles = chunk
            .into_iter()
            .map(|(idx, input)| {
                let vote_data = delegation_data[input.planned.bundle_global_index as usize].clone();
                let proposal = proposal.clone();
                std::thread::spawn(move || {
                    let start = Instant::now();
                    let result =
                        generate_cast_vote_bundle(input, &proposal, vote_data, round_id, ea_pk);
                    (idx, start, result)
                })
            })
            .collect::<Vec<_>>();

        for handle in handles {
            let (idx, start, result) = handle
                .join()
                .map_err(|_| "cast proof generation thread panicked".to_string())?;
            let latency = start.elapsed();
            let bundle = result.map_err(|err| format!("cast proof {idx}: {err}"))?;
            collector.record(Sample {
                phase: "cast_proof".to_string(),
                timestamp: start,
                latency,
                http_status: 200,
                success: true,
            });
            generated.push((idx, bundle));
            if generated.len() % 25 == 0 || generated.len() == total {
                eprintln!("[cast-proof] {}/{} generated", generated.len(), total);
            }
        }
    }
    generated.sort_by_key(|(idx, _)| *idx);
    Ok(generated.into_iter().map(|(_, bundle)| bundle).collect())
}

fn generate_cast_vote_bundle(
    input: CastInput,
    proposal: &ProposalSpec,
    vote_data: VoteProofDelegationData,
    round_id: pallas::Base,
    ea_pk: pallas::Affine,
) -> Result<CastBundle, DynError> {
    if input.planned.decision as usize >= proposal.options.len() {
        return Err(format!(
            "decision {} out of range for proposal {} with {} options",
            input.planned.decision,
            proposal.id,
            proposal.options.len()
        )
        .into());
    }

    let anchor_height = input.anchor_height;
    let alpha_v = pallas::Scalar::random(&mut OsRng);
    let single_share = input.planned.expected_unique_shares == 1;
    let bundle = build_vote_proof_from_delegation(
        &vote_data.sk,
        1,
        vote_data.total_note_value,
        vote_data.van_comm_rand,
        round_id,
        input.tree_path,
        input.tree_position,
        anchor_height,
        input.planned.proposal_id as u64,
        input.planned.decision as u64,
        ea_pk,
        alpha_v,
        input.proposal_authority,
        single_share,
    )
    .map_err(|err| format!("cast-vote proof generation failed: {err}"))?;

    let ask = orchard::keys::SpendAuthorizingKey::from(&vote_data.sk);
    let rsk = ask.randomize(&alpha_v);
    let sighash = cast_vote_sighash(
        &round_id,
        &bundle.r_vpk_bytes,
        &bundle.instance.van_nullifier,
        &bundle.instance.vote_authority_note_new,
        &bundle.instance.vote_commitment,
        input.planned.proposal_id,
        anchor_height,
    );
    let sig = rsk.sign(&mut OsRng, &sighash);
    let sig_bytes: [u8; 64] = (&sig).into();

    let cast_payload = payloads::cast_vote_payload_real(
        &round_id.to_repr(),
        anchor_height,
        &bundle.instance.van_nullifier.to_repr(),
        &bundle.instance.vote_authority_note_new.to_repr(),
        &bundle.instance.vote_commitment.to_repr(),
        input.planned.proposal_id,
        &bundle.proof,
        &bundle.r_vpk_bytes,
        &sig_bytes,
    );

    let all_enc_shares = bundle
        .encrypted_shares
        .iter()
        .map(|share| (share.c1.as_slice(), share.c2.as_slice(), share.share_index))
        .collect::<Vec<_>>();
    let share_comms = bundle
        .share_comms
        .iter()
        .map(|comm| comm.to_repr().to_vec())
        .collect::<Vec<_>>();

    let share_count = input.planned.expected_unique_shares as usize;
    let mut share_payloads = Vec::with_capacity(share_count);
    for share_idx in 0..share_count {
        let encrypted = &bundle.encrypted_shares[share_idx];
        share_payloads.push(payloads::helper_share_payload_with_submit_at(
            &round_id.to_repr(),
            &bundle.shares_hash.to_repr(),
            input.planned.proposal_id,
            input.planned.decision,
            &encrypted.c1,
            &encrypted.c2,
            encrypted.share_index,
            input.vc_tree_position,
            &all_enc_shares,
            &share_comms,
            &bundle.share_blinds[share_idx].to_repr(),
            input.submit_at,
        ));
    }

    Ok(CastBundle {
        planned: input.planned,
        cast_payload,
        share_payloads,
        vote_authority_note_new: bundle.instance.vote_authority_note_new,
        vote_commitment: bundle.instance.vote_commitment,
        vc_tree_position: input.vc_tree_position,
    })
}

fn cast_vote_sighash(
    round_id: &pallas::Base,
    r_vpk_bytes: &[u8; 32],
    van_nullifier: &pallas::Base,
    vote_authority_note_new: &pallas::Base,
    vote_commitment: &pallas::Base,
    proposal_id: u32,
    anchor_height: u32,
) -> [u8; 32] {
    let mut canonical = Vec::new();
    canonical.extend_from_slice(b"SVOTE_CAST_VOTE_SIGHASH_V0");
    extend_fp_repr(&mut canonical, round_id);
    canonical.extend_from_slice(r_vpk_bytes);
    extend_fp_repr(&mut canonical, van_nullifier);
    extend_fp_repr(&mut canonical, vote_authority_note_new);
    extend_fp_repr(&mut canonical, vote_commitment);
    let mut pid_buf = [0u8; 32];
    pid_buf[..4].copy_from_slice(&proposal_id.to_le_bytes());
    canonical.extend_from_slice(&pid_buf);
    let mut ah_buf = [0u8; 32];
    ah_buf[..8].copy_from_slice(&(anchor_height as u64).to_le_bytes());
    canonical.extend_from_slice(&ah_buf);
    let hash = blake2b_simd::Params::new().hash_length(32).hash(&canonical);
    let mut out = [0u8; 32];
    out.copy_from_slice(hash.as_bytes());
    out
}

fn extend_fp_repr(out: &mut Vec<u8>, value: &pallas::Base) {
    let repr = value.to_repr();
    let mut buf = [0u8; 32];
    buf[..repr.as_ref().len().min(32)]
        .copy_from_slice(&repr.as_ref()[..repr.as_ref().len().min(32)]);
    out.extend_from_slice(&buf);
}

fn submit_cast_stage(
    http: &HttpSubmitter,
    primary_api_url: &str,
    spec: &RunSpec,
    generated: &[CastBundle],
    round_id_hex: &str,
    vote_start_time: u64,
    collector: &Arc<MetricsCollector>,
) -> Result<(), DynError> {
    for (idx, bundle) in generated.iter().enumerate() {
        let scheduled_at = vote_start_time + bundle.planned.submit_offset_secs;
        sleep_until(scheduled_at);
        let start = Instant::now();
        let result = http.post_json(
            primary_api_url,
            "/shielded-vote/v1/cast-vote",
            &bundle.cast_payload,
        );
        let latency = start.elapsed();
        let (status, success) = result_success(result, "cast_vote", idx)?;
        collector.record(Sample {
            phase: "cast_vote".to_string(),
            timestamp: start,
            latency,
            http_status: status,
            success,
        });
        if !success {
            return Err(format!(
                "cast vote {} for proposal {} failed",
                idx, bundle.planned.proposal_id
            )
            .into());
        }
        if bundle
            .share_payloads
            .iter()
            .any(|payload| payload.get("submit_at").and_then(|v| v.as_u64()) == Some(0))
        {
            wait_for_tree_size(
                round_id_hex,
                bundle.vc_tree_position + 1,
                Duration::from_secs(120),
            )?;
        }
        submit_helper_shares(http, spec, bundle, collector);
        if (idx + 1) % 25 == 0 || idx + 1 == generated.len() {
            eprintln!(
                "[cast-vote] {}/{} submitted for proposal {}",
                idx + 1,
                generated.len(),
                bundle.planned.proposal_id
            );
        }
    }
    Ok(())
}

fn submit_helper_shares(
    http: &HttpSubmitter,
    spec: &RunSpec,
    bundle: &CastBundle,
    collector: &Arc<MetricsCollector>,
) {
    let server_count = spec.vote_servers.len();
    for payload in &bundle.share_payloads {
        let share_index = payload
            .get("enc_share")
            .and_then(|share| share.get("share_index"))
            .and_then(|v| v.as_u64())
            .unwrap_or(0) as u32;
        let targets = helper_targets_for_share(
            server_count,
            spec.helper_target_count as usize,
            bundle.planned.bundle_global_index,
            bundle.planned.proposal_id,
            share_index,
        );
        let mut accepted = 0usize;
        for target_idx in targets {
            let Some(server) = spec.vote_servers.get(target_idx) else {
                continue;
            };
            let start = Instant::now();
            let result = http.post_helper_json(&server.url, "/shielded-vote/v1/shares", payload);
            let latency = start.elapsed();
            let (status, success) = match result {
                Ok((status, json)) => {
                    let success = status == 200;
                    if !success {
                        eprintln!(
                            "[share] {} rejected share for p{} idx{} vc{}: status={} body={:?}",
                            server.label,
                            bundle.planned.proposal_id,
                            share_index,
                            bundle.vc_tree_position,
                            status,
                            json
                        );
                    }
                    (status, success)
                }
                Err(err) => {
                    eprintln!(
                        "[share] {} failed share for p{} idx{} vc{}: {}",
                        server.label,
                        bundle.planned.proposal_id,
                        share_index,
                        bundle.vc_tree_position,
                        err
                    );
                    (0, false)
                }
            };
            collector.record(Sample {
                phase: "share_enqueue".to_string(),
                timestamp: start,
                latency,
                http_status: status,
                success,
            });
            if success {
                accepted += 1;
            }
        }
        if accepted == 0 {
            eprintln!(
                "[share] no helper accepted p{} share {} at vc position {}",
                bundle.planned.proposal_id, share_index, bundle.vc_tree_position
            );
        }
    }
}

#[derive(Clone)]
struct HttpSubmitter {
    client: reqwest::blocking::Client,
    helper_token: Option<String>,
}

impl HttpSubmitter {
    fn new(spec: &RunSpec) -> Self {
        let helper_token = spec
            .helper_api_token
            .clone()
            .or_else(|| std::env::var("HELPER_API_TOKEN").ok())
            .filter(|s| !s.trim().is_empty());
        Self {
            client: reqwest::blocking::Client::builder()
                .timeout(Duration::from_secs(30))
                .build()
                .expect("reqwest client"),
            helper_token,
        }
    }

    fn post_json(
        &self,
        base_url: &str,
        path: &str,
        body: &Value,
    ) -> Result<(u16, Value), DynError> {
        self.post_json_inner(base_url, path, body, false)
    }

    fn post_helper_json(
        &self,
        base_url: &str,
        path: &str,
        body: &Value,
    ) -> Result<(u16, Value), DynError> {
        self.post_json_inner(base_url, path, body, true)
    }

    fn post_json_inner(
        &self,
        base_url: &str,
        path: &str,
        body: &Value,
        helper: bool,
    ) -> Result<(u16, Value), DynError> {
        let url = format!("{}{}", base_url.trim_end_matches('/'), path);
        let mut last_err = None;
        for attempt in 0..12u32 {
            let mut request = self.client.post(&url).json(body);
            if helper {
                if let Some(token) = &self.helper_token {
                    request = request.header("X-Helper-Token", token);
                }
            }
            match request.send() {
                Ok(resp) => {
                    let status = resp.status().as_u16();
                    let text = resp.text().unwrap_or_default();
                    let json = serde_json::from_str(&text).unwrap_or_else(|_| {
                        if text.is_empty() {
                            Value::Null
                        } else {
                            json!({ "raw": text })
                        }
                    });
                    if status == 502 && attempt < 11 {
                        std::thread::sleep(Duration::from_secs(2 * u64::from(attempt + 1)));
                        continue;
                    }
                    return Ok((status, json));
                }
                Err(err) => {
                    last_err = Some(err.to_string());
                    if attempt < 11 {
                        std::thread::sleep(Duration::from_millis(500 * u64::from(attempt + 1)));
                        continue;
                    }
                }
            }
        }
        Err(last_err
            .unwrap_or_else(|| "POST failed with no response".to_string())
            .into())
    }
}

fn result_success(
    result: Result<(u16, Value), DynError>,
    phase: &str,
    idx: usize,
) -> Result<(u16, bool), DynError> {
    match result {
        Ok((status, json)) => {
            let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(0);
            let success = status == 200 && code == 0;
            if !success {
                eprintln!(
                    "[{phase}] item {idx} failed status={status} code={code} body={:?}",
                    json
                );
            }
            Ok((status, success))
        }
        Err(err) => {
            eprintln!("[{phase}] item {idx} request failed: {err}");
            Ok((0, false))
        }
    }
}

fn ensure_cosmos_success(status: u16, json: &Value, phase: &str) -> Result<(), DynError> {
    let code = json.get("code").and_then(|c| c.as_i64()).unwrap_or(0);
    if status == 200 && code == 0 {
        return Ok(());
    }
    Err(format!("{phase} failed: status={status} code={code} body={json:?}").into())
}

fn wait_for_tree_size(round_id: &str, target: u64, timeout: Duration) -> Result<(), DynError> {
    let deadline = Instant::now() + timeout;
    let mut last = None;
    while Instant::now() < deadline {
        if let Some(next) = commitment_tree_next_index(round_id) {
            if next >= target {
                eprintln!("[tree] reached size {} (target {})", next, target);
                return Ok(());
            }
            if last != Some(next) {
                eprintln!("[tree] size {} / {}", next, target);
                last = Some(next);
            }
        }
        std::thread::sleep(Duration::from_secs(3));
    }
    Err(format!(
        "tree did not reach target {target}; last observed {:?}",
        last
    )
    .into())
}

fn round_id_fp(round_id: &[u8]) -> Result<pallas::Base, DynError> {
    let bytes: [u8; 32] = round_id
        .try_into()
        .map_err(|_| format!("round_id must be 32 bytes, got {}", round_id.len()))?;
    Option::from(pallas::Base::from_repr(bytes))
        .ok_or_else(|| "round_id is not a canonical Pallas field element".into())
}

fn round_ea_pk(round_id_hex: &str) -> Result<pallas::Affine, DynError> {
    let ea_pk_bytes = get_round_ea_pk(round_id_hex).ok_or("ACTIVE round has no ea_pk")?;
    let ea_pk_arr: [u8; 32] = ea_pk_bytes
        .try_into()
        .map_err(|_| "ea_pk must be 32 bytes")?;
    Option::from(pallas::Affine::from_bytes(&ea_pk_arr))
        .ok_or_else(|| "ea_pk is not a valid compressed Pallas point".into())
}

fn observed_phase_metrics(collector: &Arc<MetricsCollector>) -> Vec<ObservedPhaseMetric> {
    let samples = collector.snapshot();
    let aggregate = compute_aggregate(&samples, collector.wall_time());
    aggregate
        .phases
        .into_iter()
        .map(|phase| ObservedPhaseMetric {
            phase: phase.phase,
            total: phase.total_submitted as u64,
            succeeded: phase.succeeded as u64,
            failed: phase.failed as u64,
            p50_ms: Some(phase.latency_p50_ms),
            p95_ms: Some(phase.latency_p95_ms),
            p99_ms: Some(phase.latency_p99_ms),
        })
        .collect()
}

fn spawn_chaos_schedule(
    spec: &RunSpec,
    vote_start_time: u64,
    vote_end_time: u64,
) -> Arc<Mutex<Vec<ObservedChaosEvent>>> {
    let events = Arc::new(Mutex::new(Vec::new()));
    for step in spec.chaos.clone() {
        let events = Arc::clone(&events);
        let endpoint = resolve_chaos_endpoint(spec, &step.target);
        std::thread::spawn(move || {
            let start_at = vote_start_time + step.at_offset_secs;
            sleep_until(start_at);
            let started_at_offset_secs = now_unix().saturating_sub(vote_start_time);
            let target = endpoint
                .as_ref()
                .and_then(|e| e.ssh_host.clone())
                .unwrap_or_else(|| step.target.clone());
            let mut result = match step.action {
                crate::launch::ChaosAction::Stop | crate::launch::ChaosAction::StopUntilVoteEnd => {
                    run_ssh_systemctl(&target, "stop")
                }
                crate::launch::ChaosAction::Restart => run_ssh_systemctl(&target, "restart"),
            };
            let mut ended_at_offset_secs = None;
            if result.is_ok() {
                if let Some(duration) = step.duration_secs {
                    sleep_until((start_at + duration).min(vote_end_time));
                    ended_at_offset_secs = Some(now_unix().saturating_sub(vote_start_time));
                    if matches!(step.action, crate::launch::ChaosAction::Stop) {
                        result = run_ssh_systemctl(&target, "start");
                    }
                }
            }
            events.lock().unwrap().push(ObservedChaosEvent {
                id: step.id,
                target,
                started_at_offset_secs,
                ended_at_offset_secs,
                result: result
                    .map(|_| "ok".to_string())
                    .unwrap_or_else(|err| format!("error: {err}")),
            });
        });
    }
    events
}

fn resolve_chaos_endpoint(spec: &RunSpec, target: &str) -> Option<ServiceEndpoint> {
    spec.vote_servers
        .iter()
        .find(|server| server.label == target || server.ssh_host.as_deref() == Some(target))
        .cloned()
}

fn run_ssh_systemctl(target: &str, action: &str) -> Result<(), String> {
    eprintln!("[chaos] ssh {target}: sudo -n systemctl {action} svoted");
    let output = Command::new("ssh")
        .args([
            "-o",
            "StrictHostKeyChecking=accept-new",
            "-o",
            "BatchMode=yes",
            "-o",
            "ConnectTimeout=10",
            target,
            &format!("sudo -n systemctl {action} svoted"),
        ])
        .output()
        .map_err(|err| err.to_string())?;
    if output.status.success() {
        return Ok(());
    }
    Err(String::from_utf8_lossy(&output.stderr).trim().to_string())
}

fn sleep_until(unix_time: u64) {
    loop {
        let now = now_unix();
        if now >= unix_time {
            return;
        }
        let remaining = unix_time - now;
        std::thread::sleep(Duration::from_secs(remaining.min(30)));
    }
}

fn resolve_primary_api_url(spec: &RunSpec) -> Result<String, DynError> {
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

fn env_u64(name: &str, default: u64) -> u64 {
    std::env::var(name)
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(default)
}

fn env_usize(name: &str, default: usize) -> usize {
    std::env::var(name)
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(default)
}

fn now_unix() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs()
}
