use rusqlite::Connection;

use crate::VotingError;

const CURRENT_VERSION: u32 = 2;

pub fn migrate(conn: &Connection) -> Result<(), VotingError> {
    let version: u32 = conn
        .pragma_query_value(None, "user_version", |r| r.get(0))
        .map_err(|e| VotingError::Internal {
            message: format!("failed to read database version: {}", e),
        })?;

    if version < 1 {
        conn.execute_batch(include_str!("migrations/001_init.sql"))
            .map_err(|e| VotingError::Internal {
                message: format!("migration 001_init failed: {}", e),
            })?;
        conn.pragma_update(None, "user_version", 1)
            .map_err(|e| VotingError::Internal {
                message: format!("failed to update database version: {}", e),
            })?;
    }

    if version < 2 {
        // Add tables for witness caching that were added to 001_init.sql
        // after some DBs had already been created at version 1.
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS cached_tree_state (
                round_id        TEXT PRIMARY KEY REFERENCES rounds(round_id),
                snapshot_height INTEGER NOT NULL,
                tree_state      BLOB NOT NULL
            );
            CREATE TABLE IF NOT EXISTS witnesses (
                round_id        TEXT NOT NULL,
                note_position   INTEGER NOT NULL,
                note_commitment BLOB NOT NULL,
                root            BLOB NOT NULL,
                auth_path       BLOB NOT NULL,
                created_at      INTEGER NOT NULL,
                PRIMARY KEY (round_id, note_position),
                FOREIGN KEY (round_id) REFERENCES rounds(round_id)
            );",
        )
        .map_err(|e| VotingError::Internal {
            message: format!("migration to version 2 failed: {}", e),
        })?;
        conn.pragma_update(None, "user_version", 2)
            .map_err(|e| VotingError::Internal {
                message: format!("failed to update database version: {}", e),
            })?;
    }

    let final_version: u32 = conn
        .pragma_query_value(None, "user_version", |r| r.get(0))
        .map_err(|e| VotingError::Internal {
            message: format!("failed to verify database version: {}", e),
        })?;

    if final_version != CURRENT_VERSION {
        return Err(VotingError::Internal {
            message: format!(
                "unexpected database version after migration: expected {}, got {}",
                CURRENT_VERSION, final_version
            ),
        });
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_migrate_fresh_database() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();

        let version: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap();
        assert_eq!(version, CURRENT_VERSION);
    }

    #[test]
    fn test_migrate_idempotent() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();
        migrate(&conn).unwrap();

        let version: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap();
        assert_eq!(version, CURRENT_VERSION);
    }

    #[test]
    fn test_tables_created() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();

        let tables: Vec<String> = conn
            .prepare("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
            .unwrap()
            .query_map([], |row| row.get(0))
            .unwrap()
            .collect::<Result<_, _>>()
            .unwrap();

        assert!(tables.contains(&"rounds".to_string()));
        assert!(tables.contains(&"cached_tree_state".to_string()));
        assert!(tables.contains(&"proofs".to_string()));
        assert!(tables.contains(&"votes".to_string()));
    }

    /// Verify that the delegation data columns exist in the rounds table
    /// after migration and can round-trip BLOB data.
    #[test]
    fn test_delegation_data_columns_exist() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();

        // Insert a row using all nullable BLOB columns.
        // These columns are populated later by store_delegation_data()
        // after construct_delegation_action() computes the VAN.
        conn.execute(
            "INSERT INTO rounds (round_id, snapshot_height, ea_pk, nc_root, nullifier_imt_root, phase, created_at, gov_comm_rand, dummy_nullifiers, rho_signed, padded_note_data, nf_signed, cmx_new, alpha, rseed_signed, rseed_output) VALUES ('test', 1, X'00', X'00', X'00', 0, 0, X'AA', X'BB', X'CC', X'DD', X'EE', X'FF', X'11', X'22', X'33')",
            [],
        ).unwrap();

        // Verify gov_comm_rand round-trips (the VAN blinding factor)
        let rand: Vec<u8> = conn
            .query_row(
                "SELECT gov_comm_rand FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(rand, vec![0xAA]);

        // Verify dummy_nullifiers round-trips (padded note nullifiers, stored as flat blob)
        let dummies: Vec<u8> = conn
            .query_row(
                "SELECT dummy_nullifiers FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(dummies, vec![0xBB]);

        // Verify rho_signed round-trips (constrained rho, spec §1.3.4.1)
        let rho: Vec<u8> = conn
            .query_row(
                "SELECT rho_signed FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(rho, vec![0xCC]);

        // Verify padded_note_data round-trips (cmx values for padded dummy notes)
        let padded: Vec<u8> = conn
            .query_row(
                "SELECT padded_note_data FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(padded, vec![0xDD]);

        // Verify nf_signed round-trips (signed note nullifier)
        let nf: Vec<u8> = conn
            .query_row(
                "SELECT nf_signed FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(nf, vec![0xEE]);

        // Verify cmx_new round-trips (output note commitment)
        let cmx: Vec<u8> = conn
            .query_row(
                "SELECT cmx_new FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(cmx, vec![0xFF]);

        // Verify alpha round-trips (spend auth randomizer)
        let alpha: Vec<u8> = conn
            .query_row(
                "SELECT alpha FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(alpha, vec![0x11]);

        // Verify rseed_signed round-trips (signed note randomness)
        let rseed_signed: Vec<u8> = conn
            .query_row(
                "SELECT rseed_signed FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(rseed_signed, vec![0x22]);

        // Verify rseed_output round-trips (output note randomness)
        let rseed_output: Vec<u8> = conn
            .query_row(
                "SELECT rseed_output FROM rounds WHERE round_id = 'test'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(rseed_output, vec![0x33]);
    }
}
