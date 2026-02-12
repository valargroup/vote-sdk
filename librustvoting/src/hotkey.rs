use crate::types::{VotingError, VotingHotkey};

/// Generate a random voting hotkey (Pallas keypair).
/// STUB: returns a hardcoded keypair. Real implementation will use pasta_curves.
pub fn generate_hotkey() -> Result<VotingHotkey, VotingError> {
    Ok(VotingHotkey {
        secret_key: vec![0x42; 32],
        public_key: vec![0x43; 32],
        address: "zvote1stub_hotkey_address_placeholder".to_string(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_generate_hotkey_stub() {
        let hotkey = generate_hotkey().unwrap();
        assert_eq!(hotkey.secret_key.len(), 32);
        assert_eq!(hotkey.public_key.len(), 32);
        assert!(!hotkey.address.is_empty());
    }
}
