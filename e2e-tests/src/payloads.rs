//! JSON payload builders and round_id derivation for Zally REST API.
//!
//! Matches the chain's deriveRoundID: Blake2b-256(snapshot_height_BE ||
//! snapshot_blockhash || proposals_hash || vote_end_time_BE ||
//! nullifier_imt_root || nc_root).

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

/// Derive vote_round_id = Blake2b-256(snapshot_height_BE || snapshot_blockhash ||
/// proposals_hash || vote_end_time_BE || nullifier_imt_root || nc_root).
pub fn derive_round_id(fields: &SetupRoundFields) -> [u8; 32] {
    let mut hasher = blake2b_simd::Params::new().hash_length(32).to_state();
    hasher.update(&fields.snapshot_height.to_be_bytes());
    hasher.update(&fields.snapshot_blockhash);
    hasher.update(&fields.proposals_hash);
    hasher.update(&fields.vote_end_time.to_be_bytes());
    hasher.update(&fields.nullifier_imt_root);
    hasher.update(&fields.nc_root);
    let hash = hasher.finalize();
    let mut out = [0u8; 32];
    out.copy_from_slice(hash.as_bytes());
    out
}

fn to_base64(bytes: &[u8]) -> String {
    base64::Engine::encode(&base64::engine::general_purpose::STANDARD, bytes)
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
            nullifier_imt_root: [0xcc; 32],
            nc_root: [0xdd; 32],
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
        "vk_zkp1": to_base64(&[0xf1u8; 64]),
        "vk_zkp2": to_base64(&[0xf2u8; 64]),
        "vk_zkp3": to_base64(&[0xf3u8; 64]),
        "proposals": [
            { "id": 1, "title": "Proposal A", "description": "First proposal",
              "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}] },
            { "id": 2, "title": "Proposal B", "description": "Second proposal",
              "options": [{"index": 0, "label": "Support"}, {"index": 1, "label": "Oppose"}] },
        ],
    });
    (body, fields, round_id)
}

/// Delegation bundle fields (from build_delegation_bundle + create_delegation_proof).
pub struct DelegationBundlePayload {
    pub rk: Vec<u8>,
    pub spend_auth_sig: Vec<u8>,
    pub sighash: Vec<u8>,
    pub signed_note_nullifier: Vec<u8>,
    pub cmx_new: Vec<u8>,
    pub enc_memo: Vec<u8>,
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
        "enc_memo": to_base64(&bundle.enc_memo),
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
        "r_vpk_x": to_base64(&[0u8; 32]),
        "r_vpk_y": to_base64(&[0u8; 32]),
        "vote_auth_sig": to_base64(&[0u8; 64]),
        "sighash": to_base64(&[0u8; 32]),
        "r_vpk": to_base64(&[0u8; 32]),
    })
}

/// Build MsgCastVote body with a real ZKP #2 proof and public inputs.
/// Condition 4 (Spend Authority) requires r_vpk_x and r_vpk_y in the payload,
/// plus the RedPallas signature fields (vote_auth_sig, sighash, r_vpk).
pub fn cast_vote_payload_real(
    round_id: &[u8],
    anchor_height: u32,
    van_nullifier: &[u8],
    r_vpk_x: &[u8],
    r_vpk_y: &[u8],
    vote_authority_note_new: &[u8],
    vote_commitment: &[u8],
    proposal_id: u32,
    proof: &[u8],
    r_vpk: &[u8],
    sighash: &[u8],
    vote_auth_sig: &[u8],
) -> Value {
    json!({
        "van_nullifier": to_base64(van_nullifier),
        "r_vpk_x": to_base64(r_vpk_x),
        "r_vpk_y": to_base64(r_vpk_y),
        "vote_authority_note_new": to_base64(vote_authority_note_new),
        "vote_commitment": to_base64(vote_commitment),
        "proposal_id": proposal_id,
        "proof": to_base64(proof),
        "vote_round_id": to_base64(round_id),
        "vote_comm_tree_anchor_height": anchor_height,
        "r_vpk": to_base64(r_vpk),
        "sighash": to_base64(sighash),
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

/// Build MsgSetVoteManager JSON body.
pub fn set_vote_manager_payload(creator: &str, new_manager: &str) -> Value {
    json!({
        "creator": creator,
        "new_manager": new_manager,
    })
}

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

/// Build a share payload for the helper server's POST /api/v1/shares endpoint.
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
        "share_index": share_index,
        "tree_position": tree_position,
        "vote_round_id": hex::encode(round_id),
        "all_enc_shares": all_shares_json,
        "share_comms": comms_json,
        "primary_blind": to_base64(primary_blind),
    })
}
