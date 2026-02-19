use super::*;
use ff::Field;
use halo2_gadgets::poseidon::primitives::{self as poseidon, ConstantLength, P128Pow5T3};

/// Helper: make an Fp from a u64.
fn fp(v: u64) -> Fp {
    Fp::from(v)
}

// 4 nullifiers: 10, 20, 30, 40
// Expected 5 gap ranges:
//   [0, 9]    [11, 19]    [21, 29]    [31, 39]    [41, MAX]

fn four_nullifiers() -> Vec<Fp> {
    vec![fp(10), fp(20), fp(30), fp(40)]
}

#[test]
fn test_build_ranges_from_4_nullifiers() {
    let ranges = build_nf_ranges(four_nullifiers());
    assert_eq!(ranges.len(), 5);

    assert_eq!(ranges[0], [fp(0), fp(9)]);
    assert_eq!(ranges[1], [fp(11), fp(19)]);
    assert_eq!(ranges[2], [fp(21), fp(29)]);
    assert_eq!(ranges[3], [fp(31), fp(39)]);
    // Last range: [41, Fp::MAX]
    assert_eq!(ranges[4][0], fp(41));
    assert_eq!(ranges[4][1], Fp::one().neg());
}

#[test]
fn test_nullifiers_not_in_any_range() {
    let ranges = build_nf_ranges(four_nullifiers());
    for &nf in &four_nullifiers() {
        assert!(
            find_range_for_value(&ranges, nf).is_none(),
            "nullifier {:?} should not be in any gap range",
            nf
        );
    }
}

#[test]
fn test_non_nullifiers_found_in_ranges() {
    let ranges = build_nf_ranges(four_nullifiers());

    // Values in each gap
    assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(5)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(9)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(11)), Some(1));
    assert_eq!(find_range_for_value(&ranges, fp(15)), Some(1));
    assert_eq!(find_range_for_value(&ranges, fp(25)), Some(2));
    assert_eq!(find_range_for_value(&ranges, fp(35)), Some(3));
    assert_eq!(find_range_for_value(&ranges, fp(41)), Some(4));
    assert_eq!(find_range_for_value(&ranges, fp(1000)), Some(4));
}

#[test]
fn test_merkle_root_is_deterministic() {
    let tree1 = NullifierTree::build(four_nullifiers());
    let tree2 = NullifierTree::build(four_nullifiers());
    assert_eq!(tree1.root(), tree2.root());
}

#[test]
fn test_merkle_paths_verify_for_each_range() {
    let tree = NullifierTree::build(four_nullifiers());

    // Verify an exclusion proof for a value in every range
    let test_values = [fp(5), fp(15), fp(25), fp(35), fp(41)];
    for (i, &value) in test_values.iter().enumerate() {
        let proof = tree.prove(value).expect("should produce proof");
        assert_eq!(proof.leaf_pos, i as u32);
        assert!(
            proof.verify(value),
            "exclusion proof for range {} does not verify",
            i
        );
    }
}

#[test]
fn test_exclusion_proof_end_to_end() {
    let tree = NullifierTree::build(four_nullifiers());

    // Prove that 15 is not a nullifier
    let value = fp(15);
    let proof = tree.prove(value).expect("should produce proof");
    assert_eq!(proof.leaf_pos, 1); // range [11, 19]

    assert_eq!(proof.low, fp(11));
    assert_eq!(proof.high, fp(19));
    assert!(value >= proof.low && value <= proof.high);
    assert!(proof.verify(value));
}

#[test]
fn test_nullifier_has_no_proof() {
    let tree = NullifierTree::build(four_nullifiers());
    for &nf in &four_nullifiers() {
        assert!(
            tree.prove(nf).is_none(),
            "nullifier {:?} should not have an exclusion proof",
            nf
        );
    }
}

#[test]
fn test_tree_len() {
    let tree = NullifierTree::build(four_nullifiers());
    assert_eq!(tree.len(), 5);
    assert!(!tree.is_empty());
}

#[test]
fn test_save_load_round_trip() {
    let tree = NullifierTree::build(four_nullifiers());
    let dir = std::env::temp_dir().join("imt_tree_test");
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join("ranges.bin");

    tree.save(&path).unwrap();
    let loaded = NullifierTree::load(&path).unwrap();
    assert_eq!(tree.root(), loaded.root());
    assert_eq!(tree.ranges(), loaded.ranges());

    std::fs::remove_file(&path).unwrap();
}

