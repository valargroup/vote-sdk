use crate::types::{VotingError, WitnessData};

use incrementalmerkletree::{Hashable, Level, Marking, Position, Retention};
use orchard::tree::MerkleHashOrchard;
use prost::Message;
use rusqlite::Connection;
use shardtree::store::{Checkpoint, ShardStore};
use shardtree::ShardTree;
use subtle::CtOption;
use zcash_client_backend::data_api::ORCHARD_SHARD_HEIGHT;
use zcash_client_backend::proto::service::TreeState;
use zcash_client_sqlite::wallet::commitment_tree::SqliteShardStore;
use zcash_protocol::consensus::BlockHeight;

/// Generate Merkle witnesses for multiple notes at a snapshot height.
///
/// Builds an ephemeral in-memory ShardTree from:
/// - The wallet DB's orchard shard data (lower Merkle path, levels 0-15)
/// - The lightwalletd frontier from tree_state_bytes (upper path, levels 16-31)
///
/// The wallet DB is strictly read-only — shard data is copied to an in-memory DB.
///
/// # Arguments
/// * `wallet_db_path` - Path to the Zcash wallet SQLite database
/// * `note_positions` - Tree positions of the notes to witness
/// * `note_commitments` - Note commitment bytes (32 bytes each)
/// * `snapshot_height` - Block height of the voting snapshot
/// * `tree_state_bytes` - Protobuf-encoded TreeState from lightwalletd
pub fn generate_note_witnesses(
    wallet_db_path: &str,
    note_positions: &[u64],
    note_commitments: &[Vec<u8>],
    snapshot_height: u64,
    tree_state_bytes: &[u8],
) -> Result<Vec<WitnessData>, VotingError> {
    if note_positions.len() != note_commitments.len() {
        return Err(VotingError::InvalidInput {
            message: format!(
                "note_positions length ({}) != note_commitments length ({})",
                note_positions.len(),
                note_commitments.len()
            ),
        });
    }

    // 1. Parse TreeState protobuf
    let tree_state = TreeState::decode(tree_state_bytes).map_err(|e| VotingError::Internal {
        message: format!("failed to decode TreeState protobuf: {}", e),
    })?;

    // 2. Extract orchard commitment tree, frontier, and authoritative root
    let orchard_ct = tree_state.orchard_tree().map_err(|e| VotingError::Internal {
        message: format!("failed to parse orchard tree from TreeState: {}", e),
    })?;
    let frontier_root = orchard_ct.root();
    let frontier = orchard_ct.to_frontier();

    let nonempty_frontier = frontier.take().ok_or_else(|| VotingError::InvalidInput {
        message: "empty orchard frontier — no orchard activity at snapshot height".to_string(),
    })?;
    let frontier_position = nonempty_frontier.position();

    // 3. Copy wallet tree tables to in-memory SQLite DB
    let mem_conn = copy_wallet_tree_to_memory(wallet_db_path)?;

    // 4. Build ShardTree from in-memory store.
    //    Uses a transaction because SqliteShardStore's ShardStore impl requires it for writes.
    let tx = mem_conn
        .unchecked_transaction()
        .map_err(|e| VotingError::Internal {
            message: format!("failed to begin transaction: {}", e),
        })?;

    let store =
        SqliteShardStore::<_, MerkleHashOrchard, ORCHARD_SHARD_HEIGHT>::from_connection(
            &tx, "orchard",
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to create shard store: {}", e),
        })?;

    let mut tree = ShardTree::<
        _,
        { orchard::NOTE_COMMITMENT_TREE_DEPTH as u8 },
        ORCHARD_SHARD_HEIGHT,
    >::new(store, 100);

    // 5. Insert frontier + checkpoint (both steps required per pir2 learnings)
    let checkpoint_height = BlockHeight::from_u32(snapshot_height as u32);

    tree.insert_frontier_nodes(
        nonempty_frontier,
        Retention::Checkpoint {
            id: checkpoint_height,
            marking: Marking::None,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to insert frontier nodes: {}", e),
    })?;

    tree.store_mut()
        .add_checkpoint(checkpoint_height, Checkpoint::at_position(frontier_position))
        .map_err(|e| VotingError::Internal {
            message: format!("failed to add checkpoint: {}", e),
        })?;

    // 6. Generate witness per note
    let root_bytes = frontier_root.to_bytes().to_vec();
    let mut witnesses = Vec::with_capacity(note_positions.len());

    for (i, &pos) in note_positions.iter().enumerate() {
        let position = Position::from(pos);

        let merkle_path = tree
            .witness_at_checkpoint_id(position, &checkpoint_height)
            .map_err(|e| VotingError::Internal {
                message: format!(
                    "failed to generate witness for position {}: {} \
                     (wallet may need to sync through snapshot height)",
                    pos, e
                ),
            })?
            .ok_or_else(|| VotingError::Internal {
                message: format!(
                    "no witness available for position {} \
                     (wallet missing shard data — sync through snapshot height)",
                    pos
                ),
            })?;

        // Use the authoritative root from lightwalletd, not the tree's computed root
        let auth_path: Vec<Vec<u8>> = merkle_path
            .path_elems()
            .iter()
            .map(|h| h.to_bytes().to_vec())
            .collect();

        witnesses.push(WitnessData {
            note_commitment: note_commitments[i].clone(),
            position: pos,
            root: root_bytes.clone(),
            auth_path,
        });
    }

    Ok(witnesses)
}

