//! Orchard fixed bases.

#[cfg(feature = "circuit")]
use alloc::vec::Vec;

use super::{L_ORCHARD_SCALAR, L_VALUE};

#[cfg(feature = "circuit")]
use halo2_gadgets::ecc::{
    chip::{BaseFieldElem, FixedPoint, FullScalar, ShortScalar},
    FixedPoints,
};

#[cfg(feature = "circuit")]
use pasta_curves::pallas;

pub mod commit_ivk_r;
pub mod note_commit_r;
pub mod nullifier_k;
pub mod spend_auth_g;
pub mod value_commit_r;
pub mod value_commit_v;

/// SWU hash-to-curve personalization for the spending key base point and
/// the nullifier base point K^Orchard
pub const ORCHARD_PERSONALIZATION: &str = "z.cash:Orchard";

/// SWU hash-to-curve personalization for the value commitment generator
pub const VALUE_COMMITMENT_PERSONALIZATION: &str = "z.cash:Orchard-cv";

/// SWU hash-to-curve value for the value commitment generator
pub const VALUE_COMMITMENT_V_BYTES: [u8; 1] = *b"v";

/// SWU hash-to-curve value for the value commitment generator
pub const VALUE_COMMITMENT_R_BYTES: [u8; 1] = *b"r";

/// SWU hash-to-curve personalization for the note commitment generator
pub const NOTE_COMMITMENT_PERSONALIZATION: &str = "z.cash:Orchard-NoteCommit";

/// SWU hash-to-curve personalization for the IVK commitment generator
pub const COMMIT_IVK_PERSONALIZATION: &str = "z.cash:Orchard-CommitIvk";

/// Window size for fixed-base scalar multiplication
pub const FIXED_BASE_WINDOW_SIZE: usize = 3;

/// $2^{`FIXED_BASE_WINDOW_SIZE`}$
pub const H: usize = 1 << FIXED_BASE_WINDOW_SIZE;

/// Number of windows for a full-width scalar
pub const NUM_WINDOWS: usize =
    (L_ORCHARD_SCALAR + FIXED_BASE_WINDOW_SIZE - 1) / FIXED_BASE_WINDOW_SIZE;

/// Number of windows for a short signed scalar
pub const NUM_WINDOWS_SHORT: usize =
    (L_VALUE + FIXED_BASE_WINDOW_SIZE - 1) / FIXED_BASE_WINDOW_SIZE;

/// Fixed bases used in scalar mul where the scalar is a base field element.
///
/// The ECC chip's `FixedPoints::Base` associated type must be a single type,
/// so both `NullifierK` and `SpendAuthGBase` are wrapped in this enum.
/// `SpendAuthGBase` reuses the same generator and U/Z tables as
/// `OrchardFixedBasesFull::SpendAuthG` (same 85-window structure over the
/// 255-bit pallas base field), allowing `FixedPointBaseField::mul` to accept
/// an `AssignedCell` directly — no variable-base NonIdentityPoint witness needed.
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
pub enum OrchardBaseFieldBases {
    NullifierK,
    SpendAuthGBase,
}

/// Fixed bases used in scalar mul where the scalar is a short (64-bit) signed value.
///
/// `FixedPoints::ShortScalar` must be a single type, so both `ValueCommitV`
/// and `SpendAuthGShort` are wrapped in this enum. `SpendAuthGShort` uses the
/// same generator as `SpendAuthG` but with 22-window precomputed tables, enabling
/// `FixedPointShort::mul` for vote-share values bounded to 30 bits by condition 9.
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
pub enum OrchardShortScalarBases {
    /// Value commitment generator for Orchard (original short base).
    ValueCommitV,
    /// SpendAuthG with short (22-window) tables, used for [v_i]*G in ElGamal
    /// encryption where v_i is range-checked to 30 bits by condition 9.
    SpendAuthGShort,
}

#[derive(Copy, Clone, Debug, Eq, PartialEq)]
// A sum type for both full-width and short bases. This enables us to use the
// shared functionality of full-width and short fixed-base scalar multiplication.
pub enum OrchardFixedBases {
    Full(OrchardFixedBasesFull),
    Base(OrchardBaseFieldBases),
    Short(OrchardShortScalarBases),
}

impl From<OrchardFixedBasesFull> for OrchardFixedBases {
    fn from(full_width_base: OrchardFixedBasesFull) -> Self {
        Self::Full(full_width_base)
    }
}

impl From<ValueCommitV> for OrchardFixedBases {
    fn from(_value_commit_v: ValueCommitV) -> Self {
        Self::Short(OrchardShortScalarBases::ValueCommitV)
    }
}