#[test]
fn test_save_load_full_round_trip() {
    let mut tree = NullifierTree::build(four_nullifiers());
    tree.set_height(2_000_000);
    let dir = std::env::temp_dir().join("imt_tree_test_full");
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join("full_tree.bin");

    tree.save_full(&path).unwrap();
    let loaded = NullifierTree::load_full(&path).unwrap();

    assert_eq!(tree.root(), loaded.root());
    assert_eq!(tree.ranges(), loaded.ranges());
    assert_eq!(tree.len(), loaded.len());
    assert_eq!(loaded.height(), Some(2_000_000));

    // Verify all level hashes match
    let original_leaves = tree.leaves();
    let loaded_leaves = loaded.leaves();
    assert_eq!(original_leaves, loaded_leaves);

    // Verify proofs still work on the loaded tree
    let value = fp(15);
    let proof = loaded.prove(value).unwrap();
    assert!(proof.verify(value));

    std::fs::remove_file(&path).unwrap();
}

#[test]
fn test_save_load_full_no_height() {
    let tree = NullifierTree::build(four_nullifiers());
    assert_eq!(tree.height(), None);

    let dir = std::env::temp_dir().join("imt_tree_test_full_no_height");
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join("full_tree.bin");

    tree.save_full(&path).unwrap();
    let loaded = NullifierTree::load_full(&path).unwrap();
    assert_eq!(loaded.height(), None);
    assert_eq!(tree.root(), loaded.root());

    std::fs::remove_file(&path).unwrap();
}

#[test]
fn test_unsorted_input_produces_same_tree() {
    let sorted = NullifierTree::build(four_nullifiers());
    let unsorted = NullifierTree::build(vec![fp(30), fp(10), fp(40), fp(20)]);
    assert_eq!(sorted.root(), unsorted.root());
}

#[test]
fn test_precompute_empty_hashes_chain() {
    let hasher = PoseidonHasher::new();
    let empty = precompute_empty_hashes();

    assert_eq!(empty[0], hasher.hash(Fp::zero(), Fp::zero()));

    for i in 1..TREE_DEPTH {
        let expected = hasher.hash(empty[i - 1], empty[i - 1]);
        assert_eq!(
            empty[i], expected,
            "empty hash mismatch at level {}",
            i
        );
    }
}

#[test]
fn test_build_levels_consistency() {
    let hasher = PoseidonHasher::new();
    let tree = NullifierTree::build(four_nullifiers());

    for i in 0..TREE_DEPTH - 1 {
        let prev = &tree.levels[i];
        let next = &tree.levels[i + 1];
        let pairs = prev.len() / 2;
        for j in 0..pairs {
            let expected = hasher.hash(prev[j * 2], prev[j * 2 + 1]);
            assert_eq!(
                next[j], expected,
                "level {} node {} does not match hash of level {} children",
                i + 1, j, i
            );
        }
    }

    let top = &tree.levels[TREE_DEPTH - 1];
    let expected_root = hasher.hash(top[0], top[1]);
    assert_eq!(tree.root(), expected_root);
}

#[test]
fn test_leaves_accessor() {
    let tree = NullifierTree::build(four_nullifiers());
    let leaves = tree.leaves();
    assert_eq!(leaves.len(), 5);
    let expected = commit_ranges(tree.ranges());
    assert_eq!(leaves, expected.as_slice());
}

#[test]
fn test_find_range_empty_ranges() {
    let ranges: Vec<Range> = vec![];
    assert_eq!(find_range_for_value(&ranges, fp(0)), None);
    assert_eq!(find_range_for_value(&ranges, fp(42)), None);
}

#[test]
fn test_find_range_single_range() {
    let ranges = build_nf_ranges(vec![fp(100)]);
    assert_eq!(ranges.len(), 2);

    assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(99)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(100)), None);
    assert_eq!(find_range_for_value(&ranges, fp(101)), Some(1));
    assert_eq!(find_range_for_value(&ranges, fp(999)), Some(1));
}

#[test]
fn test_find_range_exact_boundaries() {
    let ranges = build_nf_ranges(four_nullifiers());
    assert_eq!(find_range_for_value(&ranges, fp(0)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(11)), Some(1));
    assert_eq!(find_range_for_value(&ranges, fp(21)), Some(2));
    assert_eq!(find_range_for_value(&ranges, fp(31)), Some(3));
    assert_eq!(find_range_for_value(&ranges, fp(41)), Some(4));

    assert_eq!(find_range_for_value(&ranges, fp(9)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(19)), Some(1));
    assert_eq!(find_range_for_value(&ranges, fp(29)), Some(2));
    assert_eq!(find_range_for_value(&ranges, fp(39)), Some(3));
}

