use rusqlite::{named_params, Connection};

use crate::types::{ProofResult, VotingError, VotingRoundParams};
use crate::storage::{RoundPhase, RoundState, RoundSummary, VoteRecord};

// --- Rounds ---

pub fn insert_round(conn: &Connection, params: &VotingRoundParams, session_json: Option<&str>) -> Result<(), VotingError> {
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

pub fn update_round_phase(conn: &Connection, round_id: &str, phase: RoundPhase) -> Result<(), VotingError> {
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

pub fn load_round_params(conn: &Connection, round_id: &str) -> Result<VotingRoundParams, VotingError> {
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
    conn.execute("DELETE FROM votes WHERE round_id = :round_id", named_params! { ":round_id": round_id })
        .map_err(|e| VotingError::Internal { message: format!("failed to clear votes: {}", e) })?;
    conn.execute("DELETE FROM proofs WHERE round_id = :round_id", named_params! { ":round_id": round_id })
        .map_err(|e| VotingError::Internal { message: format!("failed to clear proofs: {}", e) })?;
    conn.execute("DELETE FROM cached_tree_state WHERE round_id = :round_id", named_params! { ":round_id": round_id })
        .map_err(|e| VotingError::Internal { message: format!("failed to clear cached_tree_state: {}", e) })?;
    conn.execute("DELETE FROM rounds WHERE round_id = :round_id", named_params! { ":round_id": round_id })
        .map_err(|e| VotingError::Internal { message: format!("failed to clear round: {}", e) })?;
    Ok(())
}

// --- Cached Tree State ---

pub fn store_tree_state(conn: &Connection, round_id: &str, snapshot_height: u64, tree_state: &[u8]) -> Result<(), VotingError> {
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

pub fn store_proof(conn: &Connection, round_id: &str, proof: &ProofResult) -> Result<(), VotingError> {
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

pub fn mark_vote_submitted(conn: &Connection, round_id: &str, proposal_id: u32) -> Result<(), VotingError> {
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
