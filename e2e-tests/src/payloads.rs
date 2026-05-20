//! JSON payload builders and round_id derivation for Shielded-Vote REST API.
//!
//! Matches the chain's deriveRoundID: Poseidon hash of 8 Fp elements
//! derived from (snapshot_height, snapshot_blockhash, proposals_hash,
//! vote_end_time, nullifier_imt_root, nc_root).

use crate::elgamal::{self, Ciphertext};
use serde_json::{json, Value};
use std::sync::atomic::{AtomicU64, Ordering};

/// 32-byte arrays for session fields.
pub type Bytes32 = [u8; 32];

/// Session fields used to derive vote_round_id (and to create the session).
#[derive(Clone, Debug)]
pub struct SetupRoundFields {
    pub snapshot_height: u64,
    pub snapshot_blockhash: Bytes32,
    pub proposals_hash: Bytes32,
    pub vote_end_time: u64,
    pub nullifier_imt_root: Bytes32,
    pub nc_root: Bytes32,
}

static ROUND_COUNTER: AtomicU64 = AtomicU64::new(0);

fn round_counter_next() -> u64 {
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs();
    (now % 1_000_000) + ROUND_COUNTER.fetch_add(1, Ordering::Relaxed)
}

/// Derive vote_round_id via Poseidon hash of 8 Fp elements.
///
/// Same encoding as the chain's `derive_round_id_poseidon` in `sdk/circuits/src/ffi.rs`:
///   creation_height → Fp::from(u64)
///   snapshot_blockhash → 2 Fp (lo/hi u128 limbs)
///   proposals_hash → 2 Fp (lo/hi u128 limbs)
///   vote_end_time → Fp::from(u64)
///   nullifier_imt_root → Fp::from_repr() (canonical)
///   nc_root → Fp::from_repr() (canonical)
pub fn derive_round_id_at_height(fields: &SetupRoundFields, creation_height: u64) -> [u8; 32] {
    use ff::PrimeField;
    use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength, P128Pow5T3};
    use pasta_curves::pallas;

    let split = |bytes: &[u8; 32]| -> (pallas::Base, pallas::Base) {
        let lo = u128::from_le_bytes(bytes[..16].try_into().unwrap());
        let hi = u128::from_le_bytes(bytes[16..32].try_into().unwrap());
        (pallas::Base::from_u128(lo), pallas::Base::from_u128(hi))
    };

    let (bh_lo, bh_hi) = split(&fields.snapshot_blockhash);
    let (ph_lo, ph_hi) = split(&fields.proposals_hash);
    let nf_root: pallas::Base = pallas::Base::from_repr(fields.nullifier_imt_root)
        .expect("nullifier_imt_root not canonical Fp");
    let nc: pallas::Base =
        pallas::Base::from_repr(fields.nc_root).expect("nc_root not canonical Fp");

    let inputs = [
        pallas::Base::from(creation_height),
        bh_lo,
        bh_hi,
        ph_lo,
        ph_hi,
        pallas::Base::from(fields.vote_end_time),
        nf_root,
        nc,
    ];

    let hash = poseidon::Hash::<_, P128Pow5T3, ConstantLength<8>, 3, 2>::init().hash(inputs);
    hash.to_repr()
}

/// Legacy test helper for fixtures that intentionally use `snapshot_height` as
/// the creation-height input. Live e2e tests should read the emitted round_id
/// from the create transaction instead of precomputing it.
pub fn derive_round_id(fields: &SetupRoundFields) -> [u8; 32] {
    derive_round_id_at_height(fields, fields.snapshot_height)
}

fn to_base64(bytes: &[u8]) -> String {
    base64::Engine::encode(&base64::engine::general_purpose::STANDARD, bytes)
}

fn from_base64(field: &str, value: &str) -> Vec<u8> {
    base64::Engine::decode(&base64::engine::general_purpose::STANDARD, value)
        .unwrap_or_else(|err| panic!("{} must be base64 encoded: {}", field, err))
}

