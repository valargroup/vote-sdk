use subtle::CtOption;
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

/// Unwrap a `CtOption`, returning a `VotingError` on `None`.
pub fn ct_option_to_result<T>(opt: CtOption<T>, msg: &str) -> Result<T, VotingError> {
    if opt.is_some().into() {
        Ok(opt.unwrap())
    } else {
        Err(VotingError::Internal {
            message: msg.to_string(),
        })
    }
}

/// Voting hotkey pair. secret_key must be 32 bytes (Pallas scalar).
#[derive(Clone, Debug)]
pub struct VotingHotkey {
    pub secret_key: Vec<u8>,
    pub public_key: Vec<u8>,
    pub address: String,
}

/// A shielded Orchard note from the wallet DB, containing all fields needed
/// for delegation proof construction and governance PCZT building.
#[derive(Clone, Debug)]
pub struct NoteInfo {
    /// Extracted note commitment (cmx), recomputed from note parts.
    pub commitment: Vec<u8>,
    /// Nullifier (32 bytes).
    pub nullifier: Vec<u8>,
    /// Note value in zatoshis.
    pub value: u64,
    /// Position in the note commitment tree.
    pub position: u64,
    /// Diversifier bytes (11 bytes).
    pub diversifier: Vec<u8>,
    /// Rho field (32 bytes, LE encoding of pallas::Base).
    pub rho: Vec<u8>,
    /// Random seed (32 bytes).
    pub rseed: Vec<u8>,
    /// Key scope: 0 = external, 1 = internal.
    pub scope: u32,
    /// Unified full viewing key string for this note's account.
    pub ufvk_str: String,
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
    /// Signed note nullifier (32 bytes). Public input to ZKP #1.
    pub nf_signed: Vec<u8>,
    /// Output note commitment (32 bytes). Public input to ZKP #1.
    pub cmx_new: Vec<u8>,
    /// Spend auth randomizer scalar (32 bytes). Needed for Keystone signing.
    pub alpha: Vec<u8>,
    /// Spend authorization signature over `sighash` (64 bytes), supplied after Keystone signing.
    pub spend_auth_sig: Option<Vec<u8>>,
    /// Signed note rseed (32 bytes). Needed for witness reconstruction.
    pub rseed_signed: Vec<u8>,
    /// Output note rseed (32 bytes). Needed for witness reconstruction.
    pub rseed_output: Vec<u8>,
}

/// Governance PCZT for Keystone signing.
///
/// Contains a serialized PCZT whose single Orchard action IS the governance
/// dummy action (spend of signed note → output to hotkey). The PCZT's rk and
/// ZIP-244 sighash are internally consistent, so Keystone's SpendAuth signature
/// will verify against them.
#[derive(Clone, Debug)]
pub struct GovernancePczt {
    /// Serialized PCZT bytes ready for UR-encoding and Keystone signing.
    pub pczt_bytes: Vec<u8>,
    /// Randomized verification key (32 bytes). Extracted from the PCZT spend action.
    pub rk: Vec<u8>,
    /// Spend auth randomizer scalar (32 bytes). Needed for ZKP witness.
    pub alpha: Vec<u8>,
    /// Signed note nullifier (32 bytes). Public input to ZKP #1.
    pub nf_signed: Vec<u8>,
    /// Output note commitment (32 bytes). Public input to ZKP #1.
    pub cmx_new: Vec<u8>,
    /// Governance nullifiers, always padded to 4.
    pub gov_nullifiers: Vec<Vec<u8>>,
    /// 32-byte governance commitment (VAN).
    pub van: Vec<u8>,
    /// 32-byte blinding factor used for VAN (must be persisted for later use).
    pub gov_comm_rand: Vec<u8>,
    /// Random nullifiers used for padded dummy notes (needed for circuit witness).
    pub dummy_nullifiers: Vec<Vec<u8>>,
    /// Constrained rho for the signed note (32 bytes). Spec §1.3.4.1.
    pub rho_signed: Vec<u8>,
    /// Extracted note commitments (cmx) for padded dummy notes.
    pub padded_cmx: Vec<Vec<u8>>,
    /// Signed note rseed (32 bytes). Needed for witness reconstruction.
    pub rseed_signed: Vec<u8>,
    /// Output note rseed (32 bytes). Needed for witness reconstruction.
    pub rseed_output: Vec<u8>,
    /// Canonical delegation action payload for cosmos chain submission.
    pub action_bytes: Vec<u8>,
    /// Index of the real governance action within the PCZT's Orchard bundle.
    /// (Actions are padded/shuffled by the Builder.)
    pub action_index: usize,
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


/// JSON deserialization struct for IMT server exclusion proof responses.
#[derive(serde::Deserialize, Clone, Debug)]
pub struct ImtProofJson {
    /// IMT root (hex with 0x prefix).
    pub root: String,
    /// Low bound of the bracketing leaf (hex).
    pub low: String,
    /// High bound of the bracketing leaf (hex).
    pub high: String,
    /// Position of the leaf in the tree.
    pub leaf_pos: u32,
    /// Sibling hashes along the 29-level Merkle path (hex strings).
    pub path: Vec<String>,
}

/// Result of real delegation proof generation (ZKP #1).
#[derive(Clone, Debug)]
pub struct DelegationProofResult {
    /// Halo2 proof bytes.
    pub proof: Vec<u8>,
    /// 12 public input field elements, each as 32-byte LE arrays.
    pub public_inputs: Vec<Vec<u8>>,
    /// Signed note nullifier (32 bytes) — the ZKP's nf_signed (v=0 note).
    pub nf_signed: Vec<u8>,
    /// Output note commitment (32 bytes) — the ZKP's cmx_new (v=0 note).
    pub cmx_new: Vec<u8>,
    /// 4 governance nullifiers (each 32 bytes).
    pub gov_nullifiers: Vec<Vec<u8>>,
    /// Governance commitment / VAN (32 bytes).
    pub gov_comm: Vec<u8>,
    /// Randomized verification key (32 bytes, compressed).
    pub rk: Vec<u8>,
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

pub fn validate_encrypted_shares(shares: &[EncryptedShare]) -> Result<(), VotingError> {
    for (i, share) in shares.iter().enumerate() {
        validate_32_bytes(&share.c1, &format!("enc_shares[{}].c1", i))?;
        validate_32_bytes(&share.c2, &format!("enc_shares[{}].c2", i))?;
        validate_share_index(share.share_index)?;
    }
    Ok(())
}
