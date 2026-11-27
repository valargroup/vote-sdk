//! zcash_voting `VotingDb` helpers for tests that vote through the SDK.
//!
//! zcash_voting 5 made its chain submission lifecycle the only writer of
//! submission hashes and tree positions (see the `chain_submission` module
//! docs), so a test can no longer record a VAN position itself. Instead it
//! persists the delegation the fixtures built and lets `ChainSubmissionClient`
//! submit it; the lifecycle's confirmation records the position that
//! `vote::commit` checks the VAN witness against.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

use ff::PrimeField;
use zcash_voting::governance::BUNDLE_NOTE_SLOTS;
use zcash_voting::storage::{queries, VotingDb};
use zcash_voting::types::{SharePayload, VotingError};
use zcash_voting::{
    AdvanceDelegation, ChainRecoveryMode, ChainSubmissionClient, ChainSubmissionClientConfig,
    ChainSubmissionConfirmation, ChainSubmissionControl, ChainSubmissionPending,
    ChainSubmissionResult, Network, NoteInfo,
};

use crate::payloads::DelegationBundlePayload;
use crate::setup::VoteProofDelegationData;

type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// One fixture-built delegation, as `persist_delegation` writes it into a
/// bundle row.
pub struct DelegationRecord<'a> {
    /// Round the bundle belongs to, as lowercase hex.
    pub round_id_hex: &'a str,
    /// Bundle row to fill; it must not exist yet.
    pub bundle_index: u32,
    /// The real notes the delegation spends.
    pub notes: &'a [NoteInfo],
    /// The spend authorization randomizer, canonically encoded.
    pub alpha: [u8; 32],
    /// Diversifier index of the voting hotkey address that owns the VAN.
    pub address_index: u32,
    /// The chain-facing delegation fields and ZKP #1.
    pub payload: &'a DelegationBundlePayload,
    /// The VAN opening that ZKP #2 needs.
    pub vote_data: &'a VoteProofDelegationData,
}

/// Persists a fixture-built delegation so zcash_voting can submit it and later
/// prove votes from it: the bundle's notes, its setup with the TX1 effects and
/// sighash, the proof's public fields and the proof itself.
///
/// The fixtures build no PCZT, so the fields only a PCZT build produces (the
/// signed note's rho and rseeds, and the padded notes' nullifiers, cmx values
/// and secrets) are zero-filled. The lifecycle hashes them into the
/// delegation's generation but never sends them to the chain.
pub fn persist_delegation(db: &VotingDb, record: &DelegationRecord<'_>) -> Result<(), BoxError> {
    let padded = BUNDLE_NOTE_SLOTS
        .checked_sub(record.notes.len())
        .ok_or("a bundle holds at most BUNDLE_NOTE_SLOTS notes")?;
    let zero = vec![0u8; 32];
    let tx1_sighash = shielded_vote_circuits::tx1::sighash(&record.payload.tx1_effects)
        .map_err(|e| format!("delegation TX1 sighash: {e}"))?;
    let wallet_id = db.wallet_id();
    let round = record.round_id_hex;
    let bundle = record.bundle_index;
    let conn = db.conn();
    queries::insert_bundle_notes(&conn, round, &wallet_id, bundle, record.notes)?;
    queries::store_delegation_data(
        &conn,
        round,
        &wallet_id,
        bundle,
        record.vote_data.van_comm_rand.to_repr().as_ref(),
        &vec![zero.clone(); padded],
        &zero,
        &vec![zero.clone(); padded],
        &record.payload.signed_note_nullifier,
        &record.payload.cmx_new,
        &record.alpha,
        &zero,
        &zero,
        &record.payload.van_cmx,
        record.vote_data.total_note_value,
        record.address_index,
        &vec![(zero.clone(), zero.clone()); padded],
        &tx1_sighash,
        &record.payload.tx1_effects,
    )?;
    queries::store_proof_result_fields(
        &conn,
        round,
        &wallet_id,
        bundle,
        &record.payload.rk,
        &record.payload.gov_nullifiers,
        &record.payload.signed_note_nullifier,
        &record.payload.cmx_new,
    )?;
    queries::store_proof(&conn, round, &wallet_id, bundle, &record.payload.proof)?;
    Ok(())
}

