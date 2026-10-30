//! Fixed V6/NU7 signature profile for version-2 delegation effects.
//!
//! The pinned protocol crate still encodes NU7 as the proposed `0xffffffff`
//! branch. Use the assigned ID here without changing the proof dependencies.
//! This composes the header and root digests specified by ZIP 244 and ZIP 229;
//! Ironwood and empty Orchard commitments remain owned by the Orchard library.
//! https://zips.z.cash/zip-0244#txid-digest
//! https://zips.z.cash/zip-0229

use blake2b_simd::{Hash, Params};
use voting_crypto_deps::{
    orchard::{
        bundle::{commitments, TxVersion as OrchardTxVersion},
        ValuePool,
    },
    zcash_primitives::transaction::TxVersion,
};

const NU7_BRANCH_ID: u32 = 0x7719_0ad9;

/// Reconstructs the fixed shielded-only transaction digest. No caller-supplied
/// branch or transaction header can alter the version-2 signing profile.
pub(super) fn sighash(ironwood_digest: &Hash) -> [u8; 32] {
    let mut header = Params::new()
        .hash_length(32)
        .personal(b"ZTxIdHeadersHash")
        .to_state();
    for field in [
        TxVersion::V6.header(),
        TxVersion::V6.version_group_id(),
        NU7_BRANCH_ID,
        0, // lock time
        0, // expiry height
    ] {
        header.update(&field.to_le_bytes());
    }

    let transparent = empty_digest(b"ZTxIdTranspaHash");
    let sapling = empty_digest(b"ZTxIdSaplingHash");
    let orchard = commitments::hash_bundle_txid_empty(ValuePool::Orchard, OrchardTxVersion::V6)
        .expect("the empty V6 Orchard commitment is valid");

    let mut personalization = *b"ZcashTxHash_\0\0\0\0";
    personalization[12..].copy_from_slice(&NU7_BRANCH_ID.to_le_bytes());
    let mut transaction = Params::new()
        .hash_length(32)
        .personal(&personalization)
        .to_state();
    for digest in [
        header.finalize(),
        transparent,
        sapling,
        orchard,
        *ironwood_digest,
    ] {
        transaction.update(digest.as_bytes());
    }
    transaction.finalize().as_bytes().try_into().unwrap()
}

fn empty_digest(personalization: &[u8; 16]) -> Hash {
    Params::new()
        .hash_length(32)
        .personal(personalization)
        .hash(&[])
}