#[test]
fn test_find_range_consecutive_nullifiers() {
    let ranges = build_nf_ranges(vec![fp(10), fp(11), fp(12)]);
    assert_eq!(ranges.len(), 2);

    assert_eq!(find_range_for_value(&ranges, fp(5)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(9)), Some(0));
    assert_eq!(find_range_for_value(&ranges, fp(10)), None);
    assert_eq!(find_range_for_value(&ranges, fp(11)), None);
    assert_eq!(find_range_for_value(&ranges, fp(12)), None);
    assert_eq!(find_range_for_value(&ranges, fp(13)), Some(1));
}

#[test]
fn test_find_range_binary_search_large_set() {
    let nullifiers: Vec<Fp> = (0..10_000u64).map(|i| fp(i * 3 + 1)).collect();
    let ranges = build_nf_ranges(nullifiers.clone());

    for nf in &nullifiers {
        assert!(find_range_for_value(&ranges, *nf).is_none());
    }

    for (i, window) in nullifiers.windows(2).enumerate() {
        let mid = window[0] + Fp::one();
        let result = find_range_for_value(&ranges, mid);
        assert!(
            result.is_some(),
            "mid-gap value between nf[{}] and nf[{}] not found",
            i,
            i + 1
        );
        let idx = result.unwrap();
        let [low, high] = ranges[idx];
        assert!(
            mid >= low && mid <= high,
            "value not within returned range at index {}",
            idx
        );
    }
}

#[test]
fn test_find_range_agrees_with_linear_scan() {
    fn linear_find(ranges: &[Range], value: Fp) -> Option<usize> {
        for (i, [low, high]) in ranges.iter().enumerate() {
            if value >= *low && value <= *high {
                return Some(i);
            }
        }
        None
    }

    let nullifiers: Vec<Fp> = (0..500u64).map(|i| fp(i * 7 + 3)).collect();
    let ranges = build_nf_ranges(nullifiers);

    for v in 0..4000u64 {
        let val = fp(v);
        assert_eq!(
            find_range_for_value(&ranges, val),
            linear_find(&ranges, val),
            "disagreement at value {}",
            v
        );
    }
}

// -- Tree behavior at different scales ------------------------------------

#[test]
fn test_single_nullifier_tree() {
    let tree = NullifierTree::build(vec![fp(100)]);
    assert_eq!(tree.len(), 2);

    let ranges = tree.ranges();
    assert_eq!(ranges[0], [fp(0), fp(99)]);
    assert_eq!(ranges[1][0], fp(101));
    assert_eq!(ranges[1][1], Fp::one().neg());

    let proof_low = tree.prove(fp(50)).unwrap();
    assert_eq!(proof_low.leaf_pos, 0);
    assert!(proof_low.verify(fp(50)));

    let proof_high = tree.prove(fp(200)).unwrap();
    assert_eq!(proof_high.leaf_pos, 1);
    assert!(proof_high.verify(fp(200)));

    assert!(tree.prove(fp(100)).is_none());
}

#[test]
fn test_consecutive_nullifiers_collapse_gap() {
    let tree = NullifierTree::build(vec![fp(5), fp(6), fp(7)]);

    assert_eq!(tree.len(), 2);
    assert_eq!(tree.ranges()[0], [fp(0), fp(4)]);
    assert_eq!(tree.ranges()[1][0], fp(8));

    assert!(tree.prove(fp(2)).unwrap().verify(fp(2)));
    assert!(tree.prove(fp(100)).unwrap().verify(fp(100)));

    for nf in [5u64, 6, 7] {
        assert!(tree.prove(fp(nf)).is_none(), "nullifier {} should have no proof", nf);
    }
}

#[test]
fn test_adjacent_nullifiers_differ_by_one() {
    let tree = NullifierTree::build(vec![fp(5), fp(6)]);

    assert_eq!(tree.len(), 2);
    assert_eq!(tree.ranges()[0], [fp(0), fp(4)]);
    assert_eq!(tree.ranges()[1][0], fp(7));

    assert!(tree.prove(fp(4)).unwrap().verify(fp(4)));
    assert!(tree.prove(fp(7)).unwrap().verify(fp(7)));
    assert!(tree.prove(fp(5)).is_none());
    assert!(tree.prove(fp(6)).is_none());
}

