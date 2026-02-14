use crate::storage::queries;
use crate::storage::{RoundPhase, RoundState, RoundSummary, VoteRecord, VotingDb};
use crate::types::{
    DelegationAction, EncryptedShare, NoteInfo, ProofProgressReporter, ProofResult, SharePayload,
    VoteCommitmentBundle, VotingError, VotingHotkey, VotingRoundParams, WitnessData,
};

impl VotingDb {
    // --- Round management ---

    /// Initialize a new voting round. Stores params, sets phase to Initialized.
    pub fn init_round(
        &self,
        params: &VotingRoundParams,
        session_json: Option<&str>,
    ) -> Result<(), VotingError> {
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

    // --- Wallet notes ---

    /// Query unspent Orchard notes from the Zcash wallet DB at a snapshot height.
    /// The wallet DB is opened read-only at the given path.
    pub fn get_wallet_notes(
        &self,
        wallet_db_path: &str,
        snapshot_height: u64,
        network_id: u32,
    ) -> Result<Vec<NoteInfo>, VotingError> {
        crate::wallet_notes::get_wallet_notes_at_snapshot(
            wallet_db_path,
            snapshot_height,
            network_id,
        )
    }

    // --- Phase 1: Delegation setup ---

    /// Generate a voting hotkey from seed bytes. Returns the hotkey (SDK needs address for Keystone flow).
    /// The seed comes from a BIP39 mnemonic stored in iOS Keychain.
    pub fn generate_hotkey(
        &self,
        _round_id: &str,
        seed: &[u8],
    ) -> Result<VotingHotkey, VotingError> {
        crate::hotkey::generate_hotkey(seed)
    }

    /// Construct the delegation action for Keystone signing.
    /// Loads round params from db. Notes come from caller (not stored yet).
    /// Computes real governance nullifiers, VAN, signed note, output note, rk, and sighash.
    /// Persists delegation data (gov_comm_rand, dummy_nullifiers, rho_signed, etc.) to DB.
    ///
    /// - `fvk_bytes`: 96-byte orchard FullViewingKey (ak[32] || nk[32] || rivk[32])
    /// - `g_d_new_x`: 32-byte x-coordinate of hotkey diversified generator (for VAN)
    /// - `pk_d_new_x`: 32-byte x-coordinate of hotkey transmission key (for VAN)
    /// - `hotkey_raw_address`: 43-byte hotkey raw orchard address (for output note)
    pub fn construct_delegation_action(
        &self,
        round_id: &str,
        notes: &[NoteInfo],
        fvk_bytes: &[u8],
        g_d_new_x: &[u8],
        pk_d_new_x: &[u8],
        hotkey_raw_address: &[u8],
    ) -> Result<DelegationAction, VotingError> {
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        let action = crate::action::construct_delegation_action(
            notes,
            &params,
            fvk_bytes,
            g_d_new_x,
            pk_d_new_x,
            hotkey_raw_address,
        )?;
        queries::store_delegation_data(
            &conn,
            round_id,
            &action.gov_comm_rand,
            &action.dummy_nullifiers,
            &action.rho_signed,
            &action.padded_cmx,
            &action.nf_signed,
            &action.cmx_new,
            &action.alpha,
            &action.rseed_signed,
            &action.rseed_output,
        )?;
        Ok(action)
    }

    /// Cache tree state fetched from lightwalletd by SDK.
    pub fn store_tree_state(&self, round_id: &str, tree_state: &[u8]) -> Result<(), VotingError> {
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        queries::store_tree_state(&conn, round_id, params.snapshot_height, tree_state)
    }

    /// Generate Merkle inclusion witnesses for notes in a round.
    /// Uses cached tree state + wallet DB shard data to build witnesses.
    /// Results are cached in the witnesses table — subsequent calls return cached data.
    ///
    /// Must be called after store_tree_state and before build_delegation_witness.
    pub fn generate_note_witnesses(
        &self,
        round_id: &str,
        wallet_db_path: &str,
        notes: &[NoteInfo],
    ) -> Result<Vec<WitnessData>, VotingError> {
        let conn = self.conn();

        // Return cached witnesses if available
        if queries::has_witnesses(&conn, round_id)? {
            return queries::load_witnesses(&conn, round_id);
        }

        // Load cached tree state (must have been stored via store_tree_state)
        let tree_state_bytes = queries::load_tree_state(&conn, round_id)?;
        let params = queries::load_round_params(&conn, round_id)?;

        let positions: Vec<u64> = notes.iter().map(|n| n.position).collect();
        let commitments: Vec<Vec<u8>> = notes.iter().map(|n| n.commitment.clone()).collect();

        // Generate witnesses from wallet DB + frontier
        let witnesses = crate::witness::generate_note_witnesses(
            wallet_db_path,
            &positions,
            &commitments,
            params.snapshot_height,
            &tree_state_bytes,
        )?;

        // Verify each witness before caching
        for w in &witnesses {
            let valid = crate::witness::verify_witness(w)?;
            if !valid {
                return Err(VotingError::Internal {
                    message: format!(
                        "witness verification failed for position {} (internal bug)",
                        w.position
                    ),
                });
            }
        }

        // Cache results
        queries::store_witnesses(&conn, round_id, &witnesses)?;

        Ok(witnesses)
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
        let witness =
            crate::zkp1::build_delegation_witness(action, inclusion_proofs, exclusion_proofs)?;
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
        }))
        .map_err(|e| VotingError::Internal {
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

    // 64 hex chars = 32 bytes when decoded. Required because construct_delegation_action
    // hex-decodes vote_round_id and validates it as exactly 32 bytes (a Pallas field element).
    const ROUND_ID: &str = "0101010101010101010101010101010101010101010101010101010101010101";

    fn test_db() -> VotingDb {
        VotingDb::open(":memory:").unwrap()
    }

    fn test_params() -> VotingRoundParams {
        VotingRoundParams {
            vote_round_id: ROUND_ID.to_string(),
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

        let state = db.get_round_state(ROUND_ID).unwrap();
        assert_eq!(state.phase, RoundPhase::Initialized);
        assert_eq!(state.snapshot_height, 1000);
    }

    #[test]
    fn test_list_and_clear_rounds() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let rounds = db.list_rounds().unwrap();
        assert_eq!(rounds.len(), 1);

        db.clear_round(ROUND_ID).unwrap();
        assert!(db.list_rounds().unwrap().is_empty());
    }

    #[test]
    fn test_generate_hotkey() {
        let db = test_db();
        let seed = [0x42_u8; 64];
        let hotkey = db.generate_hotkey(ROUND_ID, &seed).unwrap();
        assert_eq!(hotkey.secret_key.len(), 32);
        assert_eq!(hotkey.public_key.len(), 32);
    }

    #[test]
    fn test_construct_delegation_action() {
        use orchard::keys::{FullViewingKey, SpendingKey};
        use zip32::Scope;

        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let note = NoteInfo {
            commitment: vec![0x01; 32],
            nullifier: vec![0x02; 32],
            value: 1_000_000,
            position: 42,
        };
        // Derive valid FVK and hotkey address from deterministic spending keys
        let sk = SpendingKey::from_bytes([0x42; 32]).expect("valid spending key");
        let fvk = FullViewingKey::from(&sk);
        let fvk_bytes = fvk.to_bytes().to_vec();

        let hotkey_sk = SpendingKey::from_bytes([0x43; 32]).expect("valid spending key");
        let hotkey_fvk = FullViewingKey::from(&hotkey_sk);
        let hotkey_addr = hotkey_fvk.address_at(0u32, Scope::External);
        let hotkey_raw_address = hotkey_addr.to_raw_address_bytes().to_vec();

        let hotkey_addr_43: [u8; 43] = hotkey_raw_address
            .as_slice()
            .try_into()
            .expect("hotkey raw address must be 43 bytes");
        let (g_d_x, pk_d_x) =
            crate::action::derive_hotkey_x_coords_from_raw_address(&hotkey_addr_43)
                .expect("hotkey raw address should decode");
        let g_d = g_d_x.to_vec();
        let pk_d = pk_d_x.to_vec();

        let action = db
            .construct_delegation_action(
                ROUND_ID,
                &[note],
                &fvk_bytes,
                &g_d,
                &pk_d,
                &hotkey_raw_address,
            )
            .unwrap();
        assert_eq!(action.rk.len(), 32);
        assert_ne!(action.rk, vec![0xDE; 32]);
        assert_eq!(action.sighash.len(), 32);
        assert_ne!(action.sighash, vec![0x5A; 32]);
        assert_eq!(action.gov_nullifiers.len(), 4);
        assert_eq!(action.van.len(), 32);
        assert_eq!(action.gov_comm_rand.len(), 32);

        // rho_signed is 32 bytes, non-zero
        assert_eq!(action.rho_signed.len(), 32);
        assert_ne!(action.rho_signed, vec![0u8; 32]);

        // padded_cmx: 3 padded notes (1 real + 3 padded = 4)
        assert_eq!(action.padded_cmx.len(), 3);
        for cmx in &action.padded_cmx {
            assert_eq!(cmx.len(), 32);
        }

        // New fields: nf_signed, cmx_new, alpha
        assert_eq!(action.nf_signed.len(), 32);
        assert_ne!(action.nf_signed, vec![0u8; 32]);
        assert_eq!(action.cmx_new.len(), 32);
        assert_ne!(action.cmx_new, vec![0u8; 32]);
        assert_eq!(action.alpha.len(), 32);
        assert_ne!(action.alpha, vec![0u8; 32]);
        assert_eq!(action.rseed_signed.len(), 32);
        assert_ne!(action.rseed_signed, vec![0u8; 32]);
        assert_eq!(action.rseed_output.len(), 32);
        assert_ne!(action.rseed_output, vec![0u8; 32]);

        // Verify delegation secrets were persisted
        let conn = db.conn();
        let stored_rand = queries::load_gov_comm_rand(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_rand, action.gov_comm_rand);
        let stored_dummies = queries::load_dummy_nullifiers(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_dummies, action.dummy_nullifiers);

        // Verify rho_signed and padded_cmx round-trip through DB
        let stored_rho = queries::load_rho_signed(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_rho, action.rho_signed);
        let stored_padded = queries::load_padded_cmx(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_padded, action.padded_cmx);

        // Verify new fields round-trip through DB
        let stored_nf = queries::load_nf_signed(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_nf, action.nf_signed);
        let stored_cmx = queries::load_cmx_new(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_cmx, action.cmx_new);
        let stored_alpha = queries::load_alpha(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_alpha, action.alpha);
        let stored_rseed_signed = queries::load_rseed_signed(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_rseed_signed, action.rseed_signed);
        let stored_rseed_output = queries::load_rseed_output(&conn, ROUND_ID).unwrap();
        assert_eq!(stored_rseed_output, action.rseed_output);
    }

    #[test]
    fn test_store_and_load_tree_state() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let tree_state = vec![0xCC; 1024];
        db.store_tree_state(ROUND_ID, &tree_state).unwrap();

        let conn = db.conn();
        let loaded = queries::load_tree_state(&conn, ROUND_ID).unwrap();
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
            gov_nullifiers: vec![vec![0x01; 32]; 4],
            van: vec![0x02; 32],
            gov_comm_rand: vec![0x03; 32],
            dummy_nullifiers: vec![],
            rho_signed: vec![0x04; 32],
            padded_cmx: vec![],
            nf_signed: vec![0x05; 32],
            cmx_new: vec![0x06; 32],
            alpha: vec![0x07; 32],
            rseed_signed: vec![0x08; 32],
            rseed_output: vec![0x09; 32],
        };
        let inclusion = vec![vec![0x01; 32]; 4];
        let exclusion = vec![vec![0x02; 32]; 4];

        let witness = db
            .build_delegation_witness(ROUND_ID, &action, &inclusion, &exclusion)
            .unwrap();
        assert!(!witness.is_empty());

        let state = db.get_round_state(ROUND_ID).unwrap();
        assert_eq!(state.phase, RoundPhase::WitnessBuilt);

        let reporter = NoopProgressReporter;
        let proof = db.generate_delegation_proof(ROUND_ID, &reporter).unwrap();
        assert!(proof.success);

        let state = db.get_round_state(ROUND_ID).unwrap();
        assert_eq!(state.phase, RoundPhase::DelegationProved);
    }

    #[test]
    fn test_encrypt_shares() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let shares = db.encrypt_shares(ROUND_ID, &[1, 4]).unwrap();
        assert_eq!(shares.len(), 2);
        assert_eq!(shares[0].plaintext_value, 1);
        assert_eq!(shares[1].plaintext_value, 4);
    }

    #[test]
    fn test_vote_commitment_flow() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let enc_shares = db.encrypt_shares(ROUND_ID, &[1, 4]).unwrap();
        let van_witness = vec![0xDD; 64];
        let reporter = NoopProgressReporter;

        let bundle = db
            .build_vote_commitment(ROUND_ID, 0, 0, &enc_shares, &van_witness, &reporter)
            .unwrap();
        assert_eq!(bundle.van_nullifier.len(), 32);
        assert_eq!(bundle.proposal_id, 0);

        db.mark_vote_submitted(ROUND_ID, 0).unwrap();
    }
}
