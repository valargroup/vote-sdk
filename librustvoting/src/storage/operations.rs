use crate::storage::{RoundPhase, RoundState, RoundSummary, VoteRecord, VotingDb};
use crate::storage::queries;
use crate::types::{
    DelegationAction, EncryptedShare, NoteInfo, ProofProgressReporter, ProofResult,
    SharePayload, VoteCommitmentBundle, VotingError, VotingHotkey, VotingRoundParams,
};

impl VotingDb {
    // --- Round management ---

    /// Initialize a new voting round. Stores params, sets phase to Initialized.
    pub fn init_round(&self, params: &VotingRoundParams, session_json: Option<&str>) -> Result<(), VotingError> {
        let conn = self.conn();
        queries::insert_round(&conn, params, session_json)
    }

    /// Get the current state of a voting round.
    pub fn get_round_state(&self, round_id: &str) -> Result<RoundState, VotingError> {
        let conn = self.conn();
        queries::get_round_state(&conn, round_id)
    }

    /// List all rounds.
    pub fn list_rounds(&self) -> Result<Vec<RoundSummary>, VotingError> {
        let conn = self.conn();
        queries::list_rounds(&conn)
    }

    /// Get all votes for a round (with choice and submitted status).
    pub fn get_votes(&self, round_id: &str) -> Result<Vec<VoteRecord>, VotingError> {
        let conn = self.conn();
        queries::get_votes(&conn, round_id)
    }

    /// Delete all data for a round.
    pub fn clear_round(&self, round_id: &str) -> Result<(), VotingError> {
        let conn = self.conn();
        queries::clear_round(&conn, round_id)
    }

    // --- Phase 1: Delegation setup ---

    /// Generate a voting hotkey. Returns the hotkey (SDK needs address for Keystone flow).
    /// NOTE: hotkey is NOT stored in the database yet (no hotkeys table).
    /// TODO(pre-production): Store secret key in iOS Keychain, public key in db.
    pub fn generate_hotkey(&self, _round_id: &str) -> Result<VotingHotkey, VotingError> {
        crate::hotkey::generate_hotkey()
    }

    /// Construct the dummy action for Keystone signing.
    /// Loads round params from db. Hotkey + notes come from caller (not stored yet).
    /// Returns DelegationAction (SDK passes to Keystone for signing).
    pub fn construct_delegation_action(
        &self,
        round_id: &str,
        hotkey: &VotingHotkey,
        notes: &[NoteInfo],
    ) -> Result<DelegationAction, VotingError> {
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        crate::action::construct_delegation_action(hotkey, notes, &params)
    }

    /// Cache tree state fetched from lightwalletd by SDK.
    pub fn store_tree_state(&self, round_id: &str, tree_state: &[u8]) -> Result<(), VotingError> {
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        queries::store_tree_state(&conn, round_id, params.snapshot_height, tree_state)
    }

    // --- Phase 2: Delegation proof ---

    /// Build delegation witness from PIR responses. Stores witness in db.
    /// The DelegationAction comes from the caller (not stored in db yet).
    pub fn build_delegation_witness(
        &self,
        round_id: &str,
        action: &DelegationAction,
        inclusion_proofs: &[Vec<u8>],
        exclusion_proofs: &[Vec<u8>],
    ) -> Result<Vec<u8>, VotingError> {
        let witness = crate::zkp1::build_delegation_witness(action, inclusion_proofs, exclusion_proofs)?;
        let conn = self.conn();
        queries::store_witness(&conn, round_id, &witness)?;
        queries::update_round_phase(&conn, round_id, RoundPhase::WitnessBuilt)?;
        Ok(witness)
    }

    /// Generate ZKP #1 (delegation proof). Long-running.
    /// Loads stored witness, generates proof, stores result, reports progress.
    pub fn generate_delegation_proof(
        &self,
        round_id: &str,
        progress: &dyn ProofProgressReporter,
    ) -> Result<ProofResult, VotingError> {
        let witness = {
            let conn = self.conn();
            queries::load_witness(&conn, round_id)?
        };

        let proof = crate::zkp1::generate_delegation_proof(&witness, progress)?;

        let conn = self.conn();
        queries::store_proof(&conn, round_id, &proof)?;
        queries::update_round_phase(&conn, round_id, RoundPhase::DelegationProved)?;
        Ok(proof)
    }

    // --- Phase 3: Voting ---

    /// Encrypt voting shares under ea_pk. Loads ea_pk from round params.
    pub fn encrypt_shares(
        &self,
        round_id: &str,
        shares: &[u64],
    ) -> Result<Vec<EncryptedShare>, VotingError> {
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        crate::elgamal::encrypt_shares(shares, &params.ea_pk)
    }

