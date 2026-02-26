//! ECIES encryption on the Pallas curve.
//!
//! **WARNING**: This module is intended **only for use in E2E tests** and has
//! **not been audited**. Do not use in production or security-critical contexts.
//!
//! Port of the Go implementation in sdk/crypto/ecies/ecies.go so that the
//! Rust E2E tests can encrypt ea_sk shares during ceremony bootstrap.
//!
//! Scheme:
//!   1. e ← random scalar
//!   2. E = e * G                          (ephemeral public key)
//!   3. S = e * recipientPK                (ECDH shared secret)
//!   4. k = SHA256(E_compressed || S.x)    (32-byte symmetric key)
//!   5. ct = ChaCha20-Poly1305(k, nonce=0, plaintext)

use chacha20poly1305::{
    aead::{Aead, KeyInit},
    ChaCha20Poly1305, Nonce,
};
use ff::Field;
use group::{Curve, GroupEncoding};
use pasta_curves::pallas;
use sha2::{Digest, Sha256};

/// Size of a compressed Pallas point (32 bytes).
pub const COMPRESSED_POINT_SIZE: usize = 32;

/// ECIES envelope: ephemeral public key + authenticated ciphertext.
pub struct Envelope {
    /// E = e * G (compressed, 32 bytes).
    pub ephemeral_pk: [u8; COMPRESSED_POINT_SIZE],
    /// ChaCha20-Poly1305 ciphertext (plaintext_len + 16 bytes).
    pub ciphertext: Vec<u8>,
}

/// Encrypt `plaintext` to `recipient_pk` using ECIES on the Pallas curve.
///
/// Returns an `Envelope` containing the ephemeral public key and the
/// authenticated ciphertext. The caller provides a random scalar generator.
pub fn encrypt(
    recipient_pk: &pallas::Point,
    plaintext: &[u8],
    rng: &mut impl rand_core::RngCore,
) -> Envelope {
    // Generate ephemeral scalar
    let e = pallas::Scalar::random(rng);

    // E = e * SpendAuthG (ephemeral public key).
    // Must use SpendAuthG — the same generator the Go side uses for the
    // validator Pallas keypair (elgamal.KeyGen / PallasGenerator). Using
    // the standard Pallas generator would produce a different ECDH shared
    // secret and the decryption key would not match.
    let spend_auth_g = pallas::Point::from(voting_circuits::vote_proof::spend_auth_g_affine());
    let big_e = spend_auth_g * e;
    let e_compressed = big_e.to_affine().to_bytes();

    // S = e * recipientPK (ECDH shared secret)
    let big_s = *recipient_pk * e;
    let s_compressed = big_s.to_affine().to_bytes();

    // S.x = compressed bytes with sign bit cleared
    let mut s_x = s_compressed;
    s_x[31] &= 0x7F;

    // k = SHA256(E_compressed || S.x)
    let mut hasher = Sha256::new();
    hasher.update(e_compressed);
    hasher.update(s_x);
    let key: [u8; 32] = hasher.finalize().into();

    // Encrypt with ChaCha20-Poly1305, zero nonce (safe: ephemeral key is fresh).
    let cipher = ChaCha20Poly1305::new_from_slice(&key).expect("key is 32 bytes");
    let nonce = Nonce::default(); // all zeros
    let ciphertext = cipher
        .encrypt(&nonce, plaintext)
        .expect("encryption should not fail");

    Envelope {
        ephemeral_pk: e_compressed,
        ciphertext,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::rngs::OsRng;

    #[test]
    fn encrypt_produces_valid_envelope() {
        // Use SpendAuthG as the generator (same as Go's elgamal.KeyGen).
        let g = pallas::Point::from(voting_circuits::vote_proof::spend_auth_g_affine());
        let sk = pallas::Scalar::random(&mut OsRng);
        let pk = g * sk;
        let plaintext = b"hello world";

        let env = encrypt(&pk, plaintext, &mut OsRng);

        assert_eq!(env.ephemeral_pk.len(), 32);
        // ChaCha20-Poly1305 overhead is 16 bytes
        assert_eq!(env.ciphertext.len(), plaintext.len() + 16);

        // Decrypt to verify round-trip
        let big_e =
            Option::<pallas::Point>::from(pallas::Point::from_bytes(&env.ephemeral_pk)).unwrap();
        let big_s = big_e * sk;
        let s_compressed = big_s.to_affine().to_bytes();
        let mut s_x = s_compressed;
        s_x[31] &= 0x7F;

        let mut hasher = Sha256::new();
        hasher.update(env.ephemeral_pk);
        hasher.update(s_x);
        let key: [u8; 32] = hasher.finalize().into();

        let cipher = ChaCha20Poly1305::new_from_slice(&key).unwrap();
        let nonce = Nonce::default();
        let decrypted = cipher.decrypt(&nonce, env.ciphertext.as_ref()).unwrap();
        assert_eq!(decrypted.as_slice(), plaintext);
    }
}
