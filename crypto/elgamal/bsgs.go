package elgamal

import (
	"fmt"
	"math"
	"math/big"

	"github.com/mikelodder7/curvey"
)

// BSGSTable precomputes a lookup table for baby-step giant-step discrete log.
//
// Given a point h = x * G where x is bounded by [0, N), the Solve method
// recovers x in O(√N) time using O(√N) memory.
//
// The key insight: write x = i·m + j where m = ⌈√N⌉ and 0 ≤ i, j < m. Then:
//
//	g^(i·m + j) = h   ⟹   g^j = h · (g^(-m))^i
//
// This splits one search over N values into two searches over √N values each
// — a classic meet-in-the-middle decomposition.
type BSGSTable struct {
	table map[[32]byte]uint64 // baby-step: compressed j*G → index j
	m     uint64              // ⌈√N⌉, number of steps per phase
	mG    curvey.Point        // m * G, the giant-step stride
	n     uint64              // upper bound N on the discrete log
}

// NewBSGSTable precomputes a baby-step table for discrete logs in [0, N).
//
// Baby steps: store j*G for j = 0, 1, …, m−1 in a hash table keyed by the
// 32-byte compressed point. Memory usage is approximately √N × 40 bytes.
// For the default N = 2^32, this is m = 2^16 = 65 536 entries (~2.5 MB).
//
// This constructor is expensive (O(√N) point additions) and should be called
// once and reused across multiple Solve calls.
func NewBSGSTable(N uint64) *BSGSTable {
	if N == 0 {
		return &BSGSTable{
			table: make(map[[32]byte]uint64),
			m:     0,
			mG:    new(curvey.PointPallas).Identity(),
			n:     0,
		}
	}

	m := ceilSqrt(N)

	G := PallasGenerator()
	table := make(map[[32]byte]uint64, m)

	// Baby steps: compute j*G for j = 0, 1, …, m−1 and store compressed point → j.
	// Incremental addition: current = current + G each iteration.
	current := new(curvey.PointPallas).Identity() // 0 * G
	for j := uint64(0); j < m; j++ {
		key := pointToKey(current)
		table[key] = j
		current = current.Add(G) // (j+1) * G
	}

	// Precompute m * G — the giant-step stride.
	mScalar := scalarFromUint64(m)
	mG := G.Mul(mScalar)

	return &BSGSTable{
		table: table,
		m:     m,
		mG:    mG,
		n:     N,
	}
}

// Solve recovers the discrete log x from h = x * G, where x ∈ [0, N).
// Returns an error if no solution is found within the bound.
//
// Giant steps: for i = 0, 1, 2, …, compute candidate = h · (G^(−m))^i and
// check whether the candidate appears in the baby-step table:
//
//  1. candidate ← h − i·(m·G)
//  2. Look up candidate in the baby-step table
//  3. If found at index j, then x = i·m + j
//
// Worst case: ⌈√N⌉ giant-step iterations, each involving one point
// subtraction, one point serialization, and one map lookup.
func (t *BSGSTable) Solve(h curvey.Point) (uint64, error) {
	if t.n == 0 {
		return 0, fmt.Errorf("bsgs: table bound is 0, no solutions possible")
	}

	// Giant steps: subtract i·(m·G) from h and look up in baby-step table.
	// candidate starts at h and we subtract m·G each iteration.
	candidate := h
	maxI := t.m // at most m giant steps needed to cover [0, N)
	// Since m·m ≥ N, m giant steps always suffice.

	for i := uint64(0); i <= maxI; i++ {
		key := pointToKey(candidate)
		if j, ok := t.table[key]; ok {
			x := i*t.m + j
			if x < t.n {
				return x, nil
			}
			// x ≥ N: valid collision but outside our bound.
			// Continue searching (defensive; won't happen for reasonable N).
		}
		candidate = candidate.Sub(t.mG) // h − (i+1)·m·G
	}

	return 0, fmt.Errorf("bsgs: no solution found for x in [0, %d)", t.n)
}

// pointToKey converts a curve point to a 32-byte array key for map lookups.
// Uses the compressed affine encoding (32 bytes for Pallas points).
func pointToKey(p curvey.Point) [32]byte {
	bs := p.ToAffineCompressed()
	var key [32]byte
	copy(key[:], bs)
	return key
}

// ceilSqrt returns ceil(sqrt(n)).
func ceilSqrt(n uint64) uint64 {
	if n <= 1 {
		return n
	}
	s := uint64(math.Ceil(math.Sqrt(float64(n))))
	// math.Sqrt on float64 may be imprecise for large uint64 values.
	// Verify and adjust: we need s such that s*s >= n and (s-1)*(s-1) < n.
	bi := new(big.Int).SetUint64(n)
	sb := new(big.Int).SetUint64(s)
	sq := new(big.Int).Mul(sb, sb)
	if sq.Cmp(bi) < 0 {
		s++
	}
	return s
}
