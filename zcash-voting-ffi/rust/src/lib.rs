uniffi::setup_scaffolding!();

use librustvoting as voting;
use std::sync::{Arc, Mutex};
use vote_commitment_tree::{MerklePath, TreeClient};
use vote_commitment_tree_client::http_sync_api::HttpTreeSyncApi;
use voting::storage::VotingDb;
use zcash_keys::keys::UnifiedSpendingKey;
use zcash_protocol::consensus::{MAIN_NETWORK, TEST_NETWORK};
use zip32::{AccountId, Scope};

// --- Error type ---

#[derive(Debug, thiserror::Error, uniffi::Error)]
pub enum VotingError {
    #[error("Invalid input: {message}")]
    InvalidInput { message: String },
    #[error("Proof generation failed: {message}")]
    ProofFailed { message: String },
    #[error("Internal error: {message}")]
    Internal { message: String },
}

impl From<voting::VotingError> for VotingError {
    fn from(e: voting::VotingError) -> Self {
        match e {
            voting::VotingError::InvalidInput { message } => VotingError::InvalidInput { message },
            voting::VotingError::ProofFailed { message } => VotingError::ProofFailed { message },
            voting::VotingError::Internal { message } => VotingError::Internal { message },
        }
    }
}

// --- Callback interface for proof progress ---

#[uniffi::export(callback_interface)]
pub trait ProofProgressReporter: Send + Sync {
    fn on_progress(&self, progress: f64);
}

/// Bridges FFI callback → librustvoting trait.
struct ProgressBridge {
    inner: Box<dyn ProofProgressReporter>,
}

impl voting::ProofProgressReporter for ProgressBridge {
    fn on_progress(&self, progress: f64) {
        self.inner.on_progress(progress);
    }
}

// --- UniFFI Enum/Record types ---

#[derive(Clone, Copy, Debug, PartialEq, Eq, uniffi::Enum)]
pub enum RoundPhase {
    Initialized,
    HotkeyGenerated,
    DelegationConstructed,
    DelegationProved,
    VoteReady,
}

impl From<voting::storage::RoundPhase> for RoundPhase {
    fn from(p: voting::storage::RoundPhase) -> Self {
        match p {
            voting::storage::RoundPhase::Initialized => RoundPhase::Initialized,
            voting::storage::RoundPhase::HotkeyGenerated => RoundPhase::HotkeyGenerated,
            voting::storage::RoundPhase::DelegationConstructed => RoundPhase::DelegationConstructed,
            voting::storage::RoundPhase::DelegationProved => RoundPhase::DelegationProved,
            voting::storage::RoundPhase::VoteReady => RoundPhase::VoteReady,
        }
    }
}

#[derive(Clone, uniffi::Record)]
pub struct RoundState {
    pub round_id: String,
    pub phase: RoundPhase,
    pub snapshot_height: u64,
    pub hotkey_address: Option<String>,
    pub delegated_weight: Option<u64>,
    pub proof_generated: bool,
}

impl From<voting::storage::RoundState> for RoundState {
    fn from(s: voting::storage::RoundState) -> Self {
        Self {
            round_id: s.round_id,
            phase: s.phase.into(),
            snapshot_height: s.snapshot_height,
            hotkey_address: s.hotkey_address,
            delegated_weight: s.delegated_weight,
            proof_generated: s.proof_generated,
        }
    }
}

#[derive(Clone, uniffi::Record)]
pub struct RoundSummary {
    pub round_id: String,
    pub phase: RoundPhase,
    pub snapshot_height: u64,
    pub created_at: u64,
}

impl From<voting::storage::RoundSummary> for RoundSummary {
    fn from(s: voting::storage::RoundSummary) -> Self {
        Self {
            round_id: s.round_id,
            phase: s.phase.into(),
            snapshot_height: s.snapshot_height,
            created_at: s.created_at,
        }
    }
}

#[derive(Clone, uniffi::Record)]
pub struct VoteRecord {
    pub proposal_id: u32,
    pub bundle_index: u32,
    pub choice: u32,
    pub submitted: bool,
}

impl From<voting::storage::VoteRecord> for VoteRecord {
    fn from(v: voting::storage::VoteRecord) -> Self {
        Self {
            proposal_id: v.proposal_id,
            bundle_index: v.bundle_index,
            choice: v.choice,
            submitted: v.submitted,
        }
    }
}

// --- Existing UniFFI Record types ---

#[derive(Clone, uniffi::Record)]
pub struct VotingHotkey {
    pub secret_key: Vec<u8>,
    pub public_key: Vec<u8>,
    pub address: String,
}

#[derive(Clone, uniffi::Record)]
pub struct NoteInfo {
    pub commitment: Vec<u8>,
    pub nullifier: Vec<u8>,
    pub value: u64,
    pub position: u64,
    pub diversifier: Vec<u8>,
    pub rho: Vec<u8>,
    pub rseed: Vec<u8>,
    pub scope: u32,
    pub ufvk_str: String,
}

#[derive(Clone, uniffi::Record)]
pub struct VotingRoundParams {
    pub vote_round_id: String,
    pub snapshot_height: u64,
    pub ea_pk: Vec<u8>,
    pub nc_root: Vec<u8>,
    pub nullifier_imt_root: Vec<u8>,
}

#[derive(Clone, uniffi::Record)]
pub struct GovernancePczt {
    pub pczt_bytes: Vec<u8>,
    pub rk: Vec<u8>,
    pub alpha: Vec<u8>,
    pub nf_signed: Vec<u8>,
    pub cmx_new: Vec<u8>,
    pub gov_nullifiers: Vec<Vec<u8>>,
    pub van: Vec<u8>,
    pub van_comm_rand: Vec<u8>,
    pub dummy_nullifiers: Vec<Vec<u8>>,
    pub rho_signed: Vec<u8>,
    pub padded_cmx: Vec<Vec<u8>>,
    pub rseed_signed: Vec<u8>,
    pub rseed_output: Vec<u8>,
    pub action_bytes: Vec<u8>,
    pub action_index: u32,
    pub padded_note_secrets: Vec<Vec<Vec<u8>>>,
    pub pczt_sighash: Vec<u8>,
}

