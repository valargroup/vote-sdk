use crate::types::{VotingError, WitnessData};

/// Generate Merkle witness for a note at snapshot height.
/// tree_state_bytes: protobuf-encoded TreeState from lightwalletd.
/// STUB: returns mock WitnessData with zero-filled auth path.
pub fn generate_note_witness(
    note_position: u64,
    _snapshot_height: u32,
    _tree_state_bytes: &[u8],
) -> Result<WitnessData, VotingError> {
    // Orchard tree is depth 32
    let auth_path: Vec<Vec<u8>> = (0..32).map(|_| vec![0u8; 32]).collect();

    Ok(WitnessData {
        note_commitment: vec![0xAA; 32],
        position: note_position,
        root: vec![0xBB; 32],
        auth_path,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_generate_note_witness_stub() {
        let result = generate_note_witness(42, 100_000, &[]).unwrap();
        assert_eq!(result.note_commitment.len(), 32);
        assert_eq!(result.position, 42);
        assert_eq!(result.root.len(), 32);
        assert_eq!(result.auth_path.len(), 32);
        for sibling in &result.auth_path {
            assert_eq!(sibling.len(), 32);
        }
    }
}
