//! Verifier public-input wire decoding for the CGo FFI boundary.
//!
//! This module owns the byte-level verifier payloads accepted by
//! `crate::ffi`. It translates those payloads into typed `voting-circuits`
//! instances before proof verification.

use ff::PrimeField;
use group::{Curve, GroupEncoding};
use orchard::note::nullifier::Nullifier;
use pasta_curves::{
    arithmetic::{Coordinates, CurveAffine},
    pallas,
};

use crate::{delegation, share_reveal, vote_proof};

const PUBLIC_INPUT_WIRE_BYTES: usize = 32;
const COMPRESSED_POINT_WIRE_SAVINGS: usize = 1;
const DELEGATION_DERIVED_PUBLIC_INPUTS: usize = 1;
const DELEGATION_WIRE_PUBLIC_INPUTS: usize = delegation::Instance::NUM_PUBLIC_INPUTS
    - COMPRESSED_POINT_WIRE_SAVINGS
    - DELEGATION_DERIVED_PUBLIC_INPUTS;
const VOTE_PROOF_WIRE_PUBLIC_INPUTS: usize =
    vote_proof::Instance::NUM_PUBLIC_INPUTS - 2 * COMPRESSED_POINT_WIRE_SAVINGS;

/// Byte length of the delegation verifier wire public-input payload.
pub(crate) const DELEGATION_PUBLIC_INPUT_WIRE_BYTES: usize =
    DELEGATION_WIRE_PUBLIC_INPUTS * PUBLIC_INPUT_WIRE_BYTES;
/// Byte length of the vote proof verifier wire public-input payload.
pub(crate) const VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES: usize =
    VOTE_PROOF_WIRE_PUBLIC_INPUTS * PUBLIC_INPUT_WIRE_BYTES;
/// Byte length of the share reveal verifier wire public-input payload.
pub(crate) const SHARE_REVEAL_PUBLIC_INPUT_WIRE_BYTES: usize =
    share_reveal::Instance::NUM_PUBLIC_INPUTS * PUBLIC_INPUT_WIRE_BYTES;

// `rk` is one compressed point on the wire but two circuit public inputs
// (`rk_x`, `rk_y`), so every following wire slot is shifted back by one.
const DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK: usize = COMPRESSED_POINT_WIRE_SAVINGS;
const DELEGATION_WIRE_SLOT_NF_SIGNED: usize = delegation::NF_SIGNED_PUBLIC_OFFSET;
const DELEGATION_WIRE_SLOT_RK: usize = delegation::RK_X_PUBLIC_OFFSET;
const DELEGATION_WIRE_SLOT_CMX_NEW: usize =
    delegation::CMX_NEW_PUBLIC_OFFSET - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK;
const DELEGATION_WIRE_SLOT_VAN_COMM: usize =
    delegation::VAN_COMM_PUBLIC_OFFSET - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK;
const DELEGATION_WIRE_SLOT_VOTE_ROUND_ID: usize =
    delegation::VOTE_ROUND_ID_PUBLIC_OFFSET - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK;
const DELEGATION_WIRE_SLOT_NC_ROOT: usize =
    delegation::NC_ROOT_PUBLIC_OFFSET - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK;
const DELEGATION_WIRE_SLOT_NF_IMT_ROOT: usize =
    delegation::NF_IMT_ROOT_PUBLIC_OFFSET - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK;
const DELEGATION_WIRE_SLOT_GOV_NULL: [usize; delegation::GOV_NULL_PUBLIC_OFFSETS.len()] = [
    delegation::GOV_NULL_PUBLIC_OFFSETS[0] - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK,
    delegation::GOV_NULL_PUBLIC_OFFSETS[1] - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK,
    delegation::GOV_NULL_PUBLIC_OFFSETS[2] - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK,
    delegation::GOV_NULL_PUBLIC_OFFSETS[3] - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK,
    delegation::GOV_NULL_PUBLIC_OFFSETS[4] - DELEGATION_WIRE_SLOT_SHIFT_AFTER_RK,
];

// `r_vpk` is one compressed point on the wire but two circuit public inputs
// (`r_vpk_x`, `r_vpk_y`), so fields after it shift back by one wire slot.
const VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK: usize = COMPRESSED_POINT_WIRE_SAVINGS;
const VOTE_WIRE_SLOT_VAN_NULLIFIER: usize = vote_proof::VAN_NULLIFIER_PUBLIC_OFFSET;
const VOTE_WIRE_SLOT_R_VPK: usize = vote_proof::R_VPK_X_PUBLIC_OFFSET;
const VOTE_WIRE_SLOT_VOTE_AUTHORITY_NOTE_NEW: usize =
    vote_proof::VOTE_AUTHORITY_NOTE_NEW_PUBLIC_OFFSET - VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK;
