use std::collections::HashMap;

use ff::PrimeField;

use crate::storage::queries;
use crate::storage::{RoundPhase, RoundState, RoundSummary, VoteRecord, VotingDb};
use crate::types::{
    DelegationProofResult, DelegationSubmissionData, EncryptedShare,
    GovernancePczt, NoteInfo, ProofProgressReporter, SharePayload, VoteCommitmentBundle,
    VotingError, VotingHotkey, VotingRoundParams, WitnessData,
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

    /// Split notes into value-aware bundles of up to 5 and insert bundle rows.
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
        // Persist delegation data fields
        // plus padded_note_secrets and pczt_sighash for ZCA-74 randomness threading.
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
            &result.padded_note_secrets,
            &result.pczt_sighash,
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
    /// Fetches IMT exclusion proofs from the PIR server for each note's nullifier.
    /// For padded notes (< 5 real notes), the prover fetches proofs internally via PIR.
    ///
    /// Stores the proof result and advances phase to `DelegationProved`.
    pub fn build_and_prove_delegation(
        &self,
        round_id: &str,
        bundle_index: u32,
        wallet_db_path: &str,
        hotkey_raw_address: &[u8],
        pir_server_url: &str,
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

        // Load Phase 1 randomness for ZCA-74 fix: ensures Phase 2 produces
        // the same nf_signed/cmx_new that Phase 1 committed to in the PCZT.
        let rseed_signed = queries::load_rseed_signed(&conn, round_id, bundle_index)?;
        let rseed_output = queries::load_rseed_output(&conn, round_id, bundle_index)?;
        let padded_secrets = queries::load_padded_note_secrets(&conn, round_id, bundle_index)?;

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

        // Phase 2: Fetch IMT exclusion proofs via PIR
        let pir_start = std::time::Instant::now();
        eprintln!(
            "[ZKP1] Connecting to PIR server at {} for {} notes",
            pir_server_url,
            full_notes.len()
        );
        let pir_client =
            pir_client::PirClientBlocking::connect(pir_server_url).map_err(|e| {
                VotingError::Internal {
                    message: format!("PIR server connect failed: {e}"),
                }
            })?;
        let nullifiers: Vec<pasta_curves::pallas::Base> = full_notes
            .iter()
            .enumerate()
            .map(|(i, note)| {
                let nf_bytes: [u8; 32] =
                    note.nullifier.as_slice().try_into().map_err(|_| {
                        VotingError::Internal {
                            message: format!(
                                "note[{i}] nullifier must be 32 bytes, got {}",
                                note.nullifier.len()
                            ),
                        }
                    })?;
                Option::from(pasta_curves::pallas::Base::from_repr(nf_bytes)).ok_or_else(|| {
                    VotingError::Internal {
                        message: format!("note[{i}] nullifier is not a valid field element"),
                    }
                })
            })
            .collect::<Result<Vec<_>, _>>()?;

        let imt_proofs: Vec<_> = pir_client
            .fetch_proofs(&nullifiers)
            .map_err(|e| VotingError::Internal {
                message: format!("PIR parallel fetch failed: {e}"),
            })?
            .into_iter()
            .map(crate::zkp1::convert_pir_proof)
            .collect();
        let pir_elapsed = pir_start.elapsed();
        eprintln!(
            "[ZKP1] PIR fetch total: {:.2}s for {} proofs",
            pir_elapsed.as_secs_f64(),
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

        // Construct PrecomputedRandomness from Phase 1 values (ZCA-74 fix).
        // If padded_note_secrets is empty (e.g. test data with no PCZT),
        // we fall back to None and the builder will sample fresh randomness.
        let precomputed = if !padded_secrets.is_empty() || !rseed_signed.is_empty() {
            use voting_circuits::delegation::builder::PaddedNoteData;
            let padded_notes: Vec<PaddedNoteData> = padded_secrets
                .iter()
                .map(|(rho, rseed)| {
                    let mut rho_arr = [0u8; 32];
                    let mut rseed_arr = [0u8; 32];
                    rho_arr.copy_from_slice(rho);
                    rseed_arr.copy_from_slice(rseed);
                    PaddedNoteData {
                        rho: rho_arr,
                        rseed: rseed_arr,
                    }
                })
                .collect();
            let mut rseed_signed_arr = [0u8; 32];
            rseed_signed_arr.copy_from_slice(&rseed_signed);
            let mut rseed_output_arr = [0u8; 32];
            rseed_output_arr.copy_from_slice(&rseed_output);
            Some(voting_circuits::delegation::builder::PrecomputedRandomness {
                padded_notes,
                rseed_signed: rseed_signed_arr,
                rseed_output: rseed_output_arr,
            })
        } else {
            None
        };

        let result = crate::zkp1::build_and_prove_delegation(
            &full_notes,
            hotkey_raw_address,
            &alpha,
            &van_comm_rand,
            &vote_round_id_bytes,
            &ordered_witnesses,
            &imt_proofs,
            Some(&pir_client),
            network_id,
            progress,
            precomputed.as_ref(),
        )?;
        let prove_elapsed = prove_start.elapsed();
        eprintln!(
            "[ZKP1] Proof generation: {:.2}s",
            prove_elapsed.as_secs_f64()
        );

        // Store proof bytes for debugging/recovery
        queries::store_proof(&conn, round_id, bundle_index, &result.proof)?;
        // Persist prover's public inputs — needed later for delegation TX submission.
        // With PrecomputedRandomness (ZCA-74 fix), nf_signed/cmx_new should match
        // Phase 1 values. We still store them to be explicit and support the legacy path.
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
            "[ZKP1] TOTAL: {:.2}s (DB: {:.2}s, PIR: {:.2}s, Prove: {:.2}s) — proof {} bytes",
            total_elapsed.as_secs_f64(),
            db_elapsed.as_secs_f64(),
            pir_elapsed.as_secs_f64(),
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
    /// This method loads them, derives the sender's SpendingKey from seed, loads the
    /// ZIP-244 sighash (stored during PCZT construction), signs it, and returns
    /// everything the chain needs.
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
        let sighash_vec = queries::load_pczt_sighash(&conn, round_id, bundle_index)?;
        drop(conn);

        let sighash: [u8; 32] =
            sighash_vec
                .as_slice()
                .try_into()
                .map_err(|_| VotingError::Internal {
                    message: format!(
                        "pczt_sighash must be 32 bytes, got {}",
                        sighash_vec.len()
                    ),
                })?;

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

        // enc_memo = [0x05; 64] (mock, matches e2e test and chain expectations)
        let enc_memo = [0x05u8; 64];

        // Sign the ZIP-244 sighash (extracted from PCZT during Phase 1)
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

    /// Reconstruct the delegation TX payload using a Keystone-provided signature.
    ///
    /// Unlike `get_delegation_submission`, this does NOT derive `ask` from a seed
    /// or re-sign. Instead, it uses the externally-provided Keystone signature
    /// and the ZIP-244 sighash that Keystone signed.
    pub fn get_delegation_submission_with_keystone_sig(
        &self,
        round_id: &str,
        bundle_index: u32,
        keystone_sig: &[u8],
        keystone_sighash: &[u8],
    ) -> Result<DelegationSubmissionData, VotingError> {
        if keystone_sig.len() != 64 {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "keystone_sig must be 64 bytes, got {}",
                    keystone_sig.len()
                ),
            });
        }
        if keystone_sighash.len() != 32 {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "keystone_sighash must be 32 bytes, got {}",
                    keystone_sighash.len()
                ),
            });
        }

        let conn = self.conn();
        let data = queries::load_delegation_submission_data(&conn, round_id, bundle_index)?;

        // enc_memo = [0x05; 64] (mock, matches e2e test and chain expectations)
        let enc_memo = [0x05u8; 64];

        Ok(DelegationSubmissionData {
            proof: data.proof,
            rk: data.rk,
            nf_signed: data.nf_signed,
            cmx_new: data.cmx_new,
            gov_comm: data.gov_comm,
            gov_nullifiers: data.gov_nullifiers,
            alpha: data.alpha,
            vote_round_id: data.vote_round_id,
            spend_auth_sig: keystone_sig.to_vec(),
            sighash: keystone_sighash.to_vec(),
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

    // 64 hex chars = 32 bytes when decoded. Required because build_governance_pczt
    // hex-decodes vote_round_id and validates it as exactly 32 bytes (a Pallas field element).
    const ROUND_ID: &str = "0101010101010101010101010101010101010101010101010101010101010101";

    fn test_db() -> VotingDb {
        VotingDb::open(":memory:").unwrap()
    }

    fn test_params() -> VotingRoundParams {
        // Use SpendAuthG as a valid Pallas point for ea_pk in tests.
        use group::GroupEncoding;
        let ea_pk = pasta_curves::pallas::Point::from(voting_circuits::vote_proof::spend_auth_g_affine());
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

        // 5 notes each 13M — all fit in 1 bundle (capacity 5)
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
        assert_eq!(count, 1);
        // Quantized: bundle 0 (65M → 5×12.5M=62.5M) = 62.5M
        assert_eq!(eligible, 62_500_000);
        assert_eq!(db.get_bundle_count(ROUND_ID).unwrap(), 1);
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

    /// Multi-bundle test: 6 notes → 2 bundles (5+1), independent delegation + vote storage per bundle.
    #[test]
    fn test_multi_bundle_delegation_and_voting() {
        use orchard::keys::{FullViewingKey, SpendingKey};
        use zip32::Scope;

        let db = test_db();
        db.init_round(&test_params(), None).unwrap();

        // Create 6 notes with distinct positions and unique nullifiers
        let notes: Vec<NoteInfo> = (0..6)
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

        // Setup bundles: 6 equal-value notes → sequential fill packs first 5, then 1
        // Sorted by value DESC (all equal) then position ASC: [0,1,2,3,4,5]
        // Bundle 0 = [0,1,2,3,4], bundle 1 = [5]
        let (bundle_count, eligible) = db.setup_bundles(ROUND_ID, &notes).unwrap();
        assert_eq!(bundle_count, 2);
        // Quantized: bundle 0 (65M → 5×12.5M=62.5M) + bundle 1 (13M → 1×12.5M=12.5M) = 75M
        assert_eq!(eligible, 75_000_000);
        assert_eq!(db.get_bundle_count(ROUND_ID).unwrap(), 2);

        // Verify note positions per bundle (sequential fill)
        let conn = db.conn();
        let positions_0 = queries::load_bundle_note_positions(&conn, ROUND_ID, 0).unwrap();
        assert_eq!(positions_0, vec![0, 1, 2, 3, 4]);
        let positions_1 = queries::load_bundle_note_positions(&conn, ROUND_ID, 1).unwrap();
        assert_eq!(positions_1, vec![5]);
        drop(conn);

        // Derive keys for build_governance_pczt
        let sk = SpendingKey::from_bytes([0x42; 32]).expect("valid spending key");
        let fvk = FullViewingKey::from(&sk);
        let fvk_bytes = fvk.to_bytes().to_vec();
        let hotkey_sk = SpendingKey::from_bytes([0x43; 32]).expect("valid spending key");
        let hotkey_fvk = FullViewingKey::from(&hotkey_sk);
        let hotkey_addr = hotkey_fvk.address_at(0u32, Scope::External);
        let hotkey_raw_address = hotkey_addr.to_raw_address_bytes().to_vec();
        let seed_fingerprint = [0x42u8; 32];

        // Build governance PCZT for each bundle independently
        let chunk_result = crate::types::chunk_notes(&notes);

        for (i, chunk) in chunk_result.bundles.iter().enumerate() {
            let result = db
                .build_governance_pczt(
                    ROUND_ID,
                    i as u32,
                    chunk,
                    &fvk_bytes,
                    &hotkey_raw_address,
                    0xC8E71055, // NU6 consensus branch ID
                    1,          // testnet coin type
                    &seed_fingerprint,
                    0,          // account_index
                    "test-round",
                    0,          // address_index
                )
                .unwrap();

            // Each bundle should have valid delegation data
            assert_eq!(result.rk.len(), 32);
            assert_eq!(result.van.len(), 32);
            assert_eq!(result.gov_nullifiers.len(), 5);
            assert_eq!(result.pczt_sighash.len(), 32);

            // Verify data persisted per bundle
            let conn = db.conn();
            let stored_rand = queries::load_van_comm_rand(&conn, ROUND_ID, i as u32).unwrap();
            assert_eq!(stored_rand, result.van_comm_rand);
            let stored_alpha = queries::load_alpha(&conn, ROUND_ID, i as u32).unwrap();
            assert_eq!(stored_alpha, result.alpha);

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