fn push_varint(out: &mut Vec<u8>, mut value: u64) {
    while value >= 0x80 {
        out.push((value as u8) | 0x80);
        value >>= 7;
    }
    out.push(value as u8);
}

fn push_key(out: &mut Vec<u8>, field_number: u32, wire_type: u8) {
    push_varint(out, ((field_number as u64) << 3) | wire_type as u64);
}

fn push_uint64(out: &mut Vec<u8>, field_number: u32, value: u64) {
    if value == 0 {
        return;
    }
    push_key(out, field_number, 0);
    push_varint(out, value);
}

fn push_bytes(out: &mut Vec<u8>, field_number: u32, value: &[u8]) {
    if value.is_empty() {
        return;
    }
    push_key(out, field_number, 2);
    push_varint(out, value.len() as u64);
    out.extend_from_slice(value);
}

fn push_string(out: &mut Vec<u8>, field_number: u32, value: &str) {
    push_bytes(out, field_number, value.as_bytes());
}

fn required_str<'a>(value: &'a Value, field: &str) -> &'a str {
    value
        .get(field)
        .and_then(Value::as_str)
        .unwrap_or_else(|| panic!("{} must be a string", field))
}

fn required_u64(value: &Value, field: &str) -> u64 {
    value
        .get(field)
        .and_then(Value::as_u64)
        .unwrap_or_else(|| panic!("{} must be a non-negative integer", field))
}

fn encode_vote_option(value: &Value) -> Vec<u8> {
    let mut out = Vec::new();
    push_uint64(&mut out, 1, required_u64(value, "index"));
    push_string(&mut out, 2, required_str(value, "label"));
    if let Some(description) = value.get("description").and_then(Value::as_str) {
        push_string(&mut out, 3, description);
    }
    out
}

fn encode_proposal(value: &Value) -> Vec<u8> {
    let mut out = Vec::new();
    push_uint64(&mut out, 1, required_u64(value, "id"));
    push_string(&mut out, 2, required_str(value, "title"));
    if let Some(description) = value.get("description").and_then(Value::as_str) {
        push_string(&mut out, 3, description);
    }
    let options = value
        .get("options")
        .and_then(Value::as_array)
        .unwrap_or_else(|| panic!("options must be an array"));
    for option in options {
        push_bytes(&mut out, 4, &encode_vote_option(option));
    }
    if let Some(zip_number) = value.get("zip_number").and_then(Value::as_str) {
        push_string(&mut out, 5, zip_number);
    }
    if let Some(forum_url) = value.get("forum_url").and_then(Value::as_str) {
        push_string(&mut out, 6, forum_url);
    }
    out
}

fn encode_create_voting_session(value: &Value) -> Vec<u8> {
    let mut out = Vec::new();
    push_string(&mut out, 1, required_str(value, "creator"));
    push_uint64(&mut out, 2, required_u64(value, "snapshot_height"));
    push_bytes(
        &mut out,
        3,
        &from_base64(
            "snapshot_blockhash",
            required_str(value, "snapshot_blockhash"),
        ),
    );
    push_bytes(
        &mut out,
        4,
        &from_base64("proposals_hash", required_str(value, "proposals_hash")),
    );
    push_uint64(&mut out, 5, required_u64(value, "vote_end_time"));
    push_bytes(
        &mut out,
        6,
        &from_base64(
            "nullifier_imt_root",
            required_str(value, "nullifier_imt_root"),
        ),
    );
    push_bytes(
        &mut out,
        7,
        &from_base64("nc_root", required_str(value, "nc_root")),
    );

    let proposals = value
        .get("proposals")
        .and_then(Value::as_array)
        .unwrap_or_else(|| panic!("proposals must be an array"));
    for proposal in proposals {
        push_bytes(&mut out, 8, &encode_proposal(proposal));
    }

    if let Some(description) = value.get("description").and_then(Value::as_str) {
        push_string(&mut out, 9, description);
    }
    if let Some(title) = value.get("title").and_then(Value::as_str) {
        push_string(&mut out, 10, title);
    }
    if let Some(discussion_url) = value.get("discussion_url").and_then(Value::as_str) {
        push_string(&mut out, 11, discussion_url);
    }
    out
}