impl From<voting::GovernancePczt> for GovernancePczt {
    fn from(g: voting::GovernancePczt) -> Self {
        Self {
            pczt_bytes: g.pczt_bytes,
            rk: g.rk,
            alpha: g.alpha,
            nf_signed: g.nf_signed,
            cmx_new: g.cmx_new,
            gov_nullifiers: g.gov_nullifiers,
            van: g.van,
            van_comm_rand: g.van_comm_rand,
            dummy_nullifiers: g.dummy_nullifiers,
            rho_signed: g.rho_signed,
            padded_cmx: g.padded_cmx,
            rseed_signed: g.rseed_signed,
            rseed_output: g.rseed_output,
            action_bytes: g.action_bytes,
            action_index: g.action_index as u32,
            padded_note_secrets: g
                .padded_note_secrets
                .into_iter()
                .map(|(rho, rseed)| vec![rho, rseed])
                .collect(),
            pczt_sighash: g.pczt_sighash,
        }
    }
}

/// Inputs needed for delegation action construction.
#[derive(Clone, uniffi::Record)]
pub struct DelegationInputs {
    pub fvk_bytes: Vec<u8>,
    pub g_d_new_x: Vec<u8>,
    pub pk_d_new_x: Vec<u8>,
    pub hotkey_raw_address: Vec<u8>,
    pub hotkey_public_key: Vec<u8>,
    pub hotkey_address: String,
    /// 32-byte ZIP-32 seed fingerprint (needed by Keystone to identify the signing seed).
    pub seed_fingerprint: Vec<u8>,
}

#[derive(Clone, uniffi::Record)]
pub struct EncryptedShare {
    pub c1: Vec<u8>,
    pub c2: Vec<u8>,
    pub share_index: u32,
    pub plaintext_value: u64,
    /// El Gamal randomness (32 bytes). Witness-only; must NOT be sent over the network.
    pub randomness: Vec<u8>,
}

#[derive(Clone, uniffi::Record)]
pub struct VoteCommitmentBundle {
    pub van_nullifier: Vec<u8>,
    pub vote_authority_note_new: Vec<u8>,
    pub vote_commitment: Vec<u8>,
    pub proposal_id: u32,
    pub proof: Vec<u8>,
    /// Encrypted shares generated by the ZKP #2 builder (5 shares).
    pub enc_shares: Vec<EncryptedShare>,
    /// Tree anchor height used for the proof.
    pub anchor_height: u32,
    /// Voting round ID (hex string).
    pub vote_round_id: String,
    /// Poseidon hash of encrypted share x-coordinates (32 bytes).
    pub shares_hash: Vec<u8>,
    /// Per-share blind factors (N x 32 bytes, LE pallas::Base repr).
    pub share_blinds: Vec<Vec<u8>>,
    /// Pre-computed per-share Poseidon commitments (N x 32 bytes).
    pub share_comms: Vec<Vec<u8>>,
    /// Compressed r_vpk (32 bytes) for sighash computation and signature verification.
    pub r_vpk_bytes: Vec<u8>,
    /// Spend-auth randomizer alpha_v (32 bytes, LE scalar repr).
    pub alpha_v: Vec<u8>,
}

#[derive(Clone, uniffi::Record)]
pub struct SharePayload {
    pub shares_hash: Vec<u8>,
    pub proposal_id: u32,
    pub vote_decision: u32,
    pub enc_share: EncryptedShare,
    pub tree_position: u64,
    /// All encrypted shares (needed for enc_share lookup by the helper).
    pub all_enc_shares: Vec<EncryptedShare>,
    /// Pre-computed per-share Poseidon commitments (N x 32 bytes).
    pub share_comms: Vec<Vec<u8>>,
    /// Blind factor for this specific share (32 bytes).
    pub primary_blind: Vec<u8>,
}

/// Computed signature fields for cast-vote TX submission.
#[derive(Clone, uniffi::Record)]
pub struct CastVoteSignature {
    /// Decompressed r_vpk x-coordinate (32 bytes).
    pub r_vpk_x: Vec<u8>,
    /// Decompressed r_vpk y-coordinate (32 bytes).
    pub r_vpk_y: Vec<u8>,
    /// Canonical cast-vote sighash (32 bytes).
    pub sighash: Vec<u8>,
    /// Spend auth signature over sighash (64 bytes).
    pub vote_auth_sig: Vec<u8>,
}

impl From<voting::CastVoteSignature> for CastVoteSignature {
    fn from(s: voting::CastVoteSignature) -> Self {
        Self {
            r_vpk_x: s.r_vpk_x,
            r_vpk_y: s.r_vpk_y,
            sighash: s.sighash,
            vote_auth_sig: s.vote_auth_sig,
        }
    }
}

/// Complete delegation TX payload ready for chain submission.
/// Returned by `get_delegation_submission` after proof generation.
#[derive(Clone, uniffi::Record)]
pub struct DelegationSubmission {
    pub rk: Vec<u8>,
    pub spend_auth_sig: Vec<u8>,
    pub sighash: Vec<u8>,
    pub nf_signed: Vec<u8>,
    pub cmx_new: Vec<u8>,
    pub enc_memo: Vec<u8>,
    pub gov_comm: Vec<u8>,
    pub gov_nullifiers: Vec<Vec<u8>>,
    pub proof: Vec<u8>,
    pub vote_round_id: String,
}

impl From<voting::DelegationSubmissionData> for DelegationSubmission {
    fn from(d: voting::DelegationSubmissionData) -> Self {
        Self {
            rk: d.rk,
            spend_auth_sig: d.spend_auth_sig,
            sighash: d.sighash,
            nf_signed: d.nf_signed,
            cmx_new: d.cmx_new,
            enc_memo: d.enc_memo,
            gov_comm: d.gov_comm,
            gov_nullifiers: d.gov_nullifiers,
            proof: d.proof,
            vote_round_id: d.vote_round_id,
        }
    }
}
/// Result of real delegation proof generation (ZKP #1).
#[derive(Clone, uniffi::Record)]
pub struct DelegationProofResult {
    pub proof: Vec<u8>,
    pub public_inputs: Vec<Vec<u8>>,
    pub nf_signed: Vec<u8>,
    pub cmx_new: Vec<u8>,
    pub gov_nullifiers: Vec<Vec<u8>>,
    pub van_comm: Vec<u8>,
    pub rk: Vec<u8>,
}

