//! ElGamal encryption on the Pallas curve.
//!
//! Port of the Go implementation in sdk/crypto/elgamal so that the Rust E2E
//! tests can generate ciphertexts inline without fixture files. Serialization
//! (marshal/unmarshal) is 64 bytes: two 32-byte compressed Pallas points (C1 || C2),
//! matching the chain's Go HomomorphicAdd and UnmarshalCiphertext.

use group::ff::Field;
use group::{Group, GroupEncoding};
use pasta_curves::pallas;
use rand_core::RngCore;

/// Size of a compressed Pallas point (32 bytes).
pub const COMPRESSED_POINT_SIZE: usize = 32;
/// Size of a serialized ciphertext (C1 || C2).
pub const CIPHERTEXT_SIZE: usize = 2 * COMPRESSED_POINT_SIZE;

/// Secret key: scalar in the Pallas scalar field.
#[derive(Clone)]
pub struct SecretKey(pub pallas::Scalar);

/// Public key: point on the Pallas curve (sk * G).
#[derive(Clone)]
pub struct PublicKey(pub pallas::Point);

/// ElGamal ciphertext: (C1, C2) = (r*G, v*G + r*pk).
#[derive(Clone)]
pub struct Ciphertext {
    pub c1: pallas::Point,
    pub c2: pallas::Point,
}

/// Generate a keypair: sk random, pk = sk * G.
pub fn keygen(rng: &mut impl RngCore) -> (SecretKey, PublicKey) {
    let sk = pallas::Scalar::random(rng);
    let pk = pallas::Point::generator() * sk;
    (SecretKey(sk), PublicKey(pk))
}

/// Encrypt value v under public key with randomness r: (r*G, v*G + r*pk).
fn encrypt_core(pk: &PublicKey, v: u64, r: pallas::Scalar) -> Ciphertext {
    let g = pallas::Point::generator();
    let v_scalar = pallas::Scalar::from(v);
    let c1 = g * r;
    let c2 = (g * v_scalar) + (pk.0 * r);
    Ciphertext { c1, c2 }
}

/// Encrypt value v under pk with fresh randomness from rng.
pub fn encrypt(pk: &PublicKey, v: u64, rng: &mut impl RngCore) -> Ciphertext {
    let r = pallas::Scalar::random(rng);
    encrypt_core(pk, v, r)
}

/// Homomorphic addition: component-wise sum of two ciphertexts.
/// Enc(a) + Enc(b) = Enc(a+b).
pub fn homomorphic_add(a: &Ciphertext, b: &Ciphertext) -> Ciphertext {
    Ciphertext {
        c1: a.c1 + b.c1,
        c2: a.c2 + b.c2,
    }
}

/// Serialize ciphertext to 64 bytes (C1 || C2, each 32-byte compressed).
pub fn marshal(ct: &Ciphertext) -> [u8; CIPHERTEXT_SIZE] {
    let c1_bytes = ct.c1.to_bytes();
    let c2_bytes = ct.c2.to_bytes();
    let mut out = [0u8; CIPHERTEXT_SIZE];
    out[..COMPRESSED_POINT_SIZE].copy_from_slice(c1_bytes.as_ref());
    out[COMPRESSED_POINT_SIZE..].copy_from_slice(c2_bytes.as_ref());
    out
}

/// Deserialize 64 bytes into a ciphertext.
/// Accepts identity point as 32 zero bytes (matches Go decompressPallasPoint).
pub fn unmarshal(data: &[u8]) -> Result<Ciphertext, String> {
    if data.len() != CIPHERTEXT_SIZE {
        return Err(format!(
            "expected {} bytes, got {}",
            CIPHERTEXT_SIZE,
            data.len()
        ));
    }
    let c1 = point_from_compressed(&data[..COMPRESSED_POINT_SIZE])?;
    let c2 = point_from_compressed(&data[COMPRESSED_POINT_SIZE..])?;
    Ok(Ciphertext { c1, c2 })
}

fn point_from_compressed(data: &[u8]) -> Result<pallas::Point, String> {
    let mut arr = [0u8; 32];
    arr.copy_from_slice(data);
    let all_zero = arr.iter().all(|&b| b == 0);
    if all_zero {
        return Ok(pallas::Point::identity());
    }
    let opt = pallas::Point::from_bytes(&arr);
    if opt.is_some().into() {
        Ok(opt.unwrap())
    } else {
        Err("invalid compressed Pallas point".to_string())
    }
}

/// Serialize public key to 32 bytes (compressed Pallas point).
pub fn marshal_public_key(pk: &PublicKey) -> [u8; COMPRESSED_POINT_SIZE] {
    pk.0.to_bytes()
}

/// Deserialize 32 bytes into a public key (compressed Pallas point).
pub fn unmarshal_public_key(data: &[u8; COMPRESSED_POINT_SIZE]) -> Result<PublicKey, String> {
    let pt = point_from_compressed(data)?;
    Ok(PublicKey(pt))
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::SeedableRng;
    use rand_chacha::ChaCha20Rng;

    #[test]
    fn roundtrip_ciphertext() {
        let mut rng = ChaCha20Rng::seed_from_u64(1);
        let (_sk, pk) = keygen(&mut rng);
        let ct = encrypt(&pk, 5, &mut rng);
        let bytes = marshal(&ct);
        let ct2 = unmarshal(&bytes).expect("unmarshal");
        let bytes2 = marshal(&ct2);
        assert_eq!(bytes, bytes2, "round-trip marshal/unmarshal");
    }

    #[test]
    fn cross_validate_go_vectors() {
        // Vectors from Go curvey TestCrossValidationVectors (using standard Pallas generator):
        // scalar=1 (generator point) — should match pasta_curves generator
        let go_gen_hex = "00000000ed302d991bf94c09fc98462200000000000000000000000000000040";
        let go_gen_bytes = hex::decode(go_gen_hex).unwrap();

        // Rust generator point
        let rust_gen = pallas::Point::generator();
        let rust_gen_bytes = rust_gen.to_bytes();

        assert_eq!(
            go_gen_bytes.as_slice(),
            rust_gen_bytes.as_ref(),
            "Go standard Pallas generator != Rust pasta_curves generator"
        );
    }

    #[test]
    fn homomorphic_add_marshal_roundtrip() {
        let mut rng = ChaCha20Rng::seed_from_u64(2);
        let (_sk, pk) = keygen(&mut rng);
        let ct_a = encrypt(&pk, 5, &mut rng);
        let ct_b = encrypt(&pk, 10, &mut rng);
        let sum = homomorphic_add(&ct_a, &ct_b);
        let bytes = marshal(&sum);
        let sum2 = unmarshal(&bytes).expect("unmarshal");
        let bytes2 = marshal(&sum2);
        assert_eq!(bytes, bytes2);
    }
}