const VOTE_WIRE_SLOT_VOTE_COMMITMENT: usize =
    vote_proof::VOTE_COMMITMENT_PUBLIC_OFFSET - VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK;
const VOTE_WIRE_SLOT_VOTE_COMM_TREE_ROOT: usize =
    vote_proof::VOTE_COMM_TREE_ROOT_PUBLIC_OFFSET - VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK;
const VOTE_WIRE_SLOT_VOTE_COMM_TREE_ANCHOR_HEIGHT: usize =
    vote_proof::VOTE_COMM_TREE_ANCHOR_HEIGHT_PUBLIC_OFFSET - VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK;
const VOTE_WIRE_SLOT_PROPOSAL_ID: usize =
    vote_proof::PROPOSAL_ID_PUBLIC_OFFSET - VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK;
const VOTE_WIRE_SLOT_VOTING_ROUND_ID: usize =
    vote_proof::VOTING_ROUND_ID_PUBLIC_OFFSET - VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK;
// `ea_pk` is also compressed, but its own compression does not shift its
// starting slot; only earlier compressed points affect where it appears.
const VOTE_WIRE_SLOT_EA_PK: usize =
    vote_proof::EA_PK_X_PUBLIC_OFFSET - VOTE_WIRE_SLOT_SHIFT_AFTER_R_VPK;

const SHARE_REVEAL_WIRE_SLOT_SHARE_NULLIFIER: usize = share_reveal::SHARE_NULLIFIER_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C1_X: usize = share_reveal::ENC_SHARE_C1_X_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C1_Y: usize = share_reveal::ENC_SHARE_C1_Y_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C2_X: usize = share_reveal::ENC_SHARE_C2_X_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C2_Y: usize = share_reveal::ENC_SHARE_C2_Y_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_PROPOSAL_ID: usize = share_reveal::PROPOSAL_ID_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_VOTE_DECISION: usize = share_reveal::VOTE_DECISION_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_VOTE_COMM_TREE_ROOT: usize =
    share_reveal::VOTE_COMM_TREE_ROOT_PUBLIC_OFFSET;
const SHARE_REVEAL_WIRE_SLOT_VOTING_ROUND_ID: usize = share_reveal::VOTING_ROUND_ID_PUBLIC_OFFSET;

/// Error returned while decoding verifier wire bytes into typed instances.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum PublicInputWireError {
    /// The raw public-input byte slice has the wrong length.
    InvalidLength { expected: usize, actual: usize },
    /// A 32-byte slot was not a canonical Pallas base field element.
    NonCanonicalField { slot: usize, name: &'static str },
    /// A 32-byte slot was not a valid compressed Pallas point.
    InvalidCompressedPoint {
        slot: usize,
        name: &'static str,
        prefix: [u8; 4],
    },
    /// A compressed point decoded to the identity, which has no affine coordinates.
    IdentityPoint { slot: usize, name: &'static str },
}

impl std::fmt::Display for PublicInputWireError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PublicInputWireError::InvalidLength { expected, actual } => {
                write!(f, "expected {expected} public-input bytes, got {actual}")
            }
            PublicInputWireError::NonCanonicalField { slot, name } => write!(
                f,
                "slot {slot} ({name}) is not a canonical Pallas Fp element"
            ),
            PublicInputWireError::InvalidCompressedPoint { slot, name, prefix } => write!(
                f,
                "slot {slot} ({name}) is not a valid compressed Pallas point: {prefix:02x?}"
            ),
            PublicInputWireError::IdentityPoint { slot, name } => {
                write!(f, "slot {slot} ({name}) decompressed to the identity point")
            }
        }
    }
}

impl std::error::Error for PublicInputWireError {}

