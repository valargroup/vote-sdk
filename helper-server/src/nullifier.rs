//! Share nullifier derivation.
//!
//! Per Gov Steps V1 §5.3 (extended with round binding):
//! ```text
//! share_nullifier = Poseidon(domain_tag, vote_commitment, share_index, c1_x, c2_x, voting_round_id)
//! ```
//!
//! Single `ConstantLength<6>` Poseidon hash (3 permutations at rate=2).
//! The `voting_round_id` input binds the nullifier to a specific voting round,
//! preventing cross-round proof replay (the commitment tree is global, not per-round).
//!
//! Uses the same Poseidon (P128Pow5T3 over Pallas Fp) as the vote-commitment-tree
//! crate.

use base64::prelude::*;
use ff::PrimeField;
use pasta_curves::Fp;

use vote_commitment_tree::poseidon_hash_6;

use crate::types::SharePayload;

/// Domain separator for share nullifiers, encoded as an Fp element.
/// "share spend" → first 31 bytes of the UTF-8 string, zero-padded.
fn domain_tag() -> Fp {
    let mut bytes = [0u8; 32];
    let tag = b"share spend";
    bytes[..tag.len()].copy_from_slice(tag);
    // Ensure the encoding is canonical (< field modulus). Since the tag is
    // short, the top byte is zero, so this always succeeds.
    Fp::from_repr(bytes).unwrap()
}

/// Derive the share nullifier for a queued share.
///
/// Inputs:
/// - `vote_commitment`: the VC leaf value (Fp) from the vote commitment tree
/// - `voting_round_id`: the round identifier (as Fp, using wide reduction)
///
/// Returns `None` if any field decoding fails.
pub fn derive_share_nullifier(
    payload: &SharePayload,
    vote_commitment: Fp,
    voting_round_id: Fp,
) -> Option<Fp> {
    let share_index_fp = Fp::from(payload.enc_share.share_index as u64);

    // Hash the encrypted share: Poseidon(c1_fp, c2_fp).
    let c1_bytes = BASE64_STANDARD.decode(&payload.enc_share.c1).ok()?;
    let c2_bytes = BASE64_STANDARD.decode(&payload.enc_share.c2).ok()?;
    if c1_bytes.len() != 32 || c2_bytes.len() != 32 {
        return None;
    }
    let mut c1_arr = [0u8; 32];
    let mut c2_arr = [0u8; 32];
    c1_arr.copy_from_slice(&c1_bytes);
    c2_arr.copy_from_slice(&c2_bytes);
    // Compressed Pallas encoding stores the sign of y in bit 7 of byte 31.
    // Clear it to recover the raw x-coordinate for field interpretation.
    c1_arr[31] &= 0x7F;
    c2_arr[31] &= 0x7F;
    let c1_fp = Option::from(Fp::from_repr(c1_arr))?;
    let c2_fp = Option::from(Fp::from_repr(c2_arr))?;
    // Single ConstantLength<6> Poseidon hash (matches ZKP #3 circuit condition 5).
    Some(poseidon_hash_6(
        domain_tag(),
        vote_commitment,
        share_index_fp,
        c1_fp,
        c2_fp,
        voting_round_id,
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::types::EncryptedShareWire;

    #[test]
    fn nullifier_deterministic() {
        let vc = Fp::from(42);
        let round_id = Fp::from(777);
        let c1 = BASE64_STANDARD.encode(Fp::from(1).to_repr());
        let c2 = BASE64_STANDARD.encode(Fp::from(2).to_repr());

        let payload = SharePayload {
            shares_hash: BASE64_STANDARD.encode([0u8; 32]),
            proposal_id: 0,
            vote_decision: 1,
            enc_share: EncryptedShareWire {
                c1: c1.clone(),
                c2: c2.clone(),
                share_index: 0,
            },
            share_index: 0,
            tree_position: 1,
            vote_round_id: hex::encode([0u8; 32]),
            all_enc_shares: vec![],
        };

        let nf1 = derive_share_nullifier(&payload, vc, round_id).unwrap();
        let nf2 = derive_share_nullifier(&payload, vc, round_id).unwrap();
        assert_eq!(nf1, nf2);

        // Different share_index → different nullifier.
        let mut payload2 = payload.clone();
        payload2.enc_share.share_index = 1;
        let nf3 = derive_share_nullifier(&payload2, vc, round_id).unwrap();
        assert_ne!(nf1, nf3);

        // Different voting_round_id → different nullifier.
        let nf4 = derive_share_nullifier(&payload, vc, Fp::from(888)).unwrap();
        assert_ne!(nf1, nf4);
    }

    #[test]
    fn nullifier_works_with_sign_bit_set() {
        // Simulate compressed Pallas point bytes where the sign bit (bit 7 of
        // byte 31) is set. Without clearing the sign bit, Fp::from_repr would
        // fail because the value exceeds the field modulus.
        let mut c1_bytes = Fp::from(100).to_repr();
        let mut c2_bytes = Fp::from(200).to_repr();
        // Set the sign bit on both.
        c1_bytes[31] |= 0x80;
        c2_bytes[31] |= 0x80;

        let payload = SharePayload {
            shares_hash: BASE64_STANDARD.encode([0u8; 32]),
            proposal_id: 1,
            vote_decision: 0,
            enc_share: EncryptedShareWire {
                c1: BASE64_STANDARD.encode(c1_bytes),
                c2: BASE64_STANDARD.encode(c2_bytes),
                share_index: 2,
            },
            share_index: 2,
            tree_position: 5,
            vote_round_id: hex::encode([0u8; 32]),
            all_enc_shares: vec![],
        };

        // Must not return None — the sign bit should be cleared internally.
        let nf = derive_share_nullifier(&payload, Fp::from(99), Fp::from(555));
        assert!(nf.is_some(), "nullifier derivation must handle sign bit");
    }
}
