use crate::types::{validate_32_bytes, DelegationAction, ProofProgressReporter, ProofResult, VotingError};

/// Assemble Halo2 witness from action + PIR responses.
/// STUB: returns mock witness bytes.
pub fn build_delegation_witness(
    action: &DelegationAction,
    inclusion_proofs: &[Vec<u8>],
    exclusion_proofs: &[Vec<u8>],
) -> Result<Vec<u8>, VotingError> {
    validate_32_bytes(&action.rk, "action.rk")?;
    validate_32_bytes(&action.sighash, "action.sighash")?;

    for (i, proof) in inclusion_proofs.iter().enumerate() {
        if proof.is_empty() {
            return Err(VotingError::InvalidInput {
                message: format!("inclusion_proofs[{}] must not be empty", i),
            });
        }
    }
    for (i, proof) in exclusion_proofs.iter().enumerate() {
        if proof.is_empty() {
            return Err(VotingError::InvalidInput {
                message: format!("exclusion_proofs[{}] must not be empty", i),
            });
        }
    }

    Ok(vec![0xDD; 512])
}

/// Generate delegation proof (ZKP #1). Long-running (~4-8s when real).
/// STUB: simulates ~4s with 8 progress steps, returns mock proof.
pub fn generate_delegation_proof(
    witness: &[u8],
    progress: &dyn ProofProgressReporter,
) -> Result<ProofResult, VotingError> {
    if witness.is_empty() {
        return Err(VotingError::InvalidInput {
            message: "witness must not be empty".to_string(),
        });
    }

    // Mock progress — replaced with real Halo2 prover later
    for i in 1..=8 {
        std::thread::sleep(std::time::Duration::from_millis(500));
        progress.on_progress(i as f64 / 8.0);
    }

    Ok(ProofResult {
        proof: vec![0xAB; 256],
        success: true,
        error: None,
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

    fn mock_action() -> DelegationAction {
        DelegationAction {
            action_bytes: vec![0xDA; 128],
            rk: vec![0xDE; 32],
            sighash: vec![0x5A; 32],
            gov_nullifiers: vec![vec![0x01; 32]; 4],
            van: vec![0x02; 32],
            gov_comm_rand: vec![0x03; 32],
            dummy_nullifiers: vec![],
            rho_signed: vec![0x04; 32],
            padded_cmx: vec![],
        }
    }

    #[test]
    fn test_build_delegation_witness_stub() {
        let inclusion = vec![vec![0x01; 32]; 4];
        let exclusion = vec![vec![0x02; 32]; 4];
        let result = build_delegation_witness(&mock_action(), &inclusion, &exclusion).unwrap();
        assert!(!result.is_empty());
    }

    #[test]
    fn test_generate_delegation_proof_stub() {
        let count = Arc::new(AtomicU32::new(0));
        let reporter = TestReporter { count: count.clone() };
        let witness = vec![0xDD; 512];
        let result = generate_delegation_proof(&witness, &reporter).unwrap();
        assert!(result.success);
        assert_eq!(result.proof.len(), 256);
        assert!(result.error.is_none());
        assert_eq!(count.load(Ordering::Relaxed), 8);
    }

    #[test]
    fn test_generate_delegation_proof_empty_witness() {
        let reporter = TestReporter { count: Arc::new(AtomicU32::new(0)) };
        assert!(generate_delegation_proof(&[], &reporter).is_err());
    }
}
