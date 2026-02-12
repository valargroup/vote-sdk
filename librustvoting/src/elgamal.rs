use crate::types::{validate_32_bytes, EncryptedShare, VotingError};

/// Encrypt each share under ea_pk using additively homomorphic El Gamal.
/// STUB: returns mock ciphertext pairs.
pub fn encrypt_shares(shares: &[u64], ea_pk: &[u8]) -> Result<Vec<EncryptedShare>, VotingError> {
    validate_32_bytes(ea_pk, "ea_pk")?;

    if shares.is_empty() {
        return Err(VotingError::InvalidInput {
            message: "shares must not be empty".to_string(),
        });
    }

    let mut encrypted = Vec::with_capacity(shares.len());
    for (i, &value) in shares.iter().enumerate() {
        if i > 3 {
            return Err(VotingError::InvalidInput {
                message: format!("at most 4 shares supported, got {}", shares.len()),
            });
        }
        encrypted.push(EncryptedShare {
            c1: vec![0xC1; 32],
            c2: vec![0xC2; 32],
            share_index: i as u32,
            plaintext_value: value,
        });
    }

    Ok(encrypted)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_encrypt_shares_stub() {
        let shares = vec![1, 4, 8];
        let ea_pk = vec![0xEA; 32];
        let result = encrypt_shares(&shares, &ea_pk).unwrap();
        assert_eq!(result.len(), 3);
        assert_eq!(result[0].share_index, 0);
        assert_eq!(result[0].plaintext_value, 1);
        assert_eq!(result[2].share_index, 2);
        assert_eq!(result[2].plaintext_value, 8);
    }

    #[test]
    fn test_encrypt_shares_bad_ea_pk() {
        let shares = vec![1];
        let ea_pk = vec![0xEA; 16]; // wrong length
        assert!(encrypt_shares(&shares, &ea_pk).is_err());
    }
}
