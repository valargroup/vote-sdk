use crate::types::{
    validate_encrypted_shares, EncryptedShare, SharePayload, VoteCommitmentBundle, VotingError,
};

/// Build payloads for helper server (one per share).
/// STUB: returns mock payloads.
pub fn build_share_payloads(
    enc_shares: &[EncryptedShare],
    commitment: &VoteCommitmentBundle,
) -> Result<Vec<SharePayload>, VotingError> {
    validate_encrypted_shares(enc_shares)?;

    let mut payloads = Vec::with_capacity(enc_shares.len());
    for share in enc_shares {
        payloads.push(SharePayload {
            shares_hash: vec![0xDD; 32],
            proposal_id: commitment.proposal_id.clone(),
            vote_decision: 0,
            enc_share: share.clone(),
            tree_position: 0,
        });
    }

    Ok(payloads)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn mock_enc_shares() -> Vec<EncryptedShare> {
        vec![
            EncryptedShare {
                c1: vec![0xC1; 32],
                c2: vec![0xC2; 32],
                share_index: 0,
                plaintext_value: 1,
            },
            EncryptedShare {
                c1: vec![0xC1; 32],
                c2: vec![0xC2; 32],
                share_index: 1,
                plaintext_value: 4,
            },
        ]
    }

    fn mock_commitment() -> VoteCommitmentBundle {
        VoteCommitmentBundle {
            van_nullifier: vec![0xAA; 32],
            vote_authority_note_new: vec![0xBB; 32],
            vote_commitment: vec![0xCC; 32],
            proposal_id: "prop-1".to_string(),
            proof: vec![0xAB; 256],
        }
    }

    #[test]
    fn test_build_share_payloads_stub() {
        let result = build_share_payloads(&mock_enc_shares(), &mock_commitment()).unwrap();
        assert_eq!(result.len(), 2);
        assert_eq!(result[0].proposal_id, "prop-1");
        assert_eq!(result[0].enc_share.share_index, 0);
        assert_eq!(result[1].enc_share.share_index, 1);
    }
}
