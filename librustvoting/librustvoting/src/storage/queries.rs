use rusqlite::{named_params, Connection};

use crate::storage::{RoundPhase, RoundState, RoundSummary, VoteRecord};
use crate::types::{VotingError, VotingRoundParams};

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

    // proof_generated is true only when ALL bundles have a successful proof
    // AND all bundles have a VAN leaf position (delegation TX landed on chain).
    // This prevents the UI from treating delegation as complete before the
    // on-chain submission finishes.
    let bundle_count: i64 = conn
        .query_row(
            "SELECT COUNT(*) FROM bundles WHERE round_id = :round_id",
            named_params! { ":round_id": round_id },
            |row| row.get(0),
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to count bundles: {}", e),
        })?;

    let proof_generated = if bundle_count == 0 {
        false
    } else {
        let proofs_count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM proofs WHERE round_id = :round_id AND success = 1",
                named_params! { ":round_id": round_id },
                |row| row.get(0),
            )
            .map_err(|e| VotingError::Internal {
                message: format!("failed to count proofs: {}", e),
            })?;

        let van_positions_count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM bundles WHERE round_id = :round_id AND van_leaf_position IS NOT NULL",
                named_params! { ":round_id": round_id },
                |row| row.get(0),
            )
            .map_err(|e| VotingError::Internal {
                message: format!("failed to count VAN positions: {}", e),
            })?;

        proofs_count >= bundle_count && van_positions_count >= bundle_count
    };

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

/// Delete a round and all associated data. Child tables (bundles, cached_tree_state,
/// proofs, witnesses, votes) are removed automatically via ON DELETE CASCADE.
pub fn clear_round(conn: &Connection, round_id: &str) -> Result<(), VotingError> {
    conn.execute(
        "DELETE FROM rounds WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to clear round: {}", e),
    })?;
    Ok(())
}

// --- Bundles ---

/// Insert a bundle row. `note_positions` is stored as a flat blob of u64 LE values.
pub fn insert_bundle(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
    note_positions: &[u64],
) -> Result<(), VotingError> {
    let blob: Vec<u8> = note_positions
        .iter()
        .flat_map(|p| p.to_le_bytes())
        .collect();

    conn.execute(
        "INSERT INTO bundles (round_id, bundle_index, note_positions_blob)
         VALUES (:round_id, :bundle_index, :note_positions_blob)",
        named_params! {
            ":round_id": round_id,
            ":bundle_index": bundle_index as i64,
            ":note_positions_blob": blob,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to insert bundle: {}", e),
    })?;

    Ok(())
}

/// Get the number of bundles for a round.
pub fn get_bundle_count(conn: &Connection, round_id: &str) -> Result<u32, VotingError> {
    conn.query_row(
        "SELECT COUNT(*) FROM bundles WHERE round_id = :round_id",
        named_params! { ":round_id": round_id },
        |row| row.get::<_, i64>(0).map(|c| c as u32),
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to get bundle count: {}", e),
    })
}

/// Load the note positions for a specific bundle.
pub fn load_bundle_note_positions(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
) -> Result<Vec<u64>, VotingError> {
    let blob: Vec<u8> = conn
        .query_row(
            "SELECT note_positions_blob FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
            named_params! {
                ":round_id": round_id,
                ":bundle_index": bundle_index as i64,
            },
            |row| row.get(0),
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!("bundle not found: round={}, bundle={} ({})", round_id, bundle_index, e),
        })?;

    if blob.len() % 8 != 0 {
        return Err(VotingError::Internal {
            message: format!(
                "corrupt note_positions_blob: length {} is not a multiple of 8",
                blob.len()
            ),
        });
    }
    Ok(blob
        .chunks_exact(8)
        .map(|c| u64::from_le_bytes(c.try_into().expect("chunks_exact(8) guarantees 8 bytes")))
        .collect())
}

