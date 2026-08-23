use voting_crypto_deps::halo2_proofs::{
    pasta::{EqAffine, Fp},
    plonk::{self, verify_proof, SingleVerifier},
    poly::commitment::Params,
    transcript::{Blake2bRead, Challenge255},
};

/// Error returned when a Halo2 proof does not verify as a canonical byte string.
#[derive(Debug)]
pub(crate) enum ProofVerifyError {
    Halo2(plonk::Error),
    TrailingBytes(usize),
}

/// Verify a Halo2 proof and reject bytes left unread by the transcript.
pub(crate) fn verify_halo2_proof_bytes(
    params: &Params<EqAffine>,
    vk: &plonk::VerifyingKey<EqAffine>,
    proof: &[u8],
    public_inputs: &[Fp],
) -> Result<(), ProofVerifyError> {
    let strategy = SingleVerifier::new(params);
    let mut proof_reader = proof;
    let mut transcript = Blake2bRead::<_, EqAffine, Challenge255<_>>::init(&mut proof_reader);

    verify_proof(params, vk, strategy, &[&[public_inputs]], &mut transcript)
        .map_err(ProofVerifyError::Halo2)?;

    if !proof_reader.is_empty() {
        return Err(ProofVerifyError::TrailingBytes(proof_reader.len()));
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::toy;

    #[test]
    fn verify_halo2_proof_bytes_rejects_trailing_unread_bytes() {
        let params = toy::toy_params();
        let (_pk, vk) = toy::toy_proving_key(&params);
        let (proof, public_input) = toy::create_toy_proof(2, 3);
        let public_inputs = [public_input];

        verify_halo2_proof_bytes(&params, &vk, &proof, &public_inputs)
            .expect("canonical proof should verify");

        let mut proof_with_trailing_bytes = proof;
        proof_with_trailing_bytes.extend_from_slice(b"junk");

        let err =
            verify_halo2_proof_bytes(&params, &vk, &proof_with_trailing_bytes, &public_inputs)
                .expect_err("proof with trailing bytes must be rejected");

        match err {
            ProofVerifyError::TrailingBytes(len) => assert_eq!(len, 4),
            ProofVerifyError::Halo2(e) => panic!("unexpected Halo2 error: {:?}", e),
        }
    }
}
