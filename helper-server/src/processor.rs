//! Background share processing pipeline.
//!
//! Periodically checks the share queue for shares whose delay has elapsed,
//! then for each ready share:
//! 1. Syncs the tree (if needed)
//! 2. Generates VC Merkle witness at latest anchor height
//! 3. Derives share_nullifier
//! 4. Generates ZKP #3 proof via the real Halo2 circuit
//! 5. POSTs MsgRevealShare to the chain endpoint

use base64::prelude::*;
use ff::{FromUniformBytes, PrimeField};
use pasta_curves::Fp;

use crate::chain_client::ChainClient;
use crate::nullifier::derive_share_nullifier;
use crate::store::ShareStore;
use crate::tree::TreeSync;
use crate::types::{MsgRevealShareJson, QueuedShare};

/// Run the share processing loop.
pub async fn run_processor(
    store: ShareStore,
    tree: TreeSync,
    chain: ChainClient,
    interval_secs: u64,
) {
    let interval = std::time::Duration::from_secs(interval_secs);
    loop {
        let ready = store.take_ready();
        if !ready.is_empty() {
            tracing::info!(count = ready.len(), "processing ready shares");

            // Sync tree before processing batch.
            let tree_clone = tree.clone();
            if let Err(e) = tokio::task::spawn_blocking(move || tree_clone.sync()).await {
                tracing::error!(error = %e, "tree sync for processing failed");
            }

            for share in ready {
                match process_share(&share, &tree, &chain).await {
                    Ok(()) => {
                        store.mark_submitted(
                            &share.payload.vote_round_id,
                            share.payload.enc_share.share_index,
                        );
                        tracing::info!(
                            round_id = %share.payload.vote_round_id,
                            share_index = share.payload.enc_share.share_index,
                            "share submitted"
                        );
                    }
                    Err(e) => {
                        tracing::warn!(
                            round_id = %share.payload.vote_round_id,
                            share_index = share.payload.enc_share.share_index,
                            error = %e,
                            "share processing failed"
                        );
                        store.mark_failed(
                            &share.payload.vote_round_id,
                            share.payload.enc_share.share_index,
                        );
                    }
                }
            }
        }
        tokio::time::sleep(interval).await;
    }
}

/// Decode a base64 compressed Pallas point into its x-coordinate (Fp).
///
/// Clears the sign bit (bit 7 of byte 31) to extract the raw x-coordinate,
/// matching the ExtractP convention used by the vote proof circuit.
fn decode_x_coord(b64: &str) -> Result<Fp, String> {
    let bytes = BASE64_STANDARD
        .decode(b64)
        .map_err(|e| format!("base64 decode: {e}"))?;
    if bytes.len() != 32 {
        return Err(format!("expected 32 bytes, got {}", bytes.len()));
    }
    let mut arr = [0u8; 32];
    arr.copy_from_slice(&bytes);
    // Clear the sign bit to get the raw x-coordinate.
    arr[31] &= 0x7F;
    Option::from(Fp::from_repr(arr)).ok_or_else(|| "non-canonical Fp encoding".to_string())
}

/// Decode all 4 encrypted share x-coordinates from the payload.
///
/// The `all_enc_shares` field must contain exactly 4 entries, sorted by
/// share_index. Returns `(c1_x[4], c2_x[4])`.
fn decode_all_share_x_coords(
    share: &QueuedShare,
) -> Result<([Fp; 4], [Fp; 4]), String> {
    if share.payload.all_enc_shares.len() != 4 {
        return Err(format!(
            "expected 4 encrypted shares, got {}",
            share.payload.all_enc_shares.len()
        ));
    }

    let mut c1_x = [Fp::zero(); 4];
    let mut c2_x = [Fp::zero(); 4];

    for (i, es) in share.payload.all_enc_shares.iter().enumerate() {
        c1_x[i] = decode_x_coord(&es.c1)?;
        c2_x[i] = decode_x_coord(&es.c2)?;
    }

    Ok((c1_x, c2_x))
}

