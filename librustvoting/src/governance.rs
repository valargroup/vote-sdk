use ff::PrimeField;
use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength, P128Pow5T3};
use pasta_curves::pallas;

use crate::types::VotingError;

/// Maximum proposal authority — the default for a fresh delegation.
/// Bitmask where each bit authorizes voting on the corresponding proposal.
/// Full authority is 2^16 - 1 = 65535 (all 16 proposals authorized).
pub(crate) const MAX_PROPOSAL_AUTHORITY: u64 = 65535;

/// Domain tag for Vote Authority Notes.
/// Prepended as the first Poseidon input in gov_comm for domain separation.
pub(crate) const DOMAIN_VAN: u64 = 0;

/// Domain tag for governance authorization nullifier (per spec §1.3.2).
/// `"governance authorization"` encoded as a little-endian Pallas field element.
fn gov_auth_domain_tag() -> pallas::Base {
    let mut bytes = [0u8; 32];
    bytes[..24].copy_from_slice(b"governance authorization");
    pallas::Base::from_repr(bytes).unwrap()
}

/// Poseidon hash of two field elements (ConstantLength<2>, width 3, rate 2).
/// Matches `orchard/src/delegation/imt.rs:poseidon_hash_2`.
fn poseidon_hash_2(a: pallas::Base, b: pallas::Base) -> pallas::Base {
    poseidon::Hash::<_, P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([a, b])
}

/// Convert a 32-byte slice to a Pallas base field element.
fn bytes_to_fp(bytes: &[u8]) -> Result<pallas::Base, VotingError> {
    let arr: [u8; 32] = bytes.try_into().map_err(|_| VotingError::InvalidInput {
        message: format!("expected 32 bytes, got {}", bytes.len()),
    })?;
    Option::from(pallas::Base::from_repr(arr)).ok_or_else(|| VotingError::InvalidInput {
        message: "bytes are not a valid Pallas field element".to_string(),
    })
}

/// Convert a Pallas base field element to 32 bytes.
fn fp_to_bytes(fp: pallas::Base) -> Vec<u8> {
    fp.to_repr().as_ref().to_vec()
}

/// Derive governance nullifier (per spec §1.3.2, condition 14).
///
/// `gov_null = Poseidon(nk, Poseidon(domain_tag, Poseidon(vote_round_id, real_nf)))`
///
/// Each step is Poseidon with ConstantLength<2>.
/// Matches `orchard/src/delegation/imt.rs:gov_null_hash`.
pub fn derive_gov_nullifier(
    nk: &[u8],
    vote_round_id: &[u8],
    note_nullifier: &[u8],
) -> Result<Vec<u8>, VotingError> {
    let nk_fp = bytes_to_fp(nk)?;
    let vri_fp = bytes_to_fp(vote_round_id)?;
    let nf_fp = bytes_to_fp(note_nullifier)?;

    let step1 = poseidon_hash_2(vri_fp, nf_fp);
    let step2 = poseidon_hash_2(gov_auth_domain_tag(), step1);
    let gov_null = poseidon_hash_2(nk_fp, step2);

    Ok(fp_to_bytes(gov_null))
}

/// Construct a Vote Authority Note (governance commitment, per spec §1.3.3).
///
/// ```text
/// gov_comm_core = Poseidon(DOMAIN_VAN, g_d_new_x, pk_d_new_x, v_total, vote_round_id, MAX_PROPOSAL_AUTHORITY)
/// gov_comm = Poseidon(gov_comm_core, gov_comm_rand)
/// ```
///
/// First hash is ConstantLength<6>, second is ConstantLength<2>.
/// Matches `orchard/src/delegation/circuit.rs:gov_commitment_hash`.
pub fn construct_van(
    g_d_new_x: &[u8],
    pk_d_new_x: &[u8],
    total_weight: u64,
    vote_round_id: &[u8],
    gov_comm_rand: &[u8],
) -> Result<Vec<u8>, VotingError> {
    if total_weight == 0 {
        return Err(VotingError::InvalidInput {
            message: "total_weight must be > 0".to_string(),
        });
    }

    // Parse all inputs into Pallas field elements for Poseidon.
    let g_d = bytes_to_fp(g_d_new_x)?;
    let pk_d = bytes_to_fp(pk_d_new_x)?;
    let v_total = pallas::Base::from(total_weight);
    let vri = bytes_to_fp(vote_round_id)?;
    let rcm = bytes_to_fp(gov_comm_rand)?;

    // Step 1: Hash the 6 core VAN fields into a single digest (ConstantLength<6>).
    // This binds the VAN to a specific hotkey address (g_d, pk_d), delegated weight,
    // voting round, and full proposal authority. DOMAIN_VAN=0 provides domain
    // separation from Vote Commitments (DOMAIN_VC=1) in the shared commitment tree.
    let gov_comm_core =
        poseidon::Hash::<_, P128Pow5T3, ConstantLength<6>, 3, 2>::init().hash([
            pallas::Base::from(DOMAIN_VAN),
            g_d,
            pk_d,
            v_total,
            vri,
            pallas::Base::from(MAX_PROPOSAL_AUTHORITY),
        ]);

    // Step 2: Fold in the blinding factor (ConstantLength<2>).
    // gov_comm_rand hides the VAN preimage so observers can't brute-force
    // the hotkey or weight from the on-chain commitment.
    let gov_comm = poseidon_hash_2(gov_comm_core, rcm);

    Ok(fp_to_bytes(gov_comm))
}

