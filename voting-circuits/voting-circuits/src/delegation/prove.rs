//! Real Halo2 prove/verify for the delegation circuit (ZKP #1).
//!
//! Follows the same pattern as `sdk/circuits/src/toy.rs` but for the full
//! 15-condition delegation circuit at K=14.

use alloc::format;
use alloc::string::String;
use alloc::vec::Vec;

use halo2_proofs::{
    pasta::EqAffine,
    plonk::{self, create_proof, keygen_pk, keygen_vk, verify_proof, SingleVerifier},
    poly::commitment::Params,
    transcript::{Blake2bRead, Blake2bWrite, Challenge255},
};
use pasta_curves::{pallas, vesta};
use rand::rngs::OsRng;

use super::circuit::{Circuit, Instance, K};

// ================================================================
// Params / key generation
// ================================================================

/// Generate the IPA params (SRS) for the delegation circuit.
/// Deterministic for a given `K`.
///
/// **Expensive**: K=14 params generation takes several seconds.
/// Callers should cache the result.
pub fn delegation_params() -> Params<EqAffine> {
    Params::new(K)
}

/// Generate the proving and verifying keys for the delegation circuit.
///
/// Uses `Circuit::default()` (all witnesses unknown) as the empty circuit
/// for key generation — the same pattern as the Orchard action circuit.
///
/// **Expensive**: first call involves full circuit layout. Callers should
/// cache the result alongside the params.
pub fn delegation_proving_key(
    params: &Params<EqAffine>,
) -> (
    plonk::ProvingKey<EqAffine>,
    plonk::VerifyingKey<EqAffine>,
) {
    let empty_circuit = Circuit::default();
    let vk = keygen_vk(params, &empty_circuit).expect("delegation keygen_vk should not fail");
    let pk = keygen_pk(params, vk.clone(), &empty_circuit)
        .expect("delegation keygen_pk should not fail");
    (pk, vk)
}

// ================================================================
// Prove
// ================================================================

/// Create a real Halo2 proof for the delegation circuit.
///
/// Returns the serialized proof bytes. The caller must have constructed
/// a valid `Circuit` (with all witnesses populated) and a matching
/// `Instance` (13 public inputs).
///
/// **Expensive**: K=14 proof generation takes ~30-60 seconds in release mode.
pub fn create_delegation_proof(circuit: Circuit, instance: &Instance) -> Vec<u8> {
    let params = delegation_params();
    let (pk, _vk) = delegation_proving_key(&params);

    let public_inputs = instance.to_halo2_instance();

    let mut transcript = Blake2bWrite::<_, EqAffine, Challenge255<_>>::init(vec![]);
    create_proof(
        &params,
        &pk,
        &[circuit],
        &[&[&public_inputs]],
        OsRng,
        &mut transcript,
    )
    .expect("delegation proof generation should not fail");
    transcript.finalize()
}

// ================================================================
// Verify
// ================================================================

/// Verify a delegation circuit proof given serialized proof bytes and
/// the 13 public inputs.
///
/// Returns `Ok(())` if verification succeeds, or an error message.
pub fn verify_delegation_proof(
    proof: &[u8],
    instance: &Instance,
) -> Result<(), String> {
    let params = delegation_params();
    let (_pk, vk) = delegation_proving_key(&params);

    let public_inputs = instance.to_halo2_instance();

    let strategy = SingleVerifier::new(&params);
    let mut transcript = Blake2bRead::<_, EqAffine, Challenge255<_>>::init(proof);

    verify_proof(&params, &vk, strategy, &[&[&public_inputs]], &mut transcript)
        .map_err(|e| format!("delegation verification failed: {:?}", e))
}

/// Verify a delegation circuit proof from raw field-element bytes.
///
/// This is the lower-level entry point used by the FFI layer. It takes
/// the proof bytes and a flat array of 13 × 32-byte LE-encoded Pallas
/// base field elements (the public inputs in canonical order).
///
/// Returns `Ok(())` if verification succeeds, or an error message.
pub fn verify_delegation_proof_raw(
    proof: &[u8],
    public_inputs_bytes: &[u8],
) -> Result<(), String> {
    use pasta_curves::group::ff::PrimeField;

    if public_inputs_bytes.len() != 13 * 32 {
        return Err(format!(
            "expected 416 bytes (13 × 32) for public inputs, got {}",
            public_inputs_bytes.len()
        ));
    }

    // Deserialize each 32-byte chunk as a Pallas Fp element.
    // Note: the delegation circuit's public inputs live on the Vesta
    // scalar field, which is the same as the Pallas base field.
    let mut public_inputs: Vec<vesta::Scalar> = Vec::with_capacity(13);
    for i in 0..13 {
        let start = i * 32;
        let mut repr = [0u8; 32];
        repr.copy_from_slice(&public_inputs_bytes[start..start + 32]);
        let fp_opt: Option<pallas::Base> = pallas::Base::from_repr(repr).into();
        match fp_opt {
            Some(f) => public_inputs.push(f),
            None => {
                return Err(format!(
                    "public input {} is not a canonical Pallas Fp encoding",
                    i
                ))
            }
        }
    }

    let params = delegation_params();
    let (_pk, vk) = delegation_proving_key(&params);

    let strategy = SingleVerifier::new(&params);
    let mut transcript = Blake2bRead::<_, EqAffine, Challenge255<_>>::init(proof);

    verify_proof(
        &params,
        &vk,
        strategy,
        &[&[&public_inputs]],
        &mut transcript,
    )
    .map_err(|e| format!("delegation verification failed: {:?}", e))
}