/// Decode verifier wire bytes into a delegation circuit instance.
pub(crate) fn delegation_instance_from_wire(
    raw: &[u8],
) -> Result<delegation::Instance, PublicInputWireError> {
    ensure_len(raw, DELEGATION_PUBLIC_INPUT_WIRE_BYTES)?;

    let nf_signed = fp_slot(raw, DELEGATION_WIRE_SLOT_NF_SIGNED, "nf_signed")?;
    let (rk_x, rk_y) = compressed_point_slot(raw, DELEGATION_WIRE_SLOT_RK, "rk")?;
    let cmx_new = fp_slot(raw, DELEGATION_WIRE_SLOT_CMX_NEW, "cmx_new")?;
    let van_comm = fp_slot(raw, DELEGATION_WIRE_SLOT_VAN_COMM, "van_comm")?;
    let vote_round_id = fp_slot(
        raw,
        DELEGATION_WIRE_SLOT_VOTE_ROUND_ID,
        "vote_round_id",
    )?;
    let nc_root = fp_slot(raw, DELEGATION_WIRE_SLOT_NC_ROOT, "nc_root")?;
    let nf_imt_root = fp_slot(raw, DELEGATION_WIRE_SLOT_NF_IMT_ROOT, "nf_imt_root")?;
    let gov_null_1 = fp_slot(raw, DELEGATION_WIRE_SLOT_GOV_NULL[0], "gov_null_1")?;
    let gov_null_2 = fp_slot(raw, DELEGATION_WIRE_SLOT_GOV_NULL[1], "gov_null_2")?;
    let gov_null_3 = fp_slot(raw, DELEGATION_WIRE_SLOT_GOV_NULL[2], "gov_null_3")?;
    let gov_null_4 = fp_slot(raw, DELEGATION_WIRE_SLOT_GOV_NULL[3], "gov_null_4")?;
    let gov_null_5 = fp_slot(raw, DELEGATION_WIRE_SLOT_GOV_NULL[4], "gov_null_5")?;

    let dom = {
        use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength, P128Pow5T3};
        let mut tag_bytes = [0u8; 32];
        tag_bytes[..24].copy_from_slice(b"governance authorization");
        let tag = pallas::Base::from_repr(tag_bytes).unwrap();
        poseidon::Hash::<_, P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([tag, vote_round_id])
    };

    let nf_signed = Option::from(Nullifier::from_bytes(&nf_signed.to_repr()))
        .expect("canonical Pallas Fp encoding should deserialize as a nullifier");

    Ok(delegation::Instance {
        nf_signed,
        rk_x,
        rk_y,
        cmx_new,
        van_comm,
        vote_round_id,
        nc_root,
        nf_imt_root,
        gov_null: [gov_null_1, gov_null_2, gov_null_3, gov_null_4, gov_null_5],
        dom,
    })
}

/// Decode verifier wire bytes into a vote proof circuit instance.
pub(crate) fn vote_proof_instance_from_wire(
    raw: &[u8],
) -> Result<vote_proof::Instance, PublicInputWireError> {
    ensure_len(raw, VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES)?;

    let van_nullifier = fp_slot(raw, VOTE_WIRE_SLOT_VAN_NULLIFIER, "van_nullifier")?;
    let (r_vpk_x, r_vpk_y) = compressed_point_slot(raw, VOTE_WIRE_SLOT_R_VPK, "r_vpk")?;
    let vote_authority_note_new = fp_slot(
        raw,
        VOTE_WIRE_SLOT_VOTE_AUTHORITY_NOTE_NEW,
        "vote_authority_note_new",
    )?;
    let vote_commitment = fp_slot(raw, VOTE_WIRE_SLOT_VOTE_COMMITMENT, "vote_commitment")?;
    let vote_comm_tree_root = fp_slot(
        raw,
        VOTE_WIRE_SLOT_VOTE_COMM_TREE_ROOT,
        "vote_comm_tree_root",
    )?;
    let vote_comm_tree_anchor_height =
        pallas::Base::from(u64_slot(raw, VOTE_WIRE_SLOT_VOTE_COMM_TREE_ANCHOR_HEIGHT));
    let proposal_id = pallas::Base::from(u64::from(u32_slot(raw, VOTE_WIRE_SLOT_PROPOSAL_ID)));
    let voting_round_id = fp_slot(raw, VOTE_WIRE_SLOT_VOTING_ROUND_ID, "voting_round_id")?;
    let (ea_pk_x, ea_pk_y) = compressed_point_slot(raw, VOTE_WIRE_SLOT_EA_PK, "ea_pk")?;

    Ok(vote_proof::Instance::from_parts(
        van_nullifier,
        r_vpk_x,
        r_vpk_y,
        vote_authority_note_new,
        vote_commitment,
        vote_comm_tree_root,
        vote_comm_tree_anchor_height,
        proposal_id,
        voting_round_id,
        ea_pk_x,
        ea_pk_y,
    ))
}

