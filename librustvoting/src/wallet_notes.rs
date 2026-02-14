use rusqlite::{Connection, OpenFlags};
use subtle::CtOption;

use orchard::keys::Diversifier;
use orchard::note::{ExtractedNoteCommitment, RandomSeed, Rho};
use orchard::value::NoteValue;
use zcash_keys::keys::UnifiedFullViewingKey;
use zcash_protocol::consensus::Network;
use zip32::Scope;

use crate::types::{NoteInfo, VotingError};

/// Open the Zcash wallet SQLite DB read-only and return all Orchard notes
/// that were received and unspent at the given snapshot block height.
///
/// For each note, the extracted note commitment (cmx) is computed by
/// reconstructing the note from its stored parts (diversifier, value, rho, rseed)
/// and the account's UFVK.
pub fn get_wallet_notes_at_snapshot(
    wallet_db_path: &str,
    snapshot_height: u64,
    network_id: u32,
) -> Result<Vec<NoteInfo>, VotingError> {
    let network = match network_id {
        0 => Network::MainNetwork,
        1 => Network::TestNetwork,
        _ => {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "invalid network_id {network_id}, expected 0 (mainnet) or 1 (testnet)"
                ),
            })
        }
    };

    let conn = Connection::open_with_flags(
        wallet_db_path,
        OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to open wallet db: {e}"),
    })?;

    let mut stmt = conn
        .prepare(
            "SELECT rn.diversifier, rn.value, rn.rho, rn.rseed, rn.nf,
                    rn.commitment_tree_position, rn.recipient_key_scope,
                    accounts.ufvk
             FROM orchard_received_notes rn
             JOIN transactions t_recv ON t_recv.id_tx = rn.transaction_id
             JOIN accounts ON accounts.id = rn.account_id
             WHERE t_recv.mined_height IS NOT NULL
               AND t_recv.mined_height <= :snapshot_height
               AND rn.nf IS NOT NULL
               AND rn.commitment_tree_position IS NOT NULL
               AND rn.recipient_key_scope IN (0, 1)
               AND accounts.ufvk IS NOT NULL
               AND rn.id NOT IN (
                   SELECT rns.orchard_received_note_id
                   FROM orchard_received_note_spends rns
                   JOIN transactions t_spend ON t_spend.id_tx = rns.transaction_id
                   WHERE t_spend.mined_height IS NOT NULL
                     AND t_spend.mined_height <= :snapshot_height
               )",
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to prepare query: {e}"),
        })?;

    let rows = stmt
        .query_map(&[(":snapshot_height", &(snapshot_height as i64))], |row| {
            let diversifier: Vec<u8> = row.get(0)?;
            let value: i64 = row.get(1)?;
            let rho: Vec<u8> = row.get(2)?;
            let rseed: Vec<u8> = row.get(3)?;
            let nf: Vec<u8> = row.get(4)?;
            let position: i64 = row.get(5)?;
            let scope_code: i32 = row.get(6)?;
            let ufvk_str: String = row.get(7)?;
            Ok((
                diversifier,
                value,
                rho,
                rseed,
                nf,
                position,
                scope_code,
                ufvk_str,
            ))
        })
        .map_err(|e| VotingError::Internal {
            message: format!("query failed: {e}"),
        })?;

    let mut notes = Vec::new();

    for row_result in rows {
        let (
            diversifier_bytes,
            value,
            rho_bytes,
            rseed_bytes,
            nf_bytes,
            position,
            scope_code,
            ufvk_str,
        ) = row_result.map_err(|e| VotingError::Internal {
            message: format!("row read error: {e}"),
        })?;

        let cmx = compute_cmx(
            &ufvk_str,
            scope_code,
            &diversifier_bytes,
            value as u64,
            &rho_bytes,
            &rseed_bytes,
            &network,
        )?;

        notes.push(NoteInfo {
            commitment: cmx.to_vec(),
            nullifier: nf_bytes,
            value: value as u64,
            position: position as u64,
        });
    }

    Ok(notes)
}

