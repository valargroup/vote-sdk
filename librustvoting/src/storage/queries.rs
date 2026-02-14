use rusqlite::{named_params, Connection};

use crate::storage::{RoundPhase, RoundState, RoundSummary, VoteRecord};
use crate::types::{ProofResult, VotingError, VotingRoundParams};

// --- Rounds ---

pub fn insert_round(
    conn: &Connection,
    params: &VotingRoundParams,
    session_json: Option<&str>,
) -> Result<(), VotingError> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    conn.execute(
        "INSERT INTO rounds (round_id, snapshot_height, ea_pk, nc_root, nullifier_imt_root, session_json, phase, created_at)
         VALUES (:round_id, :snapshot_height, :ea_pk, :nc_root, :nullifier_imt_root, :session_json, :phase, :created_at)",
        named_params! {
            ":round_id": params.vote_round_id,
            ":snapshot_height": params.snapshot_height as i64,
            ":ea_pk": params.ea_pk,
            ":nc_root": params.nc_root,
            ":nullifier_imt_root": params.nullifier_imt_root,
            ":session_json": session_json,
            ":phase": RoundPhase::Initialized as i32,
            ":created_at": now,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to insert round: {}", e),
    })?;

    Ok(())
}

pub fn update_round_phase(
    conn: &Connection,
    round_id: &str,
    phase: RoundPhase,
) -> Result<(), VotingError> {
    let rows = conn
        .execute(
            "UPDATE rounds SET phase = :phase WHERE round_id = :round_id",
            named_params! {
                ":phase": phase as i32,
                ":round_id": round_id,
            },
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to update round phase: {}", e),
        })?;

    if rows == 0 {
        return Err(VotingError::InvalidInput {
            message: format!("round not found: {}", round_id),
        });
    }

    Ok(())
}

pub fn load_round_params(
    conn: &Connection,
    round_id: &str,
) -> Result<VotingRoundParams, VotingError> {
    conn.query_row(
        "SELECT round_id, snapshot_height, ea_pk, nc_root, nullifier_imt_root FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| {
            Ok(VotingRoundParams {
                vote_round_id: row.get(0)?,
                snapshot_height: row.get::<_, i64>(1)? as u64,
                ea_pk: row.get(2)?,
                nc_root: row.get(3)?,
                nullifier_imt_root: row.get(4)?,
            })
        },
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("round not found: {} ({})", round_id, e),
    })
}

pub fn get_round_state(conn: &Connection, round_id: &str) -> Result<RoundState, VotingError> {
    let (phase_int, snapshot_height): (i32, i64) = conn
        .query_row(
            "SELECT phase, snapshot_height FROM rounds WHERE round_id = :round_id",
            named_params! { ":round_id": round_id },
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!("round not found: {} ({})", round_id, e),
        })?;

    let proof_generated: bool = conn
        .query_row(
            "SELECT COUNT(*) FROM proofs WHERE round_id = :round_id AND success = 1",
            named_params! { ":round_id": round_id },
            |row| row.get::<_, i64>(0).map(|c| c > 0),
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to query proof status: {}", e),
        })?;

    Ok(RoundState {
        round_id: round_id.to_string(),
        phase: RoundPhase::from_i32(phase_int),
        snapshot_height: snapshot_height as u64,
        hotkey_address: None,
        delegated_weight: None,
        proof_generated,
    })
}

pub fn list_rounds(conn: &Connection) -> Result<Vec<RoundSummary>, VotingError> {
    let mut stmt = conn
        .prepare("SELECT round_id, phase, snapshot_height, created_at FROM rounds ORDER BY created_at DESC")
        .map_err(|e| VotingError::Internal {
            message: format!("failed to prepare list_rounds query: {}", e),
        })?;

    let rounds = stmt
        .query_map([], |row| {
            Ok(RoundSummary {
                round_id: row.get(0)?,
                phase: RoundPhase::from_i32(row.get(1)?),
                snapshot_height: row.get::<_, i64>(2)? as u64,
                created_at: row.get::<_, i64>(3)? as u64,
            })
        })
        .map_err(|e| VotingError::Internal {
            message: format!("failed to list rounds: {}", e),
        })?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| VotingError::Internal {
            message: format!("failed to collect rounds: {}", e),
        })?;

    Ok(rounds)
}