#[derive(Clone, uniffi::Record)]
pub struct WitnessData {
    pub note_commitment: Vec<u8>,
    pub position: u64,
    pub root: Vec<u8>,
    pub auth_path: Vec<Vec<u8>>,
}

/// Result of value-aware bundle setup.
/// Returns the number of viable bundles and the total eligible weight
/// (excluding notes in sub-threshold bundles).
#[derive(Clone, uniffi::Record)]
pub struct BundleSetupResult {
    pub bundle_count: u32,
    pub eligible_weight: u64,
}

/// VAN Merkle witness for ZKP #2.
///
/// Contains the authentication path, leaf position, and anchor height needed
/// by `build_vote_commitment`. Generated by `generate_van_witness` after syncing
/// the vote commitment tree.
#[derive(Clone, uniffi::Record)]
pub struct VanWitness {
    /// 24 sibling hashes (32 bytes each) from leaf to root.
    pub auth_path: Vec<Vec<u8>>,
    /// Leaf position of the VAN in the tree.
    pub position: u32,
    /// Block height at which the tree was snapshotted.
    pub anchor_height: u32,
}

impl From<(MerklePath, u32)> for VanWitness {
    fn from((path, anchor_height): (MerklePath, u32)) -> Self {
        let auth_path = path
            .auth_path()
            .iter()
            .map(|h| h.to_bytes().to_vec())
            .collect();
        Self {
            auth_path,
            position: path.position(),
            anchor_height,
        }
    }
}

// --- Conversion helpers: FFI types <-> librustvoting types ---

impl From<voting::VotingHotkey> for VotingHotkey {
    fn from(h: voting::VotingHotkey) -> Self {
        Self {
            secret_key: h.secret_key,
            public_key: h.public_key,
            address: h.address,
        }
    }
}

impl From<VotingHotkey> for voting::VotingHotkey {
    fn from(h: VotingHotkey) -> Self {
        Self {
            secret_key: h.secret_key,
            public_key: h.public_key,
            address: h.address,
        }
    }
}

impl From<NoteInfo> for voting::NoteInfo {
    fn from(n: NoteInfo) -> Self {
        Self {
            commitment: n.commitment,
            nullifier: n.nullifier,
            value: n.value,
            position: n.position,
            diversifier: n.diversifier,
            rho: n.rho,
            rseed: n.rseed,
            scope: n.scope,
            ufvk_str: n.ufvk_str,
        }
    }
}

impl From<voting::NoteInfo> for NoteInfo {
    fn from(n: voting::NoteInfo) -> Self {
        Self {
            commitment: n.commitment,
            nullifier: n.nullifier,
            value: n.value,
            position: n.position,
            diversifier: n.diversifier,
            rho: n.rho,
            rseed: n.rseed,
            scope: n.scope,
            ufvk_str: n.ufvk_str,
        }
    }
}

impl From<VotingRoundParams> for voting::VotingRoundParams {
    fn from(p: VotingRoundParams) -> Self {
        Self {
            vote_round_id: p.vote_round_id,
            snapshot_height: p.snapshot_height,
            ea_pk: p.ea_pk,
            nc_root: p.nc_root,
            nullifier_imt_root: p.nullifier_imt_root,
        }
    }
}

impl From<voting::EncryptedShare> for EncryptedShare {
    fn from(s: voting::EncryptedShare) -> Self {
        Self {
            c1: s.c1,
            c2: s.c2,
            share_index: s.share_index,
            plaintext_value: s.plaintext_value,
            randomness: s.randomness,
        }
    }
}

impl From<EncryptedShare> for voting::EncryptedShare {
    fn from(s: EncryptedShare) -> Self {
        Self {
            c1: s.c1,
            c2: s.c2,
            share_index: s.share_index,
            plaintext_value: s.plaintext_value,
            randomness: s.randomness,
        }
    }
}

impl From<voting::VoteCommitmentBundle> for VoteCommitmentBundle {
    fn from(b: voting::VoteCommitmentBundle) -> Self {
        Self {
            van_nullifier: b.van_nullifier,
            vote_authority_note_new: b.vote_authority_note_new,
            vote_commitment: b.vote_commitment,
            proposal_id: b.proposal_id,
            proof: b.proof,
            enc_shares: b.enc_shares.into_iter().map(Into::into).collect(),
            anchor_height: b.anchor_height,
            vote_round_id: b.vote_round_id,
            shares_hash: b.shares_hash,
            share_blinds: b.share_blinds,
            share_comms: b.share_comms,
            r_vpk_bytes: b.r_vpk_bytes,
            alpha_v: b.alpha_v,
        }
    }
}

impl From<VoteCommitmentBundle> for voting::VoteCommitmentBundle {
    fn from(b: VoteCommitmentBundle) -> Self {
        Self {
            van_nullifier: b.van_nullifier,
            vote_authority_note_new: b.vote_authority_note_new,
            vote_commitment: b.vote_commitment,
            proposal_id: b.proposal_id,
            proof: b.proof,
            enc_shares: b.enc_shares.into_iter().map(Into::into).collect(),
            anchor_height: b.anchor_height,
            vote_round_id: b.vote_round_id,
            shares_hash: b.shares_hash,
            share_blinds: b.share_blinds,
            share_comms: b.share_comms,
            r_vpk_bytes: b.r_vpk_bytes,
            alpha_v: b.alpha_v,
        }
    }
}

impl From<voting::SharePayload> for SharePayload {
    fn from(p: voting::SharePayload) -> Self {
        Self {
            shares_hash: p.shares_hash,
            proposal_id: p.proposal_id,
            vote_decision: p.vote_decision,
            enc_share: p.enc_share.into(),
            tree_position: p.tree_position,
            all_enc_shares: p.all_enc_shares.into_iter().map(|s| s.into()).collect(),
            share_comms: p.share_comms,
            primary_blind: p.primary_blind,
        }
    }
}

