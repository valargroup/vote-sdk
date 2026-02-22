use ff::{Field, PrimeField};
use pasta_curves::arithmetic::{CurveAffine, CurveExt};
use pasta_curves::group::{Curve, Group, GroupEncoding};
use pasta_curves::pallas;
use rand::RngCore;
use subtle::CtOption;

use orchard::builder::{Builder, BundleType};
use orchard::keys::{FullViewingKey, SpendValidatingKey};
use orchard::note::{ExtractedNoteCommitment, RandomSeed, Rho};
use orchard::pczt::Zip32Derivation;
use orchard::tree::{MerkleHashOrchard, MerklePath};
use orchard::value::NoteValue;
use orchard::{Anchor, Address};
use zip32::Scope;

/// Orchard Merkle tree depth (32 levels).
const MERKLE_DEPTH: usize = 32;

use crate::governance;
use crate::types::{
    validate_notes, validate_round_params, DelegationAction, GovernancePczt, NoteInfo, VotingError,
    VotingRoundParams,
};

/// Orchard key diversification personalization for DiversifyHash^Orchard.
const ORCHARD_GD_PERSONALIZATION: &str = "z.cash:Orchard-gd";

/// Extract the affine x-coordinate bytes from a non-identity Pallas point.
fn point_x_bytes(point: &pallas::Point) -> Result<[u8; 32], VotingError> {
    point
        .to_affine()
        .coordinates()
        .map(|coords| coords.x().to_repr())
        .into_option()
        .ok_or_else(|| VotingError::InvalidInput {
            message: "point is identity; x-coordinate unavailable".to_string(),
        })
}

/// Derive (g_d_x, pk_d_x) from a 43-byte Orchard raw address.
///
/// - g_d_x: x-coordinate of DiversifyHash(d)
/// - pk_d_x: x-coordinate of the diversified transmission key pk_d
pub fn derive_hotkey_x_coords_from_raw_address(
    hotkey_raw_address: &[u8; 43],
) -> Result<([u8; 32], [u8; 32]), VotingError> {
    let diversifier: [u8; 11] = hotkey_raw_address[..11]
        .try_into()
        .expect("slice length is fixed to 11");
    let pk_d_bytes: [u8; 32] = hotkey_raw_address[11..]
        .try_into()
        .expect("slice length is fixed to 32");

    let pk_d_point: pallas::Point = pallas::Point::from_bytes(&pk_d_bytes)
        .into_option()
        .ok_or_else(|| VotingError::InvalidInput {
            message: "hotkey_raw_address contains invalid pk_d point encoding".to_string(),
        })?;
    let pk_d_x: [u8; 32] = point_x_bytes(&pk_d_point)?;

    // Orchard spec: if DiversifyHash(d) returns identity, use DiversifyHash([]) fallback.
    let hasher = pallas::Point::hash_to_curve(ORCHARD_GD_PERSONALIZATION);
    let mut g_d_point = hasher(&diversifier);
    if bool::from(g_d_point.is_identity()) {
        g_d_point = hasher(&[]);
    }
    let g_d_x: [u8; 32] = point_x_bytes(&g_d_point)?;

    Ok((g_d_x, pk_d_x))
}

/// Generate a random valid Rho (retries until the random bytes are a valid Pallas field element).
fn random_rho(rng: &mut impl RngCore) -> Rho {
    loop {
        let mut rho_bytes = [0u8; 32];
        rng.fill_bytes(&mut rho_bytes);
        let r: CtOption<Rho> = Rho::from_bytes(&rho_bytes);
        if r.is_some().into() {
            return r.expect("is_some checked above");
        }
    }
}

/// Generate a random valid RandomSeed for a given Rho.
fn random_rseed(rng: &mut impl RngCore, rho: &Rho) -> (RandomSeed, [u8; 32]) {
    loop {
        let mut rseed_bytes = [0u8; 32];
        rng.fill_bytes(&mut rseed_bytes);
        let rs: CtOption<RandomSeed> = RandomSeed::from_bytes(rseed_bytes, rho);
        if rs.is_some().into() {
            return (rs.expect("is_some checked above"), rseed_bytes);
        }
    }
}

/// Construct a 1-zatoshi orchard Note at the given address with the given Rho.
/// Uses Note::from_parts (orchard 0.11 public API).
/// Value is 1 zatoshi so that Keystone renders the transaction on screen.
fn make_dummy_note(
    addr: Address,
    rho: Rho,
    rng: &mut impl RngCore,
) -> Result<(orchard::Note, [u8; 32]), VotingError> {
    let (rseed, rseed_bytes) = random_rseed(rng, &rho);
    let note = orchard::Note::from_parts(addr, NoteValue::from_raw(1), rho, rseed);
    if !bool::from(note.is_some()) {
        return Err(VotingError::Internal {
            message: "failed to construct dummy note".to_string(),
        });
    }
    Ok((note.expect("is_some checked above"), rseed_bytes))
}

/// Canonical delegate action payload encoding for external signing.
///
/// Field order:
/// nf_signed || rk || cmx_new || van_comm || gov_null_1 || gov_null_2 ||
/// gov_null_3 || gov_null_4 || gov_null_5 || vote_round_id.
///
/// TODO: This format might change when we standardize what the cosmos chain expects.
fn encode_delegation_action_bytes(
    nf_signed: &[u8; 32],
    rk: &[u8; 32],
    cmx_new: &[u8; 32],
    van_comm: &[u8],
    gov_nullifiers: &[Vec<u8>],
    vote_round_id: &[u8; 32],
) -> Result<Vec<u8>, VotingError> {
    crate::types::validate_32_bytes(van_comm, "van_comm")?;
    if gov_nullifiers.len() != 5 {
        return Err(VotingError::InvalidInput {
            message: format!(
                "gov_nullifiers must have exactly 5 entries, got {}",
                gov_nullifiers.len()
            ),
        });
    }

    let mut out = Vec::with_capacity(32 * 10);
    out.extend_from_slice(nf_signed);
    out.extend_from_slice(rk);
    out.extend_from_slice(cmx_new);
    out.extend_from_slice(van_comm);
    for (i, gn) in gov_nullifiers.iter().enumerate() {
        crate::types::validate_32_bytes(gn, &format!("gov_nullifiers[{}]", i))?;
        out.extend_from_slice(gn);
    }
    out.extend_from_slice(vote_round_id);
    Ok(out)
}

