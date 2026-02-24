use ff::{Field, FromUniformBytes, PrimeField};
use group::{Curve, GroupEncoding};
use pasta_curves::pallas;

use orchard::keys::SpendingKey;
use orchard::vote_proof::{build_vote_proof_from_delegation, VOTE_COMM_TREE_DEPTH};

use crate::types::{
    ct_option_to_result, validate_vote_decision, EncryptedShare, ProofProgressReporter,
    VoteCommitmentBundle, VotingError,
};

/// Build vote commitment + ZKP #2.
///
/// Generates a real Halo2 vote proof by calling `build_vote_proof_from_delegation`.
/// The builder handles share decomposition and El Gamal encryption internally,
/// ensuring the ciphertexts in the proof match those returned in `enc_shares`.
///
/// # Arguments
///
/// * `hotkey_seed` - Seed bytes for the hotkey SpendingKey (from app secure storage).
/// * `network_id` - 0=mainnet, 1=testnet.
/// * `address_index` - Diversifier index used for the hotkey address during delegation.
/// * `total_note_value` - Sum of delegated note values.
/// * `gov_comm_rand` - 32-byte VAN blinding factor (from DB).
/// * `voting_round_id` - 32-byte voting round identifier (from DB, hex-decoded).
/// * `ea_pk` - 32-byte compressed election authority public key.
/// * `proposal_id` - Which proposal to vote on (0-15).
/// * `choice` - Vote decision index (0-indexed into the proposal's options).
/// * `num_options` - Number of options declared for this proposal (2-8).
/// * `van_auth_path` - 24 siblings for the VAN Merkle path in the vote commitment tree.
/// * `van_position` - Leaf position of the VAN in the tree.
/// * `anchor_height` - Block height at which the tree was snapshotted.
/// * `progress` - Callback for proof generation progress.
#[allow(clippy::too_many_arguments)]
pub fn build_vote_commitment(
    hotkey_seed: &[u8],
    network_id: u32,
    address_index: u32,
    total_note_value: u64,
    gov_comm_rand: &[u8],
    voting_round_id: &[u8],
    ea_pk: &[u8],
    proposal_id: u32,
    choice: u32,
    num_options: u32,
    van_auth_path: &[[u8; 32]],
    van_position: u32,
    anchor_height: u32,
    proposal_authority: u64,
    progress: &dyn ProofProgressReporter,
) -> Result<VoteCommitmentBundle, VotingError> {
    validate_vote_decision(choice, num_options)?;
    if proposal_id > 15 {
        return Err(VotingError::InvalidInput {
            message: format!("proposal_id must be 0..15, got {}", proposal_id),
        });
    }
    if van_auth_path.len() != VOTE_COMM_TREE_DEPTH {
        return Err(VotingError::InvalidInput {
            message: format!(
                "van_auth_path must have {} siblings, got {}",
                VOTE_COMM_TREE_DEPTH,
                van_auth_path.len()
            ),
        });
    }

    // Derive the Orchard SpendingKey from the hotkey seed via ZIP-32.
    progress.on_progress(0.05);
    let sk = derive_spending_key(hotkey_seed, network_id)?;

    // Parse gov_comm_rand → pallas::Base
    let gcr_bytes: [u8; 32] = gov_comm_rand.try_into().map_err(|_| VotingError::InvalidInput {
        message: format!(
            "gov_comm_rand must be 32 bytes, got {}",
            gov_comm_rand.len()
        ),
    })?;
    let gcr = ct_option_to_result(
        pallas::Base::from_repr(gcr_bytes),
        "gov_comm_rand is not a valid Pallas field element",
    )?;

    // Parse voting_round_id → pallas::Base via wide reduction.
    // The round ID is a Blake2b-256 hash which is non-canonical ~75% of the time
    // (value >= Pallas modulus). Zero-extend to 64 bytes for canonical reduction,
    // matching governance.rs and the chain verifier's hash_bytes_to_fp.
    let vri_bytes: [u8; 32] = voting_round_id.try_into().map_err(|_| VotingError::InvalidInput {
        message: format!(
            "voting_round_id must be 32 bytes, got {}",
            voting_round_id.len()
        ),
    })?;
    let mut vri_wide = [0u8; 64];
    vri_wide[..32].copy_from_slice(&vri_bytes);
    let vri = pallas::Base::from_uniform_bytes(&vri_wide);

    // Parse ea_pk → pallas::Affine (compressed point)
    let ea_pk_bytes: [u8; 32] = ea_pk.try_into().map_err(|_| VotingError::InvalidInput {
        message: format!("ea_pk must be 32 bytes, got {}", ea_pk.len()),
    })?;
    let ea_pk_point: pallas::Point = Option::from(pallas::Point::from_bytes(&ea_pk_bytes))
        .ok_or_else(|| VotingError::InvalidInput {
            message: "ea_pk is not a valid compressed Pallas point".to_string(),
        })?;
    let ea_pk_affine = ea_pk_point.to_affine();

    // Convert auth path from byte slices to pallas::Base field elements
    let mut auth_path = [pallas::Base::zero(); VOTE_COMM_TREE_DEPTH];
    for (i, sibling) in van_auth_path.iter().enumerate() {
        auth_path[i] = ct_option_to_result(
            pallas::Base::from_repr(*sibling),
            &format!("van_auth_path[{}] is not a valid Pallas field element", i),
        )?;
    }

    // Generate the real proof
    progress.on_progress(0.10);
    let mut rng = rand::thread_rng();
    // Generate spend-auth randomizer for the voting key.
    // The caller will need alpha_v to sign the TX2 sighash with rsk_v = ask_v.randomize(&alpha_v).
    let alpha_v = pallas::Scalar::random(&mut rng);
    let vote_bundle = build_vote_proof_from_delegation(
        &sk,
        address_index,
        total_note_value,
        gcr,
        vri,
        auth_path,
        van_position,
        anchor_height,
        proposal_id as u64,
        choice as u64,
        ea_pk_affine,
        alpha_v,
        proposal_authority,
        &mut rng,
    )
    .map_err(|e| VotingError::ProofFailed {
        message: format!("vote proof generation failed: {}", e),
    })?;
    progress.on_progress(1.0);

    // Convert Instance public inputs to byte vectors
    let van_nullifier = vote_bundle.instance.van_nullifier.to_repr().to_vec();
    let van_new = vote_bundle.instance.vote_authority_note_new.to_repr().to_vec();
    let vote_commitment = vote_bundle.instance.vote_commitment.to_repr().to_vec();

    // Convert encrypted shares from builder output to librustvoting EncryptedShare format
    let enc_shares: Vec<EncryptedShare> = vote_bundle
        .encrypted_shares
        .iter()
        .map(|es| EncryptedShare {
            c1: es.c1.to_vec(),
            c2: es.c2.to_vec(),
            share_index: es.share_index,
            plaintext_value: es.plaintext_value,
            randomness: es.randomness.to_vec(),
        })
        .collect();

    Ok(VoteCommitmentBundle {
        van_nullifier,
        vote_authority_note_new: van_new,
        vote_commitment,
        proposal_id,
        proof: vote_bundle.proof,
        enc_shares,
        anchor_height,
        vote_round_id: hex::encode(voting_round_id),
        shares_hash: vote_bundle.shares_hash.to_repr().to_vec(),
        share_blinds: vote_bundle.share_blinds.iter().map(|b| b.to_repr().to_vec()).collect(),
        r_vpk_bytes: vote_bundle.r_vpk_bytes.to_vec(),
        alpha_v: alpha_v.to_repr().to_vec(),
    })
}