impl From<voting::WitnessData> for WitnessData {
    fn from(w: voting::WitnessData) -> Self {
        Self {
            note_commitment: w.note_commitment,
            position: w.position,
            root: w.root,
            auth_path: w.auth_path,
        }
    }
}

impl From<WitnessData> for voting::WitnessData {
    fn from(w: WitnessData) -> Self {
        Self {
            note_commitment: w.note_commitment,
            position: w.position,
            root: w.root,
            auth_path: w.auth_path,
        }
    }
}

impl From<voting::DelegationProofResult> for DelegationProofResult {
    fn from(r: voting::DelegationProofResult) -> Self {
        Self {
            proof: r.proof,
            public_inputs: r.public_inputs,
            nf_signed: r.nf_signed,
            cmx_new: r.cmx_new,
            gov_nullifiers: r.gov_nullifiers,
            van_comm: r.van_comm,
            rk: r.rk,
        }
    }
}

// =============================================================================
// VotingDatabase — stateful UniFFI Object (new API)
// =============================================================================

#[derive(uniffi::Object)]
pub struct VotingDatabase {
    db: Arc<VotingDb>,
    /// Client-side vote commitment tree for VAN witness generation.
    /// Lazily created on first sync_vote_tree call.
    tree_client: Mutex<Option<TreeClient>>,
}

#[uniffi::export]
impl VotingDatabase {
    // --- Lifecycle ---

    #[uniffi::constructor]
    pub fn open(path: String) -> Result<Self, VotingError> {
        let db = VotingDb::open(&path)?;
        Ok(Self {
            db: Arc::new(db),
            tree_client: Mutex::new(None),
        })
    }

    // --- Round management ---

    pub fn init_round(
        &self,
        params: VotingRoundParams,
        session_json: Option<String>,
    ) -> Result<(), VotingError> {
        Ok(self
            .db
            .init_round(&params.into(), session_json.as_deref())?)
    }

    pub fn get_round_state(&self, round_id: String) -> Result<RoundState, VotingError> {
        Ok(self.db.get_round_state(&round_id)?.into())
    }

    pub fn list_rounds(&self) -> Result<Vec<RoundSummary>, VotingError> {
        Ok(self.db.list_rounds()?.into_iter().map(Into::into).collect())
    }

    pub fn get_votes(&self, round_id: String) -> Result<Vec<VoteRecord>, VotingError> {
        Ok(self
            .db
            .get_votes(&round_id)?
            .into_iter()
            .map(Into::into)
            .collect())
    }

    pub fn clear_round(&self, round_id: String) -> Result<(), VotingError> {
        Ok(self.db.clear_round(&round_id)?)
    }

    // --- Wallet notes ---

    pub fn get_wallet_notes(
        &self,
        wallet_db_path: String,
        snapshot_height: u64,
        network_id: u32,
    ) -> Result<Vec<NoteInfo>, VotingError> {
        Ok(self
            .db
            .get_wallet_notes(&wallet_db_path, snapshot_height, network_id)?
            .into_iter()
            .map(Into::into)
            .collect())
    }

    // --- Phase 1: Delegation setup ---

    pub fn generate_hotkey(
        &self,
        round_id: String,
        seed: Vec<u8>,
    ) -> Result<VotingHotkey, VotingError> {
        Ok(self.db.generate_hotkey(&round_id, &seed)?.into())
    }

    pub fn setup_bundles(
        &self,
        round_id: String,
        notes: Vec<NoteInfo>,
    ) -> Result<BundleSetupResult, VotingError> {
        let core_notes: Vec<voting::NoteInfo> = notes.into_iter().map(Into::into).collect();
        let (count, weight) = self.db.setup_bundles(&round_id, &core_notes)?;
        Ok(BundleSetupResult {
            bundle_count: count,
            eligible_weight: weight,
        })
    }

    pub fn get_bundle_count(&self, round_id: String) -> Result<u32, VotingError> {
        Ok(self.db.get_bundle_count(&round_id)?)
    }

    pub fn build_governance_pczt(
        &self,
        round_id: String,
        bundle_index: u32,
        notes: Vec<NoteInfo>,
        fvk_bytes: Vec<u8>,
        hotkey_raw_address: Vec<u8>,
        consensus_branch_id: u32,
        coin_type: u32,
        seed_fingerprint: Vec<u8>,
        account_index: u32,
        round_name: String,
        address_index: u32,
    ) -> Result<GovernancePczt, VotingError> {
        let core_notes: Vec<voting::NoteInfo> = notes.into_iter().map(Into::into).collect();
        let seed_fp_32: [u8; 32] =
            seed_fingerprint
                .try_into()
                .map_err(|_| VotingError::InvalidInput {
                    message: "seed_fingerprint must be 32 bytes".to_string(),
                })?;
        Ok(self
            .db
            .build_governance_pczt(
                &round_id,
                bundle_index,
                &core_notes,
                &fvk_bytes,
                &hotkey_raw_address,
                consensus_branch_id,
                coin_type,
                &seed_fp_32,
                account_index,
                &round_name,
                address_index,
            )?
            .into())
    }

    pub fn store_tree_state(
        &self,
        round_id: String,
        tree_state_bytes: Vec<u8>,
    ) -> Result<(), VotingError> {
        Ok(self.db.store_tree_state(&round_id, &tree_state_bytes)?)
    }

    /// Generate Merkle inclusion witnesses for notes in a bundle.
    /// Requires store_tree_state to have been called first.
    /// Results are cached — subsequent calls return cached data.
    pub fn generate_note_witnesses(
        &self,
        round_id: String,
        bundle_index: u32,
        wallet_db_path: String,
        notes: Vec<NoteInfo>,
    ) -> Result<Vec<WitnessData>, VotingError> {
        let core_notes: Vec<voting::NoteInfo> = notes.into_iter().map(Into::into).collect();
        Ok(self
            .db
            .generate_note_witnesses(&round_id, bundle_index, &wallet_db_path, &core_notes)?
            .into_iter()
            .map(Into::into)
            .collect())
    }

    // --- Phase 2: Delegation proof ---