/// Construct the delegation action for Keystone signing.
///
/// Computes real governance nullifiers (padded to 5), VAN, constrained rho (§1.3.4.1),
/// signed note + output note (§1.3.4.2, §1.3.6), and rk.
///
/// - `fvk_bytes`: 96-byte orchard FullViewingKey (ak[32] || nk[32] || rivk[32])
/// - `g_d_new_x`: 32-byte x-coordinate of hotkey diversified generator (for VAN)
/// - `pk_d_new_x`: 32-byte x-coordinate of hotkey transmission key (for VAN)
/// - `hotkey_raw_address`: 43-byte hotkey raw orchard address (for output note)
pub fn construct_delegation_action(
    notes: &[NoteInfo],
    params: &VotingRoundParams,
    fvk_bytes: &[u8],
    g_d_new_x: &[u8],
    pk_d_new_x: &[u8],
    hotkey_raw_address: &[u8],
) -> Result<DelegationAction, VotingError> {
    validate_notes(notes)?;
    validate_round_params(params)?;
    crate::types::validate_32_bytes(g_d_new_x, "g_d_new_x")?;
    crate::types::validate_32_bytes(pk_d_new_x, "pk_d_new_x")?;

    // Parse FVK from 96 bytes: ak[32] || nk[32] || rivk[32]
    let fvk_96: [u8; 96] = fvk_bytes
        .try_into()
        .map_err(|_| VotingError::InvalidInput {
            message: format!("fvk_bytes must be 96 bytes, got {}", fvk_bytes.len()),
        })?;
    let fvk = FullViewingKey::from_bytes(&fvk_96).ok_or_else(|| VotingError::InvalidInput {
        message: "fvk_bytes is not a valid orchard FullViewingKey".to_string(),
    })?;
    // nk bytes for gov nullifier derivation (middle 32 bytes of FVK serialization)
    let nk_bytes = &fvk_bytes[32..64];

    // Parse hotkey raw address (43 bytes: 11-byte diversifier + 32-byte pk_d)
    let addr_43: [u8; 43] =
        hotkey_raw_address
            .try_into()
            .map_err(|_| VotingError::InvalidInput {
                message: format!(
                    "hotkey_raw_address must be 43 bytes, got {}",
                    hotkey_raw_address.len()
                ),
            })?;
    let hotkey_addr: Address = Address::from_raw_address_bytes(&addr_43)
        .into_option()
        .ok_or_else(|| VotingError::InvalidInput {
            message: "hotkey_raw_address is not a valid orchard address".to_string(),
        })?;

    // Enforce VAN hotkey coordinates match the output note hotkey address.
    // This aligns with Gov Steps §1.3.6 / ZKP #1 conditions 6 & 7.
    let (derived_g_d_new_x, derived_pk_d_new_x) =
        derive_hotkey_x_coords_from_raw_address(&addr_43)?;
    if g_d_new_x != derived_g_d_new_x.as_slice() {
        return Err(VotingError::InvalidInput {
            message: "g_d_new_x does not match hotkey_raw_address diversifier".to_string(),
        });
    }
    if pk_d_new_x != derived_pk_d_new_x.as_slice() {
        return Err(VotingError::InvalidInput {
            message: "pk_d_new_x does not match hotkey_raw_address pk_d".to_string(),
        });
    }

    // Convert vote_round_id from hex string to exactly 32 bytes.
    // Rejecting non-32-byte values prevents silent truncation/padding collisions.
    let vote_round_id_bytes =
        hex::decode(&params.vote_round_id).map_err(|e| VotingError::InvalidInput {
            message: format!("vote_round_id is not valid hex: {}", e),
        })?;
    crate::types::validate_32_bytes(&vote_round_id_bytes, "vote_round_id (decoded hex)")?;
    // Safe: validate_32_bytes already ensures exactly 32 bytes.
    let vri_32: [u8; 32] = vote_round_id_bytes
        .try_into()
        .expect("validated as 32 bytes above");

    let mut rng = rand::thread_rng();

    // Compute real gov nullifiers for each input note
    let mut gov_nullifiers: Vec<Vec<u8>> = Vec::with_capacity(5);
    for note in notes {
        let gov_null = governance::derive_gov_nullifier(nk_bytes, &vri_32, &note.nullifier)?;
        gov_nullifiers.push(gov_null);
    }

    // --- Padded note generation using orchard Note API ---
    // Use the sender's FVK for padded notes (spec §1.3.5: same ivk as real notes).
    let mut padded_cmx: Vec<Vec<u8>> = Vec::new();
    let mut dummy_nullifiers: Vec<Vec<u8>> = Vec::new();
    let n_real = notes.len();

    if n_real < 5 {
        for i in n_real..5 {
            // Derive a unique address for each padded note from the sender's FVK
            let pad_addr = fvk.address_at(1000u32 + i as u32, Scope::External);

            // Generate a random Rho (represents the "previous nullifier" for this padded note)
            let rho = random_rho(&mut rng);

            // Construct the padded note with value=1 zatoshi
            let (pad_note, _) = make_dummy_note(pad_addr, rho, &mut rng)?;

            let cmx: ExtractedNoteCommitment = pad_note.commitment().into();
            let real_nf = pad_note.nullifier(&fvk);

            let gov_null =
                governance::derive_gov_nullifier(nk_bytes, &vri_32, &real_nf.to_bytes())?;

            padded_cmx.push(cmx.to_bytes().to_vec());
            gov_nullifiers.push(gov_null);
            dummy_nullifiers.push(real_nf.to_bytes().to_vec());
        }
    }

    // Compute per-bundle weight from note values (checked to prevent silent overflow)
    let total_weight: u64 = notes
        .iter()
        .try_fold(0u64, |acc, n| acc.checked_add(n.value))
        .ok_or_else(|| VotingError::InvalidInput {
            message: "total note weight overflows u64".to_string(),
        })?;

    // Sample van_comm_rand as a proper random field element
    let van_comm_rand_fp = pallas::Base::random(&mut rng);
    let van_comm_rand: [u8; 32] = van_comm_rand_fp.to_repr();

    // Compute real VAN
    let van = governance::construct_van(
        &derived_g_d_new_x,
        &derived_pk_d_new_x,
        total_weight,
        &vri_32,
        &van_comm_rand,
    )?;

    // Collect all 5 cmx values: real from NoteInfo.commitment, padded from above
    let mut all_cmx: Vec<Vec<u8>> = Vec::with_capacity(5);
    for note in notes {
        all_cmx.push(note.commitment.clone());
    }
    all_cmx.extend(padded_cmx.iter().cloned());
    if all_cmx.len() != 5 {
        return Err(VotingError::Internal {
            message: format!("expected 5 cmx values, got {}", all_cmx.len()),
        });
    }

    // Compute rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, cmx_5, van_comm, vote_round_id)
    let rho_signed = governance::compute_rho_binding(
        &all_cmx[0],
        &all_cmx[1],
        &all_cmx[2],
        &all_cmx[3],
        &all_cmx[4],
        &van,
        &vri_32,
    )?;

    // --- Signed note construction (§1.3.4.2) ---
    // Parse rho_signed as Rho for note construction
    let rho_signed_32: [u8; 32] = rho_signed
        .clone()
        .try_into()
        .expect("rho_signed is 32 bytes from compute_rho_binding");
    let rho_for_note: Rho = Rho::from_bytes(&rho_signed_32)
        .into_option()
        .ok_or_else(|| VotingError::Internal {
            message: "rho_signed is not a valid Pallas field element for Rho".to_string(),
        })?;

    // Sender address from FVK (diversifier index 0)
    let sender_address = fvk.address_at(0u32, Scope::External);

    // Build signed note: v=1 zatoshi, address from sender, rho = rho_signed (§1.3.4.2)
    let (signed_note, rseed_signed_bytes) = make_dummy_note(sender_address, rho_for_note, &mut rng)?;

    // Derive nullifier (§1.3.4.2: DeriveNullifier_nk(rho_signed, psi_signed, cm_signed))
    let nf_signed = signed_note.nullifier(&fvk);
    let nf_signed_bytes: [u8; 32] = nf_signed.to_bytes();

    // --- Output note construction (§1.3.6) ---
    // Rho for output note = nf_signed (standard Orchard chaining).
    // Construct Rho from the nullifier bytes (nf_signed is already a valid field element).
    let rho_output: Rho = Rho::from_bytes(&nf_signed_bytes)
        .into_option()
        .ok_or_else(|| VotingError::Internal {
            message: "nf_signed is not a valid Pallas field element for Rho".to_string(),
        })?;

    // Output note: to hotkey address, v=1 zatoshi, rho = nf_signed
    let (output_note, rseed_output_bytes) = make_dummy_note(hotkey_addr, rho_output, &mut rng)?;
    let cmx_new: ExtractedNoteCommitment = output_note.commitment().into();
    let cmx_new_bytes: [u8; 32] = cmx_new.to_bytes();

    // --- Compute rk (§1.3.6) ---
    // Extract ak from FVK for spend auth randomization
    let ak: SpendValidatingKey = fvk.clone().into();
    let alpha = pallas::Scalar::random(&mut rng);
    let rk_vk = ak.randomize(&alpha);
    let rk_bytes: [u8; 32] = (&rk_vk).into();
    let alpha_bytes: [u8; 32] = alpha.to_repr();

    // --- Compute action_bytes ---
    let action_bytes = encode_delegation_action_bytes(
        &nf_signed_bytes,
        &rk_bytes,
        &cmx_new_bytes,
        &van,
        &gov_nullifiers,
        &vri_32,
    )?;

    Ok(DelegationAction {
        action_bytes,
        rk: rk_bytes.to_vec(),
        gov_nullifiers,
        van,
        van_comm_rand: van_comm_rand.to_vec(),
        dummy_nullifiers,
        rho_signed,
        padded_cmx,
        nf_signed: nf_signed_bytes.to_vec(),
        cmx_new: cmx_new_bytes.to_vec(),
        alpha: alpha_bytes.to_vec(),
        spend_auth_sig: None,
        rseed_signed: rseed_signed_bytes.to_vec(),
        rseed_output: rseed_output_bytes.to_vec(),
    })
}

