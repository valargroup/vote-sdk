//! SQLite-backed share store with ephemeral in-memory scheduling.
//!
//! Payload data and processing state are persisted in SQLite so shares survive
//! crashes. Scheduling delays (which provide temporal unlinkability) are kept
//! only in memory — on recovery, shares get fresh random delays per spec.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use rand::Rng;
use rusqlite::Connection;

use crate::migrations;
use crate::types::{Config, EncryptedShareWire, QueuedShare, SharePayload, ShareState};

/// Thread-safe share queue backed by SQLite. Clone is cheap (Arc).
#[derive(Clone)]
pub struct ShareStore {
    db: Arc<Mutex<Connection>>,
    /// Ephemeral scheduling times, keyed by (round_id, share_index).
    /// Not persisted — fresh delays are assigned on recovery.
    schedule: Arc<Mutex<HashMap<(String, u32), Instant>>>,
    min_delay: Duration,
    max_delay: Duration,
}

impl ShareStore {
    pub fn new(config: &Config) -> Self {
        let conn = Connection::open(&config.db_path)
            .unwrap_or_else(|e| panic!("failed to open database at {}: {}", config.db_path, e));

        migrations::migrate(&conn).expect("database migration failed");

        let store = Self {
            db: Arc::new(Mutex::new(conn)),
            schedule: Arc::new(Mutex::new(HashMap::new())),
            min_delay: Duration::from_secs(config.min_delay_secs),
            max_delay: Duration::from_secs(config.max_delay_secs),
        };

        store.recover();
        store
    }

    /// Enqueue a share payload with a random submission delay.
    pub fn enqueue(&self, payload: SharePayload) {
        let round_id = payload.vote_round_id.clone();
        let share_index = payload.enc_share.share_index;
        let all_enc_shares_json = serde_json::to_string(&payload.all_enc_shares)
            .expect("failed to serialize all_enc_shares");

        let db = self.db.lock().unwrap();
        db.execute(
            "INSERT INTO shares (round_id, share_index, shares_hash, proposal_id, vote_decision, \
             enc_share_c1, enc_share_c2, tree_position, all_enc_shares, state, attempts) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, 0, 0)",
            rusqlite::params![
                round_id,
                share_index,
                payload.shares_hash,
                payload.proposal_id,
                payload.vote_decision,
                payload.enc_share.c1,
                payload.enc_share.c2,
                payload.tree_position,
                all_enc_shares_json,
            ],
        )
        .expect("failed to insert share into database");
        drop(db);

        // Schedule in-memory with random delay.
        let delay = self.random_delay();
        let mut schedule = self.schedule.lock().unwrap();
        schedule.insert((round_id, share_index), Instant::now() + delay);
    }

    /// Take all shares that are past their scheduled submission time and in
    /// `Received` state. Moves them to `Witnessed` state (caller is responsible
    /// for generating witness before submitting).
    pub fn take_ready(&self) -> Vec<QueuedShare> {
        let now = Instant::now();

        // Find scheduled entries whose time has elapsed.
        let mut ready_keys = Vec::new();
        {
            let schedule = self.schedule.lock().unwrap();
            for (key, &scheduled_at) in schedule.iter() {
                if scheduled_at <= now {
                    ready_keys.push(key.clone());
                }
            }
        }

        if ready_keys.is_empty() {
            return Vec::new();
        }

        let mut result = Vec::new();
        let db = self.db.lock().unwrap();

        for (round_id, share_index) in &ready_keys {
            // Only take shares in Received state (0).
            let updated = db
                .execute(
                    "UPDATE shares SET state = 1 WHERE round_id = ?1 AND share_index = ?2 AND state = 0",
                    rusqlite::params![round_id, share_index],
                )
                .expect("failed to update share state");

            if updated > 0 {
                // Load the payload from DB.
                if let Some(queued) = self.load_share(&db, round_id, *share_index) {
                    result.push(queued);
                }
            }
        }
        drop(db);

        // Remove from schedule.
        let mut schedule = self.schedule.lock().unwrap();
        for key in &ready_keys {
            schedule.remove(key);
        }

        result
    }

    /// Mark a share as submitted (by matching round_id + share_index).
    pub fn mark_submitted(&self, round_id: &str, share_index: u32) {
        let db = self.db.lock().unwrap();
        db.execute(
            "UPDATE shares SET state = 2 WHERE round_id = ?1 AND share_index = ?2 AND state = 1",
            rusqlite::params![round_id, share_index],
        )
        .expect("failed to mark share as submitted");
        drop(db);

        let mut schedule = self.schedule.lock().unwrap();
        schedule.remove(&(round_id.to_string(), share_index));
    }

