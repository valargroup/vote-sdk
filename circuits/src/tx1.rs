//! Canonical signature digest for the synthetic Ironwood delegation transaction.
//!
//! The chain receives only versioned transaction effecting data. It does not
//! receive or reconstruct a PCZT. Version 1 fixes the rest of the transaction
//! profile to V6/NU6.3 with no transparent, Sapling, or Orchard bundles and one
//! two-action Ironwood bundle.

use std::{fmt, io::Cursor};

use nonempty::NonEmpty;
use orchard::{
    bundle::{Bundle, BundleVersion, EffectsOnly, Flags, TxVersion as OrchardTxVersion},
    Anchor,
};
use zcash_primitives::transaction::{
    self,
    components::orchard::read_action_without_auth,
    txid::{to_txid, TxIdDigester},
    TransactionDigest, TxDigests, TxVersion,
};
use zcash_protocol::{
    consensus::{BlockHeight, BranchId},
    value::ZatBalance,
};

/// Version byte for the current delegation TX1 effects encoding.
pub const EFFECTS_VERSION: u8 = 1;
/// Number of Ironwood actions in the fixed delegation transaction.
pub const ACTION_COUNT: usize = 2;
/// Length of one Ironwood action's effecting data.
pub const ACTION_EFFECTS_LEN: usize = 820;
/// Length of the complete versioned delegation TX1 effects payload.
pub const EFFECTS_LEN: usize = 1 + ACTION_COUNT * ACTION_EFFECTS_LEN;

const BUNDLE_FLAGS: u8 = 0x07;
const VALUE_BALANCE_ZAT: i64 = 1;

/// An invalid delegation TX1 effects payload.
#[derive(Debug)]
pub enum Error {
    InvalidLength { actual: usize },
    UnsupportedVersion { actual: u8 },
    InvalidAction { index: usize, message: String },
    InvalidBundle(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::InvalidLength { actual } => {
                write!(f, "tx1 effects must be {EFFECTS_LEN} bytes, got {actual}")
            }
            Error::UnsupportedVersion { actual } => write!(
                f,
                "unsupported tx1 effects version: expected {EFFECTS_VERSION}, got {actual}"
            ),
            Error::InvalidAction { index, message } => {
                write!(f, "invalid Ironwood action {index}: {message}")
            }
            Error::InvalidBundle(message) => write!(f, "invalid Ironwood bundle: {message}"),
        }
    }
}

impl std::error::Error for Error {}

/// Computes the ZIP-244/ZIP-229 shielded signature digest for delegation TX1.
///
/// For a transaction without transparent inputs, the shielded signature digest
/// is identical to the transaction-ID digest. The V6 digest excludes shielded
/// anchors, proofs, and signatures, so the versioned effecting data is complete.
pub fn sighash(effects: &[u8]) -> Result<[u8; 32], Error> {
    if effects.len() != EFFECTS_LEN {
        return Err(Error::InvalidLength {
            actual: effects.len(),
        });
    }
    if effects[0] != EFFECTS_VERSION {
        return Err(Error::UnsupportedVersion { actual: effects[0] });
    }

    let actions = (0..ACTION_COUNT)
        .map(|index| {
            let start = 1 + index * ACTION_EFFECTS_LEN;
            let end = start + ACTION_EFFECTS_LEN;
            read_action_without_auth(Cursor::new(&effects[start..end])).map_err(|err| {
                Error::InvalidAction {
                    index,
                    message: err.to_string(),
                }
            })
        })
        .collect::<Result<Vec<_>, _>>()?;

    let bundle_version = BundleVersion::ironwood_v3();
    let flags = Flags::from_byte(BUNDLE_FLAGS, bundle_version)
        .expect("the fixed Ironwood V3 flag byte is valid");
    let anchor = Option::from(Anchor::from_bytes([0; 32]))
        .expect("zero is a canonical Pallas base field element");
    let effects_bundle = Bundle::from_parts(
        NonEmpty::from_vec(actions).expect("the fixed action count is nonzero"),
        flags,
        ZatBalance::from_i64(VALUE_BALANCE_ZAT)
            .expect("the fixed Ironwood value balance is in range"),
        anchor,
        EffectsOnly,
        bundle_version,
    )
    .map_err(|err| Error::InvalidBundle(err.to_string()))?;

    let digester = TxIdDigester;
    let header_digest = <TxIdDigester as TransactionDigest<transaction::Authorized>>::digest_header(
        &digester,
        TxVersion::V6,
        BranchId::Nu6_3,
        0,
        BlockHeight::from_u32(0),
    );
    let ironwood_digest = effects_bundle
        .commitment(OrchardTxVersion::V6)
        .map_err(|err| Error::InvalidBundle(err.to_string()))?
        .0;
    let digests = TxDigests {
        header_digest,
        transparent_digests: None,
        sapling_digest: None,
        orchard_digest: None,
        ironwood_digest: Some(ironwood_digest),
    };

    Ok(to_txid(TxVersion::V6, BranchId::Nu6_3, &digests).into())
}

#[cfg(test)]
mod tests {
    use base64::{engine::general_purpose::STANDARD as BASE64_STANDARD, Engine as _};
    use serde::Deserialize;

    use super::*;

    #[derive(Deserialize)]
    struct Fixture {
        transaction_version: u32,
        consensus_branch_id: String,
        lock_time: u32,
        expiry_height: u32,
        bundle_version: u32,
        bundle_flags: u8,
        value_balance_zat: i64,
        action_count: usize,
        tx1_effects: String,
        sighash: String,
    }

    fn fixture() -> Fixture {
        serde_json::from_str(include_str!(
            "../../testutil/testdata/delegation_tx1_effects_v1.json"
        ))
        .expect("fixture is valid JSON")
    }

    #[test]
    fn matches_the_wallet_fixture() {
        let fixture = fixture();
        assert_eq!(fixture.transaction_version, 6);
        assert_eq!(fixture.consensus_branch_id, "37a5165b");
        assert_eq!(fixture.lock_time, 0);
        assert_eq!(fixture.expiry_height, 0);
        assert_eq!(fixture.bundle_version, 3);
        assert_eq!(fixture.bundle_flags, BUNDLE_FLAGS);
        assert_eq!(fixture.value_balance_zat, VALUE_BALANCE_ZAT);
        assert_eq!(fixture.action_count, ACTION_COUNT);

        let effects = BASE64_STANDARD
            .decode(fixture.tx1_effects)
            .expect("fixture effects are base64");
        let expected: [u8; 32] = BASE64_STANDARD
            .decode(fixture.sighash)
            .expect("fixture sighash is base64")
            .try_into()
            .expect("fixture sighash is 32 bytes");

        assert_eq!(sighash(&effects).unwrap(), expected);
    }

    #[test]
    fn rejects_invalid_framing_and_action_encodings() {
        let effects = BASE64_STANDARD.decode(fixture().tx1_effects).unwrap();

        assert!(matches!(
            sighash(&effects[..effects.len() - 1]),
            Err(Error::InvalidLength { .. })
        ));

        let mut unsupported = effects.clone();
        unsupported[0] = EFFECTS_VERSION + 1;
        assert!(matches!(
            sighash(&unsupported),
            Err(Error::UnsupportedVersion { .. })
        ));

        let mut invalid_rk = effects;
        invalid_rk[1 + 64..1 + 96].fill(0);
        assert!(matches!(
            sighash(&invalid_rk),
            Err(Error::InvalidAction { index: 0, .. })
        ));
    }
}