/// Build a governance-specific PCZT for Keystone signing.
///
/// Constructs a PCZT whose single real Orchard action is the governance dummy action
/// (spend of signed note with constrained rho → output to hotkey). The Builder
/// generates alpha/rk internally, and the PCZT's ZIP-244 sighash is computed by
/// Keystone when it runs the Signer role.
///
/// Parameters:
/// - `notes`: 1-5 input notes for governance nullifier derivation
/// - `params`: voting round parameters (round ID, snapshot height, etc.)
/// - `fvk_bytes`: 96-byte orchard FullViewingKey (ak[32] || nk[32] || rivk[32])
/// - `hotkey_raw_address`: 43-byte hotkey raw orchard address
/// - `consensus_branch_id`: network consensus branch ID (e.g. 0xC2D6D0B4 for NU5)
/// - `coin_type`: BIP-44 coin type (133 for mainnet, 1 for testnet)
/// - `seed_fingerprint`: 32-byte ZIP-32 seed fingerprint (Keystone needs this to
///   identify which seed to derive the spending key from)
/// - `account_index`: ZIP-32 account index (typically 0)
pub fn build_governance_pczt(
    notes: &[NoteInfo],
    params: &VotingRoundParams,
    fvk_bytes: &[u8],
    hotkey_raw_address: &[u8],
    consensus_branch_id: u32,
    coin_type: u32,
    seed_fingerprint: &[u8; 32],
    account_index: u32,
    round_name: &str,
) -> Result<GovernancePczt, VotingError> {
    validate_notes(notes)?;
    validate_round_params(params)?;

    // Parse FVK from 96 bytes: ak[32] || nk[32] || rivk[32]
    let fvk_96: [u8; 96] = fvk_bytes
        .try_into()
        .map_err(|_| VotingError::InvalidInput {
            message: format!("fvk_bytes must be 96 bytes, got {}", fvk_bytes.len()),
        })?;
    let fvk = FullViewingKey::from_bytes(&fvk_96).ok_or_else(|| VotingError::InvalidInput {
        message: "fvk_bytes is not a valid orchard FullViewingKey".to_string(),
    })?;
    let nk_bytes = &fvk_bytes[32..64];

    // Parse hotkey raw address (43 bytes: 11-byte diversifier + 32-byte pk_d)
    let addr_43: [u8; 43] =
        hotkey_raw_address
            .try_into()
            .map_err(|_| VotingError::InvalidInput {
                message: format!(
                    "hotkey_raw_address must be 43 bytes, got {}",
                    hotkey_raw_address.len()
                ),
            })?;
    let hotkey_addr: Address = Address::from_raw_address_bytes(&addr_43)
        .into_option()
        .ok_or_else(|| VotingError::InvalidInput {
            message: "hotkey_raw_address is not a valid orchard address".to_string(),
        })?;

    // Derive hotkey x-coordinates for VAN
    let (derived_g_d_new_x, derived_pk_d_new_x) =
        derive_hotkey_x_coords_from_raw_address(&addr_43)?;

    // Convert vote_round_id from hex string to 32 bytes
    let vote_round_id_bytes =
        hex::decode(&params.vote_round_id).map_err(|e| VotingError::InvalidInput {
            message: format!("vote_round_id is not valid hex: {}", e),
        })?;
    crate::types::validate_32_bytes(&vote_round_id_bytes, "vote_round_id (decoded hex)")?;
    let vri_32: [u8; 32] = vote_round_id_bytes
        .try_into()
        .expect("validated as 32 bytes above");

    let mut rng = rand::thread_rng();

    // --- Compute governance nullifiers (same as construct_delegation_action) ---
    let mut gov_nullifiers: Vec<Vec<u8>> = Vec::with_capacity(5);
    for note in notes {
        let gov_null = governance::derive_gov_nullifier(nk_bytes, &vri_32, &note.nullifier)?;
        gov_nullifiers.push(gov_null);
    }

    // Padded note generation
    let mut padded_cmx: Vec<Vec<u8>> = Vec::new();
    let mut dummy_nullifiers: Vec<Vec<u8>> = Vec::new();
    let n_real = notes.len();
    if n_real < 5 {
        for i in n_real..5 {
            let pad_addr = fvk.address_at(1000u32 + i as u32, Scope::External);
            let rho = random_rho(&mut rng);
            let (pad_note, _) = make_dummy_note(pad_addr, rho, &mut rng)?;
            let cmx: ExtractedNoteCommitment = pad_note.commitment().into();
            let real_nf = pad_note.nullifier(&fvk);
            let gov_null =
                governance::derive_gov_nullifier(nk_bytes, &vri_32, &real_nf.to_bytes())?;
            padded_cmx.push(cmx.to_bytes().to_vec());
            gov_nullifiers.push(gov_null);
            dummy_nullifiers.push(real_nf.to_bytes().to_vec());
        }
    }

    // Per-bundle weight
    let total_weight: u64 = notes
        .iter()
        .try_fold(0u64, |acc, n| acc.checked_add(n.value))
        .ok_or_else(|| VotingError::InvalidInput {
            message: "total note weight overflows u64".to_string(),
        })?;

    // Sample van_comm_rand
    let van_comm_rand_fp = pallas::Base::random(&mut rng);
    let van_comm_rand: [u8; 32] = van_comm_rand_fp.to_repr();

    // Compute VAN
    let van = governance::construct_van(
        &derived_g_d_new_x,
        &derived_pk_d_new_x,
        total_weight,
        &vri_32,
        &van_comm_rand,
    )?;

    // Collect all 5 cmx values
    let mut all_cmx: Vec<Vec<u8>> = Vec::with_capacity(5);
    for note in notes {
        all_cmx.push(note.commitment.clone());
    }
    all_cmx.extend(padded_cmx.iter().cloned());
    if all_cmx.len() != 5 {
        return Err(VotingError::Internal {
            message: format!("expected 5 cmx values, got {}", all_cmx.len()),
        });
    }

    // Compute constrained rho
    let rho_signed = governance::compute_rho_binding(
        &all_cmx[0],
        &all_cmx[1],
        &all_cmx[2],
        &all_cmx[3],
        &all_cmx[4],
        &van,
        &vri_32,
    )?;

    // --- Build signed note (§1.3.4.2) ---
    let rho_signed_32: [u8; 32] = rho_signed
        .clone()
        .try_into()
        .expect("rho_signed is 32 bytes from compute_rho_binding");
    let rho_for_note: Rho = Rho::from_bytes(&rho_signed_32)
        .into_option()
        .ok_or_else(|| VotingError::Internal {
            message: "rho_signed is not a valid Pallas field element for Rho".to_string(),
        })?;
    let sender_address = fvk.address_at(0u32, Scope::External);
    let (signed_note, rseed_signed_bytes) =
        make_dummy_note(sender_address, rho_for_note, &mut rng)?;

    // --- Build PCZT using orchard Builder ---
    // Dummy MerklePath: all-zero siblings, position 0.
    // Compute the anchor from the note commitment so the Builder's anchor check passes.
    let dummy_auth_path: [MerkleHashOrchard; MERKLE_DEPTH] = {
        let zero_hash = MerkleHashOrchard::from_bytes(&[0u8; 32])
            .into_option()
            .ok_or_else(|| VotingError::Internal {
                message: "zero bytes is not a valid MerkleHashOrchard".to_string(),
            })?;
        [zero_hash; MERKLE_DEPTH]
    };
    let dummy_merkle_path = MerklePath::from_parts(0u32, dummy_auth_path);
    let anchor = {
        let cm = signed_note.commitment();
        let root = dummy_merkle_path.root(cm.into());
        Anchor::from(root)
    };

    let mut builder = Builder::new(BundleType::DEFAULT, anchor);

    // Add the governance signed note as a spend
    builder
        .add_spend(fvk.clone(), signed_note, dummy_merkle_path)
        .map_err(|e| VotingError::Internal {
            message: format!("Builder::add_spend failed: {:?}", e),
        })?;

    // Add output to hotkey address (1 zatoshi, with delegation memo)
    let ovk = fvk.to_ovk(Scope::External);
    let memo = {
        let zec_whole = total_weight / 100_000_000;
        let zec_frac = total_weight % 100_000_000;
        let memo_str = format!(
            "I am authorizing this hotkey managed by my wallet to vote on {} with {}.{:08} ZEC.",
            round_name, zec_whole, zec_frac
        );
        let mut buf = [0u8; 512];
        let bytes = memo_str.as_bytes();
        let len = bytes.len().min(512);
        buf[..len].copy_from_slice(&bytes[..len]);
        buf
    };
    builder
        .add_output(Some(ovk), hotkey_addr, NoteValue::from_raw(1), memo)
        .map_err(|e| VotingError::Internal {
            message: format!("Builder::add_output failed: {:?}", e),
        })?;

    // Build the PCZT bundle
    let (mut orchard_pczt_bundle, bundle_meta) =
        builder.build_for_pczt(&mut rng).map_err(|e| {
            VotingError::Internal {
                message: format!("Builder::build_for_pczt failed: {:?}", e),
            }
        })?;

    // Extract data from the real governance action (may be shuffled by Builder)
    let spend_idx = bundle_meta.spend_action_index(0).ok_or_else(|| {
        VotingError::Internal {
            message: "BundleMetadata missing spend action index".to_string(),
        }
    })?;
    let output_idx = bundle_meta.output_action_index(0).ok_or_else(|| {
        VotingError::Internal {
            message: "BundleMetadata missing output action index".to_string(),
        }
    })?;

    let spend_action = &orchard_pczt_bundle.actions()[spend_idx];
    let nf_signed_bytes: [u8; 32] = spend_action.spend().nullifier().to_bytes();
    let rk_bytes: [u8; 32] = spend_action.spend().rk().into();
    let alpha = spend_action
        .spend()
        .alpha()
        .ok_or_else(|| VotingError::Internal {
            message: "PCZT spend missing alpha".to_string(),
        })?;
    let alpha_bytes: [u8; 32] = alpha.to_repr();
    let rseed_signed_from_pczt = spend_action
        .spend()
        .rseed()
        .ok_or_else(|| VotingError::Internal {
            message: "PCZT spend missing rseed".to_string(),
        })?;
    // Verify rseed consistency between our note and the PCZT
    if rseed_signed_from_pczt.as_bytes() != &rseed_signed_bytes {
        return Err(VotingError::Internal {
            message: "rseed mismatch between note and PCZT".to_string(),
        });
    }

    let output_action = &orchard_pczt_bundle.actions()[output_idx];
    let cmx_new_bytes: [u8; 32] = output_action.output().cmx().to_bytes();
    let rseed_output = output_action
        .output()
        .rseed()
        .ok_or_else(|| VotingError::Internal {
            message: "PCZT output missing rseed".to_string(),
        })?;
    let rseed_output_bytes: [u8; 32] = *rseed_output.as_bytes();

    // --- Updater role: set zip32_derivation so Keystone can derive the spending key ---
    // Orchard ZIP-32 derivation path: m / 32' / coin_type' / account'
    let zip32_deriv = Zip32Derivation::parse(
        *seed_fingerprint,
        vec![
            32 | (1 << 31),              // purpose: hardened(32)
            coin_type | (1 << 31),        // coin_type
            account_index | (1 << 31),    // account
        ],
    )
    .map_err(|e| VotingError::Internal {
        message: format!("Zip32Derivation::parse failed: {:?}", e),
    })?;
    orchard_pczt_bundle
        .update_with(|mut updater| {
            updater.update_action_with(spend_idx, |mut action_updater| {
                action_updater.set_spend_zip32_derivation(zip32_deriv);
                Ok(())
            })
        })
        .map_err(|e| VotingError::Internal {
            message: format!("PCZT updater failed: {:?}", e),
        })?;

    // --- Serialize to full PCZT ---
    // Create an empty Pczt shell with Creator, then replace the orchard bundle.
    let sapling_anchor = [0u8; 32]; // No sapling bundle
    let orchard_anchor_bytes = anchor.to_bytes();
    let mut pczt = pczt::roles::creator::Creator::new(
        consensus_branch_id,
        0, // expiry_height: 0 = no expiry (never broadcast)
        coin_type,
        sapling_anchor,
        orchard_anchor_bytes,
    )
    // Keystone's determine_lock_time returns global.lock_time() for pure-Orchard PCZTs
    // (no transparent inputs). Without a fallback_lock_time, it returns None → error.
    .with_fallback_lock_time(0)
    .build();

    // Serialize the orchard pczt bundle and set it on the Pczt
    let pczt_orchard_bundle = pczt::orchard::Bundle::serialize_from(orchard_pczt_bundle);
    pczt.set_orchard(pczt_orchard_bundle);

    // Run IO Finalizer so the Signer (Keystone) can compute the sighash
    let pczt = pczt::roles::io_finalizer::IoFinalizer::new(pczt)
        .finalize_io()
        .map_err(|e| VotingError::Internal {
            message: format!("IoFinalizer::finalize_io failed: {:?}", e),
        })?;

    let pczt_bytes = pczt.serialize();

    // --- Encode canonical action bytes for cosmos chain ---
    let action_bytes = encode_delegation_action_bytes(
        &nf_signed_bytes,
        &rk_bytes,
        &cmx_new_bytes,
        &van,
        &gov_nullifiers,
        &vri_32,
    )?;

    Ok(GovernancePczt {
        pczt_bytes,
        rk: rk_bytes.to_vec(),
        alpha: alpha_bytes.to_vec(),
        nf_signed: nf_signed_bytes.to_vec(),
        cmx_new: cmx_new_bytes.to_vec(),
        gov_nullifiers,
        van,
        van_comm_rand: van_comm_rand.to_vec(),
        dummy_nullifiers,
        rho_signed,
        padded_cmx,
        rseed_signed: rseed_signed_bytes.to_vec(),
        rseed_output: rseed_output_bytes.to_vec(),
        action_bytes,
        action_index: spend_idx,
    })
}

