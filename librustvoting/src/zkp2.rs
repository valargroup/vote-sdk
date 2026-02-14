use crate::types::{
    validate_encrypted_shares, validate_vote_decision, EncryptedShare, ProofProgressReporter,
    VoteCommitmentBundle, VotingError,
};

/// Build vote commitment + ZKP #2.
/// choice: 0=support, 1=oppose, 2=skip.
/// STUB: simulates ~400ms with 4 progress steps, returns mock bundle.
pub fn build_vote_commitment(
    proposal_id: u32,
    choice: u32,
    enc_shares: &[EncryptedShare],
    van_witness: &[u8],
    progress: &dyn ProofProgressReporter,
) -> Result<VoteCommitmentBundle, VotingError> {
    validate_vote_decision(choice)?;
    validate_encrypted_shares(enc_shares)?;

    if van_witness.is_empty() {
        return Err(VotingError::InvalidInput {
            message: "van_witness must not be empty".to_string(),
        });
    }
    if proposal_id > 15 {
        return Err(VotingError::InvalidInput {
            message: format!("proposal_id must be 0..15, got {}", proposal_id),
        });
    }

    // Mock progress — replaced with real Halo2 prover later
    for i in 1..=4 {
        std::thread::sleep(std::time::Duration::from_millis(100));
        progress.on_progress(i as f64 / 4.0);
    }

    Ok(VoteCommitmentBundle {
        van_nullifier: vec![0xAA; 32],
        vote_authority_note_new: vec![0xBB; 32],
        vote_commitment: vec![0xCC; 32],
        proposal_id,
        proof: vec![0xAB; 256],
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::Arc;

    struct TestReporter {
        count: Arc<AtomicU32>,
    }

    impl ProofProgressReporter for TestReporter {
        fn on_progress(&self, _progress: f64) {
            self.count.fetch_add(1, Ordering::Relaxed);
        }
    }

    fn mock_enc_shares() -> Vec<EncryptedShare> {
        vec![
            EncryptedShare {
                c1: vec![0xC1; 32],
                c2: vec![0xC2; 32],
                share_index: 0,
                plaintext_value: 1,
            },
            EncryptedShare {
                c1: vec![0xC1; 32],
                c2: vec![0xC2; 32],
                share_index: 1,
                plaintext_value: 4,
            },
        ]
    }

    fn noop_reporter() -> TestReporter {
        TestReporter {
            count: Arc::new(AtomicU32::new(0)),
        }
    }

    #[test]
    fn test_build_vote_commitment_stub() {
        let count = Arc::new(AtomicU32::new(0));
        let reporter = TestReporter {
            count: count.clone(),
        };
        let result =
            build_vote_commitment(1, 0, &mock_enc_shares(), &[0xDD; 64], &reporter).unwrap();
        assert_eq!(result.van_nullifier.len(), 32);
        assert_eq!(result.vote_authority_note_new.len(), 32);
        assert_eq!(result.vote_commitment.len(), 32);
        assert_eq!(result.proposal_id, 1);
        assert!(!result.proof.is_empty());
        assert_eq!(count.load(Ordering::Relaxed), 4);
    }

    #[test]
    fn test_build_vote_commitment_bad_choice() {
        assert!(
            build_vote_commitment(1, 3, &mock_enc_shares(), &[0xDD; 64], &noop_reporter()).is_err()
        );
    }

    #[test]
    fn test_build_vote_commitment_empty_proposal() {
        assert!(
            build_vote_commitment(16, 0, &mock_enc_shares(), &[0xDD; 64], &noop_reporter())
                .is_err()
        );
    }
}
