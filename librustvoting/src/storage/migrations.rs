use rusqlite::Connection;

use crate::VotingError;

const CURRENT_VERSION: u32 = 3;

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

    if version < 3 {
        // v3: delegation data moved from rounds to bundles table, witnesses
        // gained bundle_index. Drop everything and recreate from 001_init.sql.
        conn.execute_batch(
            "DROP TABLE IF EXISTS votes;
             DROP TABLE IF EXISTS witnesses;
             DROP TABLE IF EXISTS proofs;
             DROP TABLE IF EXISTS bundles;
             DROP TABLE IF EXISTS cached_tree_state;
             DROP TABLE IF EXISTS rounds;"
        )
        .map_err(|e| VotingError::Internal {
            message: format!("migration to version 3 failed (drop): {}", e),
        })?;
        conn.execute_batch(include_str!("migrations/001_init.sql"))
            .map_err(|e| VotingError::Internal {
                message: format!("migration to version 3 failed (create): {}", e),
            })?;
        conn.pragma_update(None, "user_version", 3)
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
        assert!(tables.contains(&"bundles".to_string()));
        assert!(tables.contains(&"cached_tree_state".to_string()));
        assert!(tables.contains(&"proofs".to_string()));
        assert!(tables.contains(&"votes".to_string()));
    }

    /// Verify that the bundles table columns exist after migration and can round-trip BLOB data.
    #[test]
    fn test_bundle_data_columns_exist() {
        let conn = Connection::open_in_memory().unwrap();
        migrate(&conn).unwrap();

        // Insert a round first
        conn.execute(
            "INSERT INTO rounds (round_id, snapshot_height, ea_pk, nc_root, nullifier_imt_root, phase, created_at) VALUES ('test', 1, X'00', X'00', X'00', 0, 0)",
            [],
        ).unwrap();

        // Insert a bundle row using all nullable BLOB columns.
        conn.execute(
            "INSERT INTO bundles (round_id, bundle_index, van_comm_rand, dummy_nullifiers, rho_signed, padded_note_data, nf_signed, cmx_new, alpha, rseed_signed, rseed_output) VALUES ('test', 0, X'AA', X'BB', X'CC', X'DD', X'EE', X'FF', X'11', X'22', X'33')",
            [],
        ).unwrap();

        // Verify van_comm_rand round-trips (the VAN blinding factor)
        let rand: Vec<u8> = conn
            .query_row(
                "SELECT van_comm_rand FROM bundles WHERE round_id = 'test' AND bundle_index = 0",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(rand, vec![0xAA]);

        // Verify dummy_nullifiers round-trips
        let dummies: Vec<u8> = conn
            .query_row(
                "SELECT dummy_nullifiers FROM bundles WHERE round_id = 'test' AND bundle_index = 0",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(dummies, vec![0xBB]);
    }
}
