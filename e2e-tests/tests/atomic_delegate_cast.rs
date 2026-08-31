//! Real-proof E2E for the one-confirmation delegation + multi-proposal cast path.

use e2e_tests::{
    api::{
        broadcast_cosmos_msg, commitment_tree_next_index, default_cosmos_tx_config,
        get_round_ea_pk, import_first_vote_manager_key, post_helper_json, post_json,
        tally_has_proposal, wait_for_committed_event_attr, wait_for_create_round_id,
        wait_for_round_status, CosmosTxConfig, FIRST_VOTE_MANAGER_KEY_NAME, SESSION_STATUS_ACTIVE,
        SESSION_STATUS_FINALIZED,
    },
    payloads::{
        cast_vote_payload_real, coordinator_action_proposal_payload, create_voting_session_payload,
        delegate_and_cast_vote_batch_payload, helper_share_payload,
    },
    setup::{ensure_pallas_key_registered, prepare_delegation_bundle_for_hotkey},
    sighash::{delegate_and_cast_vote_batch_sighash, DelegateAndCastVoteEffect},
};
use ff::{Field, PrimeField};
use group::{Curve, GroupEncoding};
use incrementalmerkletree::{Hashable, Level};
use orchard::keys::{FullViewingKey, SpendAuthorizingKey, SpendingKey};
use pasta_curves::pallas;
use rand::rngs::OsRng;
use serde_json::Value;
use vote_commitment_tree::{MerkleHashVote, TREE_DEPTH};
use voting_circuits::vote_proof::{build_vote_proof_from_delegation, VoteProofBundle};
use zcash_keys::keys::UnifiedSpendingKey;
use zcash_voting::{Network, VotingHotkey};
use zip32::{AccountId, Scope};

fn spending_key_for_hotkey(hotkey: &VotingHotkey) -> SpendingKey {
    let spending_key =
        *UnifiedSpendingKey::from_seed(&hotkey.network(), hotkey.stored_secret(), AccountId::ZERO)
            .expect("derive voting hotkey spending key")
            .orchard();
    let address = FullViewingKey::from(&spending_key)
        .address_at(u64::from(hotkey.address_index()), Scope::External);
    assert_eq!(
        address.to_raw_address_bytes(),
        *hotkey.raw_orchard_address(),
        "derived voting key must own the delegated VAN"
    );
    spending_key
}

fn composite_digest(
    round_id: &[u8],
    van_cmx: &[u8],
    proposals: &[u32],
    bundles: &[VoteProofBundle],
) -> [u8; 32] {
    let effects = bundles
        .iter()
        .zip(proposals)
        .map(|(bundle, proposal_id)| DelegateAndCastVoteEffect {
            r_vpk: bundle.r_vpk_bytes,
            van_nullifier: bundle.instance.van_nullifier.to_repr(),
            vote_authority_note_new: bundle.instance.vote_authority_note_new.to_repr(),
            vote_commitment: bundle.instance.vote_commitment.to_repr(),
            proposal_id: *proposal_id,
        })
        .collect::<Vec<_>>();
    delegate_and_cast_vote_batch_sighash(
        round_id.try_into().expect("32-byte round id"),
        van_cmx
            .try_into()
            .expect("32-byte delegation VAN commitment"),
        &effects,
    )
}

fn single_leaf_path() -> [pallas::Base; TREE_DEPTH] {
    std::array::from_fn(|level| MerkleHashVote::empty_root(Level::from(level as u8)).inner())
}

fn single_leaf_root(leaf: pallas::Base) -> pallas::Base {
    single_leaf_path()
        .into_iter()
        .fold(leaf, vote_commitment_tree::poseidon_hash)
}

