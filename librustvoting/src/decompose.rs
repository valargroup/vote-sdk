/// Decompose voting weight into exactly 16 shares.
///
/// Votes are split into exactly 16 shares that are each encrypted as El Gamal
/// ciphertexts. We first do a binary decomposition, then distribute the
/// resulting powers-of-2 across 16 buckets using round-robin assignment,
/// summing within each bucket.
///
/// Returns empty vec for weight=0.
pub fn decompose_weight(weight: u64) -> Vec<u64> {
    if weight == 0 {
        return vec![];
    }

    // Binary decompose into powers of 2
    let mut bits = Vec::new();
    let mut remaining = weight;
    let mut bit_position = 0u32;

    while remaining > 0 {
        if remaining & 1 == 1 {
            bits.push(1u64 << bit_position);
        }
        remaining >>= 1;
        bit_position += 1;
    }

    // If 16 or fewer bits set, pad with zeros to exactly 16
    if bits.len() <= 16 {
        bits.resize(16, 0);
        return bits;
    }

    // More than 16 bits set: distribute round-robin into 16 buckets
    let mut shares = vec![0u64; 16];
    for (i, &value) in bits.iter().enumerate() {
        shares[i % 16] += value;
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
        let shares = decompose_weight(1);
        assert_eq!(shares.len(), 16);
        assert_eq!(shares.iter().sum::<u64>(), 1);
    }

    #[test]
    fn test_composite() {
        // 5 = 1 + 4 → [1, 4, 0, ..., 0]
        let shares = decompose_weight(5);
        assert_eq!(shares.len(), 16);
        assert_eq!(shares.iter().sum::<u64>(), 5);
    }

    #[test]
    fn test_sixteen_bits() {
        // 2^16 - 1 = 65535, exactly 16 bits set
        let shares = decompose_weight(65535);
        assert_eq!(shares.len(), 16);
        assert_eq!(shares.iter().sum::<u64>(), 65535);
        // Each share holds one power of 2: [1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768]
        assert_eq!(shares, (0..16).map(|i| 1u64 << i).collect::<Vec<_>>());
    }

    #[test]
    fn test_more_than_sixteen_bits() {
        // 2^17 - 1 = 131071, 17 bits set → bucketed into 16
        let shares = decompose_weight(131071);
        assert_eq!(shares.len(), 16);
        assert_eq!(shares.iter().sum::<u64>(), 131071);
        // Round-robin: bucket[0] gets bit_0 (1) + bit_16 (65536) = 65537
        assert_eq!(shares[0], 1 + 65536);
        assert_eq!(shares[1], 2);
    }

    #[test]
    fn test_voting_weight() {
        // 142.50 ZEC = 14_250_000_000 zatoshi.
        //
        // NOTE: this weight produces shares that may exceed the circuit's
        // per-share range check bound of [0, 2^30) ≈ 1.07B. The round-robin
        // decomposition distributes high-order powers-of-2 into single buckets
        // (e.g. bit 33 → bucket 1 alone = 8.59B >> 2^30). The production
        // builder (vote_proof/builder.rs) uses a safe equal-split strategy
        // instead of this function. decompose_weight is for pre-encryption
        // share generation where the caller is responsible for range validation.
        let weight = 14_250_000_000u64;
        let shares = decompose_weight(weight);
        assert_eq!(shares.len(), 16);
        assert_eq!(shares.iter().sum::<u64>(), weight);
    }

    #[test]
    fn test_real_balance() {
        // The balance from the simulator: 101768753 (well within 16 × 2^30).
        let weight = 101_768_753u64;
        let shares = decompose_weight(weight);
        assert_eq!(shares.len(), 16);
        assert_eq!(shares.iter().sum::<u64>(), weight);
        // All shares fit in the circuit's [0, 2^30) range check.
        assert!(shares.iter().all(|&s| s < (1u64 << 30)));
    }

    #[test]
    fn test_large_value() {
        // u64::MAX is far beyond 16 × 2^30 — this tests sum correctness only.
        // Shares will exceed the circuit range bound.
        let weight = u64::MAX;
        let shares = decompose_weight(weight);
        assert_eq!(shares.len(), 16);
        assert_eq!(shares.iter().sum::<u64>(), weight);
    }
}