    /// Build and prove the real delegation ZKP (#1). Long-running.
    ///
    /// Loads all required data from the voting DB and wallet DB, fetches nullifier
    /// exclusion proofs via PIR, generates a real Halo2 proof,
    /// and advances the round phase to DelegationProved.
    ///
    /// - `round_id`: Voting round hex identifier.
    /// - `wallet_db_path`: Path to the Zcash wallet SQLite DB (read-only).
    /// - `hotkey_raw_address`: 43-byte raw Orchard address of the voting hotkey.
    /// - `pir_server_url`: Base URL of the nullifier PIR server.
    /// - `network_id`: 0 = mainnet, 1 = testnet.
    /// - `progress`: Progress callback (0.0 → 1.0).
    pub fn build_and_prove_delegation(
        &self,
        round_id: String,
        bundle_index: u32,
        wallet_db_path: String,
        hotkey_raw_address: Vec<u8>,
        pir_server_url: String,
        network_id: u32,
        progress: Box<dyn ProofProgressReporter>,
    ) -> Result<DelegationProofResult, VotingError> {
        let bridge = ProgressBridge { inner: progress };
        Ok(self
            .db
            .build_and_prove_delegation(
                &round_id,
                bundle_index,
                &wallet_db_path,
                &hotkey_raw_address,
                &pir_server_url,
                network_id,
                &bridge,
            )?
            .into())
    }

    // --- Phase 3: Voting ---

    pub fn encrypt_shares(
        &self,
        round_id: String,
        shares: Vec<u64>,
    ) -> Result<Vec<EncryptedShare>, VotingError> {
        Ok(self
            .db
            .encrypt_shares(&round_id, &shares)?
            .into_iter()
            .map(Into::into)
            .collect())
    }

    pub fn build_vote_commitment(
        &self,
        round_id: String,
        bundle_index: u32,
        hotkey_seed: Vec<u8>,
        network_id: u32,
        proposal_id: u32,
        choice: u32,
        num_options: u32,
        van_auth_path: Vec<Vec<u8>>,
        van_position: u32,
        anchor_height: u32,
        progress: Box<dyn ProofProgressReporter>,
    ) -> Result<VoteCommitmentBundle, VotingError> {
        // Convert Vec<Vec<u8>> to &[[u8; 32]]
        let auth_path: Vec<[u8; 32]> = van_auth_path
            .into_iter()
            .map(|v| {
                v.try_into().map_err(|_| VotingError::InvalidInput {
                    message: "each auth_path sibling must be 32 bytes".to_string(),
                })
            })
            .collect::<Result<Vec<_>, _>>()?;
        let bridge = ProgressBridge { inner: progress };
        Ok(self
            .db
            .build_vote_commitment(
                &round_id,
                bundle_index,
                &hotkey_seed,
                network_id,
                proposal_id,
                choice,
                num_options,
                &auth_path,
                van_position,
                anchor_height,
                &bridge,
            )?
            .into())
    }

    pub fn build_share_payloads(
        &self,
        enc_shares: Vec<EncryptedShare>,
        commitment: VoteCommitmentBundle,
        vote_decision: u32,
        num_options: u32,
        vc_tree_position: u64,
    ) -> Result<Vec<SharePayload>, VotingError> {
        let core_shares: Vec<voting::EncryptedShare> =
            enc_shares.into_iter().map(Into::into).collect();
        Ok(self
            .db
            .build_share_payloads(
                &core_shares,
                &commitment.into(),
                vote_decision,
                num_options,
                vc_tree_position,
            )?
            .into_iter()
            .map(Into::into)
            .collect())
    }

    /// Reconstruct the delegation TX payload using a Keystone-provided signature.
    ///
    /// Unlike `get_delegation_submission`, this does NOT derive `ask` from a seed.
    /// It uses the externally-provided Keystone signature and the ZIP-244 sighash.
    pub fn get_delegation_submission_with_keystone_sig(
        &self,
        round_id: String,
        bundle_index: u32,
        keystone_sig: Vec<u8>,
        keystone_sighash: Vec<u8>,
    ) -> Result<DelegationSubmission, VotingError> {
        Ok(self
            .db
            .get_delegation_submission_with_keystone_sig(
                &round_id,
                bundle_index,
                &keystone_sig,
                &keystone_sighash,
            )?
            .into())
    }

    /// Reconstruct the full chain-ready delegation TX payload from DB + seed.
    ///
    /// After `build_and_prove_delegation` completes, call this to get the signed
    /// delegation payload for submission. Derives the sender's SpendingKey from
    /// seed, computes the canonical sighash, and signs it.
    pub fn get_delegation_submission(
        &self,
        round_id: String,
        bundle_index: u32,
        sender_seed: Vec<u8>,
        network_id: u32,
        account_index: u32,
    ) -> Result<DelegationSubmission, VotingError> {
        Ok(self
            .db
            .get_delegation_submission(
                &round_id,
                bundle_index,
                &sender_seed,
                network_id,
                account_index,
            )?
            .into())
    }

    /// Store the VAN leaf position after delegation TX is confirmed on chain.
    pub fn store_van_position(
        &self,
        round_id: String,
        bundle_index: u32,
        position: u32,
    ) -> Result<(), VotingError> {
        Ok(self
            .db
            .store_van_position(&round_id, bundle_index, position)?)
    }

    // --- Vote commitment tree sync ---

    /// Sync the vote commitment tree from a chain node.
    ///
    /// Creates a TreeClient on first call, then syncs incrementally on
    /// subsequent calls. VAN positions from ALL bundles are automatically
    /// marked for witness generation before syncing.
    ///
    /// Returns the latest synced block height.
    pub fn sync_vote_tree(&self, round_id: String, node_url: String) -> Result<u32, VotingError> {
        // Mark VAN positions for ALL bundles so witnesses can be generated for any bundle.
        let bundle_count = self.db.get_bundle_count(&round_id)?;

        let mut guard = self.tree_client.lock().map_err(|e| VotingError::Internal {
            message: format!("tree client lock poisoned: {}", e),
        })?;

        let client = guard.get_or_insert_with(TreeClient::empty);

        for bi in 0..bundle_count {
            // load_van_position may fail if VAN hasn't been stored yet for a bundle — skip it.
            if let Ok(pos) = self.db.load_van_position(&round_id, bi) {
                client.mark_position(pos as u64);
            }
        }

        let api = HttpTreeSyncApi::new(node_url);
        client.sync(&api).map_err(|e| VotingError::Internal {
            message: format!("vote tree sync failed: {}", e),
        })?;

        client
            .last_synced_height()
            .ok_or_else(|| VotingError::Internal {
                message: "tree has no synced height after sync".to_string(),
            })
    }

