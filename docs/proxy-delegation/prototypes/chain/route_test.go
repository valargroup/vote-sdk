package protochain

import (
	"crypto/rand"
	"encoding/binary"
	"sort"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
)

// rerand derives the deterministic zero-encryption re-randomizer for one routed bucket.
func rerand(pk *elgamal.PublicKey, roundID []byte, p, o uint32, combined *elgamal.Ciphertext) *elgamal.Ciphertext {
	cb, err := elgamal.MarshalCiphertext(combined)
	if err != nil {
		// identity C1/C2 marshals as zeros; MarshalCiphertext should still work
		panic(err)
	}
	buf := []byte("svote-route-rerand-v1")
	buf = append(buf, roundID...)
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], p)
	buf = append(buf, tmp[:]...)
	binary.BigEndian.PutUint32(tmp[:], o)
	buf = append(buf, tmp[:]...)
	buf = append(buf, cb...)
	rho := new(curvey.ScalarPallas).Hash(buf)
	z, err := elgamal.EncryptWithRandomness(pk, 0, rho)
	if err != nil {
		panic(err)
	}
	return elgamal.HomomorphicAdd(combined, z)
}

func scalarU(v uint64) curvey.Scalar { return new(curvey.ScalarPallas).New(int(v)) }

// TestIdentityAttackAndRerandomization shows two attacker-built pools whose
// randomness cancels: naive routing yields identity C1 (DLEQ verify rejects it),
// rerandomized routing does not and still decrypts to the plaintext sum.
func TestIdentityAttackAndRerandomization(t *testing.T) {
	sk, pk := elgamal.KeyGen(rand.Reader)
	r1 := new(curvey.ScalarPallas).Random(rand.Reader)
	r2 := new(curvey.ScalarPallas).Random(rand.Reader)
	r3 := r1.Add(r2).Neg()
	a, _ := elgamal.EncryptWithRandomness(pk, 5, r1)
	b, _ := elgamal.EncryptWithRandomness(pk, 3, r2)
	c, _ := elgamal.EncryptWithRandomness(pk, 7, r3)
	pool1 := elgamal.HomomorphicAdd(a, b) // AddToTally accepts: C1=(r1+r2)G != O
	pool2 := c                            // AddToTally accepts: C1=r3 G != O
	naive := elgamal.HomomorphicAdd(pool1, pool2)
	if !naive.C1.IsIdentity() {
		t.Fatal("expected identity C1 from crafted pools")
	}
	// A validator's partial decryption DLEQ over identity C1 is rejected on chain.
	share := new(curvey.ScalarPallas).Random(rand.Reader)
	proof, err := elgamal.GeneratePartialDecryptDLEQ(share, naive.C1)
	if err == nil {
		vk := elgamal.PallasGenerator().Mul(share)
		if verr := elgamal.VerifyPartialDecryptDLEQ(proof, vk, naive.C1, naive.C1.Mul(share)); verr == nil {
			t.Fatal("expected DLEQ verification to reject identity C1")
		} else {
			t.Logf("naive routing: DLEQ verify rejects identity C1: %v", verr)
		}
	}
	fixed := rerand(pk, make([]byte, 32), 1, 0, naive)
	if fixed.C1.IsIdentity() || fixed.C2.IsIdentity() {
		t.Fatal("rerandomized bucket is identity")
	}
	want := elgamal.ValuePoint(15)
	if !elgamal.DecryptToPoint(sk, fixed).Equal(want) {
		t.Fatal("rerandomized bucket does not decrypt to 15")
	}
}

type route struct{ d, p, o uint32 }