/// Reconstruct an Orchard note from its stored parts and compute the
/// extracted note commitment (cmx).
fn compute_cmx(
    ufvk_str: &str,
    scope_code: i32,
    diversifier_bytes: &[u8],
    value: u64,
    rho_bytes: &[u8],
    rseed_bytes: &[u8],
    network: &Network,
) -> Result<[u8; 32], VotingError> {
    // Parse UFVK and extract Orchard FVK
    let ufvk =
        UnifiedFullViewingKey::decode(network, ufvk_str).map_err(|e| VotingError::Internal {
            message: format!("failed to decode UFVK: {e}"),
        })?;

    let fvk = ufvk.orchard().ok_or_else(|| VotingError::Internal {
        message: "UFVK has no Orchard component".into(),
    })?;

    // Map scope code to zip32::Scope
    let scope = match scope_code {
        0 => Scope::External,
        1 => Scope::Internal,
        _ => {
            return Err(VotingError::Internal {
                message: format!("unexpected scope code: {scope_code}"),
            })
        }
    };

    // Parse diversifier (11 bytes)
    let diversifier_arr: [u8; 11] =
        diversifier_bytes
            .try_into()
            .map_err(|_| VotingError::Internal {
                message: format!(
                    "diversifier must be 11 bytes, got {}",
                    diversifier_bytes.len()
                ),
            })?;
    let diversifier = Diversifier::from_bytes(diversifier_arr);

    // Derive the recipient address from FVK + scope + diversifier
    let address = fvk.address(diversifier, scope);

    // Parse rho (32 bytes)
    let rho_arr: [u8; 32] = rho_bytes.try_into().map_err(|_| VotingError::Internal {
        message: format!("rho must be 32 bytes, got {}", rho_bytes.len()),
    })?;
    let rho: Rho = ct_option_to_result(Rho::from_bytes(&rho_arr), "invalid rho bytes")?;

    // Parse rseed (32 bytes, requires rho)
    let rseed_arr: [u8; 32] = rseed_bytes.try_into().map_err(|_| VotingError::Internal {
        message: format!("rseed must be 32 bytes, got {}", rseed_bytes.len()),
    })?;
    let rseed: RandomSeed = ct_option_to_result(
        RandomSeed::from_bytes(rseed_arr, &rho),
        "invalid rseed bytes",
    )?;

    // Reconstruct the note
    let note_value = NoteValue::from_raw(value);
    let note = ct_option_to_result(
        orchard::Note::from_parts(address, note_value, rho, rseed),
        "failed to reconstruct note from parts",
    )?;

    // Extract commitment (cmx)
    let cmx: ExtractedNoteCommitment = note.commitment().into();
    Ok(cmx.to_bytes())
}