    /// Generate a VAN Merkle witness for ZKP #2.
    ///
    /// Requires `sync_vote_tree` to have been called first. Loads the VAN
    /// position for the specified bundle and generates a witness at the given
    /// anchor height.
    pub fn generate_van_witness(
        &self,
        round_id: String,
        bundle_index: u32,
        anchor_height: u32,
    ) -> Result<VanWitness, VotingError> {
        let van_position = self.db.load_van_position(&round_id, bundle_index)?;

        let guard = self.tree_client.lock().map_err(|e| VotingError::Internal {
            message: format!("tree client lock poisoned: {}", e),
        })?;

        let client = guard.as_ref().ok_or_else(|| VotingError::InvalidInput {
            message: "must call sync_vote_tree before generate_van_witness".to_string(),
        })?;

        let path = client
            .witness(van_position as u64, anchor_height)
            .ok_or_else(|| VotingError::Internal {
                message: format!(
                    "failed to generate witness for position {} at height {}",
                    van_position, anchor_height
                ),
            })?;

        Ok(VanWitness::from((path, anchor_height)))
    }

    pub fn mark_vote_submitted(
        &self,
        round_id: String,
        bundle_index: u32,
        proposal_id: u32,
    ) -> Result<(), VotingError> {
        Ok(self
            .db
            .mark_vote_submitted(&round_id, bundle_index, proposal_id)?)
    }

    /// Drop the in-memory TreeClient so the next `sync_vote_tree()` call
    /// creates a fresh one and does a full resync from genesis. This recovers
    /// from stale state that would otherwise cause `StartIndexMismatch` or
    /// `RootMismatch` errors.
    pub fn reset_tree_client(&self) -> Result<(), VotingError> {
        let mut guard = self.tree_client.lock().map_err(|e| VotingError::Internal {
            message: format!("tree client lock poisoned: {}", e),
        })?;
        *guard = None;
        Ok(())
    }
}

// =============================================================================
// Legacy free functions (kept for backward compat during Swift migration)
// =============================================================================

#[uniffi::export]
pub fn generate_hotkey(seed: Vec<u8>) -> Result<VotingHotkey, VotingError> {
    Ok(voting::hotkey::generate_hotkey(&seed)?.into())
}

#[uniffi::export]
pub fn decompose_weight(weight: u64) -> Vec<u64> {
    voting::decompose::decompose_weight(weight)
}

#[uniffi::export]
pub fn encrypt_shares(
    shares: Vec<u64>,
    ea_pk: Vec<u8>,
) -> Result<Vec<EncryptedShare>, VotingError> {
    Ok(voting::elgamal::encrypt_shares(&shares, &ea_pk)?
        .into_iter()
        .map(Into::into)
        .collect())
}

#[uniffi::export]
pub fn build_governance_pczt(
    notes: Vec<NoteInfo>,
    params: VotingRoundParams,
    fvk_bytes: Vec<u8>,
    hotkey_raw_address: Vec<u8>,
    consensus_branch_id: u32,
    coin_type: u32,
    seed_fingerprint: Vec<u8>,
    account_index: u32,
    round_name: String,
) -> Result<GovernancePczt, VotingError> {
    let core_notes: Vec<voting::NoteInfo> = notes.into_iter().map(Into::into).collect();
    let seed_fp_32: [u8; 32] =
        seed_fingerprint
            .try_into()
            .map_err(|_| VotingError::InvalidInput {
                message: "seed_fingerprint must be 32 bytes".to_string(),
            })?;
    Ok(voting::action::build_governance_pczt(
        &core_notes,
        &params.into(),
        &fvk_bytes,
        &hotkey_raw_address,
        consensus_branch_id,
        coin_type,
        &seed_fp_32,
        account_index,
        &round_name,
    )?
    .into())
}