pub fn clear_round(conn: &Connection, round_id: &str) -> Result<(), VotingError> {
    conn.execute(
        "DELETE FROM votes WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to clear votes: {}", e),
    })?;
    conn.execute(
        "DELETE FROM proofs WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to clear proofs: {}", e),
    })?;
    conn.execute(
        "DELETE FROM witnesses WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to clear witnesses: {}", e),
    })?;
    conn.execute(
        "DELETE FROM cached_tree_state WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to clear cached_tree_state: {}", e),
    })?;
    conn.execute(
        "DELETE FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to clear round: {}", e),
    })?;
    Ok(())
}

// --- Delegation Secrets ---
//
// After construct_delegation_action computes the VAN (governance commitment),
// we persist two values needed for later proof steps:
//   - gov_comm_rand: the 32-byte blinding factor used in the VAN Poseidon hash.
//     Needed again in ZKP #2 (vote commitment) to reconstruct the VAN as a witness.
//   - dummy_nullifiers: random nullifiers generated for padded note slots (§1.3.5).
//     Each is 32 bytes. Stored so the witness builder can reconstruct padded notes.

/// Persist all delegation action data in a single UPDATE:
/// blinding factor, dummy nullifiers, constrained rho, padded note cmx values,
/// signed action fields (nf_signed, cmx_new, alpha), and note rseeds.
pub fn store_delegation_data(
    conn: &Connection,
    round_id: &str,
    gov_comm_rand: &[u8],
    dummy_nullifiers: &[Vec<u8>],
    rho_signed: &[u8],
    padded_cmx: &[Vec<u8>],
    nf_signed: &[u8],
    cmx_new: &[u8],
    alpha: &[u8],
    rseed_signed: &[u8],
    rseed_output: &[u8],
) -> Result<(), VotingError> {
    // Serialize dummy nullifiers as a flat byte blob: [nf0 (32 bytes) | nf1 | nf2 | ...].
    // Length 0 means no padding was needed (all 4 notes were real).
    // Length 32/64/96 means 1/2/3 dummy notes respectively.
    let dummy_blob: Vec<u8> = dummy_nullifiers
        .iter()
        .flat_map(|n| n.iter().copied())
        .collect();

    // Same flat-blob encoding for padded cmx values.
    let padded_blob: Vec<u8> = padded_cmx.iter().flat_map(|c| c.iter().copied()).collect();

    let rows = conn
        .execute(
            "UPDATE rounds SET gov_comm_rand = :rand, dummy_nullifiers = :dummies, rho_signed = :rho, padded_note_data = :padded, nf_signed = :nf_signed, cmx_new = :cmx_new, alpha = :alpha, rseed_signed = :rseed_signed, rseed_output = :rseed_output WHERE round_id = :round_id",
            named_params! {
                ":rand": gov_comm_rand,
                ":dummies": dummy_blob,
                ":rho": rho_signed,
                ":padded": padded_blob,
                ":nf_signed": nf_signed,
                ":cmx_new": cmx_new,
                ":alpha": alpha,
                ":rseed_signed": rseed_signed,
                ":rseed_output": rseed_output,
                ":round_id": round_id,
            },
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to store delegation data: {}", e),
        })?;

    // If no rows were updated, the round_id doesn't exist in the rounds table.
    if rows == 0 {
        return Err(VotingError::InvalidInput {
            message: format!("round not found: {}", round_id),
        });
    }

    Ok(())
}

/// Load nf_signed (signed note nullifier, 32 bytes) for a round.
pub fn load_nf_signed(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT nf_signed FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no nf_signed for round: {} ({})", round_id, e),
    })
}

/// Load cmx_new (output note commitment, 32 bytes) for a round.
pub fn load_cmx_new(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT cmx_new FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no cmx_new for round: {} ({})", round_id, e),
    })
}

/// Load alpha (spend auth randomizer scalar, 32 bytes) for a round.
pub fn load_alpha(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT alpha FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no alpha for round: {} ({})", round_id, e),
    })
}

/// Load signed note rseed (32 bytes) for a round.
pub fn load_rseed_signed(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT rseed_signed FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no rseed_signed for round: {} ({})", round_id, e),
    })
}

/// Load output note rseed (32 bytes) for a round.
pub fn load_rseed_output(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT rseed_output FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no rseed_output for round: {} ({})", round_id, e),
    })
}

/// Load the VAN blinding factor for a round. Needed as a private witness in ZKP #2.
pub fn load_gov_comm_rand(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT gov_comm_rand FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no gov_comm_rand for round: {} ({})", round_id, e),
    })
}

