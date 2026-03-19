//! Compute the Orchard note commitment tree root from a hex-encoded frontier.
//!
//! Lightwalletd's TreeState contains an `orchardTree` field: a hex-encoded
//! serialized `CommitmentTree<MerkleHashOrchard, 32>`. This module parses
//! that format and computes the Sinsemilla-based root — something Go can't
//! do natively.
//!
//! The binary format (from zcash_primitives `write_commitment_tree`):
//!   - Optional<H> left:   1-byte flag (0=None, 1=Some) + 32-byte hash if Some
//!   - Optional<H> right:  same
//!   - u8 parent_count
//!   - parent_count × Optional<H>: same encoding

use incrementalmerkletree::{Hashable, Level};
use orchard::tree::MerkleHashOrchard;

/// Orchard note commitment tree depth.
const DEPTH: u8 = 32;

/// Parse a hex-encoded orchard frontier and compute its root.
///
/// This is equivalent to `zcash_client_backend`'s
/// `TreeState::orchard_tree().root()` but without pulling in that crate.
pub fn compute_nc_root(orchard_tree_hex: &str) -> Result<[u8; 32], String> {
    let bytes = hex::decode(orchard_tree_hex)
        .map_err(|e| format!("hex decode: {e}"))?;

    let (left, right, parents) = parse_commitment_tree(&bytes)?;
    let root = commitment_tree_root(left, right, &parents);
    Ok(root.to_bytes())
}

/// Compute the root of a commitment tree from its parts.
///
/// Reimplements `CommitmentTree::root()` from incrementalmerkletree's
/// legacy API without needing the `legacy-api` feature.
fn commitment_tree_root(
    left: Option<MerkleHashOrchard>,
    right: Option<MerkleHashOrchard>,
    parents: &[Option<MerkleHashOrchard>],
) -> MerkleHashOrchard {
    // Start with the left leaf (or empty leaf if absent).
    let mut digest = left.unwrap_or_else(MerkleHashOrchard::empty_leaf);
    let mut height: u8 = 0;

    // Combine with right (or empty root at level 0).
    let right_val = right.unwrap_or_else(|| MerkleHashOrchard::empty_root(Level::from(height)));
    digest = MerkleHashOrchard::combine(Level::from(height), &digest, &right_val);
    height += 1;

    // Combine with each parent.  When the parent is Some it represents the
    // completed *left* subtree at that level, so it goes on the left and the
    // running digest (the right subtree) goes on the right.  When the parent
    // is None the running digest IS the left subtree and the right is empty.
    for parent in parents {
        match parent {
            Some(left_subtree) => {
                digest = MerkleHashOrchard::combine(Level::from(height), left_subtree, &digest);
            }
            None => {
                let empty = MerkleHashOrchard::empty_root(Level::from(height));
                digest = MerkleHashOrchard::combine(Level::from(height), &digest, &empty);
            }
        }
        height += 1;
    }

    // Fill remaining levels up to DEPTH with empty roots.
    while height < DEPTH {
        let sibling = MerkleHashOrchard::empty_root(Level::from(height));
        digest = MerkleHashOrchard::combine(Level::from(height), &digest, &sibling);
        height += 1;
    }

    digest
}

/// Parse a serialized CommitmentTree into its parts: (left, right, parents).
fn parse_commitment_tree(
    mut data: &[u8],
) -> Result<(Option<MerkleHashOrchard>, Option<MerkleHashOrchard>, Vec<Option<MerkleHashOrchard>>), String> {
    let left = read_optional_node(&mut data)?;
    let right = read_optional_node(&mut data)?;

    if data.is_empty() {
        return Err("unexpected end of data reading parent count".into());
    }
    let parent_count = data[0] as usize;
    data = &data[1..];

    let mut parents = Vec::with_capacity(parent_count);
    for i in 0..parent_count {
        parents.push(
            read_optional_node(&mut data)
                .map_err(|e| format!("parent[{i}]: {e}"))?,
        );
    }

    Ok((left, right, parents))
}

