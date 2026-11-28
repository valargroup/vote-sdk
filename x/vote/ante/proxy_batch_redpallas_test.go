//go:build redpallas && proxyenabled

package ante_test

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"math/bits"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests sign D1 with real RedPallas SpendAuth keys and verify the
// signatures through the ante with the FFI verifier (contracts-v1 §4.1,
// §5.1 step 6). Go has no RedPallas signer, so the test carries one: RedDSA
// over Pallas with the SpendAuth basepoint and H* = BLAKE2b-512
// personalized "Zcash_RedPallasH", reduced mod the scalar field order.

// pallasScalarOrder is q, the order of Pallas's scalar field.
var pallasScalarOrder, _ = new(big.Int).SetString("40000000000000000000000000000000224698fc0994a8dd8c46eb2100000001", 16)

// blake2bIV is BLAKE2b's initialization vector.
var blake2bIV = [8]uint64{
	0x6a09e667f3bcc908, 0xbb67ae8584caa73b, 0x3c6ef372fe94f82b, 0xa54ff53a5f1d36f1,
	0x510e527fade682d1, 0x9b05688c2b3e6c1f, 0x1f83d9abfb41bd6b, 0x5be0cd19137e2179,
}

// blake2bSigma is BLAKE2b's message schedule for its 12 rounds.
var blake2bSigma = [12][16]byte{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
	{11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4},
	{7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8},
	{9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13},
	{2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9},
	{12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11},
	{13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10},
	{6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5},
	{10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0},
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	{14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3},
}

// blake2b512Personal returns unkeyed BLAKE2b-512 of msg under a 16-byte
// personalization, which golang.org/x/crypto/blake2b does not offer.
func blake2b512Personal(personal string, msg []byte) [64]byte {
	h := blake2bIV
	h[0] ^= 0x01010000 ^ 64
	h[6] ^= binary.LittleEndian.Uint64([]byte(personal)[:8])
	h[7] ^= binary.LittleEndian.Uint64([]byte(personal)[8:16])
	var counter uint64
	for len(msg) > 128 {
		counter += 128
		blake2bCompress(&h, msg[:128], counter, false)
		msg = msg[128:]
	}
	var last [128]byte
	copy(last[:], msg)
	counter += uint64(len(msg))
	blake2bCompress(&h, last[:], counter, true)
	var out [64]byte
	for i, word := range h {
		binary.LittleEndian.PutUint64(out[8*i:], word)
	}
	return out
}

// blake2bCompress mixes one 128-byte block into h; counter is the number of
// message bytes so far, and final marks the last block.
func blake2bCompress(h *[8]uint64, block []byte, counter uint64, final bool) {
	var m [16]uint64
	for i := range m {
		m[i] = binary.LittleEndian.Uint64(block[8*i:])
	}
	var v [16]uint64
	copy(v[:8], h[:])
	copy(v[8:], blake2bIV[:])
	v[12] ^= counter
	if final {
		v[14] = ^v[14]
	}
	mix := func(a, b, c, d int, x, y uint64) {
		v[a] += v[b] + x
		v[d] = bits.RotateLeft64(v[d]^v[a], -32)
		v[c] += v[d]
		v[b] = bits.RotateLeft64(v[b]^v[c], -24)
		v[a] += v[b] + y
		v[d] = bits.RotateLeft64(v[d]^v[a], -16)
		v[c] += v[d]
		v[b] = bits.RotateLeft64(v[b]^v[c], -63)
	}
	for _, s := range blake2bSigma {
		mix(0, 4, 8, 12, m[s[0]], m[s[1]])
		mix(1, 5, 9, 13, m[s[2]], m[s[3]])
		mix(2, 6, 10, 14, m[s[4]], m[s[5]])
		mix(3, 7, 11, 15, m[s[6]], m[s[7]])
		mix(0, 5, 10, 15, m[s[8]], m[s[9]])
		mix(1, 6, 11, 12, m[s[10]], m[s[11]])
		mix(2, 7, 8, 13, m[s[12]], m[s[13]])
		mix(3, 4, 9, 14, m[s[14]], m[s[15]])
	}
	for i := range h {
		h[i] ^= v[i] ^ v[i+8]
	}
}