/// Load dummy nullifiers for padded note slots. Returns 0-3 entries of 32 bytes each.
/// Deserializes the flat blob back into individual 32-byte nullifiers.
pub fn load_dummy_nullifiers(
    conn: &Connection,
    round_id: &str,
) -> Result<Vec<Vec<u8>>, VotingError> {
    let blob: Vec<u8> = conn
        .query_row(
            "SELECT dummy_nullifiers FROM rounds WHERE round_id = :round_id",
            named_params! { ":round_id": round_id },
            |row| row.get(0),
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!("no dummy_nullifiers for round: {} ({})", round_id, e),
        })?;

    // Split the flat blob back into 32-byte chunks, one per dummy nullifier.
    if blob.len() % 32 != 0 {
        return Err(VotingError::Internal {
            message: format!(
                "corrupt dummy_nullifiers blob: length {} is not a multiple of 32",
                blob.len()
            ),
        });
    }
    Ok(blob.chunks_exact(32).map(|c| c.to_vec()).collect())
}

// --- Rho & Padded Note Data ---

/// Load rho_signed for a round (32-byte constrained rho).
pub fn load_rho_signed(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT rho_signed FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no rho_signed for round: {} ({})", round_id, e),
    })
}

/// Load padded note cmx data. Returns 0-3 entries of 32 bytes each.
pub fn load_padded_cmx(conn: &Connection, round_id: &str) -> Result<Vec<Vec<u8>>, VotingError> {
    let blob: Vec<u8> = conn
        .query_row(
            "SELECT padded_note_data FROM rounds WHERE round_id = :round_id",
            named_params! { ":round_id": round_id },
            |row| row.get(0),
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!("no padded_note_data for round: {} ({})", round_id, e),
        })?;

    if blob.len() % 32 != 0 {
        return Err(VotingError::Internal {
            message: format!(
                "corrupt padded_note_data blob: length {} is not a multiple of 32",
                blob.len()
            ),
        });
    }
    Ok(blob.chunks_exact(32).map(|c| c.to_vec()).collect())
}

// --- Cached Tree State ---

pub fn store_tree_state(
    conn: &Connection,
    round_id: &str,
    snapshot_height: u64,
    tree_state: &[u8],
) -> Result<(), VotingError> {
    conn.execute(
        "INSERT OR REPLACE INTO cached_tree_state (round_id, snapshot_height, tree_state)
         VALUES (:round_id, :snapshot_height, :tree_state)",
        named_params! {
            ":round_id": round_id,
            ":snapshot_height": snapshot_height as i64,
            ":tree_state": tree_state,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to store tree state: {}", e),
    })?;
    Ok(())
}

pub fn load_tree_state(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT tree_state FROM cached_tree_state WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no cached tree state for round: {} ({})", round_id, e),
    })
}

// --- Witnesses (Merkle inclusion proofs for Orchard notes) ---

/// Check if witnesses are already cached for a round.
pub fn has_witnesses(conn: &Connection, round_id: &str) -> Result<bool, VotingError> {
    conn.query_row(
        "SELECT COUNT(*) FROM witnesses WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get::<_, i64>(0).map(|c| c > 0),
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to check witnesses: {}", e),
    })
}

/// Store witness data for multiple notes in a round.
/// Each WitnessData's auth_path (Vec<Vec<u8>>) is serialized as a flat 1024-byte blob
/// (32 levels × 32 bytes each).
pub fn store_witnesses(
    conn: &Connection,
    round_id: &str,
    witnesses: &[crate::types::WitnessData],
) -> Result<(), VotingError> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    for w in witnesses {
        // Serialize auth_path as flat blob: 32 × 32 = 1024 bytes
        let auth_blob: Vec<u8> = w.auth_path.iter().flat_map(|h| h.iter().copied()).collect();

        conn.execute(
            "INSERT OR REPLACE INTO witnesses (round_id, note_position, note_commitment, root, auth_path, created_at)
             VALUES (:round_id, :position, :commitment, :root, :auth_path, :created_at)",
            named_params! {
                ":round_id": round_id,
                ":position": w.position as i64,
                ":commitment": w.note_commitment,
                ":root": w.root,
                ":auth_path": auth_blob,
                ":created_at": now,
            },
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to store witness for position {}: {}", w.position, e),
        })?;
    }

    Ok(())
}