fn ct_option_to_result<T>(opt: CtOption<T>, msg: &str) -> Result<T, VotingError> {
    if opt.is_some().into() {
        Ok(opt.unwrap())
    } else {
        Err(VotingError::Internal {
            message: msg.to_string(),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_empty_wallet_returns_no_notes() {
        // We can't use get_wallet_notes_at_snapshot with in-memory DB directly
        // because it opens by path. Instead, test the query logic by creating
        // a temp file.
        let dir = std::env::temp_dir();
        let path = dir.join("test_empty_wallet.sqlite3");
        let _ = std::fs::remove_file(&path);

        let conn = Connection::open(&path).unwrap();
        conn.execute_batch(
            "CREATE TABLE accounts (id INTEGER PRIMARY KEY, ufvk TEXT);
             CREATE TABLE transactions (id_tx INTEGER PRIMARY KEY, mined_height INTEGER);
             CREATE TABLE orchard_received_notes (
                 id INTEGER PRIMARY KEY,
                 transaction_id INTEGER NOT NULL,
                 action_index INTEGER NOT NULL,
                 account_id INTEGER NOT NULL,
                 diversifier BLOB NOT NULL,
                 value INTEGER NOT NULL,
                 rho BLOB NOT NULL,
                 rseed BLOB NOT NULL,
                 nf BLOB,
                 is_change INTEGER NOT NULL,
                 memo BLOB,
                 commitment_tree_position INTEGER,
                 recipient_key_scope INTEGER
             );
             CREATE TABLE orchard_received_note_spends (
                 orchard_received_note_id INTEGER NOT NULL,
                 transaction_id INTEGER NOT NULL
             );",
        )
        .unwrap();
        drop(conn);

        let notes = get_wallet_notes_at_snapshot(path.to_str().unwrap(), 1000, 0).unwrap();
        assert!(notes.is_empty());

        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn test_spent_note_excluded() {
        let dir = std::env::temp_dir();
        let path = dir.join("test_spent_note.sqlite3");
        let _ = std::fs::remove_file(&path);

        let conn = Connection::open(&path).unwrap();
        conn.execute_batch(
            "CREATE TABLE accounts (id INTEGER PRIMARY KEY, ufvk TEXT);
             CREATE TABLE transactions (id_tx INTEGER PRIMARY KEY, mined_height INTEGER);
             CREATE TABLE orchard_received_notes (
                 id INTEGER PRIMARY KEY,
                 transaction_id INTEGER NOT NULL,
                 action_index INTEGER NOT NULL,
                 account_id INTEGER NOT NULL,
                 diversifier BLOB NOT NULL,
                 value INTEGER NOT NULL,
                 rho BLOB NOT NULL,
                 rseed BLOB NOT NULL,
                 nf BLOB,
                 is_change INTEGER NOT NULL,
                 memo BLOB,
                 commitment_tree_position INTEGER,
                 recipient_key_scope INTEGER
             );
             CREATE TABLE orchard_received_note_spends (
                 orchard_received_note_id INTEGER NOT NULL,
                 transaction_id INTEGER NOT NULL
             );",
        )
        .unwrap();

        // Insert an account with a dummy UFVK (note: this UFVK won't parse,
        // but the note should be filtered out by the spend before we try to parse it)
        conn.execute(
            "INSERT INTO accounts (id, ufvk) VALUES (1, 'dummy_ufvk')",
            [],
        )
        .unwrap();

        // Received at height 500
        conn.execute(
            "INSERT INTO transactions (id_tx, mined_height) VALUES (1, 500)",
            [],
        )
        .unwrap();

        // Spent at height 800
        conn.execute(
            "INSERT INTO transactions (id_tx, mined_height) VALUES (2, 800)",
            [],
        )
        .unwrap();

        conn.execute(
            "INSERT INTO orchard_received_notes
             (id, transaction_id, action_index, account_id, diversifier, value, rho, rseed,
              nf, is_change, commitment_tree_position, recipient_key_scope)
             VALUES (1, 1, 0, 1, X'00000000000000000000FF', 1000000,
                     zeroblob(32), zeroblob(32), zeroblob(32), 0, 42, 0)",
            [],
        )
        .unwrap();

        // Mark note as spent at height 800
        conn.execute(
            "INSERT INTO orchard_received_note_spends
             (orchard_received_note_id, transaction_id) VALUES (1, 2)",
            [],
        )
        .unwrap();
        drop(conn);

        // Snapshot at height 1000 — note was spent at 800, so should be excluded
        let notes = get_wallet_notes_at_snapshot(path.to_str().unwrap(), 1000, 0).unwrap();
        assert!(notes.is_empty(), "spent note should be excluded");

        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn test_note_after_snapshot_excluded() {
        let dir = std::env::temp_dir();
        let path = dir.join("test_future_note.sqlite3");
        let _ = std::fs::remove_file(&path);

        let conn = Connection::open(&path).unwrap();
        conn.execute_batch(
            "CREATE TABLE accounts (id INTEGER PRIMARY KEY, ufvk TEXT);
             CREATE TABLE transactions (id_tx INTEGER PRIMARY KEY, mined_height INTEGER);
             CREATE TABLE orchard_received_notes (
                 id INTEGER PRIMARY KEY,
                 transaction_id INTEGER NOT NULL,
                 action_index INTEGER NOT NULL,
                 account_id INTEGER NOT NULL,
                 diversifier BLOB NOT NULL,
                 value INTEGER NOT NULL,
                 rho BLOB NOT NULL,
                 rseed BLOB NOT NULL,
                 nf BLOB,
                 is_change INTEGER NOT NULL,
                 memo BLOB,
                 commitment_tree_position INTEGER,
                 recipient_key_scope INTEGER
             );
             CREATE TABLE orchard_received_note_spends (
                 orchard_received_note_id INTEGER NOT NULL,
                 transaction_id INTEGER NOT NULL
             );",
        )
        .unwrap();

        conn.execute(
            "INSERT INTO accounts (id, ufvk) VALUES (1, 'dummy_ufvk')",
            [],
        )
        .unwrap();

        // Received at height 1500 (after snapshot)
        conn.execute(
            "INSERT INTO transactions (id_tx, mined_height) VALUES (1, 1500)",
            [],
        )
        .unwrap();

        conn.execute(
            "INSERT INTO orchard_received_notes
             (id, transaction_id, action_index, account_id, diversifier, value, rho, rseed,
              nf, is_change, commitment_tree_position, recipient_key_scope)
             VALUES (1, 1, 0, 1, X'00000000000000000000FF', 1000000,
                     zeroblob(32), zeroblob(32), zeroblob(32), 0, 42, 0)",
            [],
        )
        .unwrap();
        drop(conn);

        // Snapshot at height 1000 — note received at 1500 should be excluded
        let notes = get_wallet_notes_at_snapshot(path.to_str().unwrap(), 1000, 0).unwrap();
        assert!(notes.is_empty(), "future note should be excluded");

        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn test_note_spent_after_snapshot_included() {
        // A note received before snapshot but spent AFTER snapshot should be included
        // (it was unspent at snapshot time).
        // We can't do a full end-to-end test without a real UFVK, so we test
        // that the query returns the row (it will fail at cmx computation with dummy UFVK).
        let dir = std::env::temp_dir();
        let path = dir.join("test_spent_after_snapshot.sqlite3");
        let _ = std::fs::remove_file(&path);

        let conn = Connection::open(&path).unwrap();
        conn.execute_batch(
            "CREATE TABLE accounts (id INTEGER PRIMARY KEY, ufvk TEXT);
             CREATE TABLE transactions (id_tx INTEGER PRIMARY KEY, mined_height INTEGER);
             CREATE TABLE orchard_received_notes (
                 id INTEGER PRIMARY KEY,
                 transaction_id INTEGER NOT NULL,
                 action_index INTEGER NOT NULL,
                 account_id INTEGER NOT NULL,
                 diversifier BLOB NOT NULL,
                 value INTEGER NOT NULL,
                 rho BLOB NOT NULL,
                 rseed BLOB NOT NULL,
                 nf BLOB,
                 is_change INTEGER NOT NULL,
                 memo BLOB,
                 commitment_tree_position INTEGER,
                 recipient_key_scope INTEGER
             );
             CREATE TABLE orchard_received_note_spends (
                 orchard_received_note_id INTEGER NOT NULL,
                 transaction_id INTEGER NOT NULL
             );",
        )
        .unwrap();

        conn.execute(
            "INSERT INTO accounts (id, ufvk) VALUES (1, 'dummy_ufvk')",
            [],
        )
        .unwrap();

        // Received at height 500
        conn.execute(
            "INSERT INTO transactions (id_tx, mined_height) VALUES (1, 500)",
            [],
        )
        .unwrap();

        // Spent at height 1500 (after snapshot of 1000)
        conn.execute(
            "INSERT INTO transactions (id_tx, mined_height) VALUES (2, 1500)",
            [],
        )
        .unwrap();

        conn.execute(
            "INSERT INTO orchard_received_notes
             (id, transaction_id, action_index, account_id, diversifier, value, rho, rseed,
              nf, is_change, commitment_tree_position, recipient_key_scope)
             VALUES (1, 1, 0, 1, X'00000000000000000000FF', 1000000,
                     zeroblob(32), zeroblob(32), zeroblob(32), 0, 42, 0)",
            [],
        )
        .unwrap();

        conn.execute(
            "INSERT INTO orchard_received_note_spends
             (orchard_received_note_id, transaction_id) VALUES (1, 2)",
            [],
        )
        .unwrap();
        drop(conn);

        // Snapshot at 1000 — note was spent at 1500 (after snapshot) so it should be included.
        // This will error at cmx computation (dummy UFVK), but that confirms the SQL
        // correctly returns the row.
        let result = get_wallet_notes_at_snapshot(path.to_str().unwrap(), 1000, 0);
        assert!(
            result.is_err(),
            "should fail at UFVK decode (proves the row was returned by the query)"
        );
        let err_msg = result.unwrap_err().to_string();
        assert!(
            err_msg.contains("UFVK") || err_msg.contains("ufvk") || err_msg.contains("decode"),
            "error should be about UFVK decode, got: {err_msg}"
        );

        let _ = std::fs::remove_file(&path);
    }
}