/// Decode verifier wire bytes into a share reveal circuit instance.
pub(crate) fn share_reveal_instance_from_wire(
    raw: &[u8],
) -> Result<share_reveal::Instance, PublicInputWireError> {
    ensure_len(raw, SHARE_REVEAL_PUBLIC_INPUT_WIRE_BYTES)?;

    let share_nullifier = fp_slot(
        raw,
        SHARE_REVEAL_WIRE_SLOT_SHARE_NULLIFIER,
        "share_nullifier",
    )?;
    let enc_share_c1_x = fp_slot(
        raw,
        SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C1_X,
        "enc_share_c1_x",
    )?;
    let enc_share_c1_y = fp_slot(
        raw,
        SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C1_Y,
        "enc_share_c1_y",
    )?;
    let enc_share_c2_x = fp_slot(
        raw,
        SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C2_X,
        "enc_share_c2_x",
    )?;
    let enc_share_c2_y = fp_slot(
        raw,
        SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C2_Y,
        "enc_share_c2_y",
    )?;
    let proposal_id = fp_slot(raw, SHARE_REVEAL_WIRE_SLOT_PROPOSAL_ID, "proposal_id")?;
    let vote_decision = fp_slot(raw, SHARE_REVEAL_WIRE_SLOT_VOTE_DECISION, "vote_decision")?;
    let vote_comm_tree_root = fp_slot(
        raw,
        SHARE_REVEAL_WIRE_SLOT_VOTE_COMM_TREE_ROOT,
        "vote_comm_tree_root",
    )?;
    let voting_round_id = fp_slot(
        raw,
        SHARE_REVEAL_WIRE_SLOT_VOTING_ROUND_ID,
        "voting_round_id",
    )?;

    Ok(share_reveal::Instance::from_parts(
        share_nullifier,
        enc_share_c1_x,
        enc_share_c2_x,
        proposal_id,
        vote_decision,
        vote_comm_tree_root,
        voting_round_id,
        enc_share_c1_y,
        enc_share_c2_y,
    ))
}

/// Require an exact wire payload length before slot-level decoding.
///
/// Callers should run this once at the start of each circuit-specific parser.
/// The private slot helpers assume the length has already been checked.
fn ensure_len(raw: &[u8], expected: usize) -> Result<(), PublicInputWireError> {
    if raw.len() != expected {
        return Err(PublicInputWireError::InvalidLength {
            expected,
            actual: raw.len(),
        });
    }

    Ok(())
}

/// Copy a 32-byte slot from a pre-validated verifier wire payload.
///
/// # Panics
///
/// Panics if `raw` is too short for `slot`; circuit-specific parsers must call
/// [`ensure_len`] before using slot helpers.
fn chunk(raw: &[u8], slot: usize) -> [u8; 32] {
    let mut bytes = [0u8; 32];
    bytes.copy_from_slice(&raw[slot * 32..(slot + 1) * 32]);
    bytes
}

/// Decode one canonical Pallas base-field element from a 32-byte wire slot.
fn fp_slot(
    raw: &[u8],
    slot: usize,
    name: &'static str,
) -> Result<pallas::Base, PublicInputWireError> {
    Option::from(pallas::Base::from_repr(chunk(raw, slot)))
        .ok_or(PublicInputWireError::NonCanonicalField { slot, name })
}

/// Decode one compressed Pallas point and return its affine `(x, y)` coordinates.
///
/// The verifier public inputs store curve points as two field elements, while
/// the wire payload may carry them as compressed points.
fn compressed_point_slot(
    raw: &[u8],
    slot: usize,
    name: &'static str,
) -> Result<(pallas::Base, pallas::Base), PublicInputWireError> {
    let point_bytes = chunk(raw, slot);
    let point: pallas::Point =
        Option::from(pallas::Point::from_bytes(&point_bytes)).ok_or_else(|| {
            PublicInputWireError::InvalidCompressedPoint {
                slot,
                name,
                prefix: point_bytes[..4].try_into().expect("slice length is fixed"),
            }
        })?;

    // Halo2 public inputs cannot represent the identity because it has no
    // affine coordinates.
    let affine = point.to_affine();
    let coords: Coordinates<pallas::Affine> = Option::from(affine.coordinates())
        .ok_or(PublicInputWireError::IdentityPoint { slot, name })?;

    Ok((*coords.x(), *coords.y()))
}

/// Decode the low 8 bytes of a 32-byte wire slot as a little-endian `u64`.
fn u64_slot(raw: &[u8], slot: usize) -> u64 {
    let bytes = chunk(raw, slot);
    u64::from_le_bytes(bytes[..8].try_into().expect("slice length is fixed"))
}

