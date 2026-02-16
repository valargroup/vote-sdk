use ff::PrimeField as _;
use halo2_gadgets::poseidon::primitives::{P128Pow5T3, Spec};
use pasta_curves::Fp;

/// A reusable Poseidon hasher that avoids per-call initialisation overhead.
///
/// `poseidon::Hash::init()` calls `P128Pow5T3::constants()` every time,
/// heap-allocating and copying 64 round constants (~6 KiB). During tree
/// building this adds up to ~128 M unnecessary allocations. `PoseidonHasher`
/// computes the constants once and implements the permutation inline,
/// producing identical results to the canonical `poseidon::Hash` API.
///
/// Correctness is verified by `test_poseidon_hasher_equivalence`.
pub(crate) struct PoseidonHasher {
    round_constants: Vec<[Fp; 3]>,
    mds: [[Fp; 3]; 3],
    /// `ConstantLength<2>` capacity element: `L * 2^64` where `L = 2`.
    initial_capacity: Fp,
}

impl PoseidonHasher {
    /// Create a new hasher, computing round constants and MDS matrix once.
    pub(crate) fn new() -> Self {
        let (round_constants, mds, _) = P128Pow5T3::constants();
        // ConstantLength<L> encodes capacity as L * 2^64 (with output length 1).
        let initial_capacity = Fp::from_u128(2u128 << 64);
        PoseidonHasher {
            round_constants,
            mds,
            initial_capacity,
        }
    }

    /// Hash two field elements using Poseidon.
    ///
    /// For `ConstantLength<2>` with width = 3, rate = 2 the sponge absorbs
    /// both inputs in a single block (no padding), so the hash reduces to:
    ///
    /// ```text
    /// state = [left, right, capacity]
    /// permute(&mut state)
    /// return state[0]
    /// ```
    ///
    /// This equivalence is proven by the `orchard_spec_equivalence` test in
    /// halo2_gadgets and validated locally by `test_poseidon_hasher_equivalence`.
    #[inline]
    pub(crate) fn hash(&self, left: Fp, right: Fp) -> Fp {
        let mut state = [left, right, self.initial_capacity];
        self.permute(&mut state);
        state[0]
    }

    /// Poseidon permutation with P128Pow5T3 parameters (R_F = 8, R_P = 56).
    fn permute(&self, state: &mut [Fp; 3]) {
        const R_F_HALF: usize = 4; // full_rounds / 2
        const R_P: usize = 56;

        let rcs = &self.round_constants;
        let mut ri = 0;

        // First half: full rounds (S-box on every element).
        for _ in 0..R_F_HALF {
            let rc = &rcs[ri];
            state[0] = Self::pow5(state[0] + rc[0]);
            state[1] = Self::pow5(state[1] + rc[1]);
            state[2] = Self::pow5(state[2] + rc[2]);
            self.apply_mds(state);
            ri += 1;
        }

        // Partial rounds (S-box on first element only).
        for _ in 0..R_P {
            let rc = &rcs[ri];
            state[0] += rc[0];
            state[1] += rc[1];
            state[2] += rc[2];
            state[0] = Self::pow5(state[0]);
            self.apply_mds(state);
            ri += 1;
        }

        // Second half: full rounds.
        for _ in 0..R_F_HALF {
            let rc = &rcs[ri];
            state[0] = Self::pow5(state[0] + rc[0]);
            state[1] = Self::pow5(state[1] + rc[1]);
            state[2] = Self::pow5(state[2] + rc[2]);
            self.apply_mds(state);
            ri += 1;
        }
    }

    /// x^5 via explicit squaring: 3 multiplications instead of
    /// the generic variable-time exponentiation loop.
    #[inline(always)]
    fn pow5(x: Fp) -> Fp {
        let x2 = x.square();
        let x4 = x2.square();
        x4 * x
    }

    #[inline(always)]
    fn apply_mds(&self, state: &mut [Fp; 3]) {
        let [s0, s1, s2] = *state;
        state[0] = self.mds[0][0] * s0 + self.mds[0][1] * s1 + self.mds[0][2] * s2;
        state[1] = self.mds[1][0] * s0 + self.mds[1][1] * s1 + self.mds[1][2] * s2;
        state[2] = self.mds[2][0] * s0 + self.mds[2][1] * s1 + self.mds[2][2] * s2;
    }
}