/// Submits the delegation persisted for `bundle_index` through zcash_voting's
/// `ChainSubmissionClient`, as a wallet does, and repeats bounded passes until
/// the lifecycle confirms it. The confirmation records the bundle's VAN
/// position. A pass that ends `Recovering` is retried with exact-tree
/// recovery, as the client documents for resumed work.
pub fn submit_delegation(
    db: &Arc<VotingDb>,
    vote_round_id: [u8; 32],
    bundle_index: u32,
    spend_auth_sig: &[u8],
    timeout: Duration,
) -> Result<ChainSubmissionConfirmation, BoxError> {
    let config =
        ChainSubmissionClientConfig::for_network(Network::Testnet, vec![crate::api::base_url()]);
    let client = ChainSubmissionClient::new(Arc::clone(db), config)
        .map_err(|e| format!("chain submission client: {e:?}"))?;
    let request =
        AdvanceDelegation::from_signature_bytes(vote_round_id, bundle_index, spend_auth_sig)
            .map_err(|e| format!("delegation request: {e:?}"))?;
    let control = ChainSubmissionControl::new(0);
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()?;
    let deadline = Instant::now() + timeout;
    let mut recovery = ChainRecoveryMode::StatusOnly;
    loop {
        let result = runtime
            .block_on(client.advance_delegation_with_recovery(request, recovery, &control))
            .map_err(|e| format!("advance delegation: {e:?}"))?;
        match result {
            ChainSubmissionResult::Confirmed(confirmation) => return Ok(confirmation),
            ChainSubmissionResult::Pending(ChainSubmissionPending::Tracking { .. }) => {}
            ChainSubmissionResult::Pending(ChainSubmissionPending::Recovering {
                diagnostic,
                ..
            }) => {
                eprintln!(
                    "[E2E] delegation is recovering ({:?}: {}); next pass uses exact-tree recovery",
                    diagnostic.kind(),
                    diagnostic.message()
                );
                recovery = ChainRecoveryMode::ExactTree;
            }
            other => return Err(format!("delegation did not land: {other:?}").into()),
        }
        if Instant::now() >= deadline {
            return Err(format!("delegation not confirmed within {timeout:?}").into());
        }
        std::thread::sleep(Duration::from_secs(2));
    }
}

/// Rebuilds the helper share payloads of the vote committed for
/// `proposal_id` from its persisted recovery bundle, the public route to what
/// `VoteCommit::share_payloads` held before zcash_voting 5 made it private.
pub fn committed_share_payloads(
    db: &VotingDb,
    round_id_hex: &str,
    bundle_index: u32,
    proposal_id: u32,
) -> Result<Vec<SharePayload>, BoxError> {
    let recovery =
        zcash_voting::vote::recovery_bundle(db, round_id_hex, bundle_index, proposal_id)?
            .ok_or("committed vote has no recovery bundle")?;
    Ok(zcash_voting::share::recover_payloads(&recovery)?)
}

/// A copy of a `VotingDb` in a temporary SQLite file, deleted on drop.
pub struct VotingDbSnapshot {
    db: VotingDb,
    path: PathBuf,
}

impl VotingDbSnapshot {
    /// The copied database, scoped to the original's wallet.
    pub fn db(&self) -> &VotingDb {
        &self.db
    }
}

impl Drop for VotingDbSnapshot {
    /// Deletes the copy and any WAL files SQLite left beside it.
    fn drop(&mut self) {
        for suffix in ["", "-wal", "-shm"] {
            let mut file = self.path.clone().into_os_string();
            file.push(suffix);
            let _ = std::fs::remove_file(file);
        }
    }
}

/// Copies `db` into a temporary SQLite file with `VACUUM INTO` and opens the
/// copy under the same wallet, so a test can work from the same durable state
/// without changing the original.
pub fn snapshot(db: &VotingDb) -> Result<VotingDbSnapshot, BoxError> {
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)?
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "e2e-voting-db-{}-{nanos}.sqlite",
        std::process::id()
    ));
    let path_str = path.to_str().ok_or("snapshot path must be UTF-8")?;
    db.conn().execute("VACUUM INTO ?1", [path_str])?;
    let copy = VotingDb::open(path_str).map_err(|e: VotingError| format!("open snapshot: {e}"))?;
    copy.set_wallet_id(&db.wallet_id());
    Ok(VotingDbSnapshot { db: copy, path })
}