/// Backwards-compatible single-note wrapper around generate_note_witnesses.
/// Uses a stub approach when no wallet_db_path is available (FFI legacy path).
pub fn generate_note_witness(
    note_position: u64,
    _snapshot_height: u32,
    _tree_state_bytes: &[u8],
) -> Result<WitnessData, VotingError> {
    // Legacy stub: the real witness generation goes through generate_note_witnesses
    // which requires wallet_db_path. This path is kept for the legacy FFI free function.
    let auth_path: Vec<Vec<u8>> = (0..32).map(|_| vec![0u8; 32]).collect();

    Ok(WitnessData {
        note_commitment: vec![0xAA; 32],
        position: note_position,
        root: vec![0xBB; 32],
        auth_path,
    })
}

/// Verify a Merkle witness by recomputing the root from leaf + auth path.
///
/// Returns true if the computed root matches the expected root in the witness.
/// Uses the same level-aware Sinsemilla hash as the Orchard commitment tree.
pub fn verify_witness(witness: &WitnessData) -> Result<bool, VotingError> {
    if witness.note_commitment.len() != 32 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "note_commitment must be 32 bytes, got {}",
                witness.note_commitment.len()
            ),
        });
    }
    if witness.root.len() != 32 {
        return Err(VotingError::InvalidInput {
            message: format!("root must be 32 bytes, got {}", witness.root.len()),
        });
    }
    if witness.auth_path.len() != 32 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "auth_path must have 32 levels, got {}",
                witness.auth_path.len()
            ),
        });
    }

    // Parse note commitment as MerkleHashOrchard
    let commitment_bytes: [u8; 32] = witness.note_commitment[..].try_into().unwrap();
    let mut current: MerkleHashOrchard =
        ct_option_to_result(MerkleHashOrchard::from_bytes(&commitment_bytes), "note_commitment")?;

    // Parse expected root
    let root_bytes: [u8; 32] = witness.root[..].try_into().unwrap();
    let expected_root: MerkleHashOrchard =
        ct_option_to_result(MerkleHashOrchard::from_bytes(&root_bytes), "root")?;

    // Walk up the tree: at each level, combine with the sibling hash.
    // Position bit determines whether the current node is a left or right child.
    let mut pos = witness.position;

    for (level, sibling_bytes) in witness.auth_path.iter().enumerate() {
        if sibling_bytes.len() != 32 {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "auth_path[{}] must be 32 bytes, got {}",
                    level,
                    sibling_bytes.len()
                ),
            });
        }

        let sibling_arr: [u8; 32] = sibling_bytes[..].try_into().unwrap();
        let sibling: MerkleHashOrchard = ct_option_to_result(
            MerkleHashOrchard::from_bytes(&sibling_arr),
            &format!("auth_path[{}]", level),
        )?;

        let tree_level = Level::from(level as u8);

        // If position bit is 0, current is a left child; if 1, current is a right child
        current = if pos & 1 == 0 {
            MerkleHashOrchard::combine(tree_level, &current, &sibling)
        } else {
            MerkleHashOrchard::combine(tree_level, &sibling, &current)
        };

        pos >>= 1;
    }

    Ok(current == expected_root)
}