#[uniffi::export]
pub fn generate_delegation_inputs(
    sender_seed: Vec<u8>,
    hotkey_seed: Vec<u8>,
    network_id: u32,
    account_index: u32,
) -> Result<DelegationInputs, VotingError> {
    // ZIP-32 key derivation requires non-trivial seed material; fail fast with clear errors.
    if sender_seed.len() < 32 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "sender_seed must be at least 32 bytes, got {}",
                sender_seed.len()
            ),
        });
    }
    if hotkey_seed.len() < 32 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "hotkey_seed must be at least 32 bytes, got {}",
                hotkey_seed.len()
            ),
        });
    }

    // Keep account index within the ZIP-32 account domain expected by AccountId.
    let account = AccountId::try_from(account_index).map_err(|_| VotingError::InvalidInput {
        message: format!("account_index must be < 2^31, got {}", account_index),
    })?;

    // Derive the sender Orchard FVK bytes consumed by build_governance_pczt.
    // These bytes include nk (middle 32 bytes), which is used for gov nullifier derivation.
    let sender_usk = match network_id {
        0 => UnifiedSpendingKey::from_seed(&MAIN_NETWORK, &sender_seed, account),
        1 => UnifiedSpendingKey::from_seed(&TEST_NETWORK, &sender_seed, account),
        _ => {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "invalid network_id {}, expected 0 (mainnet) or 1 (testnet)",
                    network_id
                ),
            });
        }
    }
    .map_err(|e| VotingError::InvalidInput {
        message: format!("failed to derive sender UnifiedSpendingKey: {}", e),
    })?;

    let sender_fvk = sender_usk
        .to_unified_full_viewing_key()
        .orchard()
        .ok_or_else(|| VotingError::InvalidInput {
            message: "sender UFVK is missing Orchard component".to_string(),
        })?
        .to_bytes()
        .to_vec();

    // Derive hotkey-side Orchard material from the same network/account so all components
    // (raw address, g_d_x, pk_d_x) are internally consistent.
    let hotkey_usk = match network_id {
        0 => UnifiedSpendingKey::from_seed(&MAIN_NETWORK, &hotkey_seed, account),
        1 => UnifiedSpendingKey::from_seed(&TEST_NETWORK, &hotkey_seed, account),
        _ => unreachable!("network_id validated above"),
    }
    .map_err(|e| VotingError::InvalidInput {
        message: format!("failed to derive hotkey UnifiedSpendingKey: {}", e),
    })?;
    let hotkey_ufvk = hotkey_usk.to_unified_full_viewing_key();
    let hotkey_orchard_fvk = hotkey_ufvk
        .orchard()
        .ok_or_else(|| VotingError::InvalidInput {
            message: "hotkey UFVK is missing Orchard component".to_string(),
        })?;

    // App-facing hotkey (pubkey/address string) is returned alongside Orchard receiver bytes.
    // The Swift layer checks these values match before constructing/signing the delegation action.
    let app_hotkey = voting::hotkey::generate_hotkey(&hotkey_seed)?;
    let hotkey_addr = hotkey_orchard_fvk.address_at(0u32, Scope::External);
    let hotkey_raw_address = hotkey_addr.to_raw_address_bytes().to_vec();

    // Precompute x-coordinates used by VAN and ZKP public inputs from the raw Orchard receiver.
    // Rust action construction re-validates this binding to reject mismatched caller inputs.
    let hotkey_addr_43: [u8; 43] = hotkey_raw_address
        .as_slice()
        .try_into()
        .expect("address serialization must be 43 bytes");
    let (g_d_new_x, pk_d_new_x) =
        voting::action::derive_hotkey_x_coords_from_raw_address(&hotkey_addr_43)?;

    // Compute ZIP-32 seed fingerprint from the sender seed.
    // Keystone uses this to identify which seed to derive the spending key from.
    let seed_fp =
        zip32::fingerprint::SeedFingerprint::from_seed(&sender_seed).ok_or_else(|| {
            VotingError::InvalidInput {
                message: "failed to compute seed fingerprint (seed too short?)".to_string(),
            }
        })?;

    Ok(DelegationInputs {
        fvk_bytes: sender_fvk,
        g_d_new_x: g_d_new_x.to_vec(),
        pk_d_new_x: pk_d_new_x.to_vec(),
        hotkey_raw_address,
        hotkey_public_key: app_hotkey.public_key,
        hotkey_address: app_hotkey.address,
        seed_fingerprint: seed_fp.to_bytes().to_vec(),
    })
}

#[uniffi::export]
pub fn generate_note_witness(
    note_position: u64,
    snapshot_height: u32,
    tree_state_bytes: Vec<u8>,
) -> Result<WitnessData, VotingError> {
    Ok(
        voting::witness::generate_note_witness(note_position, snapshot_height, &tree_state_bytes)?
            .into(),
    )
}

#[uniffi::export]
pub fn verify_witness(witness: WitnessData) -> Result<bool, VotingError> {
    Ok(voting::witness::verify_witness(&witness.into())?)
}

/// Extract the ZIP-244 shielded sighash from finalized PCZT bytes.
///
/// Returns the 32-byte sighash that Keystone signs internally. Used to construct
/// the delegation submission with the correct sighash for chain verification.
#[uniffi::export]
pub fn extract_pczt_sighash(pczt_bytes: Vec<u8>) -> Result<Vec<u8>, VotingError> {
    Ok(voting::action::extract_pczt_sighash(&pczt_bytes)?.to_vec())
}

/// Derive delegation inputs using an explicit FVK instead of deriving from sender seed.
///
/// For Keystone accounts, the notes carry the Keystone's UFVK in the wallet DB.
/// This function uses the provided FVK bytes directly (from the note's `ufvk_str`)
/// instead of deriving from a seed, ensuring the prover and PCZT builder use the
/// same `ak`.
#[uniffi::export]
pub fn generate_delegation_inputs_with_fvk(
    fvk_bytes: Vec<u8>,
    hotkey_seed: Vec<u8>,
    network_id: u32,
    account_index: u32,
    seed_fingerprint: Vec<u8>,
) -> Result<DelegationInputs, VotingError> {
    if fvk_bytes.len() != 96 {
        return Err(VotingError::InvalidInput {
            message: format!("fvk_bytes must be 96 bytes, got {}", fvk_bytes.len()),
        });
    }
    if hotkey_seed.len() < 32 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "hotkey_seed must be at least 32 bytes, got {}",
                hotkey_seed.len()
            ),
        });
    }
    if seed_fingerprint.len() != 32 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "seed_fingerprint must be 32 bytes, got {}",
                seed_fingerprint.len()
            ),
        });
    }

    let account = AccountId::try_from(account_index).map_err(|_| VotingError::InvalidInput {
        message: format!("account_index must be < 2^31, got {}", account_index),
    })?;

    // Derive hotkey-side Orchard material
    let hotkey_usk = match network_id {
        0 => UnifiedSpendingKey::from_seed(&MAIN_NETWORK, &hotkey_seed, account),
        1 => UnifiedSpendingKey::from_seed(&TEST_NETWORK, &hotkey_seed, account),
        _ => {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "invalid network_id {}, expected 0 (mainnet) or 1 (testnet)",
                    network_id
                ),
            });
        }
    }
    .map_err(|e| VotingError::InvalidInput {
        message: format!("failed to derive hotkey UnifiedSpendingKey: {}", e),
    })?;
    let hotkey_ufvk = hotkey_usk.to_unified_full_viewing_key();
    let hotkey_orchard_fvk = hotkey_ufvk
        .orchard()
        .ok_or_else(|| VotingError::InvalidInput {
            message: "hotkey UFVK is missing Orchard component".to_string(),
        })?;

    let app_hotkey = voting::hotkey::generate_hotkey(&hotkey_seed)?;
    let hotkey_addr = hotkey_orchard_fvk.address_at(0u32, Scope::External);
    let hotkey_raw_address = hotkey_addr.to_raw_address_bytes().to_vec();

    let hotkey_addr_43: [u8; 43] = hotkey_raw_address
        .as_slice()
        .try_into()
        .expect("address serialization must be 43 bytes");
    let (g_d_new_x, pk_d_new_x) =
        voting::action::derive_hotkey_x_coords_from_raw_address(&hotkey_addr_43)?;

    Ok(DelegationInputs {
        fvk_bytes,
        g_d_new_x: g_d_new_x.to_vec(),
        pk_d_new_x: pk_d_new_x.to_vec(),
        hotkey_raw_address,
        hotkey_public_key: app_hotkey.public_key,
        hotkey_address: app_hotkey.address,
        seed_fingerprint,
    })
}

