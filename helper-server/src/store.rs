//! In-memory share queue, keyed by vote round ID.
//!
//! Thread-safe via `Arc<Mutex<...>>`. Each vote round has an independent queue
//! of [`QueuedShare`] entries ordered by scheduled submission time.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use rand::Rng;

use crate::types::{Config, QueuedShare, SharePayload, ShareState};

/// Thread-safe share queue. Clone is cheap (Arc).
#[derive(Clone)]
pub struct ShareStore {
    inner: Arc<Mutex<HashMap<String, Vec<QueuedShare>>>>,
    min_delay: Duration,
    max_delay: Duration,
}

impl ShareStore {
    pub fn new(config: &Config) -> Self {
        Self {
            inner: Arc::new(Mutex::new(HashMap::new())),
            min_delay: Duration::from_secs(config.min_delay_secs),
            max_delay: Duration::from_secs(config.max_delay_secs),
        }
    }

    /// Enqueue a share payload with a random submission delay.
    pub fn enqueue(&self, payload: SharePayload) {
        let now = Instant::now();
        let delay = self.random_delay();
        let queued = QueuedShare {
            payload,
            received_at: now,
            scheduled_submit_at: now + delay,
            state: ShareState::Received,
            attempts: 0,
        };

        let round_id = queued.payload.vote_round_id.clone();
        let mut store = self.inner.lock().unwrap();
        store.entry(round_id).or_default().push(queued);
    }

    /// Take all shares that are past their scheduled submission time and in
    /// `Received` state. Moves them to `Witnessed` state (caller is responsible
    /// for generating witness before submitting).
    pub fn take_ready(&self) -> Vec<QueuedShare> {
        let now = Instant::now();
        let mut store = self.inner.lock().unwrap();
        let mut ready = Vec::new();

        for shares in store.values_mut() {
            for share in shares.iter_mut() {
                if share.state == ShareState::Received && share.scheduled_submit_at <= now {
                    share.state = ShareState::Witnessed;
                    ready.push(share.clone());
                }
            }
        }

        ready
    }

    /// Mark a share as submitted (by matching round_id + share_index).
    pub fn mark_submitted(&self, round_id: &str, share_index: u32) {
        let mut store = self.inner.lock().unwrap();
        if let Some(shares) = store.get_mut(round_id) {
            for share in shares.iter_mut() {
                if share.payload.enc_share.share_index == share_index
                    && share.state == ShareState::Witnessed
                {
                    share.state = ShareState::Submitted;
                }
            }
        }
    }

    /// Mark a share as failed (for retry).
    pub fn mark_failed(&self, round_id: &str, share_index: u32) {
        let mut store = self.inner.lock().unwrap();
        if let Some(shares) = store.get_mut(round_id) {
            for share in shares.iter_mut() {
                if share.payload.enc_share.share_index == share_index
                    && share.state == ShareState::Witnessed
                {
                    share.state = ShareState::Failed;
                    share.attempts += 1;
                    // Re-schedule with backoff.
                    let backoff = Duration::from_secs(2u64.pow(share.attempts.min(6)));
                    share.scheduled_submit_at = Instant::now() + backoff;
                    share.state = ShareState::Received;
                }
            }
        }
    }

    /// Queue depth per round (for status endpoint).
    pub fn status(&self) -> HashMap<String, QueueStatus> {
        let store = self.inner.lock().unwrap();
        store
            .iter()
            .map(|(round_id, shares)| {
                let total = shares.len();
                let pending = shares
                    .iter()
                    .filter(|s| s.state == ShareState::Received)
                    .count();
                let submitted = shares
                    .iter()
                    .filter(|s| s.state == ShareState::Submitted)
                    .count();
                (
                    round_id.clone(),
                    QueueStatus {
                        total,
                        pending,
                        submitted,
                    },
                )
            })
            .collect()
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
}
