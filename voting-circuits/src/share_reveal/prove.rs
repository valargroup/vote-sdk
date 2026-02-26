//! Real Halo2 prove/verify for the Share Reveal circuit (ZKP #3).
//!
//! Follows the same pattern as `delegation/prove.rs` but for the
//! 5-condition share reveal circuit at K=14.

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

/// Generate the IPA params (SRS) for the share reveal circuit.
/// Deterministic for a given `K`.
///
/// **Expensive**: K=14 params generation takes several seconds.
/// Callers should cache the result.
pub fn share_reveal_params() -> Params<EqAffine> {
    Params::new(K)
}

/// Generate the proving and verifying keys for the share reveal circuit.
///
/// Uses `Circuit::default()` (all witnesses unknown) as the empty circuit
/// for key generation — the same pattern as the delegation circuit.
///
/// **Expensive**: first call involves full circuit layout. Callers should
/// cache the result alongside the params.
pub fn share_reveal_proving_key(
    params: &Params<EqAffine>,
) -> (
    plonk::ProvingKey<EqAffine>,
    plonk::VerifyingKey<EqAffine>,
) {
    let empty_circuit = Circuit::default();
    let vk = keygen_vk(params, &empty_circuit).expect("share_reveal keygen_vk should not fail");
    let pk = keygen_pk(params, vk.clone(), &empty_circuit)
        .expect("share_reveal keygen_pk should not fail");
    (pk, vk)
}

// ================================================================
// Prove
// ================================================================

/// Create a real Halo2 proof for the share reveal circuit.
///
/// Returns the serialized proof bytes. The caller must have constructed
/// a valid `Circuit` (with all witnesses populated) and a matching
/// `Instance` (7 public inputs).
///
/// **Expensive**: K=14 proof generation takes ~30-60 seconds in release mode.
pub fn create_share_reveal_proof(circuit: Circuit, instance: &Instance) -> Vec<u8> {
    let params = share_reveal_params();
    let (pk, _vk) = share_reveal_proving_key(&params);

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
    .expect("share_reveal proof generation should not fail");
    transcript.finalize()
}

// ================================================================
// Verify
// ================================================================

/// Verify a share reveal circuit proof given serialized proof bytes and
/// the 7 public inputs.
///
/// Returns `Ok(())` if verification succeeds, or an error message.
pub fn verify_share_reveal_proof(
    proof: &[u8],
    instance: &Instance,
) -> Result<(), String> {
    let params = share_reveal_params();
    let (_pk, vk) = share_reveal_proving_key(&params);

    let public_inputs = instance.to_halo2_instance();

    let strategy = SingleVerifier::new(&params);
    let mut transcript = Blake2bRead::<_, EqAffine, Challenge255<_>>::init(proof);

    verify_proof(&params, &vk, strategy, &[&[&public_inputs]], &mut transcript)
        .map_err(|e| format!("share_reveal verification failed: {:?}", e))
}

/// Verify a share reveal circuit proof from raw field-element bytes.
///
/// This is the lower-level entry point used by the FFI layer. It takes
/// the proof bytes and a flat array of 7 × 32-byte LE-encoded Pallas
/// base field elements (the public inputs in canonical order).
///
/// Returns `Ok(())` if verification succeeds, or an error message.
pub fn verify_share_reveal_proof_raw(
    proof: &[u8],
    public_inputs_bytes: &[u8],
) -> Result<(), String> {
    use pasta_curves::group::ff::PrimeField;

    const NUM_PUBLIC_INPUTS: usize = 7;
    const EXPECTED_BYTES: usize = NUM_PUBLIC_INPUTS * 32;

    if public_inputs_bytes.len() != EXPECTED_BYTES {
        return Err(format!(
            "expected {} bytes ({} × 32) for public inputs, got {}",
            EXPECTED_BYTES, NUM_PUBLIC_INPUTS, public_inputs_bytes.len()
        ));
    }

    // Deserialize each 32-byte chunk as a Pallas Fp element.
    // Note: the share reveal circuit's public inputs live on the Vesta
    // scalar field, which is the same as the Pallas base field.
    let mut public_inputs: Vec<vesta::Scalar> = Vec::with_capacity(NUM_PUBLIC_INPUTS);
    for i in 0..NUM_PUBLIC_INPUTS {
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

    let params = share_reveal_params();
    let (_pk, vk) = share_reveal_proving_key(&params);

    let strategy = SingleVerifier::new(&params);
    let mut transcript = Blake2bRead::<_, EqAffine, Challenge255<_>>::init(proof);

    verify_proof(
        &params,
        &vk,
        strategy,
        &[&[&public_inputs]],
        &mut transcript,
    )
    .map_err(|e| format!("share_reveal verification failed: {:?}", e))
}