fn cast_json(round_id: &[u8], proposal_id: u32, bundle: &VoteProofBundle, sig: &[u8]) -> Value {
    cast_vote_payload_real(
        round_id,
        0,
        &bundle.instance.van_nullifier.to_repr(),
        &bundle.instance.vote_authority_note_new.to_repr(),
        &bundle.instance.vote_commitment.to_repr(),
        proposal_id,
        &bundle.proof,
        &bundle.r_vpk_bytes,
        sig,
    )
}

#[test]
#[ignore = "requires running chain + helper server"]
fn atomic_delegate_cast_real_proofs_helper_and_tally() {
    ensure_pallas_key_registered();
    let tx_config = default_cosmos_tx_config();
    let manager = import_first_vote_manager_key(&tx_config.home_dir);
    let hotkey = VotingHotkey::from_stored_secret(&[0x6a; 64], Network::Testnet).expect("hotkey");
    let vote_sk = spending_key_for_hotkey(&hotkey);
    let vote_end = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs()
        + 180;
    let (prepared, session) =
        prepare_delegation_bundle_for_hotkey(&hotkey, Some(vote_end)).expect("prepare delegation");
    let (create, _, _) = create_voting_session_payload(&manager, 180, Some(session));
    let create =
        coordinator_action_proposal_payload(&manager, create, "/svote.v1.MsgCreateVotingSession");
    let manager_config = CosmosTxConfig {
        key_name: FIRST_VOTE_MANAGER_KEY_NAME.to_string(),
        home_dir: tx_config.home_dir.clone(),
        chain_id: tx_config.chain_id.clone(),
        node_url: tx_config.node_url.clone(),
    };
    let (status, create_result) =
        broadcast_cosmos_msg(&create, &manager_config).expect("create round");
    assert_eq!(status, 200, "create round: {create_result:?}");
    assert_eq!(
        create_result.get("code").and_then(Value::as_i64),
        Some(0),
        "create round: {create_result:?}"
    );
    let round_hex = wait_for_create_round_id(&create_result).expect("round id");
    let round_id = hex::decode(&round_hex).expect("round hex");
    let (delegation, proof_data) = prepared
        .build_for_round_id(&round_id)
        .expect("real delegation proof");
    wait_for_round_status(&round_hex, SESSION_STATUS_ACTIVE, 180_000, 2_000).expect("round active");
    let ea_bytes = get_round_ea_pk(&round_hex).expect("ea pk");
    let ea_arr: [u8; 32] = ea_bytes.try_into().expect("ea pk length");
    let ea_pk = Option::<pallas::Point>::from(pallas::Point::from_bytes(&ea_arr))
        .expect("ea point")
        .to_affine();

    let initial_authority = (1u64 << 16) - 1;
    let proposals = [1u32, 2u32];
    let mut alphas = Vec::new();
    let mut bundles: Vec<VoteProofBundle> = Vec::new();
    for (index, proposal_id) in proposals.into_iter().enumerate() {
        let alpha = pallas::Scalar::random(&mut OsRng);
        let authority = if index == 0 {
            initial_authority
        } else {
            initial_authority - (1u64 << proposals[0])
        };
        let bundle = build_vote_proof_from_delegation(
            &vote_sk,
            hotkey.address_index(),
            proof_data.total_note_value,
            proof_data.van_comm_rand,
            proof_data.vote_round_id,
            single_leaf_path(),
            0,
            0,
            proposal_id as u64,
            1,
            ea_pk,
            alpha,
            authority,
            true,
        )
        .expect("real chained cast proof");
        if index == 0 {
            assert_eq!(
                bundle.instance.vote_comm_tree_root,
                single_leaf_root(proof_data.van_comm)
            );
        } else {
            assert_eq!(
                bundle.instance.vote_comm_tree_root,
                single_leaf_root(bundles[0].instance.vote_authority_note_new)
            );
        }
        alphas.push(alpha);
        bundles.push(bundle);
    }

    let digest = composite_digest(&round_id, &delegation.van_cmx, &proposals, &bundles);
    let ask = SpendAuthorizingKey::from(&vote_sk);
    let signed_votes = || {
        bundles
            .iter()
            .zip(alphas.iter())
            .zip(proposals)
            .map(|((bundle, alpha), proposal_id)| {
                let signature = ask.randomize(alpha).sign(&mut OsRng, &digest);
                let signature: [u8; 64] = (&signature).into();
                cast_json(&round_id, proposal_id, bundle, &signature)
            })
            .collect()
    };
    let request = delegate_and_cast_vote_batch_payload(&round_id, &delegation, signed_votes());
    let pre_index = commitment_tree_next_index(&round_hex).unwrap_or(0);
    let (status, result) = post_json("/shielded-vote/v1/delegate-and-cast-vote-batch", &request)
        .expect("submit atomic delegation/cast");
    assert_eq!(status, 200, "atomic delegation/cast: {result:?}");
    assert_eq!(result.get("code").and_then(Value::as_i64), Some(0));
    let tx_hash = result
        .get("tx_hash")
        .and_then(Value::as_str)
        .expect("broadcast response tx_hash");
    let final_van_position: u64 = wait_for_committed_event_attr(
        tx_hash,
        "delegate_and_cast_vote_batch",
        "final_van_leaf_index",
        90,
    )
    .expect("committed final VAN position")
    .parse()
    .expect("numeric final VAN position");
    let vc_positions = wait_for_committed_event_attr(
        tx_hash,
        "delegate_and_cast_vote_batch",
        "vote_commitment_leaf_indices",
        90,
    )
    .expect("committed VC positions")
    .split(',')
    .map(|position| position.parse::<u64>().expect("numeric VC position"))
    .collect::<Vec<_>>();
    assert_eq!(vc_positions.len(), bundles.len());
    assert_eq!(
        vc_positions,
        vec![final_van_position + 1, final_van_position + 2]
    );
    assert!(final_van_position >= pre_index);
    assert!(
        commitment_tree_next_index(&round_hex)
            .map(|next| next >= final_van_position + 3)
            .unwrap_or(false),
        "atomic transaction's three leaves must be committed"
    );

    // The wallet now owns position resolution and submits ordinary, fully
    // position-bound shares to the helper after the atomic transaction commits.
    for (index, bundle) in bundles.iter().enumerate() {
        let encrypted = &bundle.encrypted_shares[0];
        let share_comms = bundle
            .share_comms
            .iter()
            .map(|value| value.to_repr().to_vec())
            .collect::<Vec<_>>();
        let share = helper_share_payload(
            &round_id,
            &bundle.shares_hash.to_repr(),
            proposals[index],
            1,
            &encrypted.c1,
            &encrypted.c2,
            0,
            vc_positions[index],
            &share_comms,
            &bundle.share_blinds[0].to_repr(),
        );
        let (share_status, share_result) =
            post_helper_json("/shielded-vote/v1/shares", &share).expect("queue share");
        assert!(
            share_status == 200 || share_status == 202,
            "helper rejected share: {share_result:?}"
        );
    }

    // Re-sign the same effects so this is a distinct transaction rather than an
    // idempotent rebroadcast of the already-committed transaction hash.
    let replay_request =
        delegate_and_cast_vote_batch_payload(&round_id, &delegation, signed_votes());
    assert_ne!(replay_request, request);
    let (replay_status, replay) = e2e_tests::api::post_json(
        "/shielded-vote/v1/delegate-and-cast-vote-batch",
        &replay_request,
    )
    .expect("replay request");
    assert_ne!(replay.get("code").and_then(Value::as_i64), Some(0));
    assert!(
        replay_status == 422 || replay_status == 200,
        "replay: {replay:?}"
    );

    wait_for_round_status(&round_hex, SESSION_STATUS_FINALIZED, 300_000, 2_000)
        .expect("round finalized");
    assert!(tally_has_proposal(&round_hex, 1));
    assert!(tally_has_proposal(&round_hex, 2));
}
