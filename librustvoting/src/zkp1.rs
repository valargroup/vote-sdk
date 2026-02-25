use std::collections::HashMap;
use std::sync::OnceLock;

use ff::PrimeField;
use halo2_proofs::{
    plonk,
    poly::commitment::Params,
    transcript::{Blake2bWrite, Challenge255},
};
use incrementalmerkletree::Hashable;
use orchard::{
    delegation::{
        builder::{build_delegation_bundle, PrecomputedRandomness, RealNoteInput},
        circuit::Circuit as DelegationCircuit,
        imt::{ImtError, ImtProofData, ImtProvider},
    },
    keys::{Diversifier, FullViewingKey, Scope},
    note::{RandomSeed, Rho},
    tree::{MerkleHashOrchard, MerklePath},
    value::NoteValue,
    NOTE_COMMITMENT_TREE_DEPTH,
};
use pasta_curves::{pallas, vesta};
use rand::rngs::OsRng;
use zcash_keys::keys::UnifiedFullViewingKey;
use zcash_protocol::consensus::Network;

use crate::types::{
    ct_option_to_result, validate_32_bytes, DelegationProofResult, NoteInfo,
    ProofProgressReporter, VotingError, WitnessData,
};

/// Circuit size parameter. Matches the value used in delegation builder/circuit tests.
const K: u32 = 14;

// Cached proving key — keygen is deterministic and expensive (~5 min on simulator,
// ~30s on device). Compute once per process and reuse for all subsequent proofs.
static DELEGATION_PK_CACHE: OnceLock<(
    Params<vesta::Affine>,
    plonk::ProvingKey<vesta::Affine>,
)> = OnceLock::new();

fn get_delegation_proving_key() -> &'static (Params<vesta::Affine>, plonk::ProvingKey<vesta::Affine>) {
    DELEGATION_PK_CACHE.get_or_init(|| {
        let params = Params::new(K);
        let vk = plonk::keygen_vk(&params, &DelegationCircuit::default())
            .expect("delegation keygen_vk: circuit is valid");
        let pk = plonk::keygen_pk(&params, vk, &DelegationCircuit::default())
            .expect("delegation keygen_pk: circuit is valid");
        (params, pk)
    })
}

// ================================================================
// PIR-backed IMT Provider
// ================================================================

use pir_client::PirClientBlocking;

/// Convert a PIR-crate `ImtProofData` (from `pir_client`, re-exported from `imt_tree`)
/// into the orchard-crate `ImtProofData`. Both structs have identical fields — this
/// is a field-by-field copy bridging the two crate boundaries.
pub fn convert_pir_proof(pir: pir_client::ImtProofData) -> ImtProofData {
    ImtProofData {
        root: pir.root,
        low: pir.low,
        width: pir.width,
        leaf_pos: pir.leaf_pos,
        path: pir.path,
    }
}

/// IMT provider that wraps pre-fetched proofs for real notes and
/// fetches proofs for padded notes on-the-fly via PIR.
struct PirImtProvider<'a> {
    root: pallas::Base,
    cached: HashMap<[u8; 32], ImtProofData>,
    pir_client: Option<&'a PirClientBlocking>,
}

impl ImtProvider for PirImtProvider<'_> {
    fn root(&self) -> pallas::Base {
        self.root
    }

    fn non_membership_proof(&self, nf: pallas::Base) -> Result<ImtProofData, ImtError> {
        let key: [u8; 32] = nf.to_repr();
        if let Some(proof) = self.cached.get(&key) {
            return Ok(proof.clone());
        }
        // Fetch from PIR server for padded notes (whose nullifiers weren't known in advance).
        let client = self
            .pir_client
            .ok_or_else(|| ImtError("PIR client not available for padded note proof".into()))?;
        let pir_proof = client
            .fetch_proof(nf)
            .map_err(|e| ImtError(format!("PIR fetch failed: {e}")))?;
        Ok(convert_pir_proof(pir_proof))
    }
}

// ================================================================
// Helpers
// ================================================================

