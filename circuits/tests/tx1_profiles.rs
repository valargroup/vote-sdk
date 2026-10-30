//! Cross-implementation signing vectors for the two delegation profiles.

use base64::{engine::general_purpose::STANDARD as BASE64, Engine};
use serde::Deserialize;
use shielded_vote_circuits::{redpallas::verify_spend_auth_sig, tx1};

#[derive(Deserialize)]
struct Fixture {
    tx1_effects: String,
    sighash: String,
    rk: String,
    spend_auth_sig: String,
}

fn decode<const N: usize>(encoded: &str) -> [u8; N] {
    BASE64.decode(encoded).unwrap().try_into().unwrap()
}

#[test]
fn both_profiles_match_independent_vectors_and_verify_signatures() {
    // Version 1 is the existing wallet vector. Version 2 was generated with
    // Common 4.0.0's TransactionDigest and to_txid, whose NU7 ID is 77190ad9.
    for (version, json) in [
        (
            tx1::EFFECTS_VERSION,
            include_str!("../../testutil/testdata/delegation_tx1_effects_v1.json"),
        ),
        (
            tx1::NU7_EFFECTS_VERSION,
            include_str!("../../testutil/testdata/delegation_tx1_effects_v2.json"),
        ),
    ] {
        let fixture: Fixture = serde_json::from_str(json).unwrap();
        let mut effects = BASE64.decode(&fixture.tx1_effects).unwrap();
        assert_eq!(effects[0], version);
        let digest = tx1::sighash(&effects).unwrap();
        assert_eq!(digest, decode::<32>(&fixture.sighash));
        let rk = decode::<32>(&fixture.rk);
        let signature = decode::<64>(&fixture.spend_auth_sig);
        verify_spend_auth_sig(&rk, &digest, &signature).unwrap();

        // A version change cannot transfer authorization to another profile.
        effects[0] = if version == tx1::EFFECTS_VERSION {
            tx1::NU7_EFFECTS_VERSION
        } else {
            tx1::EFFECTS_VERSION
        };
        let other_digest = tx1::sighash(&effects).unwrap();
        assert_ne!(digest, other_digest);
        assert!(verify_spend_auth_sig(&rk, &other_digest, &signature).is_err());

        effects[0] = version;
        effects[161] ^= 1; // first encrypted note byte
        let modified_digest = tx1::sighash(&effects).unwrap();
        assert!(verify_spend_auth_sig(&rk, &modified_digest, &signature).is_err());
    }
}

#[test]
fn both_profiles_reject_truncation_invalid_points_and_unknown_versions() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../testutil/testdata/delegation_tx1_effects_v2.json"
    ))
    .unwrap();
    let effects = BASE64.decode(fixture.tx1_effects).unwrap();
    for version in [tx1::EFFECTS_VERSION, tx1::NU7_EFFECTS_VERSION] {
        let mut invalid = effects.clone();
        invalid[0] = version;
        assert!(matches!(
            tx1::sighash(&invalid[..820]),
            Err(tx1::Error::InvalidLength { .. })
        ));
        invalid[65..97].fill(0);
        assert!(matches!(
            tx1::sighash(&invalid),
            Err(tx1::Error::InvalidAction { .. })
        ));
    }
    for version in [0, 3, 255] {
        let mut invalid = effects.clone();
        invalid[0] = version;
        assert!(matches!(
            tx1::sighash(&invalid),
            Err(tx1::Error::UnsupportedVersion { .. })
        ));
    }
}