/// Compute constrained rho (spec §1.3.4.1, condition 3).
///
/// `rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id)`
///
/// ConstantLength<6>, matching `orchard/src/delegation/circuit.rs:rho_binding_hash`.
pub fn compute_rho_binding(
    cmx_1: &[u8],
    cmx_2: &[u8],
    cmx_3: &[u8],
    cmx_4: &[u8],
    gov_comm: &[u8],
    vote_round_id: &[u8],
) -> Result<Vec<u8>, VotingError> {
    let c1 = bytes_to_fp(cmx_1)?;
    let c2 = bytes_to_fp(cmx_2)?;
    let c3 = bytes_to_fp(cmx_3)?;
    let c4 = bytes_to_fp(cmx_4)?;
    let gc = bytes_to_fp(gov_comm)?;
    let vri = bytes_to_fp(vote_round_id)?;

    let rho = poseidon::Hash::<_, P128Pow5T3, ConstantLength<6>, 3, 2>::init()
        .hash([c1, c2, c3, c4, gc, vri]);

    Ok(fp_to_bytes(rho))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_derive_gov_nullifier_deterministic() {
        let nk = [0x01u8; 32];
        let vri = [0x02u8; 32];
        let nf = [0x03u8; 32];

        let result1 = derive_gov_nullifier(&nk, &vri, &nf).unwrap();
        let result2 = derive_gov_nullifier(&nk, &vri, &nf).unwrap();

        assert_eq!(result1.len(), 32);
        assert_eq!(result1, result2, "gov nullifier must be deterministic");
    }

    #[test]
    fn test_derive_gov_nullifier_not_trivial() {
        let nk = [0x01u8; 32];
        let vri = [0x02u8; 32];
        let nf = [0x03u8; 32];

        let result = derive_gov_nullifier(&nk, &vri, &nf).unwrap();
        // Should not be all zeros or all same byte
        assert_ne!(result, vec![0x00; 32]);
        assert_ne!(result, vec![0xAA; 32]); // not the old mock
    }

    #[test]
    fn test_derive_gov_nullifier_different_inputs_different_outputs() {
        let nk = [0x01u8; 32];
        let vri = [0x02u8; 32];
        let nf1 = [0x03u8; 32];
        let nf2 = [0x04u8; 32];

        let result1 = derive_gov_nullifier(&nk, &vri, &nf1).unwrap();
        let result2 = derive_gov_nullifier(&nk, &vri, &nf2).unwrap();

        assert_ne!(result1, result2, "different nullifiers must produce different gov nullifiers");
    }

    #[test]
    fn test_construct_van_deterministic() {
        let g_d = [0x10u8; 32];
        let pk_d = [0x20u8; 32];
        let vri = [0x05u8; 32];
        let rcm = [0x06u8; 32];

        let result1 = construct_van(&g_d, &pk_d, 1000, &vri, &rcm).unwrap();
        let result2 = construct_van(&g_d, &pk_d, 1000, &vri, &rcm).unwrap();

        assert_eq!(result1.len(), 32);
        assert_eq!(result1, result2, "VAN must be deterministic");
    }

    #[test]
    fn test_construct_van_not_trivial() {
        let g_d = [0x10u8; 32];
        let pk_d = [0x20u8; 32];
        let vri = [0x05u8; 32];
        let rcm = [0x06u8; 32];

        let result = construct_van(&g_d, &pk_d, 1000, &vri, &rcm).unwrap();
        assert_ne!(result, vec![0x00; 32]);
        assert_ne!(result, vec![0xBB; 32]); // not the old mock
    }

    #[test]
    fn test_construct_van_zero_weight() {
        let g_d = [0x10u8; 32];
        let pk_d = [0x20u8; 32];
        let vri = [0x05u8; 32];
        let rcm = [0x06u8; 32];

        assert!(construct_van(&g_d, &pk_d, 0, &vri, &rcm).is_err());
    }

    #[test]
    fn test_construct_van_different_rand_different_output() {
        let g_d = [0x10u8; 32];
        let pk_d = [0x20u8; 32];
        let vri = [0x05u8; 32];
        let rcm1 = [0x06u8; 32];
        let rcm2 = [0x07u8; 32];

        let result1 = construct_van(&g_d, &pk_d, 1000, &vri, &rcm1).unwrap();
        let result2 = construct_van(&g_d, &pk_d, 1000, &vri, &rcm2).unwrap();

        assert_ne!(result1, result2, "different randomness must produce different VAN");
    }

    /// Known-answer test vectors for governance nullifier and VAN.
    /// These values are deterministic for the given inputs. If this test breaks,
    /// the Poseidon formula or input ordering has diverged from the spec.
    /// Cross-reference: orchard/src/delegation/imt.rs:gov_null_hash,
    ///                  orchard/src/delegation/circuit.rs:gov_commitment_hash.
    #[test]
    fn test_known_answer_gov_nullifier() {
        let nk = [0x01u8; 32];
        let vri = [0x02u8; 32];
        let nf = [0x03u8; 32];

        let result = derive_gov_nullifier(&nk, &vri, &nf).unwrap();
        let expected = hex::decode("6a8038d1868237a643da723a441ace037c03502c7a70369b21d1e31293fc302b").unwrap();
        assert_eq!(result, expected, "gov nullifier known-answer mismatch — formula may have diverged from orchard reference");
    }

    #[test]
    fn test_known_answer_van() {
        let g_d = [0x10u8; 32];
        let pk_d = [0x20u8; 32];
        let vri = [0x05u8; 32];
        let rcm = [0x06u8; 32];

        let result = construct_van(&g_d, &pk_d, 1000, &vri, &rcm).unwrap();
        let expected = hex::decode("4af713fb9de5d4f7b5ba4a28177a62f7963a084ba5e8f1a46a6b034b5fc93717").unwrap();
        assert_eq!(result, expected, "VAN known-answer mismatch — formula may have diverged from orchard reference");
    }

    #[test]
    fn test_invalid_length_inputs() {
        assert!(derive_gov_nullifier(&[0u8; 31], &[0u8; 32], &[0u8; 32]).is_err());
        assert!(derive_gov_nullifier(&[0u8; 32], &[0u8; 31], &[0u8; 32]).is_err());
        assert!(derive_gov_nullifier(&[0u8; 32], &[0u8; 32], &[0u8; 31]).is_err());

        assert!(construct_van(&[0u8; 31], &[0u8; 32], 1000, &[0u8; 32], &[0u8; 32]).is_err());
        assert!(construct_van(&[0u8; 32], &[0u8; 31], 1000, &[0u8; 32], &[0u8; 32]).is_err());
    }

    #[test]
    fn test_compute_rho_binding_deterministic() {
        let cmx1 = [0x01u8; 32];
        let cmx2 = [0x02u8; 32];
        let cmx3 = [0x03u8; 32];
        let cmx4 = [0x04u8; 32];
        let gov = [0x05u8; 32];
        let vri = [0x06u8; 32];

        let r1 = compute_rho_binding(&cmx1, &cmx2, &cmx3, &cmx4, &gov, &vri).unwrap();
        let r2 = compute_rho_binding(&cmx1, &cmx2, &cmx3, &cmx4, &gov, &vri).unwrap();

        assert_eq!(r1.len(), 32);
        assert_eq!(r1, r2, "rho_binding must be deterministic");
    }

    #[test]
    fn test_compute_rho_binding_different_cmx() {
        let cmx1 = [0x01u8; 32];
        let cmx2 = [0x02u8; 32];
        let cmx3 = [0x03u8; 32];
        let cmx4 = [0x04u8; 32];
        let gov = [0x05u8; 32];
        let vri = [0x06u8; 32];

        let base = compute_rho_binding(&cmx1, &cmx2, &cmx3, &cmx4, &gov, &vri).unwrap();

        // Changing any cmx should change the output
        let alt1 = compute_rho_binding(&[0x11u8; 32], &cmx2, &cmx3, &cmx4, &gov, &vri).unwrap();
        let alt2 = compute_rho_binding(&cmx1, &[0x12u8; 32], &cmx3, &cmx4, &gov, &vri).unwrap();
        let alt3 = compute_rho_binding(&cmx1, &cmx2, &[0x13u8; 32], &cmx4, &gov, &vri).unwrap();
        let alt4 = compute_rho_binding(&cmx1, &cmx2, &cmx3, &[0x14u8; 32], &gov, &vri).unwrap();

        assert_ne!(base, alt1, "changing cmx_1 must change rho");
        assert_ne!(base, alt2, "changing cmx_2 must change rho");
        assert_ne!(base, alt3, "changing cmx_3 must change rho");
        assert_ne!(base, alt4, "changing cmx_4 must change rho");
    }

    #[test]
    fn test_known_answer_rho_binding() {
        let cmx1 = [0x01u8; 32];
        let cmx2 = [0x02u8; 32];
        let cmx3 = [0x03u8; 32];
        let cmx4 = [0x04u8; 32];
        let gov = [0x05u8; 32];
        let vri = [0x06u8; 32];

        let result = compute_rho_binding(&cmx1, &cmx2, &cmx3, &cmx4, &gov, &vri).unwrap();

        // This is a regression test: if the hash changes, the formula has diverged.
        // Capture the actual value on first successful run.
        assert_eq!(result.len(), 32);
        assert_ne!(result, vec![0u8; 32], "rho_binding must not be zero");
        // Known-answer: captured from first successful run.
        let expected = hex::decode("37e1cdff84e15fd576c3d52bf7b33769f29a591969ec964a0e098df031e3d422").unwrap();
        assert_eq!(result, expected, "rho_binding known-answer mismatch — formula may have diverged");
    }
}