/// Parse a 32-byte LE slice as a `pallas::Base` field element.
fn bytes_to_base(bytes: &[u8], name: &str) -> Result<pallas::Base, VotingError> {
    validate_32_bytes(bytes, name)?;
    let mut arr = [0u8; 32];
    arr.copy_from_slice(bytes);
    let opt: Option<pallas::Base> = pallas::Base::from_repr(arr).into();
    opt.ok_or_else(|| VotingError::InvalidInput {
        message: format!("{name} is not a valid field element"),
    })
}

/// Parse a 32-byte hash as a `pallas::Base` via wide reduction.
///
/// Blake2b-256 outputs are non-canonical Pallas Fp ~75% of the time
/// (field modulus byte[31] = 0x40). Zero-extends to 64 bytes and uses
/// `from_uniform_bytes` for canonical reduction, matching the FFI
/// verifier's `hash_bytes_to_fp`.
///
/// TODO: Once we move vote round to a field element we can delete this.
fn hash_bytes_to_base(bytes: &[u8], name: &str) -> Result<pallas::Base, VotingError> {
    use ff::FromUniformBytes;
    validate_32_bytes(bytes, name)?;
    let mut wide = [0u8; 64];
    wide[..32].copy_from_slice(bytes);
    Ok(pallas::Base::from_uniform_bytes(&wide))
}

/// Parse a 32-byte LE slice as a `pallas::Scalar`.
fn bytes_to_scalar(bytes: &[u8], name: &str) -> Result<pallas::Scalar, VotingError> {
    validate_32_bytes(bytes, name)?;
    let mut arr = [0u8; 32];
    arr.copy_from_slice(bytes);
    let opt: Option<pallas::Scalar> = pallas::Scalar::from_repr(arr).into();
    opt.ok_or_else(|| VotingError::InvalidInput {
        message: format!("{name} is not a valid scalar"),
    })
}

/// Reconstruct an `orchard::Note` from raw wallet DB fields.
fn reconstruct_note(
    full_note: &NoteInfo,
    network: &Network,
) -> Result<(orchard::Note, FullViewingKey), VotingError> {
    let ufvk = UnifiedFullViewingKey::decode(network, &full_note.ufvk_str).map_err(|e| {
        VotingError::Internal {
            message: format!("failed to decode UFVK: {e}"),
        }
    })?;

    let fvk = ufvk.orchard().ok_or_else(|| VotingError::Internal {
        message: "UFVK has no Orchard component".into(),
    })?;

    let scope = match full_note.scope {
        0 => Scope::External,
        1 => Scope::Internal,
        _ => {
            return Err(VotingError::Internal {
                message: format!("unexpected scope code: {}", full_note.scope),
            })
        }
    };

    let diversifier_arr: [u8; 11] =
        full_note
            .diversifier
            .as_slice()
            .try_into()
            .map_err(|_| VotingError::Internal {
                message: format!(
                    "diversifier must be 11 bytes, got {}",
                    full_note.diversifier.len()
                ),
            })?;
    let diversifier = Diversifier::from_bytes(diversifier_arr);
    let address = fvk.address(diversifier, scope);

    let rho_arr: [u8; 32] =
        full_note
            .rho
            .as_slice()
            .try_into()
            .map_err(|_| VotingError::Internal {
                message: format!("rho must be 32 bytes, got {}", full_note.rho.len()),
            })?;
    let rho: Rho = ct_option_to_result(Rho::from_bytes(&rho_arr), "invalid rho bytes")?;

    let rseed_arr: [u8; 32] =
        full_note
            .rseed
            .as_slice()
            .try_into()
            .map_err(|_| VotingError::Internal {
                message: format!("rseed must be 32 bytes, got {}", full_note.rseed.len()),
            })?;
    let rseed: RandomSeed = ct_option_to_result(
        RandomSeed::from_bytes(rseed_arr, &rho),
        "invalid rseed bytes",
    )?;

    let note_value = NoteValue::from_raw(full_note.value);
    let note = ct_option_to_result(
        orchard::Note::from_parts(address, note_value, rho, rseed),
        "failed to reconstruct note from parts",
    )?;

    Ok((note, fvk.clone()))
}