// redPallasHStar is RedPallas's H*: BLAKE2b-512 personalized
// "Zcash_RedPallasH" over parts, read little-endian and reduced mod q.
func redPallasHStar(parts ...[]byte) curvey.Scalar {
	digest := blake2b512Personal("Zcash_RedPallasH", bytes.Join(parts, nil))
	le := digest[:]
	be := make([]byte, len(le))
	for i, b := range le {
		be[len(le)-1-i] = b
	}
	n := new(big.Int).Mod(new(big.Int).SetBytes(be), pallasScalarOrder)
	scalar, err := new(curvey.ScalarPallas).SetBigInt(n)
	if err != nil {
		panic(err)
	}
	return scalar
}

// redPallasKey derives a SpendAuth key pair from seed: the secret scalar and
// its compressed verification key [sk]·SpendAuthG.
func redPallasKey(seed byte) (curvey.Scalar, []byte) {
	sk := redPallasHStar([]byte("vote-sdk proxy ante test key"), []byte{seed})
	return sk, elgamal.PallasGenerator().Mul(sk).ToAffineCompressed()
}

// redPallasSign returns RedDSA's R || S over msg under sk, whose
// verification key is vk, with a nonce derived from nonceSeed.
func redPallasSign(sk curvey.Scalar, vk, msg []byte, nonceSeed byte) []byte {
	nonce := redPallasHStar(bytes.Repeat([]byte{nonceSeed}, 80), vk, msg)
	r := elgamal.PallasGenerator().Mul(nonce).ToAffineCompressed()
	c := redPallasHStar(r, vk, msg)
	s := nonce.Add(c.Mul(sk)).BigInt().FillBytes(make([]byte, 32))
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return append(r, s...)
}

// TestRedPallasProxyBatchD1 signs a two-DC batch's D1 with real SpendAuth
// keys: the FFI verifier accepts it through the ante, and rejects a
// signature over another digest and a DC grafted from another batch.
func TestRedPallasProxyBatchD1(t *testing.T) {
	s := new(ValidateTestSuite)
	s.SetT(t)
	s.SetupTest()
	s.setupProxyBatchRound(func(*types.VoteRound) {})
	m := s.proxyModes()[0]
	opts := proxyOpts(m, redpallas.NewVerifier(), zkp.NewMockVerifier())
	validate := func(msg *types.MsgProxyDelegationBatch) error {
		return ante.ValidateVoteTx(m.ctx, msg, s.keeper, opts)
	}

	type signer struct {
		sk curvey.Scalar
		vk []byte
	}
	signers := map[uint64]signer{}
	// batch builds a batch of the DCs with seeds, each under its own key.
	batch := func(seeds ...uint64) *types.MsgProxyDelegationBatch {
		msg := proxyAnteBatch(len(seeds), false)
		for i, seed := range seeds {
			sk, vk := redPallasKey(byte(seed))
			signers[seed] = signer{sk, vk}
			msg.Dcs[i] = proxyAnteDC(seed)
			msg.Dcs[i].RVpk = vk
		}
		return msg
	}
	// sign signs every DC of msg over digest.
	sign := func(msg *types.MsgProxyDelegationBatch, digest []byte, seeds ...uint64) {
		for i, seed := range seeds {
			msg.Dcs[i].VoteAuthSig = redPallasSign(signers[seed].sk, signers[seed].vk, digest, byte(i))
		}
	}
	d1 := func(msg *types.MsgProxyDelegationBatch) []byte {
		digest, err := types.ComputeProxyDelegationBatchSighash(proxyAnteChainID, msg)
		require.NoError(t, err)
		return digest
	}

	msg := batch(0x21, 0x22)
	sign(msg, d1(msg), 0x21, 0x22)
	require.NoError(t, validate(msg))

	// A signature over the batch_digest, D1 without the expiry, is not one
	// over D1.
	batchDigest, err := types.ComputeProxyDelegationBatchDigest(proxyAnteChainID, msg)
	require.NoError(t, err)
	sign(msg, batchDigest, 0x21, 0x22)
	s.requireVoteCode(validate(msg), types.ErrInvalidSignature)

	// A DC grafted from another batch keeps a signature over that batch's
	// D1, even when the receiving batch re-signs its own DCs.
	donor := batch(0x23)
	sign(donor, d1(donor), 0x23)
	require.NoError(t, validate(donor))
	grafted := batch(0x21, 0x22)
	grafted.Dcs = append(grafted.Dcs, donor.Dcs[0])
	sign(grafted, d1(grafted), 0x21, 0x22)
	err = validate(grafted)
	s.requireVoteCode(err, types.ErrInvalidSignature)
	require.ErrorContains(t, err, "dcs[2]")
}