#[test]
fn test_nullifier_at_zero() {
    let tree = NullifierTree::build(vec![Fp::zero()]);
    assert_eq!(tree.len(), 1);
    assert_eq!(tree.ranges()[0][0], fp(1));
    assert_eq!(tree.ranges()[0][1], Fp::one().neg());

    assert!(tree.prove(Fp::zero()).is_none());
    assert!(tree.prove(fp(1)).unwrap().verify(fp(1)));
    assert!(tree.prove(fp(1000)).unwrap().verify(fp(1000)));
}

#[test]
fn test_nullifier_at_zero_and_one() {
    let tree = NullifierTree::build(vec![Fp::zero(), fp(1)]);
    assert_eq!(tree.len(), 1);
    assert_eq!(tree.ranges()[0][0], fp(2));

    assert!(tree.prove(Fp::zero()).is_none());
    assert!(tree.prove(fp(1)).is_none());
    assert!(tree.prove(fp(2)).unwrap().verify(fp(2)));
}

#[test]
fn test_larger_tree_200_nullifiers() {
    let nullifiers: Vec<Fp> = (1..=200u64).map(|i| fp(i * 1000)).collect();
    let tree = NullifierTree::build(nullifiers.clone());

    assert_eq!(tree.len(), 201);

    let test_indices = [0usize, 1, 50, 100, 150, 199, 200];
    for &idx in &test_indices {
        let range = tree.ranges()[idx];
        let value = range[0];
        let proof = tree.prove(value).unwrap();
        assert_eq!(proof.leaf_pos, idx as u32);
        assert!(proof.verify(value), "proof at leaf index {} does not verify", idx);
    }

    for nf in &nullifiers {
        assert!(tree.prove(*nf).is_none());
    }
}

#[test]
fn test_larger_tree_different_sizes_have_different_roots() {
    let tree_100 = NullifierTree::build((1..=100u64).map(fp));
    let tree_200 = NullifierTree::build((1..=200u64).map(fp));
    assert_ne!(tree_100.root(), tree_200.root());
}

#[test]
fn test_duplicate_nullifiers_produce_same_tree() {
    let with_dups = NullifierTree::build(vec![fp(10), fp(10), fp(20), fp(20), fp(30)]);
    let without_dups = NullifierTree::build(vec![fp(10), fp(20), fp(30)]);
    assert_eq!(with_dups.root(), without_dups.root());
    assert_eq!(with_dups.ranges(), without_dups.ranges());
}

// ================================================================
// End-to-end sentinel tree + circuit-compatible proof tests
// ================================================================

#[test]
fn test_sentinel_tree_all_ranges_under_2_250() {
    let tree = build_sentinel_tree(&[]).unwrap();

    let two_250 = Fp::from(2u64).pow([250, 0, 0, 0]);

    for (i, [low, high]) in tree.ranges().iter().enumerate() {
        let width = *high - *low;
        let max_width = two_250 - Fp::one();
        let check = max_width - width;
        let repr = check.to_repr();
        assert!(
            repr.as_ref()[31] < 0x40,
            "range {} has width >= 2^250: low={:?}, high={:?}",
            i, low, high
        );
    }
}

#[test]
fn test_sentinel_tree_with_extra_nullifiers() {
    let extras = vec![fp(42), fp(1000000), fp(999999999)];
    let tree = build_sentinel_tree(&extras).unwrap();

    for nf in &extras {
        assert!(tree.prove(*nf).is_none(), "nullifier should be excluded");
    }

    let proof = tree.prove(fp(43)).unwrap();
    assert!(proof.verify(fp(43)));
}

#[test]
fn test_proof_fields_match_tree() {
    let tree = build_sentinel_tree(&[fp(42), fp(100)]).unwrap();
    let value = fp(50);

    let proof = tree.prove(value).expect("value should be in a gap");
    assert_eq!(proof.root, tree.root());
    assert_eq!(proof.path.len(), TREE_DEPTH);
    assert!(proof.verify(value));
}

#[test]
fn test_proof_rejects_wrong_value() {
    let tree = build_sentinel_tree(&[fp(42), fp(100)]).unwrap();
    let value = fp(50);
    let proof = tree.prove(value).expect("value should be in a gap");

    assert!(!proof.verify(fp(42)), "nullifier should not verify");
    assert!(!proof.verify(fp(100)), "nullifier should not verify");
}