/// Parse a `WitnessData` into an orchard `MerklePath`.
fn parse_merkle_path(witness: &WitnessData) -> Result<MerklePath, VotingError> {
    if witness.auth_path.len() != NOTE_COMMITMENT_TREE_DEPTH {
        return Err(VotingError::InvalidInput {
            message: format!(
                "auth_path must have {} siblings, got {}",
                NOTE_COMMITMENT_TREE_DEPTH,
                witness.auth_path.len()
            ),
        });
    }

    let mut auth_path = [MerkleHashOrchard::empty_leaf(); NOTE_COMMITMENT_TREE_DEPTH];
    for (i, sibling_bytes) in witness.auth_path.iter().enumerate() {
        let arr: [u8; 32] =
            sibling_bytes
                .as_slice()
                .try_into()
                .map_err(|_| VotingError::InvalidInput {
                    message: format!(
                        "auth_path[{i}] must be 32 bytes, got {}",
                        sibling_bytes.len()
                    ),
                })?;
        auth_path[i] = ct_option_to_result(
            MerkleHashOrchard::from_bytes(&arr),
            &format!("auth_path[{i}] is not a valid hash"),
        )?;
    }

    let pos = u32::try_from(witness.position).map_err(|_| VotingError::InvalidInput {
        message: format!("note position {} exceeds u32 range", witness.position),
    })?;
    Ok(MerklePath::from_parts(pos, auth_path))
}

// ================================================================
// Main entry point
// ================================================================