impl From<NullifierK> for OrchardFixedBases {
    fn from(_nullifier_k: NullifierK) -> Self {
        Self::Base(OrchardBaseFieldBases::NullifierK)
    }
}

impl From<OrchardBaseFieldBases> for OrchardFixedBases {
    fn from(b: OrchardBaseFieldBases) -> Self {
        Self::Base(b)
    }
}

impl From<OrchardShortScalarBases> for OrchardFixedBases {
    fn from(b: OrchardShortScalarBases) -> Self {
        Self::Short(b)
    }
}

/// The Orchard fixed bases used in scalar mul with full-width scalars.
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
pub enum OrchardFixedBasesFull {
    CommitIvkR,
    NoteCommitR,
    ValueCommitR,
    SpendAuthG,
}

/// NullifierK is used in scalar mul with a base field element.
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
pub struct NullifierK;

/// ValueCommitV is used in scalar mul with a short signed scalar.
#[derive(Copy, Clone, Debug, Eq, PartialEq)]
pub struct ValueCommitV;

#[cfg(feature = "circuit")]
impl FixedPoints<pallas::Affine> for OrchardFixedBases {
    type FullScalar = OrchardFixedBasesFull;
    type Base = OrchardBaseFieldBases;
    type ShortScalar = OrchardShortScalarBases;
}

#[cfg(feature = "circuit")]
impl FixedPoint<pallas::Affine> for OrchardFixedBasesFull {
    type FixedScalarKind = FullScalar;

    fn generator(&self) -> pallas::Affine {
        match self {
            Self::CommitIvkR => commit_ivk_r::generator(),
            Self::NoteCommitR => note_commit_r::generator(),
            Self::ValueCommitR => value_commit_r::generator(),
            Self::SpendAuthG => spend_auth_g::generator(),
        }
    }

    fn u(&self) -> Vec<[[u8; 32]; H]> {
        match self {
            Self::CommitIvkR => commit_ivk_r::U.to_vec(),
            Self::NoteCommitR => note_commit_r::U.to_vec(),
            Self::ValueCommitR => value_commit_r::U.to_vec(),
            Self::SpendAuthG => spend_auth_g::U.to_vec(),
        }
    }

    fn z(&self) -> Vec<u64> {
        match self {
            Self::CommitIvkR => commit_ivk_r::Z.to_vec(),
            Self::NoteCommitR => note_commit_r::Z.to_vec(),
            Self::ValueCommitR => value_commit_r::Z.to_vec(),
            Self::SpendAuthG => spend_auth_g::Z.to_vec(),
        }
    }
}

#[cfg(feature = "circuit")]
impl FixedPoint<pallas::Affine> for NullifierK {
    type FixedScalarKind = BaseFieldElem;

    fn generator(&self) -> pallas::Affine {
        nullifier_k::generator()
    }

    fn u(&self) -> Vec<[[u8; 32]; H]> {
        nullifier_k::U.to_vec()
    }

    fn z(&self) -> Vec<u64> {
        nullifier_k::Z.to_vec()
    }
}

#[cfg(feature = "circuit")]
impl FixedPoint<pallas::Affine> for OrchardBaseFieldBases {
    type FixedScalarKind = BaseFieldElem;

    fn generator(&self) -> pallas::Affine {
        match self {
            Self::NullifierK => nullifier_k::generator(),
            Self::SpendAuthGBase => spend_auth_g::generator(),
        }
    }

    fn u(&self) -> Vec<[[u8; 32]; H]> {
        match self {
            Self::NullifierK => nullifier_k::U.to_vec(),
            // SpendAuthG's full-scalar U/Z tables have the same 85-window
            // structure as the base-field-element variant (pallas::Base and
            // pallas::Scalar are both 255-bit); the precomputed values depend
            // only on the generator and window layout, not the scalar kind.
            Self::SpendAuthGBase => spend_auth_g::U.to_vec(),
        }
    }

    fn z(&self) -> Vec<u64> {
        match self {
            Self::NullifierK => nullifier_k::Z.to_vec(),
            Self::SpendAuthGBase => spend_auth_g::Z.to_vec(),
        }
    }
}

#[cfg(feature = "circuit")]
impl FixedPoint<pallas::Affine> for ValueCommitV {
    type FixedScalarKind = ShortScalar;

    fn generator(&self) -> pallas::Affine {
        value_commit_v::generator()
    }

