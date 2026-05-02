use e2e_tests::launch::{
    build_launch_report, collect_observation, generate_expected_model, render_report_html,
    render_summary_markdown, simulated_observation, RunObservation, RunSpec,
};
use e2e_tests::launch_execute::{dry_run_execution_plan, execute_launch_run, ExecutionOptions};
use std::{env, fs, path::PathBuf};

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<String> = env::args().collect();
    let mut spec_path: Option<PathBuf> = None;
    let mut observation_path: Option<PathBuf> = None;
    let mut output_dir: Option<PathBuf> = None;
    let mut simulate = false;
    let mut collect = false;
    let mut execute = false;
    let mut dry_run_execute = false;
    let mut yes = false;
    let mut execution_options = ExecutionOptions::default();
    let mut round_id: Option<String> = None;

    let mut i = 1;
    while i < args.len() {
        match args[i].as_str() {
            "--spec" => {
                i += 1;
                spec_path = Some(PathBuf::from(args.get(i).ok_or("--spec requires a path")?));
            }
            "--observations" => {
                i += 1;
                observation_path = Some(PathBuf::from(
                    args.get(i).ok_or("--observations requires a path")?,
                ));
            }
            "--output-dir" => {
                i += 1;
                output_dir = Some(PathBuf::from(
                    args.get(i).ok_or("--output-dir requires a path")?,
                ));
            }
            "--simulate" => {
                simulate = true;
            }
            "--execute" => {
                execute = true;
            }
            "--dry-run-execute" => {
                dry_run_execute = true;
            }
            "--yes" => {
                yes = true;
            }
            "--skip-chaos" => {
                execution_options.skip_chaos = true;
            }
            "--setup-buffer-secs" => {
                i += 1;
                execution_options.setup_buffer_secs =
                    parse_u64_arg(&args, i, "--setup-buffer-secs")?;
            }
            "--active-timeout-secs" => {
                i += 1;
                execution_options.active_timeout_secs =
                    parse_u64_arg(&args, i, "--active-timeout-secs")?;
            }
            "--phase-timeout-secs" => {
                i += 1;
                execution_options.phase_timeout_secs =
                    parse_u64_arg(&args, i, "--phase-timeout-secs")?;
            }
            "--finalization-timeout-secs" => {
                i += 1;
                execution_options.finalization_timeout_secs =
                    parse_u64_arg(&args, i, "--finalization-timeout-secs")?;
            }
            "--share-reveal-window-secs" => {
                i += 1;
                execution_options.share_reveal_window_secs =
                    parse_u64_arg(&args, i, "--share-reveal-window-secs")?;
            }
            "--cast-proof-threads" => {
                i += 1;
                execution_options.cast_proof_threads =
                    parse_usize_arg(&args, i, "--cast-proof-threads")?;
            }
            "--collect" => {
                collect = true;
            }
            "--round-id" => {
                i += 1;
                round_id = Some(args.get(i).ok_or("--round-id requires a value")?.clone());
            }
            "--help" | "-h" => {
                print_help();
                return Ok(());
            }
            other => return Err(format!("unknown argument: {other}").into()),
        }
        i += 1;
    }

    let spec = if let Some(path) = spec_path {
        serde_json::from_str::<RunSpec>(&fs::read_to_string(path)?)?
    } else {
        RunSpec::default()
    };

    let output_dir = output_dir
        .or_else(|| spec.output_dir.as_ref().map(PathBuf::from))
        .unwrap_or_else(|| PathBuf::from("artifacts/launch-validation"));

    if [simulate, collect, execute, dry_run_execute]
        .into_iter()
        .filter(|enabled| *enabled)
        .count()
        > 1
    {
        return Err(
            "choose only one of --simulate, --collect, --execute, or --dry-run-execute".into(),
        );
    }

    let observation = if let Some(path) = observation_path {
        Some(serde_json::from_str::<RunObservation>(
            &fs::read_to_string(path)?,
        )?)
    } else if collect {
        let round_id = round_id.ok_or("--collect requires --round-id")?;
        Some(collect_observation(&spec, &round_id)?)
    } else if execute {
        if !yes {
            return Err(
                "--execute submits live votes and runs SSH chaos; pass --yes to continue".into(),
            );
        }
        Some(
            execute_launch_run(&spec, &execution_options)
                .map_err(|err| format!("launch execution failed: {err}"))?,
        )
    } else if dry_run_execute {
        execution_options.dry_run = true;
        let plan = dry_run_execution_plan(&spec, &execution_options);
        eprintln!(
            "dry-run execution plan: {} bundles, {} cast votes, expected tree delta {}, vote_start={}, vote_end={}, planned helper acceptances={}",
            plan.bundle_count,
            plan.vote_count,
            plan.expected_tree_delta,
            plan.vote_start_time,
            plan.vote_end_time,
            plan.helper_acceptances_planned
        );
        None
    } else if simulate {
        let expected = generate_expected_model(&spec);
        Some(simulated_observation(&spec, &expected))
    } else {
        None
    };

    let report = build_launch_report(spec, observation);
    fs::create_dir_all(&output_dir)?;
    fs::write(
        output_dir.join("run.json"),
        serde_json::to_string_pretty(&report)?,
    )?;
    fs::write(
        output_dir.join("summary.md"),
        render_summary_markdown(&report),
    )?;
    fs::write(output_dir.join("report.html"), render_report_html(&report))?;

    eprintln!(
        "launch validation artifacts written to {}",
        output_dir.display()
    );
    eprintln!("status: {:?}", report.overall_status);
    Ok(())
}

fn print_help() {
    eprintln!(
        "usage: launch_validation [--spec run-spec.json] [--observations observations.json] [--collect --round-id HEX] [--simulate] [--execute --yes] [--output-dir artifacts/launch-validation]\n\
         \n\
         Without observations, the report shows expected launch-gate math with pending gates.\n\
         Use --collect --round-id after a live run to poll the chain/helper APIs.\n\
         Use --simulate for a self-contained report render smoke test.\n\
         Use --dry-run-execute to print the live execution plan without network side effects.\n\
         Execute options: --skip-chaos --setup-buffer-secs N --active-timeout-secs N --phase-timeout-secs N --finalization-timeout-secs N --share-reveal-window-secs N --cast-proof-threads N."
    );
}

fn parse_u64_arg(
    args: &[String],
    index: usize,
    name: &str,
) -> Result<u64, Box<dyn std::error::Error>> {
    Ok(args
        .get(index)
        .ok_or_else(|| format!("{name} requires a value"))?
        .parse()?)
}

fn parse_usize_arg(
    args: &[String],
    index: usize,
    name: &str,
) -> Result<usize, Box<dyn std::error::Error>> {
    Ok(args
        .get(index)
        .ok_or_else(|| format!("{name} requires a value"))?
        .parse()?)
}