/// Decode the low 4 bytes of a 32-byte wire slot as a little-endian `u32`.
fn u32_slot(raw: &[u8], slot: usize) -> u32 {
    let bytes = chunk(raw, slot);
    u32::from_le_bytes(bytes[..4].try_into().expect("slice length is fixed"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use group::Group;

    fn write_fp(raw: &mut [u8], slot: usize, value: pallas::Base) {
        raw[slot * 32..(slot + 1) * 32].copy_from_slice(&value.to_repr());
    }

    fn write_point(raw: &mut [u8], slot: usize, point: pallas::Point) {
        raw[slot * 32..(slot + 1) * 32].copy_from_slice(&point.to_affine().to_bytes());
    }

    fn write_identity_point(raw: &mut [u8], slot: usize) {
        raw[slot * 32..(slot + 1) * 32].copy_from_slice(&pallas::Point::identity().to_bytes());
    }

    #[test]
    fn ensure_len_reports_expected_and_actual_lengths() {
        let err = ensure_len(&[0u8; 7], 32).unwrap_err();

        assert_eq!(
            err,
            PublicInputWireError::InvalidLength {
                expected: 32,
                actual: 7
            }
        );
    }

    #[test]
    fn fp_slot_decodes_canonical_field_element() {
        let value = pallas::Base::from(42);
        let mut raw = vec![0u8; 32];
        raw.copy_from_slice(&value.to_repr());

        assert_eq!(fp_slot(&raw, 0, "field").unwrap(), value);
    }

    #[test]
    fn fp_slot_rejects_noncanonical_field_element() {
        let raw = vec![0xff; 32];

        assert_eq!(
            fp_slot(&raw, 0, "field").unwrap_err(),
            PublicInputWireError::NonCanonicalField {
                slot: 0,
                name: "field"
            }
        );
    }

    #[test]
    fn compressed_point_slot_expands_non_identity_point() {
        let point = pallas::Point::generator() * pallas::Scalar::from(7);
        let affine = point.to_affine();
        let coords = affine.coordinates().unwrap();
        let mut raw = vec![0u8; 32];
        raw.copy_from_slice(&affine.to_bytes());

        assert_eq!(
            compressed_point_slot(&raw, 0, "point").unwrap(),
            (*coords.x(), *coords.y())
        );
    }

    #[test]
    fn compressed_point_slot_rejects_identity_point() {
        let identity = pallas::Point::identity().to_bytes();
        let mut raw = vec![0u8; 32];
        raw.copy_from_slice(&identity);

        assert_eq!(
            compressed_point_slot(&raw, 0, "point").unwrap_err(),
            PublicInputWireError::IdentityPoint {
                slot: 0,
                name: "point"
            }
        );
    }

    #[test]
    fn compressed_point_slot_rejects_invalid_encoding() {
        let raw = vec![0xff; 32];

        assert_eq!(
            compressed_point_slot(&raw, 0, "point").unwrap_err(),
            PublicInputWireError::InvalidCompressedPoint {
                slot: 0,
                name: "point",
                prefix: [0xff, 0xff, 0xff, 0xff]
            }
        );
    }

    #[test]
    fn integer_slots_decode_little_endian_prefixes() {
        let mut raw = vec![0u8; 64];
        raw[..8].copy_from_slice(&42u64.to_le_bytes());
        raw[32..36].copy_from_slice(&7u32.to_le_bytes());

        assert_eq!(u64_slot(&raw, 0), 42);
        assert_eq!(u32_slot(&raw, 1), 7);
    }

    #[test]
    fn delegation_wire_parser_rejects_wrong_length() {
        let err = delegation_instance_from_wire(&[0u8; 31]).unwrap_err();

        assert_eq!(
            err,
            PublicInputWireError::InvalidLength {
                expected: DELEGATION_PUBLIC_INPUT_WIRE_BYTES,
                actual: 31
            }
        );
    }

    #[test]
    fn delegation_wire_parser_rejects_noncanonical_field() {
        let mut raw = vec![0u8; DELEGATION_PUBLIC_INPUT_WIRE_BYTES];
        raw[..32].fill(0xff);

        assert_eq!(
            delegation_instance_from_wire(&raw).unwrap_err(),
            PublicInputWireError::NonCanonicalField {
                slot: DELEGATION_WIRE_SLOT_NF_SIGNED,
                name: "nf_signed"
            }
        );
    }

    #[test]
    fn delegation_wire_parser_rejects_identity_rk() {
        let mut raw = vec![0u8; DELEGATION_PUBLIC_INPUT_WIRE_BYTES];

        for slot in 0..DELEGATION_PUBLIC_INPUT_WIRE_BYTES / 32 {
            write_fp(&mut raw, slot, pallas::Base::from(slot as u64 + 1));
        }
        write_identity_point(&mut raw, DELEGATION_WIRE_SLOT_RK);

        assert_eq!(
            delegation_instance_from_wire(&raw).unwrap_err(),
            PublicInputWireError::IdentityPoint {
                slot: DELEGATION_WIRE_SLOT_RK,
                name: "rk"
            }
        );
    }

    #[test]
    fn delegation_wire_parser_expands_rk_point() {
        let rk_point = pallas::Point::generator() * pallas::Scalar::from(7u64);
        let rk_affine = rk_point.to_affine();
        let rk_coords = rk_affine.coordinates().unwrap();
        let mut raw = vec![0u8; DELEGATION_PUBLIC_INPUT_WIRE_BYTES];

        for slot in 0..DELEGATION_PUBLIC_INPUT_WIRE_BYTES / 32 {
            write_fp(&mut raw, slot, pallas::Base::from(slot as u64 + 1));
        }
        write_point(&mut raw, DELEGATION_WIRE_SLOT_RK, rk_point);

        let instance = delegation_instance_from_wire(&raw).unwrap();

        assert_eq!(
            instance.to_halo2_instance().len(),
            delegation::Instance::NUM_PUBLIC_INPUTS
        );
        assert_eq!(instance.rk_x, *rk_coords.x());
        assert_eq!(instance.rk_y, *rk_coords.y());
    }

    #[test]
    fn delegation_wire_parser_maps_all_slots_to_public_offsets() {
        let rk_point = pallas::Point::generator() * pallas::Scalar::from(13u64);
        let rk_affine = rk_point.to_affine();
        let rk_coords = rk_affine.coordinates().unwrap();
        let nf_signed = pallas::Base::from(101);
        let cmx_new = pallas::Base::from(102);
        let van_comm = pallas::Base::from(103);
        let vote_round_id = pallas::Base::from(104);
        let nc_root = pallas::Base::from(105);
        let nf_imt_root = pallas::Base::from(106);
        let gov_nulls = [
            pallas::Base::from(107),
            pallas::Base::from(108),
            pallas::Base::from(109),
            pallas::Base::from(110),
            pallas::Base::from(111),
        ];
        let mut raw = vec![0u8; DELEGATION_PUBLIC_INPUT_WIRE_BYTES];

        write_fp(&mut raw, DELEGATION_WIRE_SLOT_NF_SIGNED, nf_signed);
        write_point(&mut raw, DELEGATION_WIRE_SLOT_RK, rk_point);
        write_fp(&mut raw, DELEGATION_WIRE_SLOT_CMX_NEW, cmx_new);
        write_fp(&mut raw, DELEGATION_WIRE_SLOT_VAN_COMM, van_comm);
        write_fp(
            &mut raw,
            DELEGATION_WIRE_SLOT_VOTE_ROUND_ID,
            vote_round_id,
        );
        write_fp(&mut raw, DELEGATION_WIRE_SLOT_NC_ROOT, nc_root);
        write_fp(&mut raw, DELEGATION_WIRE_SLOT_NF_IMT_ROOT, nf_imt_root);
        for (slot, value) in DELEGATION_WIRE_SLOT_GOV_NULL.into_iter().zip(gov_nulls) {
            write_fp(&mut raw, slot, value);
        }

        let public_inputs = delegation_instance_from_wire(&raw)
            .unwrap()
            .to_halo2_instance();

        assert_eq!(public_inputs.len(), delegation::Instance::NUM_PUBLIC_INPUTS);
        assert_eq!(public_inputs[delegation::NF_SIGNED_PUBLIC_OFFSET], nf_signed);
        assert_eq!(public_inputs[delegation::RK_X_PUBLIC_OFFSET], *rk_coords.x());
        assert_eq!(public_inputs[delegation::RK_Y_PUBLIC_OFFSET], *rk_coords.y());
        assert_eq!(public_inputs[delegation::CMX_NEW_PUBLIC_OFFSET], cmx_new);
        assert_eq!(public_inputs[delegation::VAN_COMM_PUBLIC_OFFSET], van_comm);
        assert_eq!(
            public_inputs[delegation::VOTE_ROUND_ID_PUBLIC_OFFSET],
            vote_round_id
        );
        assert_eq!(public_inputs[delegation::NC_ROOT_PUBLIC_OFFSET], nc_root);
        assert_eq!(
            public_inputs[delegation::NF_IMT_ROOT_PUBLIC_OFFSET],
            nf_imt_root
        );
        for (offset, value) in delegation::GOV_NULL_PUBLIC_OFFSETS.into_iter().zip(gov_nulls) {
            assert_eq!(public_inputs[offset], value);
        }
    }

    #[test]
    fn vote_wire_parser_maps_all_slots_to_public_offsets() {
        let r_vpk_point = pallas::Point::generator() * pallas::Scalar::from(7u64);
        let ea_pk_point = pallas::Point::generator() * pallas::Scalar::from(11u64);
        let r_vpk_affine = r_vpk_point.to_affine();
        let ea_pk_affine = ea_pk_point.to_affine();
        let r_vpk_coords = r_vpk_affine.coordinates().unwrap();
        let ea_pk_coords = ea_pk_affine.coordinates().unwrap();
        let van_nullifier = pallas::Base::from(201);
        let vote_authority_note_new = pallas::Base::from(202);
        let vote_commitment = pallas::Base::from(203);
        let vote_comm_tree_root = pallas::Base::from(204);
        let vote_comm_tree_anchor_height = 42u64;
        let proposal_id = 7u32;
        let voting_round_id = pallas::Base::from(205);
        let mut raw = vec![0u8; VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES];

        write_fp(&mut raw, VOTE_WIRE_SLOT_VAN_NULLIFIER, van_nullifier);
        write_point(&mut raw, VOTE_WIRE_SLOT_R_VPK, r_vpk_point);
        write_fp(
            &mut raw,
            VOTE_WIRE_SLOT_VOTE_AUTHORITY_NOTE_NEW,
            vote_authority_note_new,
        );
        write_fp(&mut raw, VOTE_WIRE_SLOT_VOTE_COMMITMENT, vote_commitment);
        write_fp(
            &mut raw,
            VOTE_WIRE_SLOT_VOTE_COMM_TREE_ROOT,
            vote_comm_tree_root,
        );
        raw[VOTE_WIRE_SLOT_VOTE_COMM_TREE_ANCHOR_HEIGHT * 32
            ..VOTE_WIRE_SLOT_VOTE_COMM_TREE_ANCHOR_HEIGHT * 32 + 8]
            .copy_from_slice(&vote_comm_tree_anchor_height.to_le_bytes());
        raw[VOTE_WIRE_SLOT_PROPOSAL_ID * 32..VOTE_WIRE_SLOT_PROPOSAL_ID * 32 + 4]
            .copy_from_slice(&proposal_id.to_le_bytes());
        write_fp(&mut raw, VOTE_WIRE_SLOT_VOTING_ROUND_ID, voting_round_id);
        write_point(&mut raw, VOTE_WIRE_SLOT_EA_PK, ea_pk_point);

        let public_inputs = vote_proof_instance_from_wire(&raw).unwrap().to_halo2_instance();

        assert_eq!(public_inputs.len(), vote_proof::Instance::NUM_PUBLIC_INPUTS);
        assert_eq!(
            public_inputs[vote_proof::VAN_NULLIFIER_PUBLIC_OFFSET],
            van_nullifier
        );
        assert_eq!(public_inputs[vote_proof::R_VPK_X_PUBLIC_OFFSET], *r_vpk_coords.x());
        assert_eq!(public_inputs[vote_proof::R_VPK_Y_PUBLIC_OFFSET], *r_vpk_coords.y());
        assert_eq!(
            public_inputs[vote_proof::VOTE_AUTHORITY_NOTE_NEW_PUBLIC_OFFSET],
            vote_authority_note_new
        );
        assert_eq!(
            public_inputs[vote_proof::VOTE_COMMITMENT_PUBLIC_OFFSET],
            vote_commitment
        );
        assert_eq!(
            public_inputs[vote_proof::VOTE_COMM_TREE_ROOT_PUBLIC_OFFSET],
            vote_comm_tree_root
        );
        assert_eq!(
            public_inputs[vote_proof::VOTE_COMM_TREE_ANCHOR_HEIGHT_PUBLIC_OFFSET],
            pallas::Base::from(vote_comm_tree_anchor_height)
        );
        assert_eq!(
            public_inputs[vote_proof::PROPOSAL_ID_PUBLIC_OFFSET],
            pallas::Base::from(u64::from(proposal_id))
        );
        assert_eq!(
            public_inputs[vote_proof::VOTING_ROUND_ID_PUBLIC_OFFSET],
            voting_round_id
        );
        assert_eq!(public_inputs[vote_proof::EA_PK_X_PUBLIC_OFFSET], *ea_pk_coords.x());
        assert_eq!(public_inputs[vote_proof::EA_PK_Y_PUBLIC_OFFSET], *ea_pk_coords.y());
    }

    #[test]
    fn vote_wire_parser_rejects_wrong_length() {
        let err = vote_proof_instance_from_wire(&[0u8; 31]).unwrap_err();

        assert_eq!(
            err,
            PublicInputWireError::InvalidLength {
                expected: VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES,
                actual: 31
            }
        );
    }

    #[test]
    fn vote_wire_parser_rejects_noncanonical_field() {
        let mut raw = vec![0u8; VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES];
        raw[..32].fill(0xff);

        assert_eq!(
            vote_proof_instance_from_wire(&raw).unwrap_err(),
            PublicInputWireError::NonCanonicalField {
                slot: VOTE_WIRE_SLOT_VAN_NULLIFIER,
                name: "van_nullifier"
            }
        );
    }

    #[test]
    fn vote_wire_parser_rejects_identity_r_vpk() {
        let mut raw = vec![0u8; VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES];

        for slot in 0..VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES / 32 {
            write_fp(&mut raw, slot, pallas::Base::from(slot as u64 + 1));
        }
        write_identity_point(&mut raw, VOTE_WIRE_SLOT_R_VPK);

        assert_eq!(
            vote_proof_instance_from_wire(&raw).unwrap_err(),
            PublicInputWireError::IdentityPoint {
                slot: VOTE_WIRE_SLOT_R_VPK,
                name: "r_vpk"
            }
        );
    }

    #[test]
    fn vote_wire_parser_rejects_invalid_compressed_ea_pk() {
        let r_vpk_point = pallas::Point::generator() * pallas::Scalar::from(7u64);
        let mut raw = vec![0u8; VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES];

        for slot in 0..VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES / 32 {
            write_fp(&mut raw, slot, pallas::Base::from(slot as u64 + 1));
        }
        write_point(&mut raw, VOTE_WIRE_SLOT_R_VPK, r_vpk_point);
        raw[VOTE_WIRE_SLOT_EA_PK * 32..(VOTE_WIRE_SLOT_EA_PK + 1) * 32].fill(0xff);

        assert_eq!(
            vote_proof_instance_from_wire(&raw).unwrap_err(),
            PublicInputWireError::InvalidCompressedPoint {
                slot: VOTE_WIRE_SLOT_EA_PK,
                name: "ea_pk",
                prefix: [0xff, 0xff, 0xff, 0xff]
            }
        );
    }

    #[test]
    fn vote_wire_parser_rejects_identity_ea_pk() {
        let r_vpk_point = pallas::Point::generator() * pallas::Scalar::from(7u64);
        let mut raw = vec![0u8; VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES];

        for slot in 0..VOTE_PROOF_PUBLIC_INPUT_WIRE_BYTES / 32 {
            write_fp(&mut raw, slot, pallas::Base::from(slot as u64 + 1));
        }
        write_point(&mut raw, VOTE_WIRE_SLOT_R_VPK, r_vpk_point);
        write_identity_point(&mut raw, VOTE_WIRE_SLOT_EA_PK);

        assert_eq!(
            vote_proof_instance_from_wire(&raw).unwrap_err(),
            PublicInputWireError::IdentityPoint {
                slot: VOTE_WIRE_SLOT_EA_PK,
                name: "ea_pk"
            }
        );
    }

    #[test]
    fn share_reveal_wire_parser_rejects_wrong_length() {
        let err = share_reveal_instance_from_wire(&[0u8; 31]).unwrap_err();

        assert_eq!(
            err,
            PublicInputWireError::InvalidLength {
                expected: SHARE_REVEAL_PUBLIC_INPUT_WIRE_BYTES,
                actual: 31
            }
        );
    }

    #[test]
    fn share_reveal_wire_parser_rejects_noncanonical_field() {
        let mut raw = vec![0u8; SHARE_REVEAL_PUBLIC_INPUT_WIRE_BYTES];
        raw[..32].fill(0xff);

        assert_eq!(
            share_reveal_instance_from_wire(&raw).unwrap_err(),
            PublicInputWireError::NonCanonicalField {
                slot: SHARE_REVEAL_WIRE_SLOT_SHARE_NULLIFIER,
                name: "share_nullifier"
            }
        );
    }

    #[test]
    fn share_reveal_wire_parser_maps_all_slots_to_public_offsets() {
        let values = [
            pallas::Base::from(301),
            pallas::Base::from(302),
            pallas::Base::from(303),
            pallas::Base::from(304),
            pallas::Base::from(305),
            pallas::Base::from(306),
            pallas::Base::from(307),
            pallas::Base::from(308),
            pallas::Base::from(309),
        ];
        let mut raw = vec![0u8; SHARE_REVEAL_PUBLIC_INPUT_WIRE_BYTES];

        for (slot, value) in values.into_iter().enumerate() {
            write_fp(&mut raw, slot, value);
        }

        let public_inputs = share_reveal_instance_from_wire(&raw)
            .unwrap()
            .to_halo2_instance();

        assert_eq!(public_inputs.len(), share_reveal::Instance::NUM_PUBLIC_INPUTS);
        assert_eq!(
            public_inputs[share_reveal::SHARE_NULLIFIER_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_SHARE_NULLIFIER]
        );
        assert_eq!(
            public_inputs[share_reveal::ENC_SHARE_C1_X_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C1_X]
        );
        assert_eq!(
            public_inputs[share_reveal::ENC_SHARE_C1_Y_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C1_Y]
        );
        assert_eq!(
            public_inputs[share_reveal::ENC_SHARE_C2_X_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C2_X]
        );
        assert_eq!(
            public_inputs[share_reveal::ENC_SHARE_C2_Y_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_ENC_SHARE_C2_Y]
        );
        assert_eq!(
            public_inputs[share_reveal::PROPOSAL_ID_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_PROPOSAL_ID]
        );
        assert_eq!(
            public_inputs[share_reveal::VOTE_DECISION_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_VOTE_DECISION]
        );
        assert_eq!(
            public_inputs[share_reveal::VOTE_COMM_TREE_ROOT_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_VOTE_COMM_TREE_ROOT]
        );
        assert_eq!(
            public_inputs[share_reveal::VOTING_ROUND_ID_PUBLIC_OFFSET],
            values[SHARE_REVEAL_WIRE_SLOT_VOTING_ROUND_ID]
        );
    }
}