    /// Build vote commitment + ZKP #2 for a proposal. Stores vote in db.
    /// enc_shares and van_witness come from caller (encrypted shares not stored in db yet).
    pub fn build_vote_commitment(
        &self,
        round_id: &str,
        proposal_id: u32,
        choice: u32,
        enc_shares: &[EncryptedShare],
        van_witness: &[u8],
        progress: &dyn ProofProgressReporter,
    ) -> Result<VoteCommitmentBundle, VotingError> {
        let bundle = crate::zkp2::build_vote_commitment(
            proposal_id,
            choice,
            enc_shares,
            van_witness,
            progress,
        )?;

        let conn = self.conn();
        // Store the vote commitment as serialized bytes
        let commitment_bytes = serde_json::to_vec(&serde_json::json!({
            "van_nullifier": hex::encode(&bundle.van_nullifier),
            "vote_authority_note_new": hex::encode(&bundle.vote_authority_note_new),
            "vote_commitment": hex::encode(&bundle.vote_commitment),
            "proof": hex::encode(&bundle.proof),
        })).map_err(|e| VotingError::Internal {
            message: format!("failed to serialize vote commitment: {}", e),
        })?;

        queries::store_vote(&conn, round_id, proposal_id, choice, &commitment_bytes)?;
        queries::update_round_phase(&conn, round_id, RoundPhase::VoteReady)?;
        Ok(bundle)
    }

    /// Build share payloads for helper server delegation.
    pub fn build_share_payloads(
        &self,
        enc_shares: &[EncryptedShare],
        commitment: &VoteCommitmentBundle,
    ) -> Result<Vec<SharePayload>, VotingError> {
        crate::vote_commitment::build_share_payloads(enc_shares, commitment)
    }

    /// Mark a vote as submitted to the vote chain.
    pub fn mark_vote_submitted(&self, round_id: &str, proposal_id: u32) -> Result<(), VotingError> {
        let conn = self.conn();
        queries::mark_vote_submitted(&conn, round_id, proposal_id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::NoopProgressReporter;

    fn test_db() -> VotingDb {
        VotingDb::open(":memory:").unwrap()
    }

    fn test_params() -> VotingRoundParams {
        VotingRoundParams {
            vote_round_id: "test-round-1".to_string(),
            snapshot_height: 1000,
            ea_pk: vec![0xEA; 32],
            nc_root: vec![0xAA; 32],
            nullifier_imt_root: vec![0xBB; 32],
        }
    }

    #[test]
    fn test_init_and_get_round() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let state = db.get_round_state("test-round-1").unwrap();
        assert_eq!(state.phase, RoundPhase::Initialized);
        assert_eq!(state.snapshot_height, 1000);
    }

    #[test]
    fn test_list_and_clear_rounds() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let rounds = db.list_rounds().unwrap();
        assert_eq!(rounds.len(), 1);

        db.clear_round("test-round-1").unwrap();
        assert!(db.list_rounds().unwrap().is_empty());
    }

    #[test]
    fn test_generate_hotkey() {
        let db = test_db();
        let hotkey = db.generate_hotkey("test-round-1").unwrap();
        assert_eq!(hotkey.secret_key.len(), 32);
        assert_eq!(hotkey.public_key.len(), 32);
    }

    #[test]
    fn test_construct_delegation_action() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let hotkey = db.generate_hotkey("test-round-1").unwrap();
        let note = NoteInfo {
            commitment: vec![0x01; 32],
            nullifier: vec![0x02; 32],
            value: 1_000_000,
            position: 42,
        };

        let action = db.construct_delegation_action("test-round-1", &hotkey, &[note]).unwrap();
        assert_eq!(action.rk.len(), 32);
        assert_eq!(action.sighash.len(), 32);
    }

    #[test]
    fn test_store_and_load_tree_state() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let tree_state = vec![0xCC; 1024];
        db.store_tree_state("test-round-1", &tree_state).unwrap();

        // Verify via direct query
        let conn = db.conn();
        let loaded = queries::load_tree_state(&conn, "test-round-1").unwrap();
        assert_eq!(loaded, tree_state);
    }

    #[test]
    fn test_witness_and_proof_flow() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let action = DelegationAction {
            action_bytes: vec![0xDA; 128],
            rk: vec![0xDE; 32],
            sighash: vec![0x5A; 32],
        };
        let inclusion = vec![vec![0x01; 32]; 4];
        let exclusion = vec![vec![0x02; 32]; 4];

        let witness = db.build_delegation_witness("test-round-1", &action, &inclusion, &exclusion).unwrap();
        assert!(!witness.is_empty());

        let state = db.get_round_state("test-round-1").unwrap();
        assert_eq!(state.phase, RoundPhase::WitnessBuilt);

        let reporter = NoopProgressReporter;
        let proof = db.generate_delegation_proof("test-round-1", &reporter).unwrap();
        assert!(proof.success);

        let state = db.get_round_state("test-round-1").unwrap();
        assert_eq!(state.phase, RoundPhase::DelegationProved);
    }

    #[test]
    fn test_encrypt_shares() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let shares = db.encrypt_shares("test-round-1", &[1, 4]).unwrap();
        assert_eq!(shares.len(), 2);
        assert_eq!(shares[0].plaintext_value, 1);
        assert_eq!(shares[1].plaintext_value, 4);
    }

    #[test]
    fn test_vote_commitment_flow() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let enc_shares = db.encrypt_shares("test-round-1", &[1, 4]).unwrap();
        let van_witness = vec![0xDD; 64];
        let reporter = NoopProgressReporter;

        let bundle = db.build_vote_commitment(
            "test-round-1", 0, 0, &enc_shares, &van_witness, &reporter,
        ).unwrap();
        assert_eq!(bundle.van_nullifier.len(), 32);
        assert_eq!(bundle.proposal_id, 0);

        db.mark_vote_submitted("test-round-1", 0).unwrap();
    }
}