/// Load cached witnesses for a round, ordered by position.
pub fn load_witnesses(conn: &Connection, round_id: &str) -> Result<Vec<crate::types::WitnessData>, VotingError> {
    let mut stmt = conn
        .prepare(
            "SELECT note_position, note_commitment, root, auth_path FROM witnesses
             WHERE round_id = :round_id ORDER BY note_position",
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to prepare load_witnesses: {}", e),
        })?;

    let witnesses = stmt
        .query_map(named_params! { ":round_id": round_id }, |row| {
            let position: i64 = row.get(0)?;
            let note_commitment: Vec<u8> = row.get(1)?;
            let root: Vec<u8> = row.get(2)?;
            let auth_blob: Vec<u8> = row.get(3)?;
            Ok((position as u64, note_commitment, root, auth_blob))
        })
        .map_err(|e| VotingError::Internal {
            message: format!("failed to load witnesses: {}", e),
        })?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| VotingError::Internal {
            message: format!("failed to collect witnesses: {}", e),
        })?;

    witnesses
        .into_iter()
        .map(|(position, note_commitment, root, auth_blob)| {
            // Deserialize auth_path from flat blob back to Vec<Vec<u8>>
            if auth_blob.len() != 32 * 32 {
                return Err(VotingError::Internal {
                    message: format!(
                        "corrupt auth_path blob for position {}: expected 1024 bytes, got {}",
                        position,
                        auth_blob.len()
                    ),
                });
            }
            let auth_path: Vec<Vec<u8>> = auth_blob.chunks_exact(32).map(|c| c.to_vec()).collect();

            Ok(crate::types::WitnessData {
                note_commitment,
                position,
                root,
                auth_path,
            })
        })
        .collect()
}

// --- Proofs ---

pub fn store_witness(conn: &Connection, round_id: &str, witness: &[u8]) -> Result<(), VotingError> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    conn.execute(
        "INSERT OR REPLACE INTO proofs (round_id, witness, proof, success, created_at)
         VALUES (:round_id, :witness, NULL, 0, :created_at)",
        named_params! {
            ":round_id": round_id,
            ":witness": witness,
            ":created_at": now,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to store witness: {}", e),
    })?;
    Ok(())
}

pub fn load_witness(conn: &Connection, round_id: &str) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT witness FROM proofs WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no witness for round: {} ({})", round_id, e),
    })
}

pub fn store_proof(
    conn: &Connection,
    round_id: &str,
    proof: &ProofResult,
) -> Result<(), VotingError> {
    conn.execute(
        "UPDATE proofs SET proof = :proof, success = :success WHERE round_id = :round_id",
        named_params! {
            ":proof": proof.proof,
            ":success": proof.success as i32,
            ":round_id": round_id,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to store proof: {}", e),
    })?;
    Ok(())
}

// --- Votes ---

pub fn store_vote(
    conn: &Connection,
    round_id: &str,
    proposal_id: u32,
    choice: u32,
    commitment: &[u8],
) -> Result<(), VotingError> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    conn.execute(
        "INSERT OR REPLACE INTO votes (round_id, proposal_id, choice, commitment, submitted, created_at)
         VALUES (:round_id, :proposal_id, :choice, :commitment, 0, :created_at)",
        named_params! {
            ":round_id": round_id,
            ":proposal_id": proposal_id as i64,
            ":choice": choice as i64,
            ":commitment": commitment,
            ":created_at": now,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to store vote: {}", e),
    })?;
    Ok(())
}

pub fn get_votes(conn: &Connection, round_id: &str) -> Result<Vec<VoteRecord>, VotingError> {
    let mut stmt = conn
        .prepare("SELECT proposal_id, choice, submitted FROM votes WHERE round_id = :round_id")
        .map_err(|e| VotingError::Internal {
            message: format!("failed to prepare get_votes: {}", e),
        })?;

    let votes = stmt
        .query_map(named_params! { ":round_id": round_id }, |row| {
            Ok(VoteRecord {
                proposal_id: row.get::<_, i64>(0)? as u32,
                choice: row.get::<_, i64>(1)? as u32,
                submitted: row.get::<_, i64>(2)? != 0,
            })
        })
        .map_err(|e| VotingError::Internal {
            message: format!("failed to get votes: {}", e),
        })?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| VotingError::Internal {
            message: format!("failed to collect votes: {}", e),
        })?;

    Ok(votes)
}

pub fn mark_vote_submitted(
    conn: &Connection,
    round_id: &str,
    proposal_id: u32,
) -> Result<(), VotingError> {
    conn.execute(
        "UPDATE votes SET submitted = 1 WHERE round_id = :round_id AND proposal_id = :proposal_id",
        named_params! {
            ":round_id": round_id,
            ":proposal_id": proposal_id as i64,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to mark vote submitted: {}", e),
    })?;
    Ok(())
}