fn encode_coordinator_payload(payload: &Value, payload_type_url: &str) -> Vec<u8> {
    match payload_type_url {
        "/svote.v1.MsgCreateVotingSession" => encode_create_voting_session(payload),
        other => panic!("unsupported coordinator action payload type: {}", other),
    }
}

/// Build MsgCreateVotingSession body and derive round_id.
/// If session_override is Some, use those fields (e.g. from delegation bundle);
/// otherwise use synthetic values with vote_end_time = now + expires_in_sec.
pub fn create_voting_session_payload(
    creator: &str,
    expires_in_sec: u64,
    session_override: Option<SetupRoundFields>,
) -> (Value, SetupRoundFields, [u8; 32]) {
    let fields = session_override.unwrap_or_else(|| {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();
        SetupRoundFields {
            snapshot_height: 1000 + round_counter_next(),
            snapshot_blockhash: [0xaa; 32],
            proposals_hash: [0xbb; 32],
            vote_end_time: now + expires_in_sec,
            nullifier_imt_root: [0x01; 32],
            nc_root: [0x02; 32],
        }
    });
    let round_id = derive_round_id(&fields);
    let body = json!({
        "creator": creator,
        "snapshot_height": fields.snapshot_height,
        "snapshot_blockhash": to_base64(&fields.snapshot_blockhash),
        "proposals_hash": to_base64(&fields.proposals_hash),
        "vote_end_time": fields.vote_end_time,
        "nullifier_imt_root": to_base64(&fields.nullifier_imt_root),
        "nc_root": to_base64(&fields.nc_root),
        "proposals": [
            { "id": 1, "title": "Proposal A", "description": "First proposal",
              "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}] },
            { "id": 2, "title": "Proposal B", "description": "Second proposal",
              "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}] },
        ],
    });
    (body, fields, round_id)
}

/// Wrap a coordinator-owned message body in MsgProposeCoordinatorAction.
///
/// Local e2e chains use coordinator threshold=1, so the proposal executes in
/// the same transaction while still exercising the production authority path.
pub fn coordinator_action_proposal_payload(
    creator: &str,
    payload: Value,
    payload_type_url: &str,
) -> Value {
    let payload_value = encode_coordinator_payload(&payload, payload_type_url);
    json!({
        "@type": "/svote.v1.MsgProposeCoordinatorAction",
        "creator": creator,
        "payload": {
            "type_url": payload_type_url,
            "value": to_base64(&payload_value),
        },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn coordinator_payload_uses_binary_any_value() {
        let payload = json!({
            "creator": "sv1creator",
            "snapshot_height": 1000,
            "snapshot_blockhash": to_base64(&[0xaa; 32]),
            "proposals_hash": to_base64(&[0xbb; 32]),
            "vote_end_time": 2000,
            "nullifier_imt_root": to_base64(&[0x01; 32]),
            "nc_root": to_base64(&[0x02; 32]),
            "proposals": [{
                "id": 1,
                "title": "Proposal A",
                "description": "First proposal",
                "options": [
                    {"index": 0, "label": "Support"},
                    {"index": 1, "label": "Oppose"},
                ],
            }],
        });

        let wrapped = coordinator_action_proposal_payload(
            "sv1creator",
            payload,
            "/svote.v1.MsgCreateVotingSession",
        );
        let any_payload = wrapped.get("payload").expect("payload");

        assert_eq!(
            any_payload.get("type_url").and_then(Value::as_str),
            Some("/svote.v1.MsgCreateVotingSession")
        );
        assert!(any_payload.get("@type").is_none());
        assert_eq!(
            any_payload.get("value").and_then(Value::as_str),
            Some("CgpzdjFjcmVhdG9yEOgHGiCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqiIgu7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7u7so0A8yIAEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBOiACAgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAkI1CAESClByb3Bvc2FsIEEaDkZpcnN0IHByb3Bvc2FsIgkSB1N1cHBvcnQiCggBEgZPcHBvc2U=")
        );
    }
}

/// Delegation bundle fields (from build_delegation_bundle + create_delegation_proof).
pub struct DelegationBundlePayload {
    pub rk: Vec<u8>,
    pub spend_auth_sig: Vec<u8>,
    pub sighash: Vec<u8>,
    pub signed_note_nullifier: Vec<u8>,
    pub cmx_new: Vec<u8>,
    pub van_cmx: Vec<u8>,
    pub gov_nullifiers: Vec<Vec<u8>>,
    pub proof: Vec<u8>,
}

/// Build MsgDelegateVote body.
pub fn delegate_vote_payload(round_id: &[u8], bundle: &DelegationBundlePayload) -> Value {
    let gov_nulls: Vec<String> = bundle.gov_nullifiers.iter().map(|b| to_base64(b)).collect();
    json!({
        "rk": to_base64(&bundle.rk),
        "spend_auth_sig": to_base64(&bundle.spend_auth_sig),
        "sighash": to_base64(&bundle.sighash),
        "signed_note_nullifier": to_base64(&bundle.signed_note_nullifier),
        "cmx_new": to_base64(&bundle.cmx_new),
        "van_cmx": to_base64(&bundle.van_cmx),
        "gov_nullifiers": gov_nulls,
        "proof": to_base64(&bundle.proof),
        "vote_round_id": to_base64(round_id),
    })
}

static NULLIFIER_COUNTER: AtomicU64 = AtomicU64::new(0);

/// Unique 32-byte nullifier (canonical Pallas Fp: MSB < 0x40).
fn unique_nullifier() -> [u8; 32] {
    let c = NULLIFIER_COUNTER.fetch_add(1, Ordering::Relaxed);
    let mut nf = [0xab; 32];
    nf[0..4].copy_from_slice(&(c as u32).to_be_bytes());
    nf[31] = 0x0a;
    nf
}

/// Build MsgCastVote body (mock proof).
pub fn cast_vote_payload(round_id: &[u8], anchor_height: u32) -> Value {
    json!({
        "van_nullifier": to_base64(&unique_nullifier()),
        "vote_authority_note_new": to_base64(&unique_nullifier()),
        "vote_commitment": to_base64(&unique_nullifier()),
        "proposal_id": 1,
        "proof": to_base64(b"mock-cast-vote-proof"),
        "vote_round_id": to_base64(round_id),
        "vote_comm_tree_anchor_height": anchor_height,
        "vote_auth_sig": to_base64(&[0u8; 64]),
        "r_vpk": to_base64(&[0u8; 32]),
    })
}

/// Build MsgCastVote body with a real ZKP #2 proof and public inputs.
/// The FFI decompresses r_vpk to (x, y) for the circuit's public inputs.
/// Sighash is computed on-chain from the message fields.
pub fn cast_vote_payload_real(
    round_id: &[u8],
    anchor_height: u32,
    van_nullifier: &[u8],
    vote_authority_note_new: &[u8],
    vote_commitment: &[u8],
    proposal_id: u32,
    proof: &[u8],
    r_vpk: &[u8],
    vote_auth_sig: &[u8],
) -> Value {
    json!({
        "van_nullifier": to_base64(van_nullifier),
        "vote_authority_note_new": to_base64(vote_authority_note_new),
        "vote_commitment": to_base64(vote_commitment),
        "proposal_id": proposal_id,
        "proof": to_base64(proof),
        "vote_round_id": to_base64(round_id),
        "vote_comm_tree_anchor_height": anchor_height,
        "r_vpk": to_base64(r_vpk),
        "vote_auth_sig": to_base64(vote_auth_sig),
    })
}

/// Tally entry for MsgSubmitTally.
pub struct TallyEntry {
    pub proposal_id: u32,
    pub vote_decision: u32,
    pub total_value: u64,
}

/// Build MsgSubmitTally body.
pub fn submit_tally_payload(round_id: &[u8], creator: &str, entries: &[TallyEntry]) -> Value {
    let entries_json: Vec<Value> = entries
        .iter()
        .map(|e| {
            json!({
                "proposal_id": e.proposal_id,
                "vote_decision": e.vote_decision,
                "total_value": e.total_value,
            })
        })
        .collect();
    json!({
        "vote_round_id": to_base64(round_id),
        "creator": creator,
        "entries": entries_json,
    })
}

/// Encode ElGamal ciphertext to base64 (64 bytes).
pub fn ciphertext_to_base64(ct: &Ciphertext) -> String {
    to_base64(&elgamal::marshal(ct))
}

// --- Ceremony payload builders ---

/// Build MsgRegisterPallasKey JSON body.
pub fn register_pallas_key_payload(creator: &str, pallas_pk: &[u8]) -> Value {
    json!({
        "creator": creator,
        "pallas_pk": to_base64(pallas_pk),
    })
}

// DealerPayloadInput and deal_ea_key_payload removed: dealing is now automatic
// via PrepareProposal (auto-deal). MsgReInitializeElectionAuthority also removed.

// Note: MsgAckExecutiveAuthorityKey has no payload builder — acks are injected
// in-protocol via PrepareProposal (auto-ack).

/// Build MsgCreateValidatorWithPallasKey JSON body.
/// `staking_msg` is the protobuf-encoded MsgCreateValidator bytes.
/// `pallas_pk` is the compressed Pallas point (32 bytes).
pub fn create_validator_with_pallas_key_payload(staking_msg: &[u8], pallas_pk: &[u8]) -> Value {
    json!({
        "staking_msg": to_base64(staking_msg),
        "pallas_pk": to_base64(pallas_pk),
    })
}

/// Build a share payload for the helper server's POST /shielded-vote/v1/shares endpoint.
///
/// The helper server expects base64 for binary fields and hex for vote_round_id.
pub fn helper_share_payload(
    round_id: &[u8],
    shares_hash: &[u8],
    proposal_id: u32,
    vote_decision: u32,
    enc_share_c1: &[u8],
    enc_share_c2: &[u8],
    share_index: u32,
    tree_position: u64,
    all_enc_shares: &[(&[u8], &[u8], u32)], // (c1, c2, share_index) for each of 16 shares
    share_comms: &[Vec<u8>],                // 16 x 32-byte Poseidon commitments
    primary_blind: &[u8],                   // 32-byte blind for this share
) -> Value {
    let all_shares_json: Vec<Value> = all_enc_shares
        .iter()
        .map(|(c1, c2, idx)| {
            json!({
                "c1": to_base64(c1),
                "c2": to_base64(c2),
                "share_index": idx,
            })
        })
        .collect();

    let comms_json: Vec<Value> = share_comms
        .iter()
        .map(|c| Value::String(to_base64(c)))
        .collect();

    json!({
        "shares_hash": to_base64(shares_hash),
        "proposal_id": proposal_id,
        "vote_decision": vote_decision,
        "enc_share": {
            "c1": to_base64(enc_share_c1),
            "c2": to_base64(enc_share_c2),
            "share_index": share_index,
        },
        "tree_position": tree_position,
        "vote_round_id": hex::encode(round_id),
        "all_enc_shares": all_shares_json,
        "share_comms": comms_json,
        "primary_blind": to_base64(primary_blind),
    })
}