#[test]
fn test_e2e_sentinel_tree_proof_gen_and_verify() {
    let extra_nfs = vec![fp(12345), fp(67890), fp(111111)];
    let tree = build_sentinel_tree(&extra_nfs).unwrap();

    let test_value = fp(50000);
    assert!(tree.prove(test_value).is_some(), "test value should be in a gap range");

    let proof = tree.prove(test_value).unwrap();
    assert!(proof.verify(test_value));
    assert_eq!(proof.path.len(), TREE_DEPTH);

    let tree2 = build_sentinel_tree(&extra_nfs).unwrap();
    assert_eq!(tree.root(), tree2.root());
}

#[test]
fn test_empty_hashes_match_circuit_convention() {
    let hasher = PoseidonHasher::new();
    let empty = precompute_empty_hashes();
    let expected_leaf = hasher.hash(Fp::zero(), Fp::zero());
    assert_eq!(empty[0], expected_leaf);

    for i in 1..TREE_DEPTH {
        assert_eq!(empty[i], hasher.hash(empty[i - 1], empty[i - 1]));
    }
}

#[test]
fn test_poseidon_hasher_equivalence() {
    // Compare PoseidonHasher against the canonical poseidon::Hash implementation.
    let hasher = PoseidonHasher::new();
    let canonical = |l: Fp, r: Fp| -> Fp {
        poseidon::Hash::<_, P128Pow5T3, ConstantLength<2>, 3, 2>::init().hash([l, r])
    };

    assert_eq!(
        hasher.hash(Fp::zero(), Fp::zero()),
        canonical(Fp::zero(), Fp::zero()),
    );

    assert_eq!(hasher.hash(fp(1), fp(2)), canonical(fp(1), fp(2)));
    assert_eq!(hasher.hash(fp(42), fp(0)), canonical(fp(42), fp(0)));

    let a = fp(0xDEAD_BEEF);
    let b = fp(0xCAFE_BABE);
    assert_eq!(hasher.hash(a, b), canonical(a, b));

    assert_eq!(
        hasher.hash(Fp::one().neg(), Fp::one()),
        canonical(Fp::one().neg(), Fp::one()),
    );
}

/// Frozen test vectors for Poseidon P128Pow5T3 ConstantLength<2> over Pallas.
/// Generated from the canonical `poseidon::Hash` implementation. These protect
/// against accidental changes to the permutation (e.g. optimized partial rounds).
#[test]
fn test_poseidon_frozen_vectors() {
    let hasher = PoseidonHasher::new();

    let from_hex = |s: &str| -> Fp {
        let bytes: [u8; 32] = hex::decode(s).unwrap().try_into().unwrap();
        Fp::from_repr(bytes).unwrap()
    };

    // (0, 0)
    assert_eq!(
        hasher.hash(Fp::zero(), Fp::zero()),
        from_hex("7a515983cec6c21e27c2f24fbc31c54d698400d33300ebc7f4677cb71b529403"),
    );
    // (1, 2)
    assert_eq!(
        hasher.hash(fp(1), fp(2)),
        from_hex("4ce3bd9407dc758983c62390ce00463beb82796eb0d40a0398993cb4eca55535"),
    );
    // (42, 0)
    assert_eq!(
        hasher.hash(fp(42), fp(0)),
        from_hex("fad8a97bb5213839cff67906a2d74baa2b889ae882b3c44f3c0721c7edadaf3d"),
    );
    // (0xDEAD_BEEF, 0xCAFE_BABE)
    assert_eq!(
        hasher.hash(Fp::from(0xDEAD_BEEFu64), Fp::from(0xCAFE_BABEu64)),
        from_hex("c2f13f05353ed3b31f348fd82539ed31649c8d31ee12ea0f9da8c22ba1c5b724"),
    );
    // (p-1, 1)
    assert_eq!(
        hasher.hash(Fp::one().neg(), Fp::one()),
        from_hex("576b8132d0cba1b8232040b6f89a15e52ef26ada02dda96709f3212a9234d414"),
    );
    // (u64::MAX, u64::MAX)
    assert_eq!(
        hasher.hash(Fp::from(u64::MAX), Fp::from(u64::MAX)),
        from_hex("d356503f556176a90fbccd1422c5d7fbf4eff2a2481921ae1edfbd1156eecb31"),
    );
    // (1, 1)
    assert_eq!(
        hasher.hash(Fp::one(), Fp::one()),
        from_hex("22ebbf1ee67e974899f33bba822e29877168fe77058b27d00ca332118382b01b"),
    );
    // (0, 1)
    assert_eq!(
        hasher.hash(Fp::zero(), Fp::one()),
        from_hex("8358d711a0329d38becd54fba7c283ed3e089a39c91b6a9d10efb02bc3f12f06"),
    );
}
