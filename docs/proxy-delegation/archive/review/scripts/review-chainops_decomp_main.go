package main

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
)

func main() {
	_, pk := elgamal.KeyGen(rand.Reader)
	ct, _ := elgamal.Encrypt(pk, 1, rand.Reader)
	bz, _ := elgamal.MarshalCiphertext(ct)
	const N = 10000
	t := time.Now()
	var acc *elgamal.Ciphertext
	for i := 0; i < N; i++ {
		c, err := elgamal.UnmarshalCiphertext(bz)
		if err != nil { panic(err) }
		if acc == nil { acc = c } else { acc = elgamal.HomomorphicAdd(acc, c) }
	}
	d := time.Since(t)
	fmt.Printf("unmarshal+add x%d: %v (%.1f us each)\n", N, d, float64(d.Microseconds())/N)
	t = time.Now()
	for i := 0; i < 50*N; i++ { acc = elgamal.HomomorphicAdd(acc, ct) }
	d = time.Since(t)
	fmt.Printf("add x%d: %v (%.2f us each)\n", 50*N, d, float64(d.Nanoseconds())/1000/float64(50*N))
	t = time.Now()
	for i := 0; i < 400; i++ { _, _ = elgamal.MarshalCiphertext(acc) }
	fmt.Printf("marshal x400: %v\n", time.Since(t))
}