#[uniffi::export]
pub fn extract_spend_auth_sig(
    signed_pczt_bytes: Vec<u8>,
    action_index: u32,
) -> Result<Vec<u8>, VotingError> {
    Ok(voting::action::extract_spend_auth_sig(&signed_pczt_bytes, action_index as usize)?.to_vec())
}

/// Build vote commitment + ZKP #2 (free function, uses NoopProgressReporter).
///
/// Prefer the VotingDatabase method which loads inputs from DB automatically.
#[uniffi::export]
pub fn build_vote_commitment(
    hotkey_seed: Vec<u8>,
    network_id: u32,
    address_index: u32,
    total_note_value: u64,
    gov_comm_rand: Vec<u8>,
    voting_round_id: Vec<u8>,
    ea_pk: Vec<u8>,
    proposal_id: u32,
    choice: u32,
    num_options: u32,
    van_auth_path: Vec<Vec<u8>>,
    van_position: u32,
    anchor_height: u32,
    proposal_authority: u64,
) -> Result<VoteCommitmentBundle, VotingError> {
    let auth_path: Vec<[u8; 32]> = van_auth_path
        .into_iter()
        .map(|v| {
            v.try_into().map_err(|_| VotingError::InvalidInput {
                message: "each auth_path sibling must be 32 bytes".to_string(),
            })
        })
        .collect::<Result<Vec<_>, _>>()?;
    let reporter = voting::NoopProgressReporter;
    Ok(voting::zkp2::build_vote_commitment(
        &hotkey_seed,
        network_id,
        address_index,
        total_note_value,
        &gov_comm_rand,
        &voting_round_id,
        &ea_pk,
        proposal_id,
        choice,
        num_options,
        &auth_path,
        van_position,
        anchor_height,
        proposal_authority,
        &reporter,
    )?
    .into())
}

#[uniffi::export]
pub fn build_share_payloads(
    enc_shares: Vec<EncryptedShare>,
    commitment: VoteCommitmentBundle,
    vote_decision: u32,
    num_options: u32,
    vc_tree_position: u64,
) -> Result<Vec<SharePayload>, VotingError> {
    let core_shares: Vec<voting::EncryptedShare> = enc_shares.into_iter().map(Into::into).collect();
    Ok(voting::vote_commitment::build_share_payloads(
        &core_shares,
        &commitment.into(),
        vote_decision,
        num_options,
        vc_tree_position,
    )?
    .into_iter()
    .map(Into::into)
    .collect())
}

/// Compute the canonical cast-vote sighash, decompress r_vpk, and sign.
///
/// Pure computation — takes fields from `VoteCommitmentBundle` plus hotkey seed.
/// Returns signature fields needed for the cast-vote TX payload.
#[uniffi::export]
pub fn sign_cast_vote(
    hotkey_seed: Vec<u8>,
    network_id: u32,
    vote_round_id_hex: String,
    r_vpk_bytes: Vec<u8>,
    van_nullifier: Vec<u8>,
    vote_authority_note_new: Vec<u8>,
    vote_commitment: Vec<u8>,
    proposal_id: u32,
    anchor_height: u32,
    alpha_v: Vec<u8>,
) -> Result<CastVoteSignature, VotingError> {
    Ok(voting::vote_commitment::sign_cast_vote(
        &hotkey_seed,
        network_id,
        &vote_round_id_hex,
        &r_vpk_bytes,
        &van_nullifier,
        &vote_authority_note_new,
        &vote_commitment,
        proposal_id,
        anchor_height,
        &alpha_v,
    )?
    .into())
}

/// Extract the 96-byte Orchard FVK from a UFVK string.
///
/// Decodes a Bech32-encoded Unified Full Viewing Key string and returns the
/// raw 96-byte Orchard component (ak[32] || nk[32] || rivk[32]).
/// Used for Keystone accounts where the FVK must come from the note's UFVK
/// rather than being derived from the app's seed.
#[uniffi::export]
pub fn extract_orchard_fvk_from_ufvk(
    ufvk_str: String,
    network_id: u32,
) -> Result<Vec<u8>, VotingError> {
    use zcash_keys::keys::UnifiedFullViewingKey;
    let ufvk = match network_id {
        0 => UnifiedFullViewingKey::decode(&MAIN_NETWORK, &ufvk_str),
        1 => UnifiedFullViewingKey::decode(&TEST_NETWORK, &ufvk_str),
        _ => {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "invalid network_id {}, expected 0 (mainnet) or 1 (testnet)",
                    network_id
                ),
            });
        }
    }
    .map_err(|e| VotingError::InvalidInput {
        message: format!("failed to decode UFVK string: {}", e),
    })?;
    let orchard_fvk = ufvk.orchard().ok_or_else(|| VotingError::InvalidInput {
        message: "UFVK has no Orchard component".to_string(),
    })?;
    Ok(orchard_fvk.to_bytes().to_vec())
}

/// Extract the Orchard note commitment tree root from a protobuf-encoded TreeState.
/// Returns the 32-byte nc_root needed when creating a voting session.
#[uniffi::export]
pub fn extract_nc_root(tree_state_bytes: Vec<u8>) -> Result<Vec<u8>, VotingError> {
    Ok(voting::extract_nc_root(&tree_state_bytes)?)
}

#[uniffi::export]
pub fn voting_ffi_version() -> String {
    env!("CARGO_PKG_VERSION").to_string()
}
