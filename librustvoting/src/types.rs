use thiserror::Error;

#[derive(Debug, Error)]
pub enum VotingError {
    #[error("Invalid input: {message}")]
    InvalidInput { message: String },
    #[error("Proof generation failed: {message}")]
    ProofFailed { message: String },
    #[error("Internal error: {message}")]
    Internal { message: String },
}

/// Voting hotkey pair. secret_key must be 32 bytes (Pallas scalar).
#[derive(Clone, Debug)]
pub struct VotingHotkey {
    pub secret_key: Vec<u8>,
    pub public_key: Vec<u8>,
    pub address: String,
}

/// A shielded Orchard note with data needed for delegation.
#[derive(Clone, Debug)]
pub struct NoteInfo {
    pub commitment: Vec<u8>,
    pub nullifier: Vec<u8>,
    pub value: u64,
    pub position: u64,
}

/// Parameters for a voting round, sourced from vote chain.
#[derive(Clone, Debug)]
pub struct VotingRoundParams {
    pub vote_round_id: String,
    pub snapshot_height: u64,
    pub ea_pk: Vec<u8>,
    pub nc_root: Vec<u8>,
    pub nullifier_imt_root: Vec<u8>,
}

/// Delegation action for Keystone signing.
#[derive(Clone, Debug)]
pub struct DelegationAction {
    pub action_bytes: Vec<u8>,
    pub rk: Vec<u8>,
    pub sighash: Vec<u8>,
    /// Governance nullifiers, always padded to 4.
    pub gov_nullifiers: Vec<Vec<u8>>,
    /// 32-byte governance commitment (VAN).
    pub van: Vec<u8>,
    /// 32-byte blinding factor used for VAN (must be persisted for later use).
    pub gov_comm_rand: Vec<u8>,
    /// Random nullifiers used for padded dummy notes (needed for circuit witness in later steps).
    pub dummy_nullifiers: Vec<Vec<u8>>,
    /// Constrained rho for the signed note (32 bytes). Spec §1.3.4.1.
    pub rho_signed: Vec<u8>,
    /// Extracted note commitments (cmx) for padded dummy notes.
    /// Needed for ZKP witness construction in later steps.
    pub padded_cmx: Vec<Vec<u8>>,
}

/// El Gamal ciphertext of a voting share.
#[derive(Clone, Debug)]
pub struct EncryptedShare {
    pub c1: Vec<u8>,
    pub c2: Vec<u8>,
    pub share_index: u32,
    pub plaintext_value: u64,
}

/// Complete vote commitment bundle for submission to vote chain.
#[derive(Clone, Debug)]
pub struct VoteCommitmentBundle {
    pub van_nullifier: Vec<u8>,
    pub vote_authority_note_new: Vec<u8>,
    pub vote_commitment: Vec<u8>,
    pub proposal_id: u32,
    pub proof: Vec<u8>,
}

/// Payload sent to helper server for delegated share submission.
#[derive(Clone, Debug)]
pub struct SharePayload {
    pub shares_hash: Vec<u8>,
    pub proposal_id: u32,
    pub vote_decision: u32,
    pub enc_share: EncryptedShare,
    pub tree_position: u64,
}

/// Result of ZKP generation.
#[derive(Clone, Debug)]
pub struct ProofResult {
    pub proof: Vec<u8>,
    pub success: bool,
    pub error: Option<String>,
}

/// Merkle witness for a note in the Orchard commitment tree.
#[derive(Clone, Debug)]
pub struct WitnessData {
    pub note_commitment: Vec<u8>,
    pub position: u64,
    pub root: Vec<u8>,
    pub auth_path: Vec<Vec<u8>>,
}

/// Callback for proof generation progress reporting.
/// Swift implements this trait; Rust calls it during long-running operations.
pub trait ProofProgressReporter: Send + Sync {
    fn on_progress(&self, progress: f64);
}

/// No-op progress reporter for contexts where progress isn't observed.
pub struct NoopProgressReporter;

impl ProofProgressReporter for NoopProgressReporter {
    fn on_progress(&self, _progress: f64) {}
}

// --- Validation helpers ---

pub fn validate_32_bytes(v: &[u8], name: &str) -> Result<(), VotingError> {
    if v.len() != 32 {
        return Err(VotingError::InvalidInput {
            message: format!("{} must be 32 bytes, got {}", name, v.len()),
        });
    }
    Ok(())
}

pub fn validate_share_index(index: u32) -> Result<(), VotingError> {
    if index > 3 {
        return Err(VotingError::InvalidInput {
            message: format!("share_index must be 0..3, got {}", index),
        });
    }
    Ok(())
}

pub fn validate_vote_decision(decision: u32) -> Result<(), VotingError> {
    if decision > 2 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "vote_decision must be 0 (support), 1 (oppose), or 2 (skip), got {}",
                decision
            ),
        });
    }
    Ok(())
}

pub fn validate_notes(notes: &[NoteInfo]) -> Result<(), VotingError> {
    if notes.is_empty() || notes.len() > 4 {
        return Err(VotingError::InvalidInput {
            message: format!("notes must have 1..4 entries, got {}", notes.len()),
        });
    }
    for (i, note) in notes.iter().enumerate() {
        validate_32_bytes(&note.commitment, &format!("notes[{}].commitment", i))?;
        validate_32_bytes(&note.nullifier, &format!("notes[{}].nullifier", i))?;
    }
    Ok(())
}

pub fn validate_round_params(params: &VotingRoundParams) -> Result<(), VotingError> {
    validate_32_bytes(&params.ea_pk, "ea_pk")?;
    validate_32_bytes(&params.nc_root, "nc_root")?;
    validate_32_bytes(&params.nullifier_imt_root, "nullifier_imt_root")?;
    Ok(())
}

pub fn validate_hotkey(hotkey: &VotingHotkey) -> Result<(), VotingError> {
    validate_32_bytes(&hotkey.secret_key, "hotkey.secret_key")?;
    validate_32_bytes(&hotkey.public_key, "hotkey.public_key")?;
    Ok(())
}

pub fn validate_encrypted_shares(shares: &[EncryptedShare]) -> Result<(), VotingError> {
    for (i, share) in shares.iter().enumerate() {
        validate_32_bytes(&share.c1, &format!("enc_shares[{}].c1", i))?;
        validate_32_bytes(&share.c2, &format!("enc_shares[{}].c2", i))?;
        validate_share_index(share.share_index)?;
    }
    Ok(())
}
