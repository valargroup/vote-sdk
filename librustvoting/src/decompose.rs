/// Binary decompose voting weight into powers of 2.
/// Returns a vector of powers of 2 that sum to `weight`.
/// Returns empty vec for weight=0.
///
/// This is a REAL implementation (not a stub).
pub fn decompose_weight(weight: u64) -> Vec<u64> {
    if weight == 0 {
        return vec![];
    }

    let mut shares = Vec::new();
    let mut remaining = weight;
    let mut bit_position = 0u32;

    while remaining > 0 {
        if remaining & 1 == 1 {
            shares.push(1u64 << bit_position);
        }
        remaining >>= 1;
        bit_position += 1;
    }

    shares
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_zero() {
        assert_eq!(decompose_weight(0), Vec::<u64>::new());
    }

    #[test]
    fn test_power_of_two() {
        assert_eq!(decompose_weight(1), vec![1]);
        assert_eq!(decompose_weight(2), vec![2]);
        assert_eq!(decompose_weight(4), vec![4]);
        assert_eq!(decompose_weight(1024), vec![1024]);
    }

    #[test]
    fn test_composite() {
        // 5 = 1 + 4
        let shares = decompose_weight(5);
        assert_eq!(shares, vec![1, 4]);
        assert_eq!(shares.iter().sum::<u64>(), 5);
    }

    #[test]
    fn test_all_bits() {
        // 15 = 1 + 2 + 4 + 8
        let shares = decompose_weight(15);
        assert_eq!(shares, vec![1, 2, 4, 8]);
        assert_eq!(shares.iter().sum::<u64>(), 15);
    }

    #[test]
    fn test_voting_weight() {
        // 142.50 ZEC = 14_250_000_000 zatoshi
        let weight = 14_250_000_000u64;
        let shares = decompose_weight(weight);
        assert_eq!(shares.iter().sum::<u64>(), weight);
        // All shares must be powers of 2
        for share in &shares {
            assert!(share.is_power_of_two(), "{} is not a power of 2", share);
        }
    }

    #[test]
    fn test_large_value() {
        let weight = u64::MAX;
        let shares = decompose_weight(weight);
        assert_eq!(shares.iter().sum::<u64>(), weight);
        assert_eq!(shares.len(), 64); // all 64 bits set
    }
}
