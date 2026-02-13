use ff::{Field, PrimeField};
use pasta_curves::pallas;
use rand::RngCore;
use subtle::CtOption;

use orchard::keys::{FullViewingKey, SpendingKey};
use orchard::note::{ExtractedNoteCommitment, RandomSeed, Rho};
use orchard::value::NoteValue;
use zip32::Scope;

use crate::governance;
use crate::types::{
    validate_hotkey, validate_notes, validate_round_params, DelegationAction, NoteInfo,
    VotingError, VotingHotkey, VotingRoundParams,
};

/// Try to construct a `SpendingKey` from random bytes, retrying until valid.
fn random_spending_key(rng: &mut impl RngCore) -> SpendingKey {
    loop {
        let mut bytes = [0u8; 32];
        rng.fill_bytes(&mut bytes);
        let sk: CtOption<SpendingKey> = SpendingKey::from_bytes(bytes);
        if sk.is_some().into() {
            return sk.expect("is_some checked above");
        }
    }
}

/// Construct the delegation action for Keystone signing.
///
/// Computes real governance nullifiers (padded to 4), VAN, and constrained rho (§1.3.4.1).
/// action_bytes/rk/sighash remain stubs (they need the signed note, which depends on rho — step 3).
///
/// - `nk`: 32-byte nullifier deriving key
/// - `g_d_new_x`: 32-byte x-coordinate of hotkey diversified generator
/// - `pk_d_new_x`: 32-byte x-coordinate of hotkey transmission key
pub fn construct_delegation_action(
    hotkey: &VotingHotkey,
    notes: &[NoteInfo],
    params: &VotingRoundParams,
    nk: &[u8],
    g_d_new_x: &[u8],
    pk_d_new_x: &[u8],
) -> Result<DelegationAction, VotingError> {
    validate_hotkey(hotkey)?;
    validate_notes(notes)?;
    validate_round_params(params)?;
    crate::types::validate_32_bytes(nk, "nk")?;
    crate::types::validate_32_bytes(g_d_new_x, "g_d_new_x")?;
    crate::types::validate_32_bytes(pk_d_new_x, "pk_d_new_x")?;

    // Convert vote_round_id from hex string to exactly 32 bytes.
    // Rejecting non-32-byte values prevents silent truncation/padding collisions.
    let vote_round_id_bytes = hex::decode(&params.vote_round_id).map_err(|e| {
        VotingError::InvalidInput {
            message: format!("vote_round_id is not valid hex: {}", e),
        }
    })?;
    crate::types::validate_32_bytes(&vote_round_id_bytes, "vote_round_id (decoded hex)")?;
    // Safe: validate_32_bytes already ensures exactly 32 bytes.
    let vri_32: [u8; 32] = vote_round_id_bytes.try_into().expect("validated as 32 bytes above");

    let mut rng = rand::thread_rng();

    // Compute real gov nullifiers for each input note
    let mut gov_nullifiers: Vec<Vec<u8>> = Vec::with_capacity(4);
    for note in notes {
        let gov_null = governance::derive_gov_nullifier(nk, &vri_32, &note.nullifier)?;
        gov_nullifiers.push(gov_null);
    }

    // --- Padded note generation using orchard Note API ---
    // TODO(real-keys): Replace random_spending_key() with the user's real FVK derived from
    // wallet key material. Padded notes must use the same ivk as real notes for ZKP condition 11
    // (pk_d = [ivk] * g_d). When this changes, rho_signed and all downstream values
    // (action_bytes, sighash) will change.
    let mut padded_cmx: Vec<Vec<u8>> = Vec::new();
    let mut dummy_nullifiers: Vec<Vec<u8>> = Vec::new();
    let n_real = notes.len();

    if n_real < 4 {
        let pad_sk = random_spending_key(&mut rng);
        let pad_fvk: FullViewingKey = (&pad_sk).into();

        for i in n_real..4 {
            // Derive a unique address for each padded note
            let pad_addr = pad_fvk.address_at(1000u32 + i as u32, Scope::External);

            // Generate a random Rho (represents the "previous nullifier" for this padded note)
            let rho = loop {
                let mut rho_bytes = [0u8; 32];
                rng.fill_bytes(&mut rho_bytes);
                let r: CtOption<Rho> = Rho::from_bytes(&rho_bytes);
                if r.is_some().into() {
                    break r.expect("is_some checked above");
                }
            };

            // Generate a random RandomSeed
            let rseed = loop {
                let mut rseed_bytes = [0u8; 32];
                rng.fill_bytes(&mut rseed_bytes);
                let rs: CtOption<RandomSeed> = RandomSeed::from_bytes(rseed_bytes, &rho);
                if rs.is_some().into() {
                    break rs.expect("is_some checked above");
                }
            };

            // Construct the padded note with value=0
            let pad_note = orchard::Note::from_parts(pad_addr, NoteValue::from_raw(0), rho, rseed);
            if !bool::from(pad_note.is_some()) {
                return Err(VotingError::Internal {
                    message: format!("failed to construct padded note {}", i),
                });
            }
            let pad_note = pad_note.expect("is_some checked above");

            let cmx: ExtractedNoteCommitment = pad_note.commitment().into();
            let real_nf = pad_note.nullifier(&pad_fvk);

            // TODO(real-keys): The padded note nullifier is derived using pad_fvk (mock).
            // For the ZKP, the nullifier derivation key (nk) must come from the user's real
            // key material, not a random key. Currently nk is passed in as a param (mock
            // value from Swift).
            let gov_null = governance::derive_gov_nullifier(nk, &vri_32, &real_nf.to_bytes())?;

            padded_cmx.push(cmx.to_bytes().to_vec());
            gov_nullifiers.push(gov_null);
            dummy_nullifiers.push(real_nf.to_bytes().to_vec());
        }
    }

    // Compute total weight from note values (checked to prevent silent overflow)
    let total_weight: u64 = notes
        .iter()
        .try_fold(0u64, |acc, n| acc.checked_add(n.value))
        .ok_or_else(|| VotingError::InvalidInput {
            message: "total note weight overflows u64".to_string(),
        })?;

    // Sample gov_comm_rand as a proper random field element
    let gov_comm_rand_fp = pallas::Base::random(&mut rng);
    let gov_comm_rand: [u8; 32] = gov_comm_rand_fp.to_repr();

    // Compute real VAN
    let van = governance::construct_van(
        g_d_new_x,
        pk_d_new_x,
        total_weight,
        &vri_32,
        &gov_comm_rand,
    )?;

    // Collect all 4 cmx values: real from NoteInfo.commitment, padded from above
    let mut all_cmx: Vec<Vec<u8>> = Vec::with_capacity(4);
    for note in notes {
        all_cmx.push(note.commitment.clone());
    }
    all_cmx.extend(padded_cmx.iter().cloned());
    if all_cmx.len() != 4 {
        return Err(VotingError::Internal {
            message: format!("expected 4 cmx values, got {}", all_cmx.len()),
        });
    }

    // Compute rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, gov_comm, vote_round_id)
    let rho_signed = governance::compute_rho_binding(
        &all_cmx[0], &all_cmx[1], &all_cmx[2], &all_cmx[3],
        &van, &vri_32,
    )?;

    // TODO(step-3): Use rho_signed to construct the dummy signed note and derive the sign
    // action nullifier. See spec §1.3.4.2.
    Ok(DelegationAction {
        // Stubs for now — these need the signed note derived from rho_signed (step 3)
        action_bytes: vec![0xDA; 128],
        rk: vec![0xDE; 32],
        sighash: vec![0x5A; 32],
        gov_nullifiers,
        van,
        gov_comm_rand: gov_comm_rand.to_vec(),
        dummy_nullifiers,
        rho_signed,
        padded_cmx,
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
            // Hex string representing 32 bytes
            vote_round_id: "0101010101010101010101010101010101010101010101010101010101010101"
                .to_string(),
            snapshot_height: 100_000,
            ea_pk: vec![0xEA; 32],
            nc_root: vec![0xCC; 32],
            nullifier_imt_root: vec![0xDD; 32],
        }
    }

    fn mock_nk() -> Vec<u8> {
        vec![0x11; 32]
    }

    fn mock_g_d() -> Vec<u8> {
        vec![0x22; 32]
    }

    fn mock_pk_d() -> Vec<u8> {
        vec![0x33; 32]
    }

    #[test]
    fn test_construct_delegation_action_one_note() {
        let result = construct_delegation_action(
            &mock_hotkey(),
            &[mock_note()],
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        )
        .unwrap();

        assert_eq!(result.rk.len(), 32);
        assert_eq!(result.sighash.len(), 32);
        assert!(!result.action_bytes.is_empty());

        // Gov nullifiers always padded to 4
        assert_eq!(result.gov_nullifiers.len(), 4);
        for gnull in &result.gov_nullifiers {
            assert_eq!(gnull.len(), 32);
        }

        // VAN is 32 bytes
        assert_eq!(result.van.len(), 32);
        assert_ne!(result.van, vec![0xBB; 32]);

        // gov_comm_rand is 32 bytes
        assert_eq!(result.gov_comm_rand.len(), 32);

        // First gov nullifier is real (deterministic for same inputs)
        assert_ne!(result.gov_nullifiers[0], vec![0xAA; 32]);

        // rho_signed is 32 bytes and non-zero
        assert_eq!(result.rho_signed.len(), 32);
        assert_ne!(result.rho_signed, vec![0u8; 32]);

        // padded_cmx: 3 padded notes (1 real + 3 padded = 4)
        assert_eq!(result.padded_cmx.len(), 3);
        for cmx in &result.padded_cmx {
            assert_eq!(cmx.len(), 32);
        }
    }

    #[test]
    fn test_construct_delegation_action_four_notes() {
        let notes: Vec<NoteInfo> = (0..4)
            .map(|i| NoteInfo {
                commitment: vec![i as u8 + 1; 32],
                nullifier: vec![i as u8 + 0x10; 32],
                value: 250_000,
                position: i as u64,
            })
            .collect();

        let result = construct_delegation_action(
            &mock_hotkey(),
            &notes,
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        )
        .unwrap();

        assert_eq!(result.gov_nullifiers.len(), 4);
        // All 4 should be real (no padding needed).
        // They should all be different since inputs differ.
        for i in 0..4 {
            for j in (i + 1)..4 {
                assert_ne!(
                    result.gov_nullifiers[i], result.gov_nullifiers[j],
                    "gov nullifiers {} and {} should differ",
                    i, j
                );
            }
        }

        // No padding needed — padded_cmx should be empty
        assert!(result.padded_cmx.is_empty());

        // rho_signed still computed from the 4 real cmx values
        assert_eq!(result.rho_signed.len(), 32);
        assert_ne!(result.rho_signed, vec![0u8; 32]);
    }

    #[test]
    fn test_construct_delegation_action_deterministic_gov_nullifiers() {
        let result1 = construct_delegation_action(
            &mock_hotkey(),
            &[mock_note()],
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        )
        .unwrap();

        let result2 = construct_delegation_action(
            &mock_hotkey(),
            &[mock_note()],
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        )
        .unwrap();

        // First gov nullifier (real) should be deterministic
        assert_eq!(result1.gov_nullifiers[0], result2.gov_nullifiers[0]);

        // VAN will differ because gov_comm_rand is randomly sampled each time
        // (this is expected)
    }

    #[test]
    fn test_construct_delegation_action_no_notes() {
        let result = construct_delegation_action(
            &mock_hotkey(),
            &[],
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_construct_delegation_action_too_many_notes() {
        let notes: Vec<NoteInfo> = (0..5).map(|_| mock_note()).collect();
        let result = construct_delegation_action(
            &mock_hotkey(),
            &notes,
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_construct_delegation_action_rejects_short_vote_round_id() {
        let mut params = mock_params();
        // 31 bytes (62 hex chars)
        params.vote_round_id = "01".repeat(31);

        let result = construct_delegation_action(
            &mock_hotkey(),
            &[mock_note()],
            &params,
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_construct_delegation_action_rejects_long_vote_round_id() {
        let mut params = mock_params();
        // 33 bytes (66 hex chars)
        params.vote_round_id = "01".repeat(33);

        let result = construct_delegation_action(
            &mock_hotkey(),
            &[mock_note()],
            &params,
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_rho_changes_with_different_notes() {
        // Use small byte values that are guaranteed valid Pallas field elements
        // (values with the high byte < 0x40 are always in range).
        let notes_a: Vec<NoteInfo> = (0..4)
            .map(|i| {
                let mut commitment = vec![0u8; 32];
                commitment[0] = i as u8 + 0x10;
                let mut nullifier = vec![0u8; 32];
                nullifier[0] = i as u8 + 0x20;
                NoteInfo {
                    commitment,
                    nullifier,
                    value: 250_000,
                    position: i as u64,
                }
            })
            .collect();

        let notes_b: Vec<NoteInfo> = (0..4)
            .map(|i| {
                let mut commitment = vec![0u8; 32];
                commitment[0] = i as u8 + 0x30;
                let mut nullifier = vec![0u8; 32];
                nullifier[0] = i as u8 + 0x40;
                NoteInfo {
                    commitment,
                    nullifier,
                    value: 250_000,
                    position: i as u64,
                }
            })
            .collect();

        let result_a = construct_delegation_action(
            &mock_hotkey(),
            &notes_a,
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        )
        .unwrap();

        let result_b = construct_delegation_action(
            &mock_hotkey(),
            &notes_b,
            &mock_params(),
            &mock_nk(),
            &mock_g_d(),
            &mock_pk_d(),
        )
        .unwrap();

        // Different note commitments should produce different rho
        // (VAN also differs due to random gov_comm_rand, reinforcing the difference)
        assert_ne!(
            result_a.rho_signed, result_b.rho_signed,
            "different note sets must produce different rho_signed"
        );
    }
}
