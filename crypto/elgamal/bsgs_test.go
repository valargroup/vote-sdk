package elgamal

import (
	"crypto/rand"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
)

// TestBSGSSolveKnownValues verifies BSGS recovers known values from v*G.
func TestBSGSSolveKnownValues(t *testing.T) {
	table := NewBSGSTable(1 << 16) // bound = 65536

	G := PallasGenerator()

	values := []uint64{0, 1, 2, 3, 7, 42, 100, 255, 1000, 12345, 65535}
	for _, v := range values {
		point := G.Mul(scalarFromUint64(v))
		got, err := table.Solve(point)
		require.NoError(t, err, "should solve for v=%d", v)
		require.Equal(t, v, got, "should recover v=%d", v)
	}
}

// TestBSGSSolveZero verifies that 0*G (identity) is correctly solved.
func TestBSGSSolveZero(t *testing.T) {
	table := NewBSGSTable(1 << 16)
	id := new(curvey.PointPallas).Identity()
	got, err := table.Solve(id)
	require.NoError(t, err)
	require.Equal(t, uint64(0), got)
}

// TestBSGSSolveBoundary verifies BSGS works at the exact boundary N-1.
func TestBSGSSolveBoundary(t *testing.T) {
	N := uint64(1000)
	table := NewBSGSTable(N)
	G := PallasGenerator()

	// N-1 should succeed
	point := G.Mul(scalarFromUint64(N - 1))
	got, err := table.Solve(point)
	require.NoError(t, err)
	require.Equal(t, N-1, got)

	// N should fail (out of range)
	pointN := G.Mul(scalarFromUint64(N))
	_, err = table.Solve(pointN)
	require.Error(t, err, "v=N should be out of range")
}

// TestBSGSSolveSmallBound verifies BSGS with a very small table.
func TestBSGSSolveSmallBound(t *testing.T) {
	table := NewBSGSTable(10)
	G := PallasGenerator()

	for v := uint64(0); v < 10; v++ {
		point := G.Mul(scalarFromUint64(v))
		got, err := table.Solve(point)
		require.NoError(t, err, "should solve v=%d", v)
		require.Equal(t, v, got)
	}

	// 10 should fail
	point := G.Mul(scalarFromUint64(10))
	_, err := table.Solve(point)
	require.Error(t, err)
}

// TestBSGSSolvePowerOfTwo verifies BSGS with a power-of-two bound (perfect square).
func TestBSGSSolvePowerOfTwo(t *testing.T) {
	table := NewBSGSTable(256) // sqrt(256) = 16
	G := PallasGenerator()

	// Test several values across the range
	for _, v := range []uint64{0, 1, 15, 16, 17, 100, 200, 255} {
		point := G.Mul(scalarFromUint64(v))
		got, err := table.Solve(point)
		require.NoError(t, err, "should solve v=%d", v)
		require.Equal(t, v, got)
	}
}

// TestBSGSSolveNotPerfectSquare verifies BSGS with a non-perfect-square bound.
func TestBSGSSolveNotPerfectSquare(t *testing.T) {
	table := NewBSGSTable(1000) // ceil(sqrt(1000)) = 32
	G := PallasGenerator()

	for _, v := range []uint64{0, 1, 31, 32, 33, 500, 999} {
		point := G.Mul(scalarFromUint64(v))
		got, err := table.Solve(point)
		require.NoError(t, err, "should solve v=%d", v)
		require.Equal(t, v, got)
	}
}

// TestBSGSSolveOutOfRange verifies error on values outside the bound.
func TestBSGSSolveOutOfRange(t *testing.T) {
	table := NewBSGSTable(100)
	G := PallasGenerator()

	// Values at and beyond bound should fail
	for _, v := range []uint64{100, 101, 200, 1000} {
		point := G.Mul(scalarFromUint64(v))
		_, err := table.Solve(point)
		require.Error(t, err, "v=%d should be out of range for N=100", v)
	}
}

// TestBSGSZeroBound verifies a zero-bound table returns an error.
func TestBSGSZeroBound(t *testing.T) {
	table := NewBSGSTable(0)
	id := new(curvey.PointPallas).Identity()
	_, err := table.Solve(id)
	require.Error(t, err)
}

// TestBSGSWithEncryptDecrypt integrates BSGS with El Gamal encrypt/decrypt
// to verify full plaintext recovery.
func TestBSGSWithEncryptDecrypt(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	table := NewBSGSTable(1 << 16)

	values := []uint64{0, 1, 42, 100, 1000, 65535}
	for _, v := range values {
		ct := mustEncrypt(t, pk, v)
		vGot := DecryptToPoint(sk, ct)
		plaintext, err := table.Solve(vGot)
		require.NoError(t, err, "should recover v=%d after encrypt+decrypt", v)
		require.Equal(t, v, plaintext)
	}
}

// TestBSGSWithHomomorphicSum integrates BSGS with homomorphic addition
// to verify aggregate plaintext recovery.
func TestBSGSWithHomomorphicSum(t *testing.T) {
	sk, pk := KeyGen(rand.Reader)
	table := NewBSGSTable(1 << 16)

	shares := []uint64{64, 32, 2, 1} // total = 99
	acc, err := EncryptZero(pk, rand.Reader)
	require.NoError(t, err)
	for _, v := range shares {
		acc = HomomorphicAdd(acc, mustEncrypt(t, pk, v))
	}

	vGot := DecryptToPoint(sk, acc)
	total, err := table.Solve(vGot)
	require.NoError(t, err)
	require.Equal(t, uint64(99), total)
}

// TestBSGSLargerBound tests BSGS with a 2^20 bound to verify correctness
// with larger tables.
func TestBSGSLargerBound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping larger BSGS table test in short mode")
	}

	table := NewBSGSTable(1 << 20)
	G := PallasGenerator()

	// Test values spread across the range
	values := []uint64{0, 1, 1023, 1024, 65535, 65536, 500000, (1 << 20) - 1}
	for _, v := range values {
		point := G.Mul(scalarFromUint64(v))
		got, err := table.Solve(point)
		require.NoError(t, err, "should solve v=%d", v)
		require.Equal(t, v, got)
	}
}

// TestCeilSqrt verifies the ceilSqrt helper.
func TestCeilSqrt(t *testing.T) {
	cases := []struct {
		n    uint64
		want uint64
	}{
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 2},
		{4, 2},
		{5, 3},
		{9, 3},
		{10, 4},
		{16, 4},
		{17, 5},
		{100, 10},
		{256, 16},
		{1000, 32},
		{65536, 256},
		{1 << 32, 1 << 16},
	}
	for _, tc := range cases {
		got := ceilSqrt(tc.n)
		require.Equal(t, tc.want, got, "ceilSqrt(%d)", tc.n)
		// Verify invariant: got^2 >= n and (got-1)^2 < n (for n > 0)
		if tc.n > 0 {
			require.GreaterOrEqual(t, got*got, tc.n, "got^2 >= n for n=%d", tc.n)
			if got > 0 {
				require.Less(t, (got-1)*(got-1), tc.n, "(got-1)^2 < n for n=%d", tc.n)
			}
		}
	}
}