// routeAll is the reference routing algorithm: pools summed per (p,o) in sorted order, then
// added to the direct accumulator and rerandomized.
func routeAll(pk *elgamal.PublicKey, roundID []byte, direct map[[2]uint32]*elgamal.Ciphertext, pools map[uint32]*elgamal.Ciphertext, routes []route) map[[2]uint32]*elgamal.Ciphertext {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].d != routes[j].d {
			return routes[i].d < routes[j].d
		}
		return routes[i].p < routes[j].p
	})
	sums := map[[2]uint32]*elgamal.Ciphertext{}
	for _, r := range routes {
		pool, ok := pools[r.d]
		if !ok {
			continue
		}
		k := [2]uint32{r.p, r.o}
		if s, ok := sums[k]; ok {
			sums[k] = elgamal.HomomorphicAdd(s, pool)
		} else {
			sums[k] = pool
		}
	}
	keys := make([][2]uint32, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	out := map[[2]uint32]*elgamal.Ciphertext{}
	for k, v := range direct {
		out[k] = v
	}
	for _, k := range keys {
		comb := sums[k]
		if base, ok := out[k]; ok {
			comb = elgamal.HomomorphicAdd(base, comb)
		}
		out[k] = rerand(pk, roundID, k[0], k[1], comb)
	}
	return out
}

// TestRoutingConservation checks decrypted bucket totals equal the plaintext model.
func TestRoutingConservation(t *testing.T) {
	sk, pk := elgamal.KeyGen(rand.Reader)
	roundID := make([]byte, 32)
	pools := map[uint32]*elgamal.Ciphertext{}
	poolVal := map[uint32]uint64{}
	for d := uint32(1); d <= 20; d++ {
		v := uint64(d * 11)
		ct, _ := elgamal.Encrypt(pk, v, rand.Reader)
		pools[d] = ct
		poolVal[d] = v
	}
	direct := map[[2]uint32]*elgamal.Ciphertext{}
	directVal := map[[2]uint32]uint64{}
	for p := uint32(1); p <= 5; p++ {
		ct, _ := elgamal.Encrypt(pk, uint64(p*100), rand.Reader)
		direct[[2]uint32{p, 0}] = ct
		directVal[[2]uint32{p, 0}] = uint64(p * 100)
	}
	var routes []route
	want := map[[2]uint32]uint64{}
	for k, v := range directVal {
		want[k] = v
	}
	for d := uint32(1); d <= 20; d++ {
		for p := uint32(1); p <= 5; p++ {
			if (d+p)%4 == 0 {
				continue // unrouted -> abstain
			}
			o := (d * p) % 3
			routes = append(routes, route{d, p, o})
			want[[2]uint32{p, o}] += poolVal[d]
		}
	}
	got := routeAll(pk, roundID, direct, pools, routes)
	for k, v := range want {
		if !elgamal.DecryptToPoint(sk, got[k]).Equal(elgamal.ValuePoint(v)) {
			t.Fatalf("bucket %v mismatch", k)
		}
	}
	// Determinism: shuffled route order gives byte-identical accumulators.
	shuffled := append([]route(nil), routes...)
	for i := range shuffled {
		j := len(shuffled) - 1 - i
		if i < j {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		}
	}
	got2 := routeAll(pk, roundID, direct, pools, shuffled)
	for k := range got {
		a, _ := elgamal.MarshalCiphertext(got[k])
		b, _ := elgamal.MarshalCiphertext(got2[k])
		if string(a) != string(b) {
			t.Fatalf("non-deterministic bucket %v", k)
		}
	}
}

// BenchmarkRouting measures EndBlock routing cost for D delegates each routing 50 proposals.
func BenchmarkRouting1000x50(b *testing.B) { benchRouting(b, 1000) }
func BenchmarkRouting10000x50(b *testing.B) { benchRouting(b, 10000) }

func benchRouting(b *testing.B, D int) {
	_, pk := elgamal.KeyGen(rand.Reader)
	roundID := make([]byte, 32)
	pools := map[uint32]*elgamal.Ciphertext{}
	base, _ := elgamal.Encrypt(pk, 1, rand.Reader)
	for d := 1; d <= D; d++ {
		pools[uint32(d)] = base
	}
	var routes []route
	for d := 1; d <= D; d++ {
		for p := 1; p <= 50; p++ {
			routes = append(routes, route{uint32(d), uint32(p), uint32((d + p) % 8)})
		}
	}
	direct := map[[2]uint32]*elgamal.Ciphertext{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = routeAll(pk, roundID, direct, pools, append([]route(nil), routes...))
	}
}
