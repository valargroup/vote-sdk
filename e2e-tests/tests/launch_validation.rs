use e2e_tests::launch::{
    build_launch_report, chunk_notes, generate_expected_model, render_report_html,
    simulated_observation, GateStatus, NotePlan, RunSpec, TimingCohort, BALLOT_DIVISOR,
};
use e2e_tests::launch_execute::{
    helper_targets_for_share, launch_bundle_note_values, planned_share_submit_at,
};
use e2e_tests::setup::prepare_launch_delegation_bundles;

#[test]
fn chunk_notes_matches_expected_bundling_rules() {
    let notes = vec![
        NotePlan {
            position: 4,
            value_zatoshi: 1_000_000,
        },
        NotePlan {
            position: 2,
            value_zatoshi: 8_000_000,
        },
        NotePlan {
            position: 1,
            value_zatoshi: 7_000_000,
        },
        NotePlan {
            position: 3,
            value_zatoshi: 5_000_000,
        },
        NotePlan {
            position: 5,
            value_zatoshi: 4_000_000,
        },
        NotePlan {
            position: 6,
            value_zatoshi: 3_000_000,
        },
    ];

    let bundles = chunk_notes(&notes);
    assert_eq!(bundles.len(), 1);
    assert_eq!(bundles[0].raw_total_zatoshi, 27_000_000);
    assert_eq!(bundles[0].eligible_weight_zatoshi, 2 * BALLOT_DIVISOR);
    assert_eq!(
        bundles[0]
            .notes
            .iter()
            .map(|n| n.position)
            .collect::<Vec<_>>(),
        vec![1, 2, 3, 5, 6]
    );
}

#[test]
fn launch_note_preparation_accepts_real_bundle_shape() {
    let note_values = vec![
        vec![BALLOT_DIVISOR],
        vec![4_000_000, 3_000_000, 2_500_000, 2_000_000, 1_000_000],
    ];

    let (_prepared, round_fields) =
        prepare_launch_delegation_bundles(&note_values, Some(4_102_444_800), 3_317_500)
            .expect("prepare launch notes");

    assert_ne!(round_fields.nc_root, [0u8; 32]);
    assert_eq!(round_fields.vote_end_time, 4_102_444_800);
    assert_eq!(round_fields.snapshot_height, 3_317_500);
}

#[test]
fn launch_note_preparation_rejects_dust_bundle() {
    let err =
        match prepare_launch_delegation_bundles(&[vec![1_000_000, 2_000_000]], None, 3_317_500) {
            Ok(_) => panic!("dust-only bundle must fail"),
            Err(err) => err,
        };

    assert!(err.to_string().contains("below 0.125 ZEC"));
}

#[test]
fn default_launch_model_has_strict_expected_counts() {
    let spec = RunSpec::default();
    let expected = generate_expected_model(&spec);

    assert_eq!(expected.voter_count, 1_000);
    assert_eq!(expected.dust_wallets, 50);
    assert_eq!(expected.eligible_wallets, 950);
    assert!(expected.surviving_bundles > expected.eligible_wallets as u64);
    assert_eq!(
        expected.expected_tree_delta,
        expected.surviving_bundles + 2 * expected.vote_commitments
    );
    assert_eq!(
        expected.expected_helper_acceptances,
        expected.expected_unique_shares * u64::from(spec.helper_target_count)
    );
    assert_eq!(
        expected.planned_votes.len() as u64,
        expected.vote_commitments
    );
    assert!(expected
        .planned_votes
        .iter()
        .any(|vote| vote.expected_unique_shares == 1));
    assert!(expected
        .planned_votes
        .iter()
        .any(|vote| vote.expected_unique_shares == 16));
    assert_eq!(
        launch_bundle_note_values(&expected).len() as u64,
        expected.surviving_bundles
    );

    for vote in &expected.planned_votes {
        assert!(vote.bundle_global_index < expected.surviving_bundles);
    }

    for proposal in &spec.proposals {
        let proposal_total: u64 = expected
            .expected_tally
            .iter()
            .filter(|entry| entry.proposal_id == proposal.id)
            .map(|entry| entry.total_value_zatoshi)
            .sum();
        let expected_total: u64 = expected
            .wallets
            .iter()
            .map(|wallet| wallet.eligible_weight)
            .sum();
        assert_eq!(proposal_total, expected_total);
    }
}

#[test]
fn launch_executor_helper_targets_are_deterministic_and_redundant() {
    assert_eq!(helper_targets_for_share(5, 3, 0, 1, 0), vec![1, 2, 3]);
    assert_eq!(helper_targets_for_share(5, 3, 4, 2, 15), vec![1, 2, 3]);
    assert_eq!(helper_targets_for_share(2, 3, 0, 1, 0), vec![1, 0]);
    assert!(helper_targets_for_share(0, 3, 0, 1, 0).is_empty());
}

#[test]
fn launch_executor_submit_at_matches_last_moment_contract() {
    let spec = RunSpec::default();
    let expected = generate_expected_model(&spec);
    let last_moment = expected
        .planned_votes
        .iter()
        .find(|vote| vote.timing == TimingCohort::LastMoment)
        .expect("default spec has last-moment votes");
    let normal = expected
        .planned_votes
        .iter()
        .find(|vote| vote.timing != TimingCohort::LastMoment)
        .expect("default spec has normal votes");

    assert_eq!(planned_share_submit_at(last_moment, 4_102_444_800, 600), 0);
    let submit_at = planned_share_submit_at(normal, 4_102_444_800, 600);
    assert!(submit_at < 4_102_444_800);
    assert!(submit_at >= 4_102_444_800 - 600);
}

#[test]
fn simulated_observation_passes_all_strict_gates() {
    let spec = RunSpec::default();
    let expected = generate_expected_model(&spec);
    let observation = simulated_observation(&spec, &expected);
    let report = build_launch_report(spec, Some(observation));

    assert_eq!(report.overall_status, GateStatus::Pass);
    assert!(report
        .gates
        .iter()
        .all(|gate| gate.status == GateStatus::Pass));
}

#[test]
fn missing_unique_share_reveal_fails_gate() {
    let spec = RunSpec::default();
    let expected = generate_expected_model(&spec);
    let mut observation = simulated_observation(&spec, &expected);
    observation.unique_shares_revealed = Some(expected.expected_unique_shares - 1);

    let report = build_launch_report(spec, Some(observation));
    let gate = report
        .gates
        .iter()
        .find(|gate| gate.name == "unique shares revealed")
        .expect("share reveal gate exists");

    assert_eq!(gate.status, GateStatus::Fail);
    assert_eq!(report.overall_status, GateStatus::Fail);
}

#[test]
fn report_html_is_self_contained_and_interactive() {
    let spec = RunSpec::default();
    let expected = generate_expected_model(&spec);
    let observation = simulated_observation(&spec, &expected);
    let report = build_launch_report(spec, Some(observation));

    let html = render_report_html(&report);
    assert!(html.contains("<script id=\"run-data\" type=\"application/json\">"));
    assert!(html.contains("data-tab"));
    assert!(html.contains("tally-filter"));
    assert!(html.contains("installSort"));
}
