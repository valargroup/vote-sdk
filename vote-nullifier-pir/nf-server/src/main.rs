//! Unified CLI binary for the nullifier PIR pipeline.
//!
//! Provides three subcommands:
//!   - `ingest` — Sync nullifiers from a lightwalletd instance into flat files.
//!   - `export` — Build the PIR tree and write tier files for the server.
//!   - `serve`  — Start the PIR HTTP server (feature-gated behind `serve`).

mod cmd_export;
mod cmd_ingest;
#[cfg(feature = "serve")]
mod cmd_serve;
#[cfg(feature = "serve")]
mod serve;

use clap::{Parser, Subcommand};

/// Top-level CLI parser.
#[derive(Parser)]
#[command(name = "nf-server", about = "Unified nullifier pipeline: ingest, export, and serve PIR data")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

/// Available subcommands.
#[derive(Subcommand)]
enum Command {
    /// Sync nullifiers from lightwalletd into nullifiers.bin
    Ingest(cmd_ingest::Args),
    /// Build PIR tree and export tier files from nullifiers.bin
    Export(cmd_export::Args),
    /// Start the PIR HTTP server (requires --features serve)
    #[cfg(feature = "serve")]
    Serve(cmd_serve::Args),
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();
    match cli.command {
        Command::Ingest(args) => cmd_ingest::run(args).await,
        Command::Export(args) => cmd_export::run(args),
        #[cfg(feature = "serve")]
        Command::Serve(args) => cmd_serve::run(args).await,
    }
}
