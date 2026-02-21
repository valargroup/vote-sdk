use std::collections::HashMap;

use ff::PrimeField;

use crate::storage::queries;
use crate::storage::{RoundPhase, RoundState, RoundSummary, VoteRecord, VotingDb};
use crate::types::{
    DelegationAction, DelegationProofResult, DelegationSubmissionData, EncryptedShare,
    GovernancePczt, NoteInfo, ProofProgressReporter, SharePayload, VoteCommitmentBundle,
    VotingError, VotingHotkey, VotingRoundParams, WitnessData,
};

/// Append exactly 32 bytes to `out` from `b` (pad with zeros if shorter).
fn extend_padded32(out: &mut Vec<u8>, b: &[u8]) {
    let mut buf = [0u8; 32];
    let n = b.len().min(32);
    buf[..n].copy_from_slice(&b[..n]);
    out.extend_from_slice(&buf);
}

/// Append exactly 64 bytes to `out` from `b` (pad with zeros if shorter).
fn extend_padded64(out: &mut Vec<u8>, b: &[u8]) {
    let mut buf = [0u8; 64];
    let n = b.len().min(64);
    buf[..n].copy_from_slice(&b[..n]);
    out.extend_from_slice(&buf);
}

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

    /// Get all votes for a round (with choice, bundle_index, and submitted status).
    pub fn get_votes(&self, round_id: &str) -> Result<Vec<VoteRecord>, VotingError> {
        let conn = self.conn();
        queries::get_votes(&conn, round_id)
    }

    /// Delete all data for a round.
    pub fn clear_round(&self, round_id: &str) -> Result<(), VotingError> {
        let conn = self.conn();
        queries::clear_round(&conn, round_id)
    }

    // --- Bundles ---

    /// Split notes into value-aware bundles of up to 4 and insert bundle rows.
    /// Returns (bundle_count, eligible_weight) — only bundles meeting the BALLOT_DIVISOR
    /// threshold are created. Notes in sub-threshold bundles are dropped.
    pub fn setup_bundles(&self, round_id: &str, notes: &[NoteInfo]) -> Result<(u32, u64), VotingError> {
        let conn = self.conn();
        let result = crate::types::chunk_notes(notes);
        if result.dropped_count > 0 {
            eprintln!(
                "[setup_bundles] Dropped {} notes in sub-threshold bundles (eligible: {} of {} notes)",
                result.dropped_count,
                notes.len() - result.dropped_count,
                notes.len()
            );
        }
        for (i, chunk) in result.bundles.iter().enumerate() {
            let positions: Vec<u64> = chunk.iter().map(|n| n.position).collect();
            queries::insert_bundle(&conn, round_id, i as u32, &positions)?;
        }
        Ok((result.bundles.len() as u32, result.eligible_weight))
    }

    /// Get the number of bundles for a round.
    pub fn get_bundle_count(&self, round_id: &str) -> Result<u32, VotingError> {
        let conn = self.conn();
        queries::get_bundle_count(&conn, round_id)
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
    /// Computes real governance nullifiers, VAN, signed note, output note, and rk.
    /// Persists delegation data (van_comm_rand, dummy_nullifiers, rho_signed, etc.) to DB.
    ///
    /// - `fvk_bytes`: 96-byte orchard FullViewingKey (ak[32] || nk[32] || rivk[32])
    /// - `g_d_new_x`: 32-byte x-coordinate of hotkey diversified generator (for VAN)
    /// - `pk_d_new_x`: 32-byte x-coordinate of hotkey transmission key (for VAN)
    /// - `hotkey_raw_address`: 43-byte hotkey raw orchard address (for output note)
    pub fn construct_delegation_action(
        &self,
        round_id: &str,
        bundle_index: u32,
        notes: &[NoteInfo],
        fvk_bytes: &[u8],
        g_d_new_x: &[u8],
        pk_d_new_x: &[u8],
        hotkey_raw_address: &[u8],
        address_index: u32,
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
        // Compute total note value from input notes
        let total_note_value: u64 = notes
            .iter()
            .try_fold(0u64, |acc, n| acc.checked_add(n.value))
            .ok_or_else(|| VotingError::InvalidInput {
                message: "total note weight overflows u64".to_string(),
            })?;
        queries::store_delegation_data(
            &conn,
            round_id,
            bundle_index,
            &action.van_comm_rand,
            &action.dummy_nullifiers,
            &action.rho_signed,
            &action.padded_cmx,
            &action.nf_signed,
            &action.cmx_new,
            &action.alpha,
            &action.rseed_signed,
            &action.rseed_output,
            &action.van,
            total_note_value,
            address_index,
        )?;
        Ok(action)
    }

    /// Build a governance-specific PCZT for Keystone signing.
    /// Loads round params from db. Notes come from caller.
    /// Computes governance values and builds a PCZT whose single Orchard action
    /// IS the governance dummy action (spend of signed note → output to hotkey).
    ///
    /// - `fvk_bytes`: 96-byte orchard FullViewingKey (ak[32] || nk[32] || rivk[32])
    /// - `hotkey_raw_address`: 43-byte hotkey raw orchard address (for output note)
    /// - `consensus_branch_id`: NU6 = 0xC8E71055
    /// - `coin_type`: 133 (mainnet) or 1 (testnet)
    /// - `seed_fingerprint`: 32-byte ZIP-32 seed fingerprint for Keystone signing
    /// - `account_index`: ZIP-32 account index (typically 0)
    pub fn build_governance_pczt(
        &self,
        round_id: &str,
        bundle_index: u32,
        notes: &[NoteInfo],
        fvk_bytes: &[u8],
        hotkey_raw_address: &[u8],
        consensus_branch_id: u32,
        coin_type: u32,
        seed_fingerprint: &[u8; 32],
        account_index: u32,
        round_name: &str,
        address_index: u32,
    ) -> Result<GovernancePczt, VotingError> {
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        let result = crate::action::build_governance_pczt(
            notes,
            &params,
            fvk_bytes,
            hotkey_raw_address,
            consensus_branch_id,
            coin_type,
            seed_fingerprint,
            account_index,
            round_name,
        )?;
        // Compute total note value from input notes
        let total_note_value: u64 = notes
            .iter()
            .try_fold(0u64, |acc, n| acc.checked_add(n.value))
            .ok_or_else(|| VotingError::InvalidInput {
                message: "total note weight overflows u64".to_string(),
            })?;
        // Persist the same delegation data fields as construct_delegation_action
        queries::store_delegation_data(
            &conn,
            round_id,
            bundle_index,
            &result.van_comm_rand,
            &result.dummy_nullifiers,
            &result.rho_signed,
            &result.padded_cmx,
            &result.nf_signed,
            &result.cmx_new,
            &result.alpha,
            &result.rseed_signed,
            &result.rseed_output,
            &result.van,
            total_note_value,
            address_index,
        )?;
        Ok(result)
    }

    /// Cache tree state fetched from lightwalletd by SDK.
    pub fn store_tree_state(&self, round_id: &str, tree_state: &[u8]) -> Result<(), VotingError> {
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        queries::store_tree_state(&conn, round_id, params.snapshot_height, tree_state)
    }

    /// Generate Merkle inclusion witnesses for notes in a bundle.
    /// Uses cached tree state + wallet DB shard data to build witnesses.
    /// Results are cached in the witnesses table — subsequent calls return cached data.
    ///
    /// Must be called after store_tree_state and before build_and_prove_delegation.
    pub fn generate_note_witnesses(
        &self,
        round_id: &str,
        bundle_index: u32,
        wallet_db_path: &str,
        notes: &[NoteInfo],
    ) -> Result<Vec<WitnessData>, VotingError> {
        let conn = self.conn();

        // Return cached witnesses if available
        if queries::has_witnesses(&conn, round_id, bundle_index)? {
            return queries::load_witnesses(&conn, round_id, bundle_index);
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
        queries::store_witnesses(&conn, round_id, bundle_index, &witnesses)?;

        Ok(witnesses)
    }

    // --- Phase 2: Delegation proof ---

    /// Build and prove the real delegation ZKP (#1). Long-running.
    ///
    /// Loads all required data from the voting DB and wallet DB:
    /// - alpha, van_comm_rand from delegation data (stored by `build_governance_pczt`)
    /// - Merkle witnesses (stored by `generate_note_witnesses`)
    /// - Full note data (queried from wallet DB, filtered to this bundle's positions)
    /// - Vote round params (stored by `init_round`)
    ///
    /// Fetches IMT exclusion proofs from the IMT server for each note's nullifier.
    /// For padded notes (< 4 real notes), the prover fetches proofs internally.
    ///
    /// Stores the proof result and advances phase to `DelegationProved`.
    pub fn build_and_prove_delegation(
        &self,
        round_id: &str,
        bundle_index: u32,
        wallet_db_path: &str,
        hotkey_raw_address: &[u8],
        imt_server_url: &str,
        network_id: u32,
        progress: &dyn ProofProgressReporter,
    ) -> Result<DelegationProofResult, VotingError> {
        let total_start = std::time::Instant::now();

        // Phase 1: DB queries
        let db_start = std::time::Instant::now();
        let conn = self.conn();
        let params = queries::load_round_params(&conn, round_id)?;
        let alpha = queries::load_alpha(&conn, round_id, bundle_index)?;
        let van_comm_rand = queries::load_van_comm_rand(&conn, round_id, bundle_index)?;
        let witnesses = queries::load_witnesses(&conn, round_id, bundle_index)?;

        // Load note positions for this bundle, then fetch all wallet notes and filter
        let bundle_positions: std::collections::HashSet<u64> =
            queries::load_bundle_note_positions(&conn, round_id, bundle_index)?
                .into_iter()
                .collect();
        let all_notes = crate::wallet_notes::get_wallet_notes_at_snapshot(
            wallet_db_path,
            params.snapshot_height,
            network_id,
        )?;
        let full_notes: Vec<NoteInfo> = all_notes
            .into_iter()
            .filter(|n| bundle_positions.contains(&n.position))
            .collect();
        // Witnesses are persisted keyed by note position and loaded sorted by position.
        // Proof generation, however, consumes notes in `full_notes` order, so we must
        // re-align witnesses by note commitment to avoid mismatched note/path pairs.
        let witness_count = witnesses.len();
        if witness_count != full_notes.len() {
            return Err(VotingError::Internal {
                message: format!(
                    "witness count ({}) does not match note count ({}) for round {}",
                    witness_count,
                    full_notes.len(),
                    round_id
                ),
            });
        }

        let mut witnesses_by_commitment: HashMap<Vec<u8>, WitnessData> =
            HashMap::with_capacity(witness_count);
        for w in witnesses {
            if witnesses_by_commitment
                .insert(w.note_commitment.clone(), w)
                .is_some()
            {
                return Err(VotingError::Internal {
                    message: "duplicate witness note_commitment in cache".to_string(),
                });
            }
        }

        let mut ordered_witnesses = Vec::with_capacity(full_notes.len());
        for (i, n) in full_notes.iter().enumerate() {
            let w = witnesses_by_commitment
                .remove(&n.commitment)
                .ok_or_else(|| VotingError::Internal {
                    message: format!(
                        "missing witness for note[{i}] commitment {}",
                        hex::encode(&n.commitment)
                    ),
                })?;
            ordered_witnesses.push(w);
        }
        if !witnesses_by_commitment.is_empty() {
            return Err(VotingError::Internal {
                message: "extra cached witnesses not matched to selected notes".to_string(),
            });
        }

        let db_elapsed = db_start.elapsed();
        eprintln!(
            "[ZKP1] DB queries: {:.2}s ({} notes, {} witnesses)",
            db_elapsed.as_secs_f64(),
            full_notes.len(),
            witness_count
        );

        // Phase 2: Fetch IMT exclusion proofs
        let imt_start = std::time::Instant::now();
        eprintln!(
            "[ZKP1] Fetching IMT exclusion proofs for {} notes from {}",
            full_notes.len(),
            imt_server_url
        );
        let mut imt_proofs = Vec::new();
        for (i, note) in full_notes.iter().enumerate() {
            let hex_nf = hex::encode(&note.nullifier);
            let url = format!("{}/exclusion-proof/{}", imt_server_url, hex_nf);
            let note_start = std::time::Instant::now();
            let resp = reqwest::blocking::get(&url).map_err(|e| VotingError::Internal {
                message: format!("IMT server request to {url} failed: {e}"),
            })?;
            if !resp.status().is_success() {
                return Err(VotingError::Internal {
                    message: format!(
                        "IMT server returned {} for nullifier {}",
                        resp.status(),
                        hex_nf
                    ),
                });
            }
            let body = resp.bytes().map_err(|e| VotingError::Internal {
                message: format!("failed to read IMT response body: {e}"),
            })?;
            eprintln!(
                "[ZKP1] Note {}: IMT proof {} bytes in {:.2}s",
                i,
                body.len(),
                note_start.elapsed().as_secs_f64()
            );
            imt_proofs.push(body.to_vec());
        }
        let imt_elapsed = imt_start.elapsed();
        eprintln!(
            "[ZKP1] IMT fetch total: {:.2}s for {} proofs",
            imt_elapsed.as_secs_f64(),
            imt_proofs.len()
        );

        // Phase 3: Proof generation
        let prove_start = std::time::Instant::now();
        eprintln!("[ZKP1] Starting proof generation...");

        // Parse vote_round_id from hex string to 32-byte field element
        let vote_round_id_bytes =
            hex::decode(&params.vote_round_id).map_err(|e| VotingError::Internal {
                message: format!("invalid vote_round_id hex '{}': {e}", params.vote_round_id),
            })?;

        let result = crate::zkp1::build_and_prove_delegation(
            &full_notes,
            hotkey_raw_address,
            &alpha,
            &van_comm_rand,
            &vote_round_id_bytes,
            &ordered_witnesses,
            &imt_proofs,
            imt_server_url,
            network_id,
            progress,
        )?;
        let prove_elapsed = prove_start.elapsed();
        eprintln!(
            "[ZKP1] Proof generation: {:.2}s",
            prove_elapsed.as_secs_f64()
        );

        // Store proof bytes for debugging/recovery
        queries::store_proof(&conn, round_id, bundle_index, &result.proof)?;
        // Persist prover's public inputs — needed later for delegation TX submission.
        // Overwrites nf_signed/cmx_new from constructDelegationAction since the prover
        // generates its own random rseeds for the signed/output notes.
        queries::store_proof_result_fields(
            &conn,
            round_id,
            bundle_index,
            &result.rk,
            &result.gov_nullifiers,
            &result.nf_signed,
            &result.cmx_new,
        )?;
        queries::update_round_phase(&conn, round_id, RoundPhase::DelegationProved)?;

        let total_elapsed = total_start.elapsed();
        eprintln!(
            "[ZKP1] TOTAL: {:.2}s (DB: {:.2}s, IMT: {:.2}s, Prove: {:.2}s) — proof {} bytes",
            total_elapsed.as_secs_f64(),
            db_elapsed.as_secs_f64(),
            imt_elapsed.as_secs_f64(),
            prove_elapsed.as_secs_f64(),
            result.proof.len(),
        );

        Ok(result)
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
    ///
    /// Loads ZKP #2 inputs (gov_comm_rand, total_note_value, address_index, ea_pk,
    /// voting_round_id) from the DB, derives the SpendingKey from hotkey_seed,
    /// and generates a real Halo2 vote proof.
    ///
    /// The builder handles share decomposition and El Gamal encryption internally.
    /// The returned bundle includes the encrypted shares for reveal-share payloads.
    pub fn build_vote_commitment(
        &self,
        round_id: &str,
        bundle_index: u32,
        hotkey_seed: &[u8],
        network_id: u32,
        proposal_id: u32,
        choice: u32,
        num_options: u32,
        van_auth_path: &[[u8; 32]],
        van_position: u32,
        anchor_height: u32,
        progress: &dyn ProofProgressReporter,
    ) -> Result<VoteCommitmentBundle, VotingError> {
        let conn = self.conn();
        let zkp2_data = queries::load_zkp2_inputs(&conn, round_id, bundle_index)?;

        // Decode voting_round_id from hex string to 32 bytes
        let voting_round_id_bytes =
            hex::decode(&zkp2_data.voting_round_id).map_err(|e| VotingError::Internal {
                message: format!(
                    "invalid voting_round_id hex '{}': {e}",
                    zkp2_data.voting_round_id
                ),
            })?;

        let bundle = crate::zkp2::build_vote_commitment(
            hotkey_seed,
            network_id,
            zkp2_data.address_index,
            zkp2_data.total_note_value,
            &zkp2_data.gov_comm_rand,
            &voting_round_id_bytes,
            &zkp2_data.ea_pk,
            proposal_id,
            choice,
            num_options,
            van_auth_path,
            van_position,
            anchor_height,
            zkp2_data.proposal_authority,
            progress,
        )?;

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

        queries::store_vote(
            &conn,
            round_id,
            bundle_index,
            proposal_id,
            choice,
            &commitment_bytes,
        )?;
        queries::update_round_phase(&conn, round_id, RoundPhase::VoteReady)?;
        Ok(bundle)
    }

    /// Build share payloads for helper server delegation.
    ///
    /// - `vote_decision`: The voter's choice (0-indexed into the proposal's options).
    /// - `num_options`: Number of options declared for this proposal (2-8).
    /// - `vc_tree_position`: Position of the Vote Commitment leaf in the VC tree,
    ///   known after the cast-vote TX is confirmed on chain.
    pub fn build_share_payloads(
        &self,
        enc_shares: &[EncryptedShare],
        commitment: &VoteCommitmentBundle,
        vote_decision: u32,
        num_options: u32,
        vc_tree_position: u64,
    ) -> Result<Vec<SharePayload>, VotingError> {
        crate::vote_commitment::build_share_payloads(
            enc_shares,
            commitment,
            vote_decision,
            num_options,
            vc_tree_position,
        )
    }

    /// Store the VAN leaf position after delegation TX is confirmed on chain.
    /// The app calls this after parsing the delegation TX response events.
    pub fn store_van_position(
        &self,
        round_id: &str,
        bundle_index: u32,
        position: u32,
    ) -> Result<(), VotingError> {
        let conn = self.conn();
        queries::store_van_position(&conn, round_id, bundle_index, position)
    }

    /// Load the VAN leaf position for a bundle.
    pub fn load_van_position(&self, round_id: &str, bundle_index: u32) -> Result<u32, VotingError> {
        let conn = self.conn();
        queries::load_van_position(&conn, round_id, bundle_index)
    }

    /// Reconstruct the full chain-ready delegation TX payload from DB + seed.
    ///
    /// After `build_and_prove_delegation` completes, all proof artifacts (proof, rk,
    /// gov_nullifiers, nf_signed, cmx_new, gov_comm, alpha) are persisted in the DB.
    /// This method loads them, derives the sender's SpendingKey from seed, computes the
    /// canonical sighash, signs it, and returns everything the chain needs.
    pub fn get_delegation_submission(
        &self,
        round_id: &str,
        bundle_index: u32,
        sender_seed: &[u8],
        network_id: u32,
        _account_index: u32,
    ) -> Result<DelegationSubmissionData, VotingError> {
        let conn = self.conn();
        let data = queries::load_delegation_submission_data(&conn, round_id, bundle_index)?;
        drop(conn);

        // Derive sender SpendingKey from seed via ZIP-32 (same as delegation)
        let sk = crate::zkp2::derive_spending_key(sender_seed, network_id)?;
        let ask = orchard::keys::SpendAuthorizingKey::from(&sk);

        // Deserialize alpha
        let alpha_arr: [u8; 32] =
            data.alpha
                .as_slice()
                .try_into()
                .map_err(|_| VotingError::Internal {
                    message: format!("alpha must be 32 bytes, got {}", data.alpha.len()),
                })?;
        let alpha: pasta_curves::pallas::Scalar =
            Option::from(pasta_curves::pallas::Scalar::from_repr(alpha_arr)).ok_or_else(|| {
                VotingError::Internal {
                    message: "alpha is not a valid Pallas scalar".to_string(),
                }
            })?;

        // Compute rsk = ask.randomize(alpha)
        let rsk = ask.randomize(&alpha);

        // Decode vote_round_id from hex string to bytes
        let vote_round_id_bytes =
            hex::decode(&data.vote_round_id).map_err(|e| VotingError::Internal {
                message: format!("invalid vote_round_id hex: {e}"),
            })?;

        // enc_memo = [0x05; 64] (mock, matches e2e test and chain expectations)
        let enc_memo = [0x05u8; 64];

        // Canonical sighash: Blake2b-256(domain || vote_round_id || rk || nf_signed || cmx_new || enc_memo || gov_comm || gov_nullifiers)
        // Must match Go's ComputeDelegationSighash.
        const SIGHASH_DOMAIN: &[u8] = b"ZALLY_DELEGATION_SIGHASH_V0";
        let mut canonical =
            Vec::with_capacity(SIGHASH_DOMAIN.len() + 32 + 32 + 32 + 32 + 64 + 32 + 4 * 32);
        canonical.extend_from_slice(SIGHASH_DOMAIN);
        extend_padded32(&mut canonical, &vote_round_id_bytes);
        canonical.extend_from_slice(&data.rk);
        extend_padded32(&mut canonical, &data.nf_signed);
        canonical.extend_from_slice(&data.cmx_new);
        extend_padded64(&mut canonical, &enc_memo);
        extend_padded32(&mut canonical, &data.gov_comm);
        for i in 0..4 {
            if i < data.gov_nullifiers.len() {
                canonical.extend_from_slice(&data.gov_nullifiers[i]);
            } else {
                canonical.extend_from_slice(&[0u8; 32]);
            }
        }
        let sighash_full = blake2b_simd::Params::new().hash_length(32).hash(&canonical);
        let mut sighash = [0u8; 32];
        sighash.copy_from_slice(sighash_full.as_bytes());

        // Sign
        let mut rng = rand::rngs::OsRng;
        let sig = rsk.sign(&mut rng, &sighash);
        let sig_bytes: [u8; 64] = (&sig).into();

        Ok(DelegationSubmissionData {
            proof: data.proof,
            rk: data.rk,
            nf_signed: data.nf_signed,
            cmx_new: data.cmx_new,
            gov_comm: data.gov_comm,
            gov_nullifiers: data.gov_nullifiers,
            alpha: data.alpha,
            vote_round_id: data.vote_round_id,
            spend_auth_sig: sig_bytes.to_vec(),
            sighash: sighash.to_vec(),
            enc_memo: enc_memo.to_vec(),
        })
    }

    /// Mark a vote as submitted to the vote chain.
    pub fn mark_vote_submitted(
        &self,
        round_id: &str,
        bundle_index: u32,
        proposal_id: u32,
    ) -> Result<(), VotingError> {
        let conn = self.conn();
        queries::mark_vote_submitted(&conn, round_id, bundle_index, proposal_id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // 64 hex chars = 32 bytes when decoded. Required because construct_delegation_action
    // hex-decodes vote_round_id and validates it as exactly 32 bytes (a Pallas field element).
    const ROUND_ID: &str = "0101010101010101010101010101010101010101010101010101010101010101";

    fn test_db() -> VotingDb {
        VotingDb::open(":memory:").unwrap()
    }

    fn test_params() -> VotingRoundParams {
        // Use SpendAuthG as a valid Pallas point for ea_pk in tests.
        use group::GroupEncoding;
        let ea_pk = pasta_curves::pallas::Point::from(orchard::vote_proof::spend_auth_g_affine());
        VotingRoundParams {
            vote_round_id: ROUND_ID.to_string(),
            snapshot_height: 1000,
            ea_pk: ea_pk.to_bytes().to_vec(),
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
    fn test_setup_bundles() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        // 5 notes each 13M — sequential fill: 4+1, both above threshold
        let notes: Vec<NoteInfo> = (0..5)
            .map(|i| NoteInfo {
                commitment: vec![0x01; 32],
                nullifier: vec![0x02; 32],
                value: 13_000_000,
                position: i as u64,
                diversifier: vec![0; 11],
                rho: vec![0; 32],
                rseed: vec![0; 32],
                scope: 0,
                ufvk_str: String::new(),
            })
            .collect();

        let (count, eligible) = db.setup_bundles(ROUND_ID, &notes).unwrap();
        assert_eq!(count, 2);
        // Quantized: bundle 0 (39M → 37.5M) + bundle 1 (26M → 25M) = 62.5M
        assert_eq!(eligible, 62_500_000);
        assert_eq!(db.get_bundle_count(ROUND_ID).unwrap(), 2);
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
            value: 15_000_000,
            position: 42,
            diversifier: vec![0; 11],
            rho: vec![0; 32],
            rseed: vec![0; 32],
            scope: 0,
            ufvk_str: String::new(),
        };

        // Setup bundle first (single note above threshold → 1 bundle)
        db.setup_bundles(ROUND_ID, &[note.clone()]).unwrap();

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
                0, // bundle_index
                &[note],
                &fvk_bytes,
                &g_d,
                &pk_d,
                &hotkey_raw_address,
                0u32, // address_index
            )
            .unwrap();
        assert_eq!(action.rk.len(), 32);
        assert_ne!(action.rk, vec![0xDE; 32]);
        assert_eq!(action.gov_nullifiers.len(), 4);
        assert_eq!(action.van.len(), 32);
        assert_eq!(action.van_comm_rand.len(), 32);

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

        // Verify delegation secrets were persisted in bundles table
        let conn = db.conn();
        let stored_rand = queries::load_van_comm_rand(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_rand, action.van_comm_rand);
        let stored_dummies = queries::load_dummy_nullifiers(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_dummies, action.dummy_nullifiers);

        // Verify rho_signed and padded_cmx round-trip through DB
        let stored_rho = queries::load_rho_signed(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_rho, action.rho_signed);
        let stored_padded = queries::load_padded_cmx(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_padded, action.padded_cmx);

        // Verify new fields round-trip through DB
        let stored_nf = queries::load_nf_signed(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_nf, action.nf_signed);
        let stored_cmx = queries::load_cmx_new(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_cmx, action.cmx_new);
        let stored_alpha = queries::load_alpha(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_alpha, action.alpha);
        let stored_rseed_signed = queries::load_rseed_signed(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(stored_rseed_signed, action.rseed_signed);
        let stored_rseed_output = queries::load_rseed_output(&conn, ROUND_ID, 0).unwrap();
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
    fn test_encrypt_shares() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let shares = db.encrypt_shares(ROUND_ID, &[1, 4]).unwrap();
        assert_eq!(shares.len(), 2);
        assert_eq!(shares[0].plaintext_value, 1);
        assert_eq!(shares[1].plaintext_value, 4);
    }

    #[test]
    fn test_zkp2_inputs_round_trip() {
        // Verify that delegation data persisted for ZKP #2 can be loaded back.
        // The real vote proof generation is too expensive for a unit test (~30-60s);
        // the e2e test exercises the full path.
        use orchard::keys::{FullViewingKey, SpendingKey};
        use zip32::Scope;

        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        let note = NoteInfo {
            commitment: vec![0x01; 32],
            nullifier: vec![0x02; 32],
            value: 13_000_000, // must be >= 12_500_000 (BALLOT_DIVISOR) to produce at least 1 ballot
            position: 42,
            diversifier: vec![0; 11],
            rho: vec![0; 32],
            rseed: vec![0; 32],
            scope: 0,
            ufvk_str: String::new(),
        };

        // Setup bundle first (single note above threshold)
        db.setup_bundles(ROUND_ID, &[note.clone()]).unwrap();

        let sk = SpendingKey::from_bytes([0x42; 32]).expect("valid spending key");
        let fvk = FullViewingKey::from(&sk);
        let fvk_bytes = fvk.to_bytes().to_vec();
        let hotkey_sk = SpendingKey::from_bytes([0x43; 32]).expect("valid spending key");
        let hotkey_fvk = FullViewingKey::from(&hotkey_sk);
        let hotkey_addr = hotkey_fvk.address_at(0u32, Scope::External);
        let hotkey_raw_address = hotkey_addr.to_raw_address_bytes().to_vec();
        let hotkey_addr_43: [u8; 43] = hotkey_raw_address.as_slice().try_into().unwrap();
        let (g_d_x, pk_d_x) =
            crate::action::derive_hotkey_x_coords_from_raw_address(&hotkey_addr_43).unwrap();

        db.construct_delegation_action(
            ROUND_ID,
            0, // bundle_index
            &[note],
            &fvk_bytes,
            &g_d_x.to_vec(),
            &pk_d_x.to_vec(),
            &hotkey_raw_address,
            0u32,
        )
        .unwrap();

        // Verify ZKP2 inputs can be loaded
        {
            let conn = db.conn();
            let zkp2 = queries::load_zkp2_inputs(&conn, ROUND_ID, 0).unwrap();
            assert_eq!(zkp2.total_note_value, 13_000_000);
            assert_eq!(zkp2.address_index, 0);
            assert_eq!(zkp2.gov_comm_rand.len(), 32);
            assert_eq!(zkp2.ea_pk.len(), 32);
            assert_eq!(zkp2.voting_round_id, ROUND_ID);
        }

        // Verify VAN position can be stored and loaded
        db.store_van_position(ROUND_ID, 0, 42).unwrap();
        let pos = queries::load_van_position(&db.conn(), ROUND_ID, 0).unwrap();
        assert_eq!(pos, 42);
    }

    #[test]
    fn test_mark_vote_submitted() {
        let db = test_db();
        db.init_round(&test_params(), None).unwrap();
        db.setup_bundles(
            ROUND_ID,
            &[NoteInfo {
                commitment: vec![0x01; 32],
                nullifier: vec![0x02; 32],
                value: 13_000_000,
                position: 0,
                diversifier: vec![0; 11],
                rho: vec![0; 32],
                rseed: vec![0; 32],
                scope: 0,
                ufvk_str: String::new(),
            }],
        )
        .unwrap();

        queries::store_vote(&db.conn(), ROUND_ID, 0, 0, 0, &[0xAA; 32]).unwrap();
        db.mark_vote_submitted(ROUND_ID, 0, 0).unwrap();
    }

    /// Multi-bundle test: 5 notes → 2 bundles, independent delegation + vote storage per bundle.
    #[test]
    fn test_multi_bundle_delegation_and_voting() {
        use orchard::keys::{FullViewingKey, SpendingKey};
        use zip32::Scope;

        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        // Create 5 notes with distinct positions and unique nullifiers
        let notes: Vec<NoteInfo> = (0..5)
            .map(|i| NoteInfo {
                commitment: vec![0x01; 32],
                nullifier: {
                    let mut nf = vec![0u8; 32];
                    nf[0] = i as u8;
                    nf
                },
                value: 13_000_000,
                position: i as u64,
                diversifier: vec![0; 11],
                rho: vec![0; 32],
                rseed: vec![0; 32],
                scope: 0,
                ufvk_str: String::new(),
            })
            .collect();

        // Setup bundles: 5 equal-value notes → sequential fill packs first 4, then 1
        // Sorted by value DESC (all equal) then position ASC: [0,1,2,3,4]
        // Bundle 0 = [0,1,2,3], bundle 1 = [4]
        let (bundle_count, eligible) = db.setup_bundles(ROUND_ID, &notes).unwrap();
        assert_eq!(bundle_count, 2);
        // Quantized: bundle 0 (52M → 4×12.5M=50M) + bundle 1 (13M → 1×12.5M=12.5M) = 62.5M
        assert_eq!(eligible, 62_500_000);
        assert_eq!(db.get_bundle_count(ROUND_ID).unwrap(), 2);

        // Verify note positions per bundle (sequential fill)
        let conn = db.conn();
        let positions_0 = queries::load_bundle_note_positions(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(positions_0, vec![0, 1, 2, 3]);
        let positions_1 = queries::load_bundle_note_positions(&conn, ROUND_ID, 1).unwrap();
        assert_eq!(positions_1, vec![4]);
        drop(conn);

        // Derive keys for construct_delegation_action
        let sk = SpendingKey::from_bytes([0x42; 32]).expect("valid spending key");
        let fvk = FullViewingKey::from(&sk);
        let fvk_bytes = fvk.to_bytes().to_vec();
        let hotkey_sk = SpendingKey::from_bytes([0x43; 32]).expect("valid spending key");
        let hotkey_fvk = FullViewingKey::from(&hotkey_sk);
        let hotkey_addr = hotkey_fvk.address_at(0u32, Scope::External);
        let hotkey_raw_address = hotkey_addr.to_raw_address_bytes().to_vec();
        let hotkey_addr_43: [u8; 43] = hotkey_raw_address.as_slice().try_into().unwrap();
        let (g_d_x, pk_d_x) =
            crate::action::derive_hotkey_x_coords_from_raw_address(&hotkey_addr_43).unwrap();

        // Construct delegation for each bundle independently
        let chunk_result = crate::types::chunk_notes(&notes);

        for (i, chunk) in chunk_result.bundles.iter().enumerate() {
            let action = db
                .construct_delegation_action(
                    ROUND_ID,
                    i as u32,
                    chunk,
                    &fvk_bytes,
                    &g_d_x.to_vec(),
                    &pk_d_x.to_vec(),
                    &hotkey_raw_address,
                    0u32,
                )
                .unwrap();

            // Each bundle should have valid delegation data
            assert_eq!(action.rk.len(), 32);
            assert_eq!(action.van.len(), 32);
            assert_eq!(action.gov_nullifiers.len(), 4);

            // Verify data persisted per bundle
            let conn = db.conn();
            let stored_rand = queries::load_van_comm_rand(&conn, ROUND_ID, i as u32).unwrap();
            assert_eq!(stored_rand, action.van_comm_rand);
            let stored_alpha = queries::load_alpha(&conn, ROUND_ID, i as u32).unwrap();
            assert_eq!(stored_alpha, action.alpha);

            // ZKP2 inputs loadable per bundle
            let zkp2 = queries::load_zkp2_inputs(&conn, ROUND_ID, i as u32).unwrap();
            assert_eq!(zkp2.gov_comm_rand.len(), 32);
        }

        // Store VAN positions for each bundle
        db.store_van_position(ROUND_ID, 0, 100).unwrap();
        db.store_van_position(ROUND_ID, 1, 101).unwrap();
        assert_eq!(
            queries::load_van_position(&db.conn(), ROUND_ID, 0).unwrap(),
            100
        );
        assert_eq!(
            queries::load_van_position(&db.conn(), ROUND_ID, 1).unwrap(),
            101
        );

        // Store votes for proposal 0 across both bundles
        let conn = db.conn();
        queries::store_vote(&conn, ROUND_ID, 0, 0, 0, &[0xAA; 32]).unwrap();
        queries::store_vote(&conn, ROUND_ID, 1, 0, 0, &[0xBB; 32]).unwrap();
        drop(conn);

        let votes = db.get_votes(ROUND_ID).unwrap();
        assert_eq!(votes.len(), 2);
        assert_eq!(votes[0].bundle_index, 0);
        assert_eq!(votes[1].bundle_index, 1);

        // Mark bundle 0's vote submitted, verify bundle 1 still unsubmitted
        db.mark_vote_submitted(ROUND_ID, 0, 0).unwrap();
        let votes = db.get_votes(ROUND_ID).unwrap();
        assert!(
            votes
                .iter()
                .find(|v| v.bundle_index == 0)
                .unwrap()
                .submitted
        );
        assert!(
            !votes
                .iter()
                .find(|v| v.bundle_index == 1)
                .unwrap()
                .submitted
        );

        // Verify proposal_authority reflects per-bundle submission state
        let conn = db.conn();
        let zkp2_0 = queries::load_zkp2_inputs(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(zkp2_0.proposal_authority, 0xFFFF & !(1u64 << 0)); // bit 0 cleared
        let zkp2_1 = queries::load_zkp2_inputs(&conn, ROUND_ID, 1).unwrap();
        assert_eq!(zkp2_1.proposal_authority, 0xFFFF); // no bits cleared
        drop(conn);

        // Verify cascade: clearing the round removes everything
        db.clear_round(ROUND_ID).unwrap();
        assert!(db.list_rounds().unwrap().is_empty());
        assert_eq!(db.get_bundle_count(ROUND_ID).unwrap(), 0);
    }
}