// --- Delegation Secrets ---
//
// After build_governance_pczt computes the VAN (governance commitment),
// we persist two values needed for later proof steps:
//   - van_comm_rand: the 32-byte blinding factor used in the VAN Poseidon hash.
//     Needed again in ZKP #2 (vote commitment) to reconstruct the VAN as a witness.
//   - dummy_nullifiers: random nullifiers generated for padded note slots (§1.3.5).
//     Each is 32 bytes. Stored so the witness builder can reconstruct padded notes.

/// Persist all delegation action data in a single UPDATE on the bundles table:
/// blinding factor, dummy nullifiers, constrained rho, padded note cmx values,
/// signed action fields (nf_signed, cmx_new, alpha), note rseeds,
/// VAN leaf value, total note value, and address index.
pub fn store_delegation_data(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
    van_comm_rand: &[u8],
    dummy_nullifiers: &[Vec<u8>],
    rho_signed: &[u8],
    padded_cmx: &[Vec<u8>],
    nf_signed: &[u8],
    cmx_new: &[u8],
    alpha: &[u8],
    rseed_signed: &[u8],
    rseed_output: &[u8],
    gov_comm: &[u8],
    total_note_value: u64,
    address_index: u32,
    padded_note_secrets: &[(Vec<u8>, Vec<u8>)],
    pczt_sighash: &[u8],
) -> Result<(), VotingError> {
    // Serialize dummy nullifiers as a flat byte blob: [nf0 (32 bytes) | nf1 | nf2 | ...].
    // Length 0 means no padding was needed (all 5 notes were real).
    // Length 32/64/96/128 means 1/2/3/4 dummy notes respectively.
    let dummy_blob: Vec<u8> = dummy_nullifiers
        .iter()
        .flat_map(|n| n.iter().copied())
        .collect();

    // Same flat-blob encoding for padded cmx values.
    let padded_blob: Vec<u8> = padded_cmx.iter().flat_map(|c| c.iter().copied()).collect();

    // Serialize padded_note_secrets as flat blob: N * 64 bytes (rho[32] || rseed[32] per entry).
    let secrets_blob: Vec<u8> = padded_note_secrets
        .iter()
        .flat_map(|(rho, rseed)| rho.iter().copied().chain(rseed.iter().copied()))
        .collect();

    let rows = conn
        .execute(
            "UPDATE bundles SET van_comm_rand = :rand, dummy_nullifiers = :dummies, \
             rho_signed = :rho, padded_note_data = :padded, nf_signed = :nf_signed, \
             cmx_new = :cmx_new, alpha = :alpha, rseed_signed = :rseed_signed, \
             rseed_output = :rseed_output, gov_comm = :gov_comm, \
             total_note_value = :total_note_value, address_index = :address_index, \
             padded_note_secrets = :secrets, pczt_sighash = :sighash \
             WHERE round_id = :round_id AND bundle_index = :bundle_index",
            named_params! {
                ":rand": van_comm_rand,
                ":dummies": dummy_blob,
                ":rho": rho_signed,
                ":padded": padded_blob,
                ":nf_signed": nf_signed,
                ":cmx_new": cmx_new,
                ":alpha": alpha,
                ":rseed_signed": rseed_signed,
                ":rseed_output": rseed_output,
                ":gov_comm": gov_comm,
                ":total_note_value": total_note_value as i64,
                ":address_index": address_index as i64,
                ":secrets": secrets_blob,
                ":sighash": pczt_sighash,
                ":round_id": round_id,
                ":bundle_index": bundle_index as i64,
            },
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to store delegation data: {}", e),
        })?;

    // If no rows were updated, the bundle doesn't exist.
    if rows == 0 {
        return Err(VotingError::InvalidInput {
            message: format!("bundle not found: round={}, bundle={}", round_id, bundle_index),
        });
    }

    Ok(())
}

