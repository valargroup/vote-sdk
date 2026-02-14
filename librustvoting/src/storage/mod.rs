mod migrations;
pub mod operations;
pub mod queries;

use std::sync::Mutex;

use rusqlite::Connection;

use crate::types::VotingError;

/// Current phase of a voting round.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(i32)]
pub enum RoundPhase {
    Initialized = 0,
    HotkeyGenerated = 1,
    DelegationConstructed = 2,
    WitnessBuilt = 3,
    DelegationProved = 4,
    VoteReady = 5,
}

impl RoundPhase {
    pub fn from_i32(v: i32) -> Self {
        match v {
            0 => Self::Initialized,
            1 => Self::HotkeyGenerated,
            2 => Self::DelegationConstructed,
            3 => Self::WitnessBuilt,
            4 => Self::DelegationProved,
            5 => Self::VoteReady,
            _ => Self::Initialized,
        }
    }
}

/// Summary state of a voting round (for UI / SDK queries).
#[derive(Clone, Debug)]
pub struct RoundState {
    pub round_id: String,
    pub phase: RoundPhase,
    pub snapshot_height: u64,
    pub hotkey_address: Option<String>,
    pub delegated_weight: Option<u64>,
    pub proof_generated: bool,
}

/// A vote record from the votes table.
#[derive(Clone, Debug)]
pub struct VoteRecord {
    pub proposal_id: u32,
    pub choice: u32,
    pub submitted: bool,
}

/// Compact round info for list_rounds().
#[derive(Clone, Debug)]
pub struct RoundSummary {
    pub round_id: String,
    pub phase: RoundPhase,
    pub snapshot_height: u64,
    pub created_at: u64,
}

/// Database handle for voting state. Wraps a SQLite connection.
pub struct VotingDb {
    conn: Mutex<Connection>,
}

impl VotingDb {
    /// Open (or create) the voting database at the given path.
    /// Runs migrations automatically.
    pub fn open(path: &str) -> Result<Self, VotingError> {
        let conn = if path == ":memory:" {
            Connection::open_in_memory()
        } else {
            Connection::open(path)
        }
        .map_err(|e| VotingError::Internal {
            message: format!("failed to open database: {}", e),
        })?;

        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;")
            .map_err(|e| VotingError::Internal {
                message: format!("failed to set pragmas: {}", e),
            })?;

        migrations::migrate(&conn)?;

        Ok(Self {
            conn: Mutex::new(conn),
        })
    }

    /// Get a lock on the underlying connection for query execution.
    pub fn conn(&self) -> std::sync::MutexGuard<'_, Connection> {
        self.conn.lock().expect("database mutex poisoned")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::VotingRoundParams;

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
    fn test_open_in_memory() {
        let db = test_db();
        let conn = db.conn();
        let version: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap();
        assert_eq!(version, 2);
    }

    #[test]
    fn test_round_lifecycle() {
        let db = test_db();
        let conn = db.conn();
        let params = test_params();

        queries::insert_round(&conn, &params, None).unwrap();

        let state = queries::get_round_state(&conn, "test-round-1").unwrap();
        assert_eq!(state.phase, RoundPhase::Initialized);
        assert_eq!(state.snapshot_height, 1000);
        assert!(!state.proof_generated);

        let rounds = queries::list_rounds(&conn).unwrap();
        assert_eq!(rounds.len(), 1);
        assert_eq!(rounds[0].round_id, "test-round-1");

        queries::clear_round(&conn, "test-round-1").unwrap();
        let rounds = queries::list_rounds(&conn).unwrap();
        assert!(rounds.is_empty());
    }

    #[test]
    fn test_tree_state_cache() {
        let db = test_db();
        let conn = db.conn();
        queries::insert_round(&conn, &test_params(), None).unwrap();

        let tree_state = vec![0xCC; 1024];
        queries::store_tree_state(&conn, "test-round-1", 1000, &tree_state).unwrap();

        let loaded = queries::load_tree_state(&conn, "test-round-1").unwrap();
        assert_eq!(loaded, tree_state);
    }

    #[test]
    fn test_proof_storage() {
        let db = test_db();
        let conn = db.conn();
        queries::insert_round(&conn, &test_params(), None).unwrap();

        let witness = vec![0xDD; 512];
        queries::store_witness(&conn, "test-round-1", &witness).unwrap();

        let loaded = queries::load_witness(&conn, "test-round-1").unwrap();
        assert_eq!(loaded, witness);

        let proof = crate::types::ProofResult {
            proof: vec![0xAB; 256],
            success: true,
            error: None,
        };
        queries::store_proof(&conn, "test-round-1", &proof).unwrap();

        let state = queries::get_round_state(&conn, "test-round-1").unwrap();
        assert!(state.proof_generated);
    }

    #[test]
    fn test_vote_storage() {
        let db = test_db();
        let conn = db.conn();
        queries::insert_round(&conn, &test_params(), None).unwrap();

        let commitment = vec![0xCC; 128];
        queries::store_vote(&conn, "test-round-1", 0, 0, &commitment).unwrap();
        queries::store_vote(&conn, "test-round-1", 1, 1, &commitment).unwrap();

        queries::mark_vote_submitted(&conn, "test-round-1", 0).unwrap();
    }

    #[test]
    fn test_get_votes() {
        let db = test_db();
        let conn = db.conn();
        queries::insert_round(&conn, &test_params(), None).unwrap();

        // No votes initially
        let votes = queries::get_votes(&conn, "test-round-1").unwrap();
        assert!(votes.is_empty());

        // Store two votes with different choices
        let commitment = vec![0xCC; 128];
        queries::store_vote(&conn, "test-round-1", 0, 0, &commitment).unwrap();
        queries::store_vote(&conn, "test-round-1", 1, 2, &commitment).unwrap();

        let votes = queries::get_votes(&conn, "test-round-1").unwrap();
        assert_eq!(votes.len(), 2);
        assert_eq!(votes[0].proposal_id, 0);
        assert_eq!(votes[0].choice, 0);
        assert!(!votes[0].submitted);
        assert_eq!(votes[1].proposal_id, 1);
        assert_eq!(votes[1].choice, 2);

        // Mark first vote submitted and verify
        queries::mark_vote_submitted(&conn, "test-round-1", 0).unwrap();
        let votes = queries::get_votes(&conn, "test-round-1").unwrap();
        assert!(votes[0].submitted);
        assert!(!votes[1].submitted);
    }
}