/// Derive an Orchard SpendingKey from hotkey seed bytes using ZIP-32.
pub fn derive_spending_key(hotkey_seed: &[u8], network_id: u32) -> Result<SpendingKey, VotingError> {
    use zcash_keys::keys::UnifiedSpendingKey;
    use zcash_protocol::consensus::{MAIN_NETWORK, TEST_NETWORK};
    use zip32::AccountId;

    if hotkey_seed.len() < 32 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "hotkey_seed must be at least 32 bytes, got {}",
                hotkey_seed.len()
            ),
        });
    }

    let account = AccountId::try_from(0u32).expect("account 0 is valid");

    let usk = match network_id {
        0 => UnifiedSpendingKey::from_seed(&MAIN_NETWORK, hotkey_seed, account),
        1 => UnifiedSpendingKey::from_seed(&TEST_NETWORK, hotkey_seed, account),
        _ => {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "invalid network_id {}, expected 0 (mainnet) or 1 (testnet)",
                    network_id
                ),
            });
        }
    }
    .map_err(|e| VotingError::InvalidInput {
        message: format!("failed to derive UnifiedSpendingKey from hotkey_seed: {}", e),
    })?;

    // Extract the Orchard SpendingKey from the USK.
    // UnifiedSpendingKey internally holds the orchard SpendingKey but doesn't
    // expose it directly. We go through the FVK + transparent derivation path.
    // However, the vote proof builder needs the SpendingKey, not the FVK.
    // The orchard SpendingKey can be extracted by converting via the
    // raw bytes that UnifiedSpendingKey stores.
    //
    // UnifiedSpendingKey::orchard() returns &orchard::keys::SpendingKey
    let sk: &SpendingKey = usk.orchard();
    Ok(sk.clone())
}

#[cfg(test)]
mod tests {
    use super::*;

    struct TestReporter;

    impl ProofProgressReporter for TestReporter {
        fn on_progress(&self, _progress: f64) {}
    }

    #[test]
    fn test_build_vote_commitment_bad_choice() {
        assert!(build_vote_commitment(
            &[0x42; 64],
            1,
            0,
            1_000_000,
            &[0u8; 32],
            &[0u8; 32],
            &[0u8; 32],
            0,
            3, // invalid choice (num_options=2)
            2,
            &[[0u8; 32]; 24],
            0,
            1,
            65535,
            &TestReporter,
        )
        .is_err());
    }

    #[test]
    fn test_build_vote_commitment_bad_proposal_id() {
        assert!(build_vote_commitment(
            &[0x42; 64],
            1,
            0,
            1_000_000,
            &[0u8; 32],
            &[0u8; 32],
            &[0u8; 32],
            16, // invalid proposal_id
            0,
            2,
            &[[0u8; 32]; 24],
            0,
            1,
            65535,
            &TestReporter,
        )
        .is_err());
    }

    #[test]
    fn test_build_vote_commitment_wrong_auth_path_len() {
        assert!(build_vote_commitment(
            &[0x42; 64],
            1,
            0,
            1_000_000,
            &[0u8; 32],
            &[0u8; 32],
            &[0u8; 32],
            0,
            0,
            2,
            &[[0u8; 32]; 10], // wrong length
            0,
            1,
            65535,
            &TestReporter,
        )
        .is_err());
    }
}
