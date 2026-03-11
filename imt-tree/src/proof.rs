use pasta_curves::Fp;

use crate::hasher::PoseidonHasher;
use crate::tree::TREE_DEPTH;

/// Circuit-compatible IMT non-membership proof data.
///
/// Each field maps directly to a circuit witness:
///
/// - `root`: public input, checked against the IMT root in the instance column
/// - `low`, `width`: witnessed interval `(low, width)` pair, hashed to the leaf commitment
/// - `leaf_pos`: position bits determine swap ordering at each Merkle level
/// - `path`: sibling hashes for the 29-level Merkle authentication path
#[derive(Clone, Debug)]
pub struct ImtProofData {
    /// The Merkle root of the IMT.
    pub root: Fp,
    /// Interval start (low bound of the bracketing leaf).
    pub low: Fp,
    /// Interval width (`high - low`, pre-computed during tree construction).
    pub width: Fp,
    /// Position of the leaf in the tree.
    pub leaf_pos: u32,
    /// Sibling hashes along the 29-level Merkle path (pure siblings).
    pub path: [Fp; TREE_DEPTH],
}

impl ImtProofData {
    /// Verify this proof out-of-circuit.
    ///
    /// Checks that `value` falls within `[low, low + width]` and that the
    /// Merkle path recomputes to `root`.
    pub fn verify(&self, value: Fp) -> bool {
        // value - low <= width: if value < low, field subtraction wraps to a
        // huge value that exceeds any valid width, so the check fails correctly.
        let offset = value - self.low;
        if offset > self.width {
            return false;
        }
        let hasher = PoseidonHasher::new();
        let leaf = hasher.hash(self.low, self.width);
        let mut current = leaf;
        let mut pos = self.leaf_pos;
        for sibling in self.path.iter() {
            let (l, r) = if pos & 1 == 0 {
                (current, *sibling)
            } else {
                (*sibling, current)
            };
            current = hasher.hash(l, r);
            pos >>= 1;
        }
        current == self.root
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::{fp, four_nullifiers};
    use crate::tree::{NullifierTree, TREE_DEPTH};

    #[test]
    fn test_proof_verify_rejects_wrong_value() {
        let tree = NullifierTree::build(four_nullifiers());

        let proof = tree.prove(fp(15)).unwrap();
        assert!(!proof.verify(fp(5)));
        assert!(!proof.verify(fp(10)));
    }

    #[test]
    fn test_proof_verify_rejects_wrong_root() {
        let tree = NullifierTree::build(four_nullifiers());

        let mut proof = tree.prove(fp(15)).unwrap();
        proof.root = Fp::zero();
        assert!(!proof.verify(fp(15)));
    }

    #[test]
    fn test_verify_rejects_tampered_auth_path_level_0() {
        let tree = NullifierTree::build(four_nullifiers());
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        proof.path[0] += Fp::one();
        assert!(
            !proof.verify(value),
            "tampered auth_path[0] should fail verification"
        );
    }

    #[test]
    fn test_verify_rejects_tampered_auth_path_mid_level() {
        let tree = NullifierTree::build(four_nullifiers());
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        let mid = TREE_DEPTH / 2;
        proof.path[mid] = Fp::zero();
        assert!(
            !proof.verify(value),
            "tampered auth_path[{}] should fail verification",
            mid
        );
    }

    #[test]
    fn test_verify_rejects_tampered_low() {
        let tree = NullifierTree::build(four_nullifiers());
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        proof.low = Fp::from(999u64);
        assert!(
            !proof.verify(value),
            "tampered low bound should fail verification"
        );
    }

    #[test]
    fn test_verify_rejects_tampered_position() {
        let tree = NullifierTree::build(four_nullifiers());
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();
        assert_eq!(proof.leaf_pos, 1);

        proof.leaf_pos = 0;
        assert!(!proof.verify(value), "position 0 (wrong) should fail");

        proof.leaf_pos = 2;
        assert!(!proof.verify(value), "position 2 (wrong) should fail");

        proof.leaf_pos = u32::MAX;
        assert!(!proof.verify(value), "position MAX (wrong) should fail");
    }

    #[test]
    fn test_verify_rejects_swapped_range_fields() {
        let tree = NullifierTree::build(four_nullifiers());
        let value = fp(15);
        let mut proof = tree.prove(value).unwrap();

        let (low, width) = (proof.low, proof.width);
        proof.low = width;
        proof.width = low;
        assert!(
            !proof.verify(value),
            "swapped range fields should fail verification"
        );
    }
}
