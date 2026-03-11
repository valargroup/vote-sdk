use blake2b_simd::Params;
use ff::{FromUniformBytes, PrimeField};
use group::{Curve, Group, GroupEncoding};
use pasta_curves::pallas;

use crate::types::{VotingError, VotingHotkey};

/// Derive a voting hotkey (Pallas keypair) deterministically from seed bytes.
///
/// Uses Blake2b with personalization to derive a scalar, then computes the
/// corresponding public key on the Pallas curve.
pub fn generate_hotkey(seed: &[u8]) -> Result<VotingHotkey, VotingError> {
    if seed.len() < 32 {
        return Err(VotingError::InvalidInput {
            message: format!("seed must be at least 32 bytes, got {}", seed.len()),
        });
    }

    // Blake2b-512 with personalization → 64 bytes → Scalar::from_bytes_wide
    let hash = Params::new()
        .hash_length(64)
        .personal(b"ZcashVotingHotKy")
        .hash(seed);

    let wide: &[u8; 64] = hash.as_bytes().try_into().expect("blake2b-512 is 64 bytes");
    let sk = pallas::Scalar::from_uniform_bytes(wide);

    let pk = pallas::Point::generator() * sk;
    let pk_bytes = pk.to_affine().to_bytes();
    let sk_bytes = sk.to_repr();

    // Address: sv1 + hex of first 20 bytes of public key (placeholder encoding)
    let address = format!("sv1{}", hex::encode(&pk_bytes[..20]));

    Ok(VotingHotkey {
        secret_key: sk_bytes.to_vec(),
        public_key: pk_bytes.to_vec(),
        address,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_generate_hotkey_deterministic() {
        let seed = [0xAB_u8; 64];
        let h1 = generate_hotkey(&seed).unwrap();
        let h2 = generate_hotkey(&seed).unwrap();
        assert_eq!(h1.secret_key, h2.secret_key);
        assert_eq!(h1.public_key, h2.public_key);
        assert_eq!(h1.address, h2.address);
    }

    #[test]
    fn test_generate_hotkey_key_sizes() {
        let seed = [0x42_u8; 64];
        let hotkey = generate_hotkey(&seed).unwrap();
        assert_eq!(hotkey.secret_key.len(), 32);
        assert_eq!(hotkey.public_key.len(), 32);
        assert!(hotkey.address.starts_with("sv1"));
    }

    #[test]
    fn test_generate_hotkey_different_seeds() {
        let h1 = generate_hotkey(&[0x01; 32]).unwrap();
        let h2 = generate_hotkey(&[0x02; 32]).unwrap();
        assert_ne!(h1.secret_key, h2.secret_key);
        assert_ne!(h1.public_key, h2.public_key);
    }

    #[test]
    fn test_generate_hotkey_short_seed_rejected() {
        let result = generate_hotkey(&[0x01; 16]);
        assert!(result.is_err());
    }
}