    fn u(&self) -> Vec<[[u8; 32]; H]> {
        value_commit_v::U_SHORT.to_vec()
    }

    fn z(&self) -> Vec<u64> {
        value_commit_v::Z_SHORT.to_vec()
    }
}

#[cfg(feature = "circuit")]
impl FixedPoint<pallas::Affine> for OrchardShortScalarBases {
    type FixedScalarKind = ShortScalar;

    fn generator(&self) -> pallas::Affine {
        match self {
            Self::ValueCommitV => value_commit_v::generator(),
            Self::SpendAuthGShort => spend_auth_g::generator(),
        }
    }

    fn u(&self) -> Vec<[[u8; 32]; H]> {
        match self {
            Self::ValueCommitV => value_commit_v::U_SHORT.to_vec(),
            Self::SpendAuthGShort => spend_auth_g::U_SHORT.to_vec(),
        }
    }

    fn z(&self) -> Vec<u64> {
        match self {
            Self::ValueCommitV => value_commit_v::Z_SHORT.to_vec(),
            Self::SpendAuthGShort => spend_auth_g::Z_SHORT.to_vec(),
        }
    }
}

#[cfg(all(test, feature = "circuit"))]
mod tests {
    use super::*;

    /// Ensures that `OrchardBaseFieldBases::SpendAuthGBase` routes to the
    /// correct generator and tables via the `FixedPoint` trait.  The U/Z data
    /// is identical to `OrchardFixedBasesFull::SpendAuthG` (same generator,
    /// same 85-window structure); this test makes the dispatch wiring explicit.
    #[test]
    fn spend_auth_g_base_field_routes_correctly() {

        let full = OrchardFixedBasesFull::SpendAuthG;
        let base = OrchardBaseFieldBases::SpendAuthGBase;

        assert_eq!(
            full.generator(),
            base.generator(),
            "SpendAuthGBase must share the SpendAuthG generator"
        );
        assert_eq!(
            full.u(),
            base.u(),
            "SpendAuthGBase U tables must match SpendAuthG full-scalar U tables"
        );
        assert_eq!(
            full.z(),
            base.z(),
            "SpendAuthGBase Z tables must match SpendAuthG full-scalar Z tables"
        );
    }

    /// Ensures that `OrchardBaseFieldBases::NullifierK` still routes to the
    /// NullifierK generator and tables (regression guard for the enum refactor).
    #[test]
    fn nullifier_k_base_field_routes_correctly() {

        let base = OrchardBaseFieldBases::NullifierK;

        assert_eq!(
            base.generator(),
            nullifier_k::generator(),
            "OrchardBaseFieldBases::NullifierK must use the NullifierK generator"
        );
        assert_eq!(
            base.u(),
            nullifier_k::U.to_vec(),
            "OrchardBaseFieldBases::NullifierK U tables must match"
        );
        assert_eq!(
            base.z(),
            nullifier_k::Z.to_vec(),
            "OrchardBaseFieldBases::NullifierK Z tables must match"
        );
    }

    /// Ensures that `OrchardShortScalarBases::SpendAuthGShort` routes to the
    /// SpendAuthG generator and the 22-window short tables.
    #[test]
    fn spend_auth_g_short_routes_correctly() {

        let short = OrchardShortScalarBases::SpendAuthGShort;
        let full = OrchardFixedBasesFull::SpendAuthG;

        assert_eq!(
            short.generator(),
            full.generator(),
            "SpendAuthGShort must share the SpendAuthG generator"
        );
        assert_eq!(
            short.u().len(),
            NUM_WINDOWS_SHORT,
            "SpendAuthGShort U table must have NUM_WINDOWS_SHORT entries"
        );
        assert_eq!(
            short.z().len(),
            NUM_WINDOWS_SHORT,
            "SpendAuthGShort Z table must have NUM_WINDOWS_SHORT entries"
        );
        // Short tables must differ from the full 85-window tables.
        assert_ne!(
            short.u(),
            full.u()[..NUM_WINDOWS_SHORT].to_vec(),
            "SpendAuthGShort U table must use the short-scalar window structure"
        );
    }

    /// Ensures that `OrchardShortScalarBases::ValueCommitV` still routes to
    /// the ValueCommitV generator and tables (regression guard for the enum change).
    #[test]
    fn value_commit_v_short_routes_correctly() {

        let short = OrchardShortScalarBases::ValueCommitV;
        let legacy = ValueCommitV;

        assert_eq!(short.generator(), legacy.generator());
        assert_eq!(short.u(), legacy.u());
        assert_eq!(short.z(), legacy.z());
    }
}
