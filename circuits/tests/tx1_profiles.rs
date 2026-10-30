//! Frozen NU7 authorization vector and rejection of the retired NU6.3 profile.

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

fn fixture(json: &str) -> Fixture {
    serde_json::from_str(json).unwrap()
}

#[test]
fn nu7_matches_the_frozen_vector_and_authenticates_action_bytes() {
    let fixture = fixture(include_str!(
        "../../testutil/testdata/delegation_tx1_effects_v2.json"
    ));
    let mut effects = BASE64.decode(&fixture.tx1_effects).unwrap();
    assert_eq!(effects[0], tx1::EFFECTS_VERSION);
    let digest = tx1::sighash(&effects).unwrap();
    assert_eq!(digest, decode::<32>(&fixture.sighash));
    let rk = decode::<32>(&fixture.rk);
    let signature = decode::<64>(&fixture.spend_auth_sig);
    verify_spend_auth_sig(&rk, &digest, &signature).unwrap();

    effects[161] ^= 1;
    let modified_digest = tx1::sighash(&effects).unwrap();
    assert!(verify_spend_auth_sig(&rk, &modified_digest, &signature).is_err());
}

#[test]
fn legacy_authorization_is_rejected_and_cannot_be_relabelled() {
    let fixture = fixture(include_str!(
        "../../testutil/testdata/delegation_tx1_effects_v1.json"
    ));
    let mut effects = BASE64.decode(&fixture.tx1_effects).unwrap();
    assert!(matches!(
        tx1::sighash(&effects),
        Err(tx1::Error::UnsupportedVersion { actual: 1 })
    ));

    effects[0] = tx1::EFFECTS_VERSION;
    let digest = tx1::sighash(&effects).unwrap();
    assert_ne!(digest, decode::<32>(&fixture.sighash));
    assert!(verify_spend_auth_sig(
        &decode(&fixture.rk),
        &digest,
        &decode(&fixture.spend_auth_sig)
    )
    .is_err());
}

#[test]
fn rejects_truncation_invalid_points_and_unsupported_versions() {
    let fixture = fixture(include_str!(
        "../../testutil/testdata/delegation_tx1_effects_v2.json"
    ));
    let effects = BASE64.decode(fixture.tx1_effects).unwrap();
    assert!(matches!(
        tx1::sighash(&effects[..820]),
        Err(tx1::Error::InvalidLength { .. })
    ));
    let mut invalid = effects.clone();
    invalid[65..97].fill(0);
    assert!(matches!(
        tx1::sighash(&invalid),
        Err(tx1::Error::InvalidAction { .. })
    ));
    for version in [0, 1, 3, 255] {
        let mut invalid = effects.clone();
        invalid[0] = version;
        assert!(matches!(
            tx1::sighash(&invalid),
            Err(tx1::Error::UnsupportedVersion { .. })
        ));
    }
}
