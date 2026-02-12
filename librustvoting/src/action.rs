use crate::types::{
    validate_hotkey, validate_notes, validate_round_params, DelegationAction, NoteInfo,
    VotingError, VotingHotkey, VotingRoundParams,
};

/// Construct the dummy action for keystone signing.
/// Notes are padded to 4 internally if fewer are provided.
/// STUB: returns mock action bytes, rk, and sighash.
pub fn construct_delegation_action(
    hotkey: &VotingHotkey,
    notes: &[NoteInfo],
    params: &VotingRoundParams,
) -> Result<DelegationAction, VotingError> {
    validate_hotkey(hotkey)?;
    validate_notes(notes)?;
    validate_round_params(params)?;

    Ok(DelegationAction {
        action_bytes: vec![0xDA; 128],
        rk: vec![0xDE; 32],
        sighash: vec![0x5A; 32],
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn mock_note() -> NoteInfo {
        NoteInfo {
            commitment: vec![0x01; 32],
            nullifier: vec![0x02; 32],
            value: 1_000_000,
            position: 42,
        }
    }

    fn mock_hotkey() -> VotingHotkey {
        VotingHotkey {
            secret_key: vec![0x42; 32],
            public_key: vec![0x43; 32],
            address: "zvote1test".to_string(),
        }
    }

    fn mock_params() -> VotingRoundParams {
        VotingRoundParams {
            vote_round_id: "round-1".to_string(),
            snapshot_height: 100_000,
            ea_pk: vec![0xEA; 32],
            nc_root: vec![0xCC; 32],
            nullifier_imt_root: vec![0xDD; 32],
        }
    }

    #[test]
    fn test_construct_delegation_action_stub() {
        let result =
            construct_delegation_action(&mock_hotkey(), &[mock_note()], &mock_params()).unwrap();
        assert_eq!(result.rk.len(), 32);
        assert_eq!(result.sighash.len(), 32);
        assert!(!result.action_bytes.is_empty());
    }

    #[test]
    fn test_construct_delegation_action_no_notes() {
        let result = construct_delegation_action(&mock_hotkey(), &[], &mock_params());
        assert!(result.is_err());
    }

    #[test]
    fn test_construct_delegation_action_too_many_notes() {
        let notes: Vec<NoteInfo> = (0..5).map(|_| mock_note()).collect();
        let result = construct_delegation_action(&mock_hotkey(), &notes, &mock_params());
        assert!(result.is_err());
    }
}