/// Load nf_signed (signed note nullifier, 32 bytes) for a bundle.
pub fn load_nf_signed(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT nf_signed FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no nf_signed for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load cmx_new (output note commitment, 32 bytes) for a bundle.
pub fn load_cmx_new(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT cmx_new FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no cmx_new for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load alpha (spend auth randomizer scalar, 32 bytes) for a bundle.
pub fn load_alpha(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT alpha FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no alpha for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load signed note rseed (32 bytes) for a bundle.
pub fn load_rseed_signed(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT rseed_signed FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no rseed_signed for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load output note rseed (32 bytes) for a bundle.
pub fn load_rseed_output(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT rseed_output FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no rseed_output for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load padded note secrets (rho + rseed pairs) for Phase 2 randomness threading.
/// Returns Vec of (rho[32], rseed[32]) pairs. Deserializes from flat 64-byte-per-entry blob.
pub fn load_padded_note_secrets(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
) -> Result<Vec<(Vec<u8>, Vec<u8>)>, VotingError> {
    let blob: Vec<u8> = conn
        .query_row(
            "SELECT padded_note_secrets FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
            named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
            |row| row.get(0),
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!("no padded_note_secrets for round={}, bundle={} ({})", round_id, bundle_index, e),
        })?;

    if blob.len() % 64 != 0 {
        return Err(VotingError::Internal {
            message: format!(
                "corrupt padded_note_secrets blob: length {} is not a multiple of 64",
                blob.len()
            ),
        });
    }
    Ok(blob
        .chunks_exact(64)
        .map(|c| (c[..32].to_vec(), c[32..].to_vec()))
        .collect())
}

/// Load the ZIP-244 sighash extracted from the PCZT (32 bytes).
pub fn load_pczt_sighash(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT pczt_sighash FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no pczt_sighash for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load the VAN blinding factor for a bundle. Needed as a private witness in ZKP #2.
pub fn load_van_comm_rand(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT van_comm_rand FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no van_comm_rand for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load dummy nullifiers for padded note slots. Returns 0-3 entries of 32 bytes each.
/// Deserializes the flat blob back into individual 32-byte nullifiers.
pub fn load_dummy_nullifiers(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
) -> Result<Vec<Vec<u8>>, VotingError> {
    let blob: Vec<u8> = conn
        .query_row(
            "SELECT dummy_nullifiers FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
            named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
            |row| row.get(0),
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!("no dummy_nullifiers for round={}, bundle={} ({})", round_id, bundle_index, e),
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

/// Load rho_signed for a bundle (32-byte constrained rho).
pub fn load_rho_signed(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<u8>, VotingError> {
    conn.query_row(
        "SELECT rho_signed FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no rho_signed for round={}, bundle={} ({})", round_id, bundle_index, e),
    })
}

/// Load padded note cmx data. Returns 0-3 entries of 32 bytes each.
pub fn load_padded_cmx(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<Vec<u8>>, VotingError> {
    let blob: Vec<u8> = conn
        .query_row(
            "SELECT padded_note_data FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
            named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
            |row| row.get(0),
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!("no padded_note_data for round={}, bundle={} ({})", round_id, bundle_index, e),
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

// --- ZKP #2 inputs ---

/// Data from delegation that ZKP #2 needs.
pub struct Zkp2DelegationData {
    pub gov_comm_rand: Vec<u8>,
    pub total_note_value: u64,
    pub address_index: u32,
    pub ea_pk: Vec<u8>,
    pub voting_round_id: String,
    /// Current proposal authority bitmask (starts at 0xFFFF, decremented per submitted vote).
    /// Bit `i` is set iff the voter has not yet cast a vote for proposal `i`.
    /// Since proposal IDs are 1-indexed (matching on-chain IDs), bit 0 is never
    /// cleared and acts as a structural invariant — it corresponds to the circuit's
    /// sentinel value rejected by the non-zero gate.
    pub proposal_authority: u64,
}

/// Initial authority bitmask: all 16 bits set. Bit 0 is the dead sentinel
/// (proposal_id=0 is rejected by the circuit); bits 1–15 are the usable slots.
const MAX_PROPOSAL_AUTHORITY: u64 = 65535;

/// Load all fields ZKP #2 needs from the bundles table (persisted during delegation).
/// Computes proposal_authority from submitted votes — each submitted vote clears its
/// proposal's bit, so the next vote's VAN reconstruction matches what's in the VC tree.
pub fn load_zkp2_inputs(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
) -> Result<Zkp2DelegationData, VotingError> {
    let data = conn.query_row(
        "SELECT b.van_comm_rand, b.total_note_value, b.address_index, r.ea_pk, r.round_id \
         FROM bundles b JOIN rounds r ON b.round_id = r.round_id \
         WHERE b.round_id = :round_id AND b.bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| {
            Ok(Zkp2DelegationData {
                gov_comm_rand: row.get(0)?,
                total_note_value: row.get::<_, i64>(1)? as u64,
                address_index: row.get::<_, i64>(2)? as u32,
                ea_pk: row.get(3)?,
                voting_round_id: row.get(4)?,
                proposal_authority: 0, // computed below
            })
        },
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("failed to load ZKP2 inputs for round={}, bundle={} ({})", round_id, bundle_index, e),
    })?;

    // Compute current proposal_authority by clearing bits for already-submitted votes
    // for THIS bundle specifically.
    let mut authority = MAX_PROPOSAL_AUTHORITY;
    let mut stmt = conn
        .prepare("SELECT proposal_id FROM votes WHERE round_id = :round_id AND bundle_index = :bundle_index AND submitted = 1")
        .map_err(|e| VotingError::Internal {
            message: format!("failed to prepare proposal_authority query: {}", e),
        })?;
    let rows = stmt
        .query_map(
            named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
            |row| row.get::<_, i64>(0),
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to query submitted votes: {}", e),
        })?;
    for row in rows {
        let pid = row.map_err(|e| VotingError::Internal {
            message: format!("failed to read proposal_id: {}", e),
        })? as u64;
        authority &= !(1u64 << pid);
    }

    Ok(Zkp2DelegationData {
        proposal_authority: authority,
        ..data
    })
}

// --- VAN leaf position ---

/// Store the VAN leaf position after delegation TX is confirmed on chain.
pub fn store_van_position(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
    position: u32,
) -> Result<(), VotingError> {
    let rows = conn
        .execute(
            "UPDATE bundles SET van_leaf_position = :position WHERE round_id = :round_id AND bundle_index = :bundle_index",
            named_params! {
                ":position": position as i64,
                ":round_id": round_id,
                ":bundle_index": bundle_index as i64,
            },
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to store VAN position: {}", e),
        })?;
    if rows == 0 {
        return Err(VotingError::InvalidInput {
            message: format!("bundle not found: round={}, bundle={}", round_id, bundle_index),
        });
    }
    Ok(())
}

/// Load the VAN leaf position for witness generation.
pub fn load_van_position(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<u32, VotingError> {
    conn.query_row(
        "SELECT van_leaf_position FROM bundles WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get::<_, Option<i64>>(0),
    )
    .map_err(|e| VotingError::InvalidInput {
        message: format!("no van_leaf_position for round={}, bundle={} ({})", round_id, bundle_index, e),
    })?
    .map(|v| v as u32)
    .ok_or_else(|| VotingError::InvalidInput {
        message: format!("van_leaf_position not yet set for round={}, bundle={}", round_id, bundle_index),
    })
}

// --- Delegation proof result fields ---

/// Persist rk and gov_nullifiers from DelegationProofResult after proof generation.
/// These survive the FFI boundary and are needed later for delegation TX submission.
pub fn store_proof_result_fields(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
    rk: &[u8],
    gov_nullifiers: &[Vec<u8>],
    nf_signed: &[u8],
    cmx_new: &[u8],
) -> Result<(), VotingError> {
    // Serialize gov_nullifiers as flat blob: [nf0 (32 bytes) | nf1 | nf2 | nf3]
    let gov_nullifiers_blob: Vec<u8> = gov_nullifiers
        .iter()
        .flat_map(|n| n.iter().copied())
        .collect();

    let rows = conn
        .execute(
            "UPDATE bundles SET rk = :rk, gov_nullifiers_blob = :gov_nullifiers_blob, \
             nf_signed = :nf_signed, cmx_new = :cmx_new \
             WHERE round_id = :round_id AND bundle_index = :bundle_index",
            named_params! {
                ":rk": rk,
                ":gov_nullifiers_blob": gov_nullifiers_blob,
                ":nf_signed": nf_signed,
                ":cmx_new": cmx_new,
                ":round_id": round_id,
                ":bundle_index": bundle_index as i64,
            },
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to store proof result fields: {}", e),
        })?;

    if rows == 0 {
        return Err(VotingError::InvalidInput {
            message: format!("bundle not found: round={}, bundle={}", round_id, bundle_index),
        });
    }

    Ok(())
}

/// Raw delegation data loaded from DB for submission reconstruction.
pub struct DelegationDbFields {
    pub proof: Vec<u8>,
    pub rk: Vec<u8>,
    pub nf_signed: Vec<u8>,
    pub cmx_new: Vec<u8>,
    pub gov_comm: Vec<u8>,
    pub gov_nullifiers: Vec<Vec<u8>>,
    pub alpha: Vec<u8>,
    pub vote_round_id: String,
}

/// Load all fields needed to reconstruct the chain-ready delegation TX payload.
pub fn load_delegation_submission_data(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
) -> Result<DelegationDbFields, VotingError> {
    let (proof_bytes, rk, nf_signed, cmx_new, gov_comm, gov_nullifiers_blob, alpha, vote_round_id): (
        Vec<u8>, Vec<u8>, Vec<u8>, Vec<u8>, Vec<u8>, Vec<u8>, Vec<u8>, String,
    ) = conn
        .query_row(
            "SELECT p.proof, b.rk, b.nf_signed, b.cmx_new, b.gov_comm, \
             b.gov_nullifiers_blob, b.alpha, b.round_id \
             FROM bundles b JOIN proofs p ON b.round_id = p.round_id AND b.bundle_index = p.bundle_index \
             WHERE b.round_id = :round_id AND b.bundle_index = :bundle_index AND p.success = 1",
            named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
            |row| {
                Ok((
                    row.get(0)?,
                    row.get(1)?,
                    row.get(2)?,
                    row.get(3)?,
                    row.get(4)?,
                    row.get(5)?,
                    row.get(6)?,
                    row.get(7)?,
                ))
            },
        )
        .map_err(|e| VotingError::InvalidInput {
            message: format!(
                "failed to load delegation submission data for round={}, bundle={} ({})",
                round_id, bundle_index, e
            ),
        })?;

    // Deserialize gov_nullifiers from flat blob back to Vec<Vec<u8>>
    if gov_nullifiers_blob.len() % 32 != 0 {
        return Err(VotingError::Internal {
            message: format!(
                "corrupt gov_nullifiers_blob: length {} is not a multiple of 32",
                gov_nullifiers_blob.len()
            ),
        });
    }
    let gov_nullifiers: Vec<Vec<u8>> = gov_nullifiers_blob
        .chunks_exact(32)
        .map(|c| c.to_vec())
        .collect();

    Ok(DelegationDbFields {
        proof: proof_bytes,
        rk,
        nf_signed,
        cmx_new,
        gov_comm,
        gov_nullifiers,
        alpha,
        vote_round_id,
    })
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

/// Check if witnesses are already cached for a bundle.
pub fn has_witnesses(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<bool, VotingError> {
    conn.query_row(
        "SELECT COUNT(*) FROM witnesses WHERE round_id = :round_id AND bundle_index = :bundle_index",
        named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
        |row| row.get::<_, i64>(0).map(|c| c > 0),
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to check witnesses: {}", e),
    })
}

/// Store witness data for multiple notes in a bundle.
/// Each WitnessData's auth_path (Vec<Vec<u8>>) is serialized as a flat 1024-byte blob
/// (32 levels × 32 bytes each).
pub fn store_witnesses(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
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
            "INSERT OR REPLACE INTO witnesses (round_id, bundle_index, note_position, note_commitment, root, auth_path, created_at)
             VALUES (:round_id, :bundle_index, :position, :commitment, :root, :auth_path, :created_at)",
            named_params! {
                ":round_id": round_id,
                ":bundle_index": bundle_index as i64,
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

/// Load cached witnesses for a bundle, ordered by position.
pub fn load_witnesses(conn: &Connection, round_id: &str, bundle_index: u32) -> Result<Vec<crate::types::WitnessData>, VotingError> {
    let mut stmt = conn
        .prepare(
            "SELECT note_position, note_commitment, root, auth_path FROM witnesses
             WHERE round_id = :round_id AND bundle_index = :bundle_index ORDER BY note_position",
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to prepare load_witnesses: {}", e),
        })?;

    let witnesses = stmt
        .query_map(
            named_params! { ":round_id": round_id, ":bundle_index": bundle_index as i64 },
            |row| {
                let position: i64 = row.get(0)?;
                let note_commitment: Vec<u8> = row.get(1)?;
                let root: Vec<u8> = row.get(2)?;
                let auth_blob: Vec<u8> = row.get(3)?;
                Ok((position as u64, note_commitment, root, auth_blob))
            },
        )
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

pub fn store_proof(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
    proof_bytes: &[u8],
) -> Result<(), VotingError> {
    conn.execute(
        "INSERT INTO proofs (round_id, bundle_index, proof, success, created_at)
         VALUES (:round_id, :bundle_index, :proof, 1, strftime('%s','now'))
         ON CONFLICT(round_id, bundle_index) DO UPDATE SET proof = :proof, success = 1",
        named_params! {
            ":proof": proof_bytes,
            ":round_id": round_id,
            ":bundle_index": bundle_index as i64,
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
    bundle_index: u32,
    proposal_id: u32,
    choice: u32,
    commitment: &[u8],
) -> Result<(), VotingError> {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    conn.execute(
        "INSERT OR REPLACE INTO votes (round_id, bundle_index, proposal_id, choice, commitment, submitted, created_at)
         VALUES (:round_id, :bundle_index, :proposal_id, :choice, :commitment, 0, :created_at)",
        named_params! {
            ":round_id": round_id,
            ":bundle_index": bundle_index as i64,
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

/// Get all votes for a round (across all bundles).
pub fn get_votes(conn: &Connection, round_id: &str) -> Result<Vec<VoteRecord>, VotingError> {
    let mut stmt = conn
        .prepare("SELECT proposal_id, bundle_index, choice, submitted FROM votes WHERE round_id = :round_id")
        .map_err(|e| VotingError::Internal {
            message: format!("failed to prepare get_votes: {}", e),
        })?;

    let votes = stmt
        .query_map(named_params! { ":round_id": round_id }, |row| {
            Ok(VoteRecord {
                proposal_id: row.get::<_, i64>(0)? as u32,
                bundle_index: row.get::<_, i64>(1)? as u32,
                choice: row.get::<_, i64>(2)? as u32,
                submitted: row.get::<_, i64>(3)? != 0,
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

/// Delete all bundles (and their cascaded witnesses/proofs) with index >= `from_index`.
/// Used when the user skips remaining Keystone bundles — we remove the unsigned
/// bundle rows so that `proof_generated` (which counts ALL DB bundles) reflects
/// only the signed+proven bundles.
pub fn delete_bundles_from(
    conn: &Connection,
    round_id: &str,
    from_index: u32,
) -> Result<u64, VotingError> {
    let rows = conn
        .execute(
            "DELETE FROM bundles WHERE round_id = :round_id AND bundle_index >= :from_index",
            named_params! {
                ":round_id": round_id,
                ":from_index": from_index as i64,
            },
        )
        .map_err(|e| VotingError::Internal {
            message: format!("failed to delete bundles from index {}: {}", from_index, e),
        })?;
    Ok(rows as u64)
}

pub fn mark_vote_submitted(
    conn: &Connection,
    round_id: &str,
    bundle_index: u32,
    proposal_id: u32,
) -> Result<(), VotingError> {
    conn.execute(
        "UPDATE votes SET submitted = 1 WHERE round_id = :round_id AND bundle_index = :bundle_index AND proposal_id = :proposal_id",
        named_params! {
            ":round_id": round_id,
            ":bundle_index": bundle_index as i64,
            ":proposal_id": proposal_id as i64,
        },
    )
    .map_err(|e| VotingError::Internal {
        message: format!("failed to mark vote submitted: {}", e),
    })?;
    Ok(())
}
