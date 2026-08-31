//! Canonical wallet-side encodings for vote transaction signature digests.

use blake2b_simd::Params;

/// Domain separator for an atomic delegation followed by an ordered cast batch.
pub const DELEGATE_AND_CAST_VOTE_BATCH_DOMAIN: &[u8] =
    b"SVOTE_DELEGATE_AND_CAST_VOTE_BATCH_SIGHASH_V1";

/// Effecting public fields for one cast in a composite delegation/cast batch.
pub struct DelegateAndCastVoteEffect {
    pub r_vpk: [u8; 32],
    pub van_nullifier: [u8; 32],
    pub vote_authority_note_new: [u8; 32],
    pub vote_commitment: [u8; 32],
    pub proposal_id: u32,
}

fn write32(out: &mut Vec<u8>, value: &[u8; 32]) {
    out.extend_from_slice(value);
}

fn write_u32(out: &mut Vec<u8>, value: u32) {
    let mut field = [0u8; 32];
    field[..4].copy_from_slice(&value.to_le_bytes());
    out.extend_from_slice(&field);
}

/// Computes the digest every cast authorization signs in a composite batch.
///
/// The chain requires 32-byte byte fields and validates that every cast belongs
/// to `round_id`. This helper mirrors the fixed-width Go consensus encoding.
pub fn delegate_and_cast_vote_batch_sighash(
    round_id: &[u8; 32],
    delegation_van_cmx: &[u8; 32],
    effects: &[DelegateAndCastVoteEffect],
) -> [u8; 32] {
    let mut canonical = DELEGATE_AND_CAST_VOTE_BATCH_DOMAIN.to_vec();
    write32(&mut canonical, round_id);
    write32(&mut canonical, delegation_van_cmx);
    write_u32(&mut canonical, effects.len() as u32);
    for (index, effect) in effects.iter().enumerate() {
        write_u32(&mut canonical, index as u32);
        write32(&mut canonical, &effect.r_vpk);
        write32(&mut canonical, &effect.van_nullifier);
        write32(&mut canonical, &effect.vote_authority_note_new);
        write32(&mut canonical, &effect.vote_commitment);
        write_u32(&mut canonical, effect.proposal_id);
    }
    let hash = Params::new().hash_length(32).hash(&canonical);
    hash.as_bytes().try_into().expect("Blake2b-256 output")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn delegate_and_cast_vote_batch_frozen_vector() {
        let first = DelegateAndCastVoteEffect {
            r_vpk: [2; 32],
            van_nullifier: [3; 32],
            vote_authority_note_new: [4; 32],
            vote_commitment: [5; 32],
            proposal_id: 1,
        };
        let second = DelegateAndCastVoteEffect {
            r_vpk: [6; 32],
            van_nullifier: [7; 32],
            vote_authority_note_new: [8; 32],
            vote_commitment: [9; 32],
            proposal_id: 2,
        };
        let digest = delegate_and_cast_vote_batch_sighash(&[1; 32], &[9; 32], &[first, second]);
        assert_eq!(
            hex::encode(digest),
            "1b884143da3d43cd2834a1f347c60d76b2d9a5b0ba5da6f91a4b2b09511f6e23"
        );
    }

    #[test]
    fn delegate_and_cast_vote_batch_redpallas_fixture() {
        use rand_chacha::{rand_core::SeedableRng, ChaCha20Rng};
        use reddsa::{orchard::SpendAuth, SigningKey, VerificationKey};

        let mut rng = ChaCha20Rng::from_seed([0x42; 32]);
        let signing_key = SigningKey::<SpendAuth>::new(&mut rng);
        let verification_key = VerificationKey::from(&signing_key);
        let r_vpk: [u8; 32] = verification_key.into();
        let effect = DelegateAndCastVoteEffect {
            r_vpk,
            van_nullifier: [3; 32],
            vote_authority_note_new: [4; 32],
            vote_commitment: [5; 32],
            proposal_id: 1,
        };
        let digest = delegate_and_cast_vote_batch_sighash(&[1; 32], &[9; 32], &[effect]);
        let signature: [u8; 64] = signing_key.sign(&mut rng, &digest).into();

        assert_eq!(
            (hex::encode(r_vpk), hex::encode(signature)),
            (
                "eccd7a0045727bbf2ddb854442a300485e5c74f8da7d5ece7c2ed7ddbe7b4022"
                    .to_string(),
                "9d0f50a84e68efb23fce19443ec876d2c124f451a185520dc56c06f71ae9ac00d510f720bdbffa365ad8ca9c6a7c05fb3cff575fcfcc6dc1008d5b89d4029d1b"
                    .to_string(),
            )
        );
    }
}