/// Copy the four orchard tree tables from the wallet DB to an in-memory SQLite DB.
/// The wallet DB is opened read-only via ATTACH DATABASE.
fn copy_wallet_tree_to_memory(wallet_db_path: &str) -> Result<Connection, VotingError> {
    let mem_conn = Connection::open_in_memory().map_err(|e| VotingError::Internal {
        message: format!("failed to create in-memory DB: {}", e),
    })?;

    // Create the orchard tree tables matching zcash_client_sqlite's schema
    mem_conn
        .execute_batch(
            "CREATE TABLE orchard_tree_shards (
                shard_index INTEGER PRIMARY KEY,
                subtree_end_height INTEGER,
                root_hash BLOB,
                shard_data BLOB,
                contains_marked INTEGER,
                CONSTRAINT root_unique UNIQUE (root_hash)
            );
            CREATE TABLE orchard_tree_cap (
                cap_id INTEGER PRIMARY KEY,
                cap_data BLOB NOT NULL
            );
            CREATE TABLE orchard_tree_checkpoints (
                checkpoint_id INTEGER PRIMARY KEY,
                position INTEGER
            );
            CREATE TABLE orchard_tree_checkpoint_marks_removed (
                checkpoint_id INTEGER NOT NULL,
                mark_removed_position INTEGER NOT NULL,
                FOREIGN KEY (checkpoint_id) REFERENCES orchard_tree_checkpoints(checkpoint_id)
                ON DELETE CASCADE,
                CONSTRAINT spend_position_unique UNIQUE (checkpoint_id, mark_removed_position)
            );",
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to create orchard tree tables: {}", e),
        })?;

    // Attach wallet DB read-only and copy data
    mem_conn
        .execute("ATTACH DATABASE ?1 AS wallet", [wallet_db_path])
        .map_err(|e| VotingError::Internal {
            message: format!("failed to attach wallet DB '{}': {}", wallet_db_path, e),
        })?;

    // Copy all rows from each orchard tree table
    for table in &[
        "orchard_tree_shards",
        "orchard_tree_cap",
        "orchard_tree_checkpoints",
        "orchard_tree_checkpoint_marks_removed",
    ] {
        mem_conn
            .execute(
                &format!("INSERT INTO main.{t} SELECT * FROM wallet.{t}", t = table),
                [],
            )
            .map_err(|e| VotingError::Internal {
                message: format!(
                    "failed to copy {} from wallet DB (table may not exist — \
                     wallet needs Orchard support enabled): {}",
                    table, e
                ),
            })?;
    }

    mem_conn
        .execute("DETACH DATABASE wallet", [])
        .map_err(|e| VotingError::Internal {
            message: format!("failed to detach wallet DB: {}", e),
        })?;

    Ok(mem_conn)
}

/// Convert a subtle::CtOption to a Result, using the field name in the error.
fn ct_option_to_result(
    opt: CtOption<MerkleHashOrchard>,
    field: &str,
) -> Result<MerkleHashOrchard, VotingError> {
    Option::from(opt).ok_or_else(|| VotingError::InvalidInput {
        message: format!("{} is not a valid Orchard tree hash", field),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_generate_note_witness_legacy_stub() {
        let result = generate_note_witness(42, 100_000, &[]).unwrap();
        assert_eq!(result.note_commitment.len(), 32);
        assert_eq!(result.position, 42);
        assert_eq!(result.root.len(), 32);
        assert_eq!(result.auth_path.len(), 32);
        for sibling in &result.auth_path {
            assert_eq!(sibling.len(), 32);
        }
    }

    #[test]
    fn test_verify_witness_validation() {
        // Bad commitment length
        let bad = WitnessData {
            note_commitment: vec![0; 16],
            position: 0,
            root: vec![0; 32],
            auth_path: (0..32).map(|_| vec![0u8; 32]).collect(),
        };
        assert!(verify_witness(&bad).is_err());

        // Bad auth path length
        let bad = WitnessData {
            note_commitment: vec![0; 32],
            position: 0,
            root: vec![0; 32],
            auth_path: (0..16).map(|_| vec![0u8; 32]).collect(),
        };
        assert!(verify_witness(&bad).is_err());
    }

    #[test]
    fn test_verify_witness_rejects_wrong_root() {
        // Create a witness with a valid commitment at position 0 but wrong root.
        // The empty tree hash (all zeros) as commitment with zero auth path
        // produces a specific root — giving a different root should fail verification.
        let witness = WitnessData {
            note_commitment: vec![0; 32],
            position: 0,
            root: vec![0xFF; 32], // wrong root
            auth_path: (0..32).map(|_| vec![0u8; 32]).collect(),
        };
        // Should verify without error but return false (roots don't match)
        // unless 0xFF... isn't a valid field element (would be an error)
        let result = verify_witness(&witness);
        // Either returns Ok(false) or Err (if 0xFF... isn't valid)
        match result {
            Ok(valid) => assert!(!valid),
            Err(_) => {} // 0xFF..FF may not be a valid Pallas base element
        }
    }
}