    /// Mark a share as failed (for retry, up to MAX_ATTEMPTS).
    pub fn mark_failed(&self, round_id: &str, share_index: u32) {
        const MAX_ATTEMPTS: u32 = 5;

        let db = self.db.lock().unwrap();

        // Read current attempts.
        let attempts: u32 = db
            .query_row(
                "SELECT attempts FROM shares WHERE round_id = ?1 AND share_index = ?2",
                rusqlite::params![round_id, share_index],
                |row| row.get(0),
            )
            .unwrap_or(0);

        let new_attempts = attempts + 1;
        if new_attempts >= MAX_ATTEMPTS {
            // Permanently failed.
            db.execute(
                "UPDATE shares SET state = 3, attempts = ?3 WHERE round_id = ?1 AND share_index = ?2",
                rusqlite::params![round_id, share_index, new_attempts],
            )
            .expect("failed to mark share as permanently failed");
            tracing::warn!(
                round_id,
                share_index,
                attempts = new_attempts,
                "share permanently failed after max attempts"
            );

            drop(db);
            let mut schedule = self.schedule.lock().unwrap();
            schedule.remove(&(round_id.to_string(), share_index));
        } else {
            // Re-schedule with exponential backoff.
            db.execute(
                "UPDATE shares SET state = 0, attempts = ?3 WHERE round_id = ?1 AND share_index = ?2",
                rusqlite::params![round_id, share_index, new_attempts],
            )
            .expect("failed to reschedule share");

            drop(db);
            let backoff = Duration::from_secs(2u64.pow(new_attempts.min(6)));
            let mut schedule = self.schedule.lock().unwrap();
            schedule.insert(
                (round_id.to_string(), share_index),
                Instant::now() + backoff,
            );
        }
    }

    /// Queue depth per round (for status endpoint).
    pub fn status(&self) -> HashMap<String, QueueStatus> {
        let db = self.db.lock().unwrap();
        let mut stmt = db
            .prepare(
                "SELECT round_id, state, COUNT(*) FROM shares GROUP BY round_id, state",
            )
            .expect("failed to prepare status query");

        let mut status_map: HashMap<String, QueueStatus> = HashMap::new();

        let rows = stmt
            .query_map([], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, u32>(1)?,
                    row.get::<_, usize>(2)?,
                ))
            })
            .expect("failed to query status");

        for row in rows {
            let (round_id, state, count) = row.expect("failed to read status row");
            let entry = status_map.entry(round_id).or_insert(QueueStatus {
                total: 0,
                pending: 0,
                submitted: 0,
                failed: 0,
            });
            entry.total += count;
            match state {
                0 => entry.pending += count,
                1 => entry.pending += count, // Witnessed counts as pending in the status view.
                2 => entry.submitted += count,
                3 => entry.failed += count,
                _ => {}
            }
        }

        status_map
    }

    /// Recover non-terminal shares from SQLite on startup.
    /// Witnessed shares are reset to Received (they were in-flight when crash happened).
    /// Fresh random delays are assigned per spec (no timing info persisted).
    fn recover(&self) {
        let db = self.db.lock().unwrap();

        // Reset Witnessed (1) → Received (0) — these were in-flight at crash time.
        db.execute("UPDATE shares SET state = 0 WHERE state = 1", [])
            .expect("failed to reset witnessed shares on recovery");

        // Load all non-terminal shares (Received = 0).
        let mut stmt = db
            .prepare("SELECT round_id, share_index FROM shares WHERE state = 0")
            .expect("failed to prepare recovery query");

        let keys: Vec<(String, u32)> = stmt
            .query_map([], |row| {
                Ok((row.get::<_, String>(0)?, row.get::<_, u32>(1)?))
            })
            .expect("failed to query shares for recovery")
            .collect::<Result<_, _>>()
            .expect("failed to read recovery rows");

        drop(stmt);
        drop(db);

        if keys.is_empty() {
            return;
        }

        tracing::info!(count = keys.len(), "recovering shares with fresh random delays");

        let mut schedule = self.schedule.lock().unwrap();
        for (round_id, share_index) in keys {
            let delay = self.random_delay();
            schedule.insert((round_id, share_index), Instant::now() + delay);
        }
    }

    /// Load a share from the database and reconstruct a QueuedShare.
    fn load_share(&self, db: &Connection, round_id: &str, share_index: u32) -> Option<QueuedShare> {
        db.query_row(
            "SELECT shares_hash, proposal_id, vote_decision, enc_share_c1, enc_share_c2, \
             tree_position, all_enc_shares, state, attempts \
             FROM shares WHERE round_id = ?1 AND share_index = ?2",
            rusqlite::params![round_id, share_index],
            |row| {
                let all_enc_shares_json: String = row.get(6)?;
                let all_enc_shares: Vec<EncryptedShareWire> =
                    serde_json::from_str(&all_enc_shares_json).expect("invalid all_enc_shares JSON in DB");

                let state_int: u32 = row.get(7)?;
                let state = match state_int {
                    0 => ShareState::Received,
                    1 => ShareState::Witnessed,
                    2 => ShareState::Submitted,
                    3 => ShareState::Failed,
                    _ => panic!("unknown share state: {}", state_int),
                };

                let now = Instant::now();
                Ok(QueuedShare {
                    payload: SharePayload {
                        shares_hash: row.get(0)?,
                        proposal_id: row.get(1)?,
                        vote_decision: row.get(2)?,
                        enc_share: EncryptedShareWire {
                            c1: row.get(3)?,
                            c2: row.get(4)?,
                            share_index,
                        },
                        share_index,
                        tree_position: row.get(5)?,
                        vote_round_id: round_id.to_string(),
                        all_enc_shares,
                    },
                    received_at: now,
                    scheduled_submit_at: now,
                    state,
                    attempts: row.get(8)?,
                })
            },
        )
        .ok()
    }

    fn random_delay(&self) -> Duration {
        let mut rng = rand::thread_rng();
        let secs = rng.gen_range(self.min_delay.as_secs()..=self.max_delay.as_secs());
        Duration::from_secs(secs)
    }
}

/// Per-round queue statistics.
#[derive(Debug, Clone, serde::Serialize)]
pub struct QueueStatus {
    pub total: usize,
    pub pending: usize,
    pub submitted: usize,
    pub failed: usize,
}