/// Build and prove the delegation ZKP (#1).
///
/// Constructs the circuit from wallet notes, Merkle witnesses, and
/// IMT exclusion proofs (fetched via PIR), then generates a Halo2 proof.
///
/// # Arguments
///
/// - `full_notes`: 1–5 wallet notes (from `get_wallet_notes_at_snapshot`).
/// - `hotkey_raw_address`: 43-byte raw Orchard address of the voting hotkey.
/// - `alpha_bytes`: 32-byte spend auth randomizer scalar.
/// - `van_comm_rand_bytes`: 32-byte governance commitment blinding factor.
/// - `vote_round_id_bytes`: 32-byte voting round identifier.
/// - `merkle_witnesses`: Merkle inclusion proofs for each note (from `generate_note_witnesses`).
/// - `imt_proofs`: Pre-fetched IMT exclusion proofs (one per real note, from PIR client).
/// - `pir_client`: PIR client for fetching proofs for padded notes (None if 5 real notes).
/// - `network_id`: 0 = mainnet, 1 = testnet (for UFVK decoding).
/// - `progress`: Progress callback.
#[allow(clippy::too_many_arguments)]
pub fn build_and_prove_delegation(
    full_notes: &[NoteInfo],
    hotkey_raw_address: &[u8],
    alpha_bytes: &[u8],
    van_comm_rand_bytes: &[u8],
    vote_round_id_bytes: &[u8],
    merkle_witnesses: &[WitnessData],
    imt_proofs: &[ImtProofData],
    pir_client: Option<&PirClientBlocking>,
    network_id: u32,
    progress: &dyn ProofProgressReporter,
    precomputed_randomness: Option<&PrecomputedRandomness>,
) -> Result<DelegationProofResult, VotingError> {
    let n = full_notes.len();
    if n == 0 || n > 5 {
        return Err(VotingError::InvalidInput {
            message: format!("expected 1–5 notes, got {n}"),
        });
    }
    if merkle_witnesses.len() != n {
        return Err(VotingError::InvalidInput {
            message: format!(
                "merkle_witnesses count ({}) must match notes count ({n})",
                merkle_witnesses.len()
            ),
        });
    }
    if imt_proofs.len() != n {
        return Err(VotingError::InvalidInput {
            message: format!(
                "imt_proofs count ({}) must match notes count ({n})",
                imt_proofs.len()
            ),
        });
    }

    let network = match network_id {
        0 => Network::MainNetwork,
        1 => Network::TestNetwork,
        _ => {
            return Err(VotingError::InvalidInput {
                message: format!(
                    "invalid network_id {network_id}, expected 0 (mainnet) or 1 (testnet)"
                ),
            })
        }
    };

    // Parse scalar/field inputs.
    let alpha = bytes_to_scalar(alpha_bytes, "alpha")?;
    let van_comm_rand = bytes_to_base(van_comm_rand_bytes, "van_comm_rand")?;
    // vote_round_id is a Blake2b-256 hash — use wide reduction for canonical Fp.
    //
    // TODO: Once we move vote round to a field element we use bytes_to_base directly.
    let vote_round_id = hash_bytes_to_base(vote_round_id_bytes, "vote_round_id")?;

    // Parse hotkey address (43-byte raw Orchard address).
    let addr_arr: [u8; 43] =
        hotkey_raw_address
            .try_into()
            .map_err(|_| VotingError::InvalidInput {
                message: format!(
                    "hotkey address must be 43 bytes, got {}",
                    hotkey_raw_address.len()
                ),
            })?;
    let output_recipient = ct_option_to_result(
        orchard::Address::from_raw_address_bytes(&addr_arr),
        "invalid hotkey address bytes",
    )?;

    // Reconstruct notes and parse Merkle paths + IMT proofs.
    let mut real_inputs = Vec::with_capacity(n);
    let mut imt_cache = HashMap::new();
    let mut shared_fvk: Option<FullViewingKey> = None;
    let mut nc_root: Option<pallas::Base> = None;
    let mut nf_imt_root: Option<pallas::Base> = None;

    for i in 0..n {
        let (note, note_fvk) = reconstruct_note(&full_notes[i], &network)?;
        let merkle_path = parse_merkle_path(&merkle_witnesses[i])?;
        let imt_proof = imt_proofs[i].clone();

        // All notes must share the same FVK (same account).
        match &shared_fvk {
            None => shared_fvk = Some(note_fvk.clone()),
            Some(existing) => {
                if existing.to_bytes() != note_fvk.to_bytes() {
                    return Err(VotingError::InvalidInput {
                        message: format!("note[{i}] has a different FVK than note[0]"),
                    });
                }
            }
        }

        // Verify consistent roots.
        let witness_root = bytes_to_base(&merkle_witnesses[i].root, &format!("witness[{i}].root"))?;
        match nc_root {
            None => nc_root = Some(witness_root),
            Some(r) if r != witness_root => {
                return Err(VotingError::InvalidInput {
                    message: format!("witness[{i}] has a different nc_root than witness[0]"),
                });
            }
            _ => {}
        }
        match nf_imt_root {
            None => nf_imt_root = Some(imt_proof.root),
            Some(r) if r != imt_proof.root => {
                return Err(VotingError::InvalidInput {
                    message: format!("imt_proof[{i}] has a different root than imt_proof[0]"),
                });
            }
            _ => {}
        }

        // Cache this proof for the ServerImtProvider.
        let nf = note.nullifier(&note_fvk);
        imt_cache.insert(nf.to_bytes(), imt_proof.clone());

        let scope = match full_notes[i].scope {
            0 => Scope::External,
            1 => Scope::Internal,
            _ => {
                return Err(VotingError::Internal {
                    message: format!("unexpected scope code: {}", full_notes[i].scope),
                })
            }
        };
        real_inputs.push(RealNoteInput {
            note,
            fvk: note_fvk,
            merkle_path,
            imt_proof,
            scope,
        });
    }

    let fvk = shared_fvk.expect("guaranteed by n >= 1 check");
    let nc_root = nc_root.expect("guaranteed by n >= 1 check");
    let nf_imt_root = nf_imt_root.expect("guaranteed by n >= 1 check");

    // Create IMT provider: pre-fetched proofs for real notes, PIR for padded notes.
    let imt_provider = PirImtProvider {
        root: nf_imt_root,
        cached: imt_cache,
        pir_client,
    };

    // Build the delegation bundle (circuit + instance).
    let mut rng = OsRng;
    let bundle = build_delegation_bundle(
        real_inputs,
        &fvk,
        alpha,
        output_recipient,
        vote_round_id,
        nc_root,
        van_comm_rand,
        &imt_provider,
        &mut rng,
        precomputed_randomness,
    )
    .map_err(|e| VotingError::ProofFailed {
        message: format!("delegation bundle build failed: {e}"),
    })?;

    progress.on_progress(0.1);

    // Use the process-lifetime cached proving key (keygen runs once, then is reused).
    let (params, pk) = get_delegation_proving_key();

    progress.on_progress(0.5);

    // Create the proof.
    let instance_vec = bundle.instance.to_halo2_instance();
    let instance_refs: Vec<&[vesta::Scalar]> = vec![instance_vec.as_slice()];
    let mut transcript = Blake2bWrite::<_, vesta::Affine, Challenge255<_>>::init(vec![]);
    plonk::create_proof(
        &params,
        &pk,
        &[bundle.circuit],
        &[instance_refs.as_slice()],
        &mut rng,
        &mut transcript,
    )
    .map_err(|e| VotingError::ProofFailed {
        message: format!("create_proof failed: {e}"),
    })?;
    let proof_bytes = transcript.finalize();

    progress.on_progress(1.0);

    // Extract public inputs as 32-byte LE arrays.
    let public_inputs: Vec<Vec<u8>> = instance_vec
        .iter()
        .map(|fe| fe.to_repr().to_vec())
        .collect();

    // Extract named outputs from the instance.
    let rk_bytes: [u8; 32] = bundle.instance.rk.clone().into();

    Ok(DelegationProofResult {
        proof: proof_bytes,
        public_inputs,
        nf_signed: bundle.instance.nf_signed.to_bytes().to_vec(),
        cmx_new: bundle.instance.cmx_new.to_repr().to_vec(),
        gov_nullifiers: bundle
            .instance
            .gov_null
            .iter()
            .map(|g| g.to_repr().to_vec())
            .collect(),
        van_comm: bundle.instance.van_comm.to_repr().to_vec(),
        rk: rk_bytes.to_vec(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::Arc;

    use ff::Field;
    use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength};
    use incrementalmerkletree::{Hashable, Level};
    use orchard::{
        delegation::imt::IMT_DEPTH as TEST_IMT_DEPTH, keys::Scope,
        note::commitment::ExtractedNoteCommitment, note::Rho, tree::MerkleHashOrchard,
        value::NoteValue, NOTE_COMMITMENT_TREE_DEPTH as TEST_TREE_DEPTH,
    };

    struct TestReporter {
        count: Arc<AtomicU32>,
    }

    impl ProofProgressReporter for TestReporter {
        fn on_progress(&self, _progress: f64) {
            self.count.fetch_add(1, Ordering::Relaxed);
        }
    }

    // ================================================================
    // Test-only IMT (replicates SpacedLeafImtProvider logic)
    // ================================================================

    /// Poseidon hash of 2 field elements (same as orchard's poseidon_hash_2).
    fn poseidon2(a: pallas::Base, b: pallas::Base) -> pallas::Base {
        poseidon::Hash::<pallas::Base, poseidon::P128Pow5T3, ConstantLength<2>, 3, 2>::init()
            .hash([a, b])
    }

    /// Precomputed empty subtree hashes for the test IMT.
    fn empty_imt_hashes() -> Vec<pallas::Base> {
        let empty_leaf = poseidon2(pallas::Base::zero(), pallas::Base::zero());
        let mut hashes = vec![empty_leaf];
        for _ in 1..=TEST_IMT_DEPTH {
            let prev = *hashes.last().unwrap();
            hashes.push(poseidon2(prev, prev));
        }
        hashes
    }

    /// SpacedLeaf IMT for testing — generates valid non-membership proofs
    /// as `ImtProofData` structs for `build_and_prove_delegation`.
    struct TestImt {
        root: pallas::Base,
        leaves: Vec<(pallas::Base, pallas::Base)>,
        subtree_levels: Vec<Vec<pallas::Base>>,
    }

    impl TestImt {
        fn new() -> Self {
            let step = pallas::Base::from(2u64).pow([250, 0, 0, 0]);
            let empties = empty_imt_hashes();

            // Build 17 brackets covering the entire Pallas field.
            let mut leaves = Vec::with_capacity(17);
            for k in 0u64..17 {
                let low = step * pallas::Base::from(k) + pallas::Base::one();
                let high = if k < 16 {
                    step * pallas::Base::from(k + 1) - pallas::Base::one()
                } else {
                    -pallas::Base::one() // p - 1
                };
                let width = high - low;
                leaves.push((low, width));
            }

            // Build 32-leaf subtree. Each leaf is Poseidon(low, width).
            let empty_leaf_hash = poseidon2(pallas::Base::zero(), pallas::Base::zero());
            let mut level0 = vec![empty_leaf_hash; 32];
            for (k, (low, width)) in leaves.iter().enumerate() {
                level0[k] = poseidon2(*low, *width);
            }

            let mut subtree_levels = vec![level0];
            for _ in 1..=5 {
                let prev = subtree_levels.last().unwrap();
                let mut current = Vec::with_capacity(prev.len() / 2);
                for j in 0..(prev.len() / 2) {
                    current.push(poseidon2(prev[2 * j], prev[2 * j + 1]));
                }
                subtree_levels.push(current);
            }

            // Hash subtree root up through levels 5..29 with empty siblings.
            let mut root = subtree_levels[5][0];
            for l in 5..TEST_IMT_DEPTH {
                root = poseidon2(root, empties[l]);
            }

            TestImt {
                root,
                leaves,
                subtree_levels,
            }
        }

        /// Generate an IMT non-membership proof for the given nullifier.
        fn proof(&self, nf: pallas::Base) -> ImtProofData {
            // Determine bracket: k = nf >> 250. In LE repr, bit 250 is bit 2 of byte 31.
            let repr: [u8; 32] = nf.to_repr();
            let k = ((repr[31] >> 2) as usize).min(16);
            let (low, width) = self.leaves[k];

            let empties = empty_imt_hashes();

            // Build 29-level Merkle path (pure siblings).
            let mut path = [pallas::Base::zero(); TEST_IMT_DEPTH];
            let mut idx = k;
            for l in 0..5 {
                path[l] = self.subtree_levels[l][idx ^ 1];
                idx >>= 1;
            }
            for l in 5..TEST_IMT_DEPTH {
                path[l] = empties[l];
            }

            ImtProofData {
                root: self.root,
                low,
                width,
                leaf_pos: k as u32,
                path,
            }
        }
    }

    #[test]
    fn test_build_and_prove_validation() {
        let reporter = TestReporter {
            count: Arc::new(AtomicU32::new(0)),
        };
        // Empty notes should fail.
        let result = build_and_prove_delegation(
            &[],
            &[0u8; 43],
            &[0u8; 32],
            &[0u8; 32],
            &[0u8; 32],
            &[],
            &[],
            None,
            0,
            &reporter,
            None,
        );
        assert!(result.is_err());
        assert!(result.unwrap_err().to_string().contains("1–5 notes"));
    }

    /// Real Halo2 delegation proof end-to-end test.
    ///
    /// Creates synthetic wallet notes, builds a Merkle tree, constructs valid IMT
    /// non-membership proofs as `ImtProofData`, and calls
    /// `build_and_prove_delegation()` to generate a real Halo2 proof.
    ///
    /// Uses 5 notes to avoid padding (no PIR server needed for padded notes).
    /// Long-running due to keygen + proof generation.
    ///
    /// Run with: `cargo test -p librustvoting test_real_delegation_proof -- --ignored --nocapture`
    #[test]
    #[ignore]
    fn test_real_delegation_proof() {
        use zcash_keys::keys::UnifiedSpendingKey;
        use zcash_protocol::consensus::MAIN_NETWORK;
        use zip32::AccountId;

        println!("=== Real Delegation Proof Test ===");
        println!("Setting up test keys...");

        // 1. Deterministic test keys
        let seed = [0x42u8; 32];
        let account = AccountId::try_from(0u32).unwrap();
        let usk = UnifiedSpendingKey::from_seed(&MAIN_NETWORK, &seed, account).unwrap();
        let ufvk = usk.to_unified_full_viewing_key();
        let ufvk_str = ufvk.encode(&MAIN_NETWORK);
        let fvk = ufvk.orchard().unwrap().clone();

        // 2. Hotkey (output note recipient)
        let hotkey_seed = [0x43u8; 32];
        let hotkey_usk =
            UnifiedSpendingKey::from_seed(&MAIN_NETWORK, &hotkey_seed, account).unwrap();
        let hotkey_fvk = hotkey_usk
            .to_unified_full_viewing_key()
            .orchard()
            .unwrap()
            .clone();
        let hotkey_addr = hotkey_fvk.address_at(0u32, Scope::External);
        let hotkey_raw_address = hotkey_addr.to_raw_address_bytes().to_vec();

        // 3. Create 5 notes (fills all 5 slots → no padding → no IMT server needed)
        let mut rng = OsRng;
        let note_values = [4_000_000u64, 4_000_000, 3_000_000, 2_000_000, 1_000_000]; // 14M total >= 12.5M min
        let address = fvk.address_at(0u32, Scope::External);

        let mut notes = Vec::new();
        for &v in &note_values {
            let (_, _, dummy_parent) = orchard::Note::dummy(&mut rng, None);
            let note = orchard::Note::new(
                address,
                NoteValue::from_raw(v),
                Rho::from_nf_old(dummy_parent.nullifier(&fvk)),
                &mut rng,
            );
            notes.push(note);
        }

        println!(
            "Created {} notes, total value: {} zatoshis",
            notes.len(),
            note_values.iter().sum::<u64>()
        );
        println!("Building Merkle tree...");

        // 4. Build Merkle tree (5 leaves in a 32-level tree)
        // Layout: 8-leaf subtree with 5 real leaves and 3 empty padding leaves.
        let empty_leaf = MerkleHashOrchard::empty_leaf();
        let mut leaves = [empty_leaf; 8];
        for (i, note) in notes.iter().enumerate() {
            let cmx = ExtractedNoteCommitment::from(note.commitment());
            leaves[i] = MerkleHashOrchard::from_cmx(&cmx);
        }

        // Level 0 pairs
        let l1_0 = MerkleHashOrchard::combine(Level::from(0), &leaves[0], &leaves[1]);
        let l1_1 = MerkleHashOrchard::combine(Level::from(0), &leaves[2], &leaves[3]);
        let l1_2 = MerkleHashOrchard::combine(Level::from(0), &leaves[4], &leaves[5]);
        let l1_3 = MerkleHashOrchard::combine(Level::from(0), &leaves[6], &leaves[7]);
        // Level 1 pairs
        let l2_0 = MerkleHashOrchard::combine(Level::from(1), &l1_0, &l1_1);
        let l2_1 = MerkleHashOrchard::combine(Level::from(1), &l1_2, &l1_3);
        // Level 2
        let l3_0 = MerkleHashOrchard::combine(Level::from(2), &l2_0, &l2_1);

        let mut current = l3_0;
        for level in 3..TEST_TREE_DEPTH {
            let sibling = MerkleHashOrchard::empty_root(Level::from(level as u8));
            current = MerkleHashOrchard::combine(Level::from(level as u8), &current, &sibling);
        }
        let nc_root_bytes = current.to_bytes().to_vec();

        // Build auth paths for each note
        let l1 = [l1_0, l1_1, l1_2, l1_3];
        let l2 = [l2_0, l2_1];
        let mut merkle_witnesses = Vec::new();
        for (i, note) in notes.iter().enumerate() {
            let mut auth_path_hashes = [MerkleHashOrchard::empty_leaf(); TEST_TREE_DEPTH];
            // Level 0 sibling: XOR bit 0
            auth_path_hashes[0] = leaves[i ^ 1];
            // Level 1 sibling: XOR bit 1
            auth_path_hashes[1] = l1[(i >> 1) ^ 1];
            // Level 2 sibling: XOR bit 2
            auth_path_hashes[2] = l2[(i >> 2) ^ 1];
            for level in 3..TEST_TREE_DEPTH {
                auth_path_hashes[level] = MerkleHashOrchard::empty_root(Level::from(level as u8));
            }

            let cmx = ExtractedNoteCommitment::from(note.commitment());
            merkle_witnesses.push(WitnessData {
                note_commitment: MerkleHashOrchard::from_cmx(&cmx).to_bytes().to_vec(),
                position: i as u64,
                root: nc_root_bytes.clone(),
                auth_path: auth_path_hashes
                    .iter()
                    .map(|h| h.to_bytes().to_vec())
                    .collect(),
            });
        }

        println!("Building IMT proofs...");

        // 5. Build IMT non-membership proofs
        let imt = TestImt::new();
        let imt_proofs: Vec<ImtProofData> = notes
            .iter()
            .map(|note| {
                let nf_bytes = note.nullifier(&fvk).to_bytes();
                let nf_base: pallas::Base = pallas::Base::from_repr(nf_bytes).unwrap();
                imt.proof(nf_base)
            })
            .collect();

        // 6. Serialize notes into NoteInfo
        let full_notes: Vec<NoteInfo> = notes
            .iter()
            .enumerate()
            .map(|(i, note)| {
                let cmx: orchard::note::ExtractedNoteCommitment = note.commitment().into();
                NoteInfo {
                    commitment: cmx.to_bytes().to_vec(),
                    diversifier: note.recipient().diversifier().as_array().to_vec(),
                    value: note_values[i],
                    rho: note.rho().to_bytes().to_vec(),
                    rseed: note.rseed().as_bytes().to_vec(),
                    nullifier: note.nullifier(&fvk).to_bytes().to_vec(),
                    position: i as u64,
                    scope: 0,
                    ufvk_str: ufvk_str.clone(),
                }
            })
            .collect();

        // 7. Generate random parameters
        let alpha = pallas::Scalar::random(&mut rng);
        let van_comm_rand = pallas::Base::random(&mut rng);
        let vote_round_id = pallas::Base::random(&mut rng);

        let count = Arc::new(AtomicU32::new(0));
        let reporter = TestReporter {
            count: count.clone(),
        };

        println!("Starting build_and_prove_delegation (keygen + proving)...");
        println!("This will take a while (keygen + proving)...");

        let start = std::time::Instant::now();
        let result = build_and_prove_delegation(
            &full_notes,
            &hotkey_raw_address,
            &alpha.to_repr(),
            &van_comm_rand.to_repr(),
            &vote_round_id.to_repr(),
            &merkle_witnesses,
            &imt_proofs,
            None, // 5 notes = no padding, no PIR client needed
            0,    // mainnet
            &reporter,
            None,
        )
        .expect("build_and_prove_delegation should succeed");
        let elapsed = start.elapsed();

        println!("Proof generated in {:.1}s", elapsed.as_secs_f64());

        // 8. Verify result structure
        assert!(!result.proof.is_empty(), "proof bytes should be non-empty");
        assert_eq!(
            result.public_inputs.len(),
            12,
            "should have 12 public inputs"
        );
        for (i, pi) in result.public_inputs.iter().enumerate() {
            assert_eq!(pi.len(), 32, "public_input[{i}] should be 32 bytes");
        }
        assert_eq!(result.nf_signed.len(), 32, "nf_signed should be 32 bytes");
        assert_eq!(result.cmx_new.len(), 32, "cmx_new should be 32 bytes");
        assert_eq!(
            result.gov_nullifiers.len(),
            5,
            "should have 5 gov nullifiers"
        );
        for (i, gn) in result.gov_nullifiers.iter().enumerate() {
            assert_eq!(gn.len(), 32, "gov_nullifier[{i}] should be 32 bytes");
        }
        assert_eq!(result.van_comm.len(), 32, "van_comm should be 32 bytes");
        assert_eq!(result.rk.len(), 32, "rk should be 32 bytes");

        // Verify proof is NOT the old mock pattern
        assert_ne!(
            &result.proof[..result.proof.len().min(256)],
            &vec![0xAB; result.proof.len().min(256)][..],
            "proof should not be mock data"
        );

        // Verify progress was reported (at least 3 calls: 0.1, 0.5, 1.0)
        let progress_count = count.load(Ordering::Relaxed);
        assert!(
            progress_count >= 3,
            "expected at least 3 progress callbacks, got {progress_count}"
        );

        println!("=== Test passed ===");
        println!("  Proof size: {} bytes", result.proof.len());
        println!("  Progress callbacks: {progress_count}");
    }
}
