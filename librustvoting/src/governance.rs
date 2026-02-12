use crate::types::VotingError;

/// Derive governance nullifier: Poseidon_nk("governance authorization", voting_round_id, real_nullifier).
/// STUB: returns mock 32-byte nullifier.
pub fn derive_gov_nullifier(
    nullifier: &[u8],
    _voting_round_id: &str,
    _nk: &[u8],
) -> Result<Vec<u8>, VotingError> {
    crate::types::validate_32_bytes(nullifier, "nullifier")?;
    Ok(vec![0xAA; 32])
}

/// Construct a Vote Authority Note.
/// STUB: returns mock 32-byte VAN commitment.
pub fn construct_van(
    hotkey_pk: &[u8],
    weight: u64,
    _voting_round_id: &str,
) -> Result<Vec<u8>, VotingError> {
    crate::types::validate_32_bytes(hotkey_pk, "hotkey_pk")?;
    if weight == 0 {
        return Err(VotingError::InvalidInput {
            message: "weight must be > 0".to_string(),
        });
    }
    Ok(vec![0xBB; 32])
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_derive_gov_nullifier_stub() {
        let nf = vec![0x01; 32];
        let result = derive_gov_nullifier(&nf, "round-1", &[0x02; 32]).unwrap();
        assert_eq!(result.len(), 32);
    }

    #[test]
    fn test_construct_van_stub() {
        let pk = vec![0x43; 32];
        let result = construct_van(&pk, 1000, "round-1").unwrap();
        assert_eq!(result.len(), 32);
    }

    #[test]
    fn test_construct_van_zero_weight() {
        let pk = vec![0x43; 32];
        assert!(construct_van(&pk, 0, "round-1").is_err());
    }
}