/// Process a single share: witness → nullifier → proof → submit.
async fn process_share(
    share: &QueuedShare,
    tree: &TreeSync,
    chain: &ChainClient,
) -> Result<(), String> {
    let anchor_height = tree
        .latest_height()
        .ok_or("tree not synced yet (no checkpoint)")?;

    // Generate VC Merkle witness. If the position was synced before being
    // marked (share arrived after background sync), this rebuilds the tree
    // from scratch with the position marked and retries.
    // witness_or_resync does blocking HTTP, so run it on the blocking pool.
    let tree_for_witness = tree.clone();
    let w_position = share.payload.tree_position;
    let w_anchor = anchor_height;
    let witness = tokio::task::spawn_blocking(move || {
        tree_for_witness.witness_or_resync(w_position, w_anchor)
    })
    .await
    .map_err(|e| format!("witness task failed: {}", e))?
    .ok_or_else(|| {
        format!(
            "no witness for position {} at height {} (even after resync)",
            share.payload.tree_position, anchor_height
        )
    })?;

    // Reconstruct the vote commitment (VC) leaf value.
    // The VC leaf is vote_commitment_hash(shares_hash, proposal_id, vote_decision),
    // NOT the bare shares_hash.
    let shares_hash_bytes = BASE64_STANDARD
        .decode(&share.payload.shares_hash)
        .map_err(|e| format!("decode shares_hash: {}", e))?;
    let mut sh_arr = [0u8; 32];
    sh_arr.copy_from_slice(&shares_hash_bytes);
    let shares_hash_fp =
        Option::from(Fp::from_repr(sh_arr)).ok_or("non-canonical shares_hash Fp")?;

    let vote_commitment = vote_commitment_tree::vote_commitment_hash(
        shares_hash_fp,
        Fp::from(u64::from(share.payload.proposal_id)),
        Fp::from(u64::from(share.payload.vote_decision)),
    );

    // Convert vote_round_id from hex to bytes and to Fp (needed for nullifier + circuit).
    let round_id_bytes = hex::decode(&share.payload.vote_round_id)
        .map_err(|e| format!("decode vote_round_id: {}", e))?;

    // Decode voting_round_id as an Fp element.
    // Round IDs are Blake2b-256 hashes which are frequently non-canonical as
    // raw Fp encodings (~75% of random 32-byte values exceed the Pallas
    // modulus). Use wide reduction (zero-extend to 64 bytes) to get a
    // canonical field element, matching the FFI verifier's hash_bytes_to_fp.
    let voting_round_id_fp = {
        if round_id_bytes.len() != 32 {
            return Err(format!(
                "vote_round_id must be 32 bytes, got {}",
                round_id_bytes.len()
            ));
        }
        let mut wide = [0u8; 64];
        wide[..32].copy_from_slice(&round_id_bytes);
        Fp::from_uniform_bytes(&wide)
    };

    // Derive share nullifier (includes voting_round_id for round binding).
    let nullifier = derive_share_nullifier(&share.payload, vote_commitment, voting_round_id_fp)
        .ok_or("nullifier derivation failed")?;

    // Build MsgRevealShare.
    // enc_share on chain is C1 || C2 (64 bytes).
    let c1 = BASE64_STANDARD
        .decode(&share.payload.enc_share.c1)
        .map_err(|e| format!("decode c1: {}", e))?;
    let c2 = BASE64_STANDARD
        .decode(&share.payload.enc_share.c2)
        .map_err(|e| format!("decode c2: {}", e))?;
    let mut enc_share_bytes = Vec::with_capacity(64);
    enc_share_bytes.extend_from_slice(&c1);
    enc_share_bytes.extend_from_slice(&c2);

    // --- ZKP #3 proof generation ---
    // Decode all 4 encrypted share x-coordinates from the payload.
    let (all_c1_x, all_c2_x) = decode_all_share_x_coords(share)?;

    // Verify shares_hash consistency: the payload's shares_hash must match
    // what all_enc_shares actually hash to. A mismatch means the circuit
    // witness (from all_enc_shares) would produce a different vote_commitment
    // than the nullifier (from payload.shares_hash), causing proof verification
    // to fail on-chain after wasting 30-60s of proof generation.
    let recomputed_shares_hash = orchard::vote_proof::shares_hash(all_c1_x, all_c2_x);
    if recomputed_shares_hash != shares_hash_fp {
        return Err(
            "shares_hash does not match all_enc_shares: payload is internally inconsistent"
                .to_string(),
        );
    }

    // Extract the Merkle auth path as raw Fp values for the builder.
    let auth_path_fp: [Fp; vote_commitment_tree::TREE_DEPTH] = {
        let raw = witness.auth_path();
        let mut arr = [Fp::zero(); vote_commitment_tree::TREE_DEPTH];
        for (i, h) in raw.iter().enumerate() {
            arr[i] = h.inner();
        }
        arr
    };

    // Build the share reveal bundle (circuit + instance).
    let bundle = orchard::share_reveal::builder::build_share_reveal(
        auth_path_fp,
        witness.position(),
        all_c1_x,
        all_c2_x,
        share.payload.enc_share.share_index,
        Fp::from(u64::from(share.payload.proposal_id)),
        Fp::from(u64::from(share.payload.vote_decision)),
        voting_round_id_fp,
    );

    // Generate the real Halo2 proof (CPU-intensive, ~30-60s in release mode).
    let proof_bytes = tokio::task::spawn_blocking(move || {
        orchard::share_reveal::create_share_reveal_proof(bundle.circuit, &bundle.instance)
    })
    .await
    .map_err(|e| format!("proof generation task failed: {}", e))?;

    let msg = MsgRevealShareJson {
        share_nullifier: BASE64_STANDARD.encode(nullifier.to_repr()),
        enc_share: BASE64_STANDARD.encode(&enc_share_bytes),
        proposal_id: share.payload.proposal_id,
        vote_decision: share.payload.vote_decision,
        proof: BASE64_STANDARD.encode(&proof_bytes),
        vote_round_id: BASE64_STANDARD.encode(&round_id_bytes),
        vote_comm_tree_anchor_height: anchor_height as u64,
    };

    let result = chain.submit_reveal_share(&msg).await?;
    if result.code != 0 {
        return Err(format!(
            "chain rejected tx (code {}): {}",
            result.code, result.log
        ));
    }

    tracing::debug!(tx_hash = %result.tx_hash, "MsgRevealShare broadcast ok");
    Ok(())
}