/// Extract the ZIP-244 shielded sighash from finalized PCZT bytes.
///
/// Creates a Signer from the PCZT to compute the v5 signature hash, then
/// returns it. This is the sighash that Keystone signs internally.
pub fn extract_pczt_sighash(pczt_bytes: &[u8]) -> Result<[u8; 32], VotingError> {
    let pczt = pczt::Pczt::parse(pczt_bytes).map_err(|e| VotingError::Internal {
        message: format!("Failed to parse PCZT: {:?}", e),
    })?;
    let signer =
        pczt::roles::signer::Signer::new(pczt).map_err(|e| VotingError::Internal {
            message: format!("Failed to create Signer from PCZT: {:?}", e),
        })?;
    Ok(signer.shielded_sighash())
}

/// Extract the spend_auth_sig from a signed PCZT.
///
/// Keystone redacts sensitive fields (alpha, rseed, zip32_derivation, etc.) after signing,
/// so a byte-diff between unsigned and signed PCZTs doesn't work. This function parses
/// the signed PCZT structurally and reads the `spend_auth_sig` field directly.
///
/// Tries `action_index` first, then falls back to scanning all actions. The Builder
/// shuffles action order, so the governance spend may not end up at the expected index
/// from Keystone's perspective. Our governance PCZT has exactly 2 actions (1 real +
/// 1 dummy padding); only the real one gets signed (the dummy lacks zip32_derivation).
///
/// Returns the 64-byte SpendAuthSig, or an error if no signed action is found.
pub fn extract_spend_auth_sig(
    signed_pczt_bytes: &[u8],
    action_index: usize,
) -> Result<[u8; 64], VotingError> {
    let pczt = pczt::Pczt::parse(signed_pczt_bytes).map_err(|e| VotingError::Internal {
        message: format!("Failed to parse signed PCZT: {:?}", e),
    })?;

    let actions = pczt.orchard().actions();

    // Try the expected action index first.
    if action_index < actions.len() {
        if let Some(sig) = actions[action_index].spend().spend_auth_sig() {
            return Ok(*sig);
        }
    }

    // Fallback: scan all actions for a signature.
    // The governance PCZT has 2 actions; only the real governance spend gets signed
    // by Keystone (the padding action has no zip32_derivation so Keystone skips it).
    // This is safe because there is exactly one signable action.
    for action in actions {
        if let Some(sig) = action.spend().spend_auth_sig() {
            return Ok(*sig);
        }
    }

    Err(VotingError::Internal {
        message: format!(
            "No spend_auth_sig found in any of the {} actions in the signed PCZT",
            actions.len()
        ),
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use orchard::keys::SpendingKey;

    fn mock_note() -> NoteInfo {
        NoteInfo {
            commitment: vec![0x01; 32],
            nullifier: vec![0x02; 32],
            value: 15_000_000,
            position: 42,
            diversifier: vec![0; 11],
            rho: vec![0; 32],
            rseed: vec![0; 32],
            scope: 0,
            ufvk_str: String::new(),
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

    /// Derive a valid 96-byte FVK from a deterministic SpendingKey.
    fn mock_fvk_bytes() -> Vec<u8> {
        let sk = SpendingKey::from_bytes([0x42; 32]).expect("valid spending key");
        let fvk = FullViewingKey::from(&sk);
        fvk.to_bytes().to_vec()
    }

    /// Derive a valid 43-byte raw orchard address from a mock FVK.
    fn mock_hotkey_address() -> Vec<u8> {
        // Use a different key so the hotkey address differs from the sender
        let sk = SpendingKey::from_bytes([0x43; 32]).expect("valid spending key");
        let fvk = FullViewingKey::from(&sk);
        let addr = fvk.address_at(0u32, Scope::External);
        addr.to_raw_address_bytes().to_vec()
    }

    fn mock_g_d() -> Vec<u8> {
        let addr_43: [u8; 43] = mock_hotkey_address()
            .try_into()
            .expect("mock hotkey address is 43 bytes");
        let (g_d_x, _) =
            derive_hotkey_x_coords_from_raw_address(&addr_43).expect("valid mock hotkey address");
        g_d_x.to_vec()
    }

    fn mock_pk_d() -> Vec<u8> {
        let addr_43: [u8; 43] = mock_hotkey_address()
            .try_into()
            .expect("mock hotkey address is 43 bytes");
        let (_, pk_d_x) =
            derive_hotkey_x_coords_from_raw_address(&addr_43).expect("valid mock hotkey address");
        pk_d_x.to_vec()
    }

    #[test]
    fn test_construct_delegation_action_one_note() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        // rk is 32 bytes and NOT the old stub pattern
        assert_eq!(result.rk.len(), 32);
        assert_ne!(result.rk, vec![0xDE; 32]);

        // action_bytes is non-empty and NOT the old stub pattern
        assert!(!result.action_bytes.is_empty());
        assert_ne!(result.action_bytes, vec![0xDA; 128]);

        // nf_signed is 32 bytes, non-zero
        assert_eq!(result.nf_signed.len(), 32);
        assert_ne!(result.nf_signed, vec![0u8; 32]);

        // cmx_new is 32 bytes, non-zero
        assert_eq!(result.cmx_new.len(), 32);
        assert_ne!(result.cmx_new, vec![0u8; 32]);

        // alpha is 32 bytes, non-zero
        assert_eq!(result.alpha.len(), 32);
        assert_ne!(result.alpha, vec![0u8; 32]);

        // rseed values are 32 bytes each and non-zero
        assert_eq!(result.rseed_signed.len(), 32);
        assert_ne!(result.rseed_signed, vec![0u8; 32]);
        assert_eq!(result.rseed_output.len(), 32);
        assert_ne!(result.rseed_output, vec![0u8; 32]);

        // Gov nullifiers always padded to 5
        assert_eq!(result.gov_nullifiers.len(), 5);
        for gnull in &result.gov_nullifiers {
            assert_eq!(gnull.len(), 32);
        }

        // VAN is 32 bytes
        assert_eq!(result.van.len(), 32);
        assert_ne!(result.van, vec![0xBB; 32]);

        // van_comm_rand is 32 bytes
        assert_eq!(result.van_comm_rand.len(), 32);

        // First gov nullifier is real (deterministic for same inputs)
        assert_ne!(result.gov_nullifiers[0], vec![0xAA; 32]);

        // rho_signed is 32 bytes and non-zero
        assert_eq!(result.rho_signed.len(), 32);
        assert_ne!(result.rho_signed, vec![0u8; 32]);

        // padded_cmx: 4 padded notes (1 real + 4 padded = 5)
        assert_eq!(result.padded_cmx.len(), 4);
        for cmx in &result.padded_cmx {
            assert_eq!(cmx.len(), 32);
        }
    }

    #[test]
    fn test_construct_delegation_action_five_notes() {
        let notes: Vec<NoteInfo> = (0..5)
            .map(|i| NoteInfo {
                commitment: vec![i as u8 + 1; 32],
                nullifier: vec![i as u8 + 0x10; 32],
                value: 13_000_000,
                position: i as u64,
                diversifier: vec![0; 11],
                rho: vec![0; 32],
                rseed: vec![0; 32],
                scope: 0,
                ufvk_str: String::new(),
            })
            .collect();

        let result = construct_delegation_action(
            &notes,
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        assert_eq!(result.gov_nullifiers.len(), 5);
        // All 5 should be real (no padding needed).
        // They should all be different since inputs differ.
        for i in 0..5 {
            for j in (i + 1)..5 {
                assert_ne!(
                    result.gov_nullifiers[i], result.gov_nullifiers[j],
                    "gov nullifiers {} and {} should differ",
                    i, j
                );
            }
        }

        // No padding needed — padded_cmx should be empty
        assert!(result.padded_cmx.is_empty());

        // rho_signed still computed from the 5 real cmx values
        assert_eq!(result.rho_signed.len(), 32);
        assert_ne!(result.rho_signed, vec![0u8; 32]);
    }

    #[test]
    fn test_construct_delegation_action_deterministic_gov_nullifiers() {
        let result1 = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        let result2 = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        // First gov nullifier (real) should be deterministic
        assert_eq!(result1.gov_nullifiers[0], result2.gov_nullifiers[0]);

        // VAN will differ because van_comm_rand is randomly sampled each time
        // (this is expected)
    }

    #[test]
    fn test_construct_delegation_action_no_notes() {
        let result = construct_delegation_action(
            &[],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_construct_delegation_action_too_many_notes() {
        let notes: Vec<NoteInfo> = (0..6).map(|_| mock_note()).collect();
        let result = construct_delegation_action(
            &notes,
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_construct_delegation_action_rejects_short_vote_round_id() {
        let mut params = mock_params();
        // 31 bytes (62 hex chars)
        params.vote_round_id = "01".repeat(31);

        let result = construct_delegation_action(
            &[mock_note()],
            &params,
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_construct_delegation_action_rejects_long_vote_round_id() {
        let mut params = mock_params();
        // 33 bytes (66 hex chars)
        params.vote_round_id = "01".repeat(33);

        let result = construct_delegation_action(
            &[mock_note()],
            &params,
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_rho_changes_with_different_notes() {
        // Use small byte values that are guaranteed valid Pallas field elements
        // (values with the high byte < 0x40 are always in range).
        let notes_a: Vec<NoteInfo> = (0..5)
            .map(|i| {
                let mut commitment = vec![0u8; 32];
                commitment[0] = i as u8 + 0x10;
                let mut nullifier = vec![0u8; 32];
                nullifier[0] = i as u8 + 0x20;
                NoteInfo {
                    commitment,
                    nullifier,
                    value: 13_000_000,
                    position: i as u64,
                    diversifier: vec![0; 11],
                    rho: vec![0; 32],
                    rseed: vec![0; 32],
                    scope: 0,
                    ufvk_str: String::new(),
                }
            })
            .collect();

        let notes_b: Vec<NoteInfo> = (0..5)
            .map(|i| {
                let mut commitment = vec![0u8; 32];
                commitment[0] = i as u8 + 0x30;
                let mut nullifier = vec![0u8; 32];
                nullifier[0] = i as u8 + 0x40;
                NoteInfo {
                    commitment,
                    nullifier,
                    value: 13_000_000,
                    position: i as u64,
                    diversifier: vec![0; 11],
                    rho: vec![0; 32],
                    rseed: vec![0; 32],
                    scope: 0,
                    ufvk_str: String::new(),
                }
            })
            .collect();

        let result_a = construct_delegation_action(
            &notes_a,
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        let result_b = construct_delegation_action(
            &notes_b,
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        // Different note commitments should produce different rho
        // (VAN also differs due to random van_comm_rand, reinforcing the difference)
        assert_ne!(
            result_a.rho_signed, result_b.rho_signed,
            "different note sets must produce different rho_signed"
        );
    }

    #[test]
    fn test_rk_changes_with_different_alpha() {
        // Two calls should produce different rk because alpha is randomized each time
        let result1 = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        let result2 = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        // rk should differ between calls (different random alpha)
        assert_ne!(
            result1.rk, result2.rk,
            "rk should differ due to random alpha"
        );
        assert_ne!(
            result1.alpha, result2.alpha,
            "alpha should differ between calls"
        );
    }

    #[test]
    fn test_nf_signed_is_nonzero() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        assert_eq!(result.nf_signed.len(), 32);
        assert_ne!(result.nf_signed, vec![0u8; 32]);
        assert_eq!(result.cmx_new.len(), 32);
        assert_ne!(result.cmx_new, vec![0u8; 32]);
    }

    #[test]
    fn test_rejects_invalid_fvk() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &vec![0x00; 96],
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_rejects_wrong_length_fvk() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &vec![0x42; 32],
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_rejects_mismatched_pk_d_x() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &vec![0xAA; 32],
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_rejects_mismatched_g_d_x() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &vec![0xBB; 32],
            &mock_pk_d(),
            &mock_hotkey_address(),
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_rejects_wrong_length_hotkey_address() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &vec![0x42; 32],
        );
        assert!(result.is_err());
    }

    #[test]
    fn test_action_bytes_canonical_encoding_order() {
        let nf_signed = [0x01; 32];
        let rk = [0x02; 32];
        let cmx_new = [0x03; 32];
        let van_comm = vec![0x04; 32];
        let gov_nullifiers = vec![
            vec![0x05; 32],
            vec![0x06; 32],
            vec![0x07; 32],
            vec![0x08; 32],
            vec![0x0A; 32],
        ];
        let vote_round_id = [0x09; 32];

        let encoded = encode_delegation_action_bytes(
            &nf_signed,
            &rk,
            &cmx_new,
            &van_comm,
            &gov_nullifiers,
            &vote_round_id,
        )
        .unwrap();

        assert_eq!(encoded.len(), 32 * 10);
        assert_eq!(&encoded[0..32], &nf_signed);
        assert_eq!(&encoded[32..64], &rk);
        assert_eq!(&encoded[64..96], &cmx_new);
        assert_eq!(&encoded[96..128], &van_comm);
        assert_eq!(&encoded[128..160], &gov_nullifiers[0]);
        assert_eq!(&encoded[160..192], &gov_nullifiers[1]);
        assert_eq!(&encoded[192..224], &gov_nullifiers[2]);
        assert_eq!(&encoded[224..256], &gov_nullifiers[3]);
        assert_eq!(&encoded[256..288], &gov_nullifiers[4]);
        assert_eq!(&encoded[288..320], &vote_round_id);
    }

    #[test]
    fn test_action_bytes_rejects_non_canonical_gov_nullifier_count() {
        let encoded = encode_delegation_action_bytes(
            &[0x01; 32],
            &[0x02; 32],
            &[0x03; 32],
            &[0x04; 32],
            &vec![vec![0x05; 32]; 4],
            &[0x06; 32],
        );
        assert!(encoded.is_err());
    }

    #[test]
    fn test_constructed_action_bytes_match_canonical_encoding() {
        let result = construct_delegation_action(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_g_d(),
            &mock_pk_d(),
            &mock_hotkey_address(),
        )
        .unwrap();

        let nf_signed: [u8; 32] = result
            .nf_signed
            .clone()
            .try_into()
            .expect("nf_signed should be 32 bytes");
        let rk: [u8; 32] = result.rk.clone().try_into().expect("rk should be 32 bytes");
        let cmx_new: [u8; 32] = result
            .cmx_new
            .clone()
            .try_into()
            .expect("cmx_new should be 32 bytes");
        let vote_round_id: [u8; 32] = hex::decode(&mock_params().vote_round_id)
            .unwrap()
            .try_into()
            .expect("vote_round_id should be 32 bytes");

        let expected = encode_delegation_action_bytes(
            &nf_signed,
            &rk,
            &cmx_new,
            &result.van,
            &result.gov_nullifiers,
            &vote_round_id,
        )
        .unwrap();

        assert_eq!(result.action_bytes, expected);
    }

    // --- build_governance_pczt tests ---

    /// NU5 mainnet consensus branch ID
    const NU5_BRANCH_ID: u32 = 0xC2D6D0B4;
    /// Mainnet coin type
    const MAINNET_COIN_TYPE: u32 = 133;
    /// Mock seed fingerprint (32 bytes)
    const MOCK_SEED_FP: [u8; 32] = [0xAA; 32];
    /// Mock account index
    const MOCK_ACCOUNT: u32 = 0;

    #[test]
    fn test_build_governance_pczt_one_note() {
        let result = build_governance_pczt(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_hotkey_address(),
            NU5_BRANCH_ID,
            MAINNET_COIN_TYPE,
            &MOCK_SEED_FP,
            MOCK_ACCOUNT,
            "Test Round",
        )
        .unwrap();

        // PCZT bytes are non-empty and parseable
        assert!(!result.pczt_bytes.is_empty());
        let parsed = pczt::Pczt::parse(&result.pczt_bytes);
        assert!(parsed.is_ok(), "PCZT bytes should parse: {:?}", parsed.err());

        // rk is 32 bytes, non-zero
        assert_eq!(result.rk.len(), 32);
        assert_ne!(result.rk, vec![0u8; 32]);

        // alpha is 32 bytes, non-zero
        assert_eq!(result.alpha.len(), 32);
        assert_ne!(result.alpha, vec![0u8; 32]);

        // nf_signed is 32 bytes, non-zero
        assert_eq!(result.nf_signed.len(), 32);
        assert_ne!(result.nf_signed, vec![0u8; 32]);

        // cmx_new is 32 bytes, non-zero
        assert_eq!(result.cmx_new.len(), 32);
        assert_ne!(result.cmx_new, vec![0u8; 32]);

        // Gov nullifiers padded to 5
        assert_eq!(result.gov_nullifiers.len(), 5);
        for gn in &result.gov_nullifiers {
            assert_eq!(gn.len(), 32);
        }

        // VAN is 32 bytes
        assert_eq!(result.van.len(), 32);

        // van_comm_rand is 32 bytes
        assert_eq!(result.van_comm_rand.len(), 32);

        // rho_signed is 32 bytes
        assert_eq!(result.rho_signed.len(), 32);
        assert_ne!(result.rho_signed, vec![0u8; 32]);

        // 4 padded cmx (1 real + 4 padded = 5)
        assert_eq!(result.padded_cmx.len(), 4);

        // rseed values are 32 bytes each
        assert_eq!(result.rseed_signed.len(), 32);
        assert_ne!(result.rseed_signed, vec![0u8; 32]);
        assert_eq!(result.rseed_output.len(), 32);
        assert_ne!(result.rseed_output, vec![0u8; 32]);

        // action_bytes is 10 * 32 = 320 bytes
        assert_eq!(result.action_bytes.len(), 320);

        // action_index is 0 or 1 (2 actions total: 1 real + 1 dummy padding)
        assert!(result.action_index <= 1);

        // The parsed PCZT should have 2 orchard actions (1 real + 1 padding)
        let pczt = parsed.unwrap();
        assert_eq!(pczt.orchard().actions().len(), 2);
    }

    #[test]
    fn test_build_governance_pczt_five_notes() {
        let notes: Vec<NoteInfo> = (0..5)
            .map(|i| NoteInfo {
                commitment: vec![i as u8 + 1; 32],
                nullifier: vec![i as u8 + 0x10; 32],
                value: 13_000_000,
                position: i as u64,
                diversifier: vec![0; 11],
                rho: vec![0; 32],
                rseed: vec![0; 32],
                scope: 0,
                ufvk_str: String::new(),
            })
            .collect();

        let result = build_governance_pczt(
            &notes,
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_hotkey_address(),
            NU5_BRANCH_ID,
            MAINNET_COIN_TYPE,
            &MOCK_SEED_FP,
            MOCK_ACCOUNT,
            "Test Round",
        )
        .unwrap();

        assert_eq!(result.gov_nullifiers.len(), 5);
        assert!(result.padded_cmx.is_empty());
        assert!(result.dummy_nullifiers.is_empty());

        // Gov nullifiers should all differ
        for i in 0..5 {
            for j in (i + 1)..5 {
                assert_ne!(result.gov_nullifiers[i], result.gov_nullifiers[j]);
            }
        }
    }

    #[test]
    fn test_build_governance_pczt_different_rk_each_call() {
        let result1 = build_governance_pczt(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_hotkey_address(),
            NU5_BRANCH_ID,
            MAINNET_COIN_TYPE,
            &MOCK_SEED_FP,
            MOCK_ACCOUNT,
            "Test Round",
        )
        .unwrap();

        let result2 = build_governance_pczt(
            &[mock_note()],
            &mock_params(),
            &mock_fvk_bytes(),
            &mock_hotkey_address(),
            NU5_BRANCH_ID,
            MAINNET_COIN_TYPE,
            &MOCK_SEED_FP,
            MOCK_ACCOUNT,
            "Test Round",
        )
        .unwrap();

        // rk and alpha should differ due to randomization
        assert_ne!(result1.rk, result2.rk);
        assert_ne!(result1.alpha, result2.alpha);

        // nf_signed should be deterministic (same rho_signed from same notes/params)
        // but rho_signed differs because VAN includes random van_comm_rand
        // So nf_signed will differ between calls
    }
}