/// Read an `Option<MerkleHashOrchard>` from the wire format:
/// 1 byte flag (0=None, 1=Some), then 32 bytes if Some.
fn read_optional_node(
    data: &mut &[u8],
) -> Result<Option<MerkleHashOrchard>, String> {
    if data.is_empty() {
        return Err("unexpected end of data reading flag byte".into());
    }
    let flag = data[0];
    *data = &data[1..];

    match flag {
        0 => Ok(None),
        1 => {
            if data.len() < 32 {
                return Err("unexpected end of data reading 32-byte hash".into());
            }
            let mut buf = [0u8; 32];
            buf.copy_from_slice(&data[..32]);
            *data = &data[32..];
            let node = MerkleHashOrchard::from_bytes(&buf);
            if node.is_none().into() {
                return Err("non-canonical MerkleHashOrchard encoding".into());
            }
            Ok(Some(node.unwrap()))
        }
        other => Err(format!("invalid flag byte {other}, expected 0 or 1")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_empty_tree() {
        // An empty commitment tree: left=None, right=None, 0 parents.
        // Hex: "00 00 00"
        let result = compute_nc_root("000000");
        assert!(result.is_ok());
        let root = result.unwrap();
        // Should match the canonical empty root.
        let expected = MerkleHashOrchard::empty_root(Level::from(DEPTH));
        assert_eq!(root, expected.to_bytes());
    }

    #[test]
    fn test_single_leaf() {
        // A tree with one leaf: left=Some(leaf), right=None, 0 parents.
        // The leaf is just Fp(1) for simplicity.
        use pasta_curves::group::ff::PrimeField;
        let leaf = pasta_curves::pallas::Base::one().to_repr();

        // Encode: flag=1 + 32-byte leaf, flag=0 (no right), count=0
        let mut encoded = Vec::new();
        encoded.push(1);          // left present
        encoded.extend_from_slice(&leaf);
        encoded.push(0);          // right absent
        encoded.push(0);          // 0 parents

        let hex_str = hex::encode(&encoded);
        let root = compute_nc_root(&hex_str).unwrap();

        // Manually compute expected root: combine(0, leaf, empty_root(0)),
        // then hash up through 31 more levels with empty siblings.
        let leaf_hash = MerkleHashOrchard::from_bytes(&leaf).unwrap();
        let right_empty = MerkleHashOrchard::empty_root(Level::from(0));
        let mut digest = MerkleHashOrchard::combine(Level::from(0), &leaf_hash, &right_empty);
        for h in 1..32u8 {
            let sibling = MerkleHashOrchard::empty_root(Level::from(h));
            digest = MerkleHashOrchard::combine(Level::from(h), &digest, &sibling);
        }

        assert_eq!(root, digest.to_bytes());
    }

    /// Verify against the known-correct nc_root from zcash_client_backend at mainnet height 3245500.
    #[test]
    fn test_mainnet_3245500() {
        let hex_str = "01ca2a21eba4c591869db2b8fdf4dd5ba493d6bfceac23be46ca558ffed0dc921d001f0001cff1b87edbd466dbba37844dc634e456af9650eff4707e38f3ac7abadd20811d0000015b2cefe8fb1ea9a719a7ca95121b2ed30d5232a11fb2147648d1a295b8eb433e014ba2c229d7a29d9eb8d037696bd2821bcceaeb6ed295428487333d3f12e791340000000001f3715072c78b20389957accac152bcea31bdf72570c7b27fe971bf73b6890a1701882a011843c0e6e4006e0a061222047775ccb2f5e560170807f466010d3ac41b000160ba626f9d0861510dd8bad09e1479eb74a93ed48cbbdea8dd14e1f63fd1123b00015fd78b01fa7f2d305ef8c6b968027c4020ba0ad8dd6a2d12218b9a9120747c2f01fb82740a3629216088191f9cd359c52a2f35b1c58f6cc905781bd9687b66ad3801eac2b89b3f966d833626434df98d553e000324bbafb8d6e1fe03b8d7f854cf2a00017c8ece2b2ab2355d809b58809b21c7a5e95cfc693cd689387f7533ec8749261e01cc2dcaa338b312112db04b435a706d63244dd435238f0aa1e9e1598d35470810012dcc4273c8a0ed2337ecf7879380a07e7d427c7f9d82e538002bd1442978402c01daf63debf5b40df902dae98dadc029f281474d190cddecef1b10653248a234150001e2bca6a8d987d668defba89dc082196a922634ed88e065c669e526bb8815ee1b000000000000";
        let root = compute_nc_root(hex_str).unwrap();
        assert_eq!(
            hex::encode(root),
            "698e8409ae1d6b2e977ee5b8d37098f4fce2f07d5ac0b62269170b8cca077103",
        );
    }
}
