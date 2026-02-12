use crate::types::{
    validate_encrypted_shares, validate_vote_decision, EncryptedShare,
    VoteCommitmentBundle, VotingError,
};

/// Build vote commitment + ZKP #2.
/// choice: 0=support, 1=oppose, 2=skip.
/// STUB: returns mock bundle.
pub fn build_vote_commitment(
    proposal_id: &str,
    choice: u32,
    enc_shares: &[EncryptedShare],
    van_witness: &[u8],
) -> Result<VoteCommitmentBundle, VotingError> {
    validate_vote_decision(choice)?;
    validate_encrypted_shares(enc_shares)?;

    if van_witness.is_empty() {
        return Err(VotingError::InvalidInput {
            message: "van_witness must not be empty".to_string(),
        });
    }
    if proposal_id.is_empty() {
        return Err(VotingError::InvalidInput {
            message: "proposal_id must not be empty".to_string(),
        });
    }

    Ok(VoteCommitmentBundle {
        van_nullifier: vec![0xAA; 32],
        vote_authority_note_new: vec![0xBB; 32],
        vote_commitment: vec![0xCC; 32],
        proposal_id: proposal_id.to_string(),
        proof: vec![0xAB; 256],
    })
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

    #[test]
    fn test_build_vote_commitment_stub() {
        let result =
            build_vote_commitment("prop-1", 0, &mock_enc_shares(), &[0xDD; 64]).unwrap();
        assert_eq!(result.van_nullifier.len(), 32);
        assert_eq!(result.vote_authority_note_new.len(), 32);
        assert_eq!(result.vote_commitment.len(), 32);
        assert_eq!(result.proposal_id, "prop-1");
        assert!(!result.proof.is_empty());
    }

    #[test]
    fn test_build_vote_commitment_bad_choice() {
        assert!(build_vote_commitment("prop-1", 3, &mock_enc_shares(), &[0xDD; 64]).is_err());
    }

    #[test]
    fn test_build_vote_commitment_empty_proposal() {
        assert!(build_vote_commitment("", 0, &mock_enc_shares(), &[0xDD; 64]).is_err());
    }
}
