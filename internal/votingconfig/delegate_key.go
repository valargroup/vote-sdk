package votingconfig

import "strings"

// DelegateKeyHRP is the bech32m human-readable part of a delegate key's
// display form, zvdk1… (contracts-v1 §11.2).
const DelegateKeyHRP = "zvdk"

// bech32Charset is the bech32 data alphabet (BIP-173).
const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// EncodeDelegateKey returns dk's display form (contracts-v1 §11.2): bech32m
// (BIP-350) with HRP DelegateKeyHRP and zero padding bits.
func EncodeDelegateKey(dk [32]byte) string {
	const hrp = DelegateKeyHRP
	data := make([]byte, 0, (len(dk)*8+4)/5+6)
	var acc uint32
	bits := 0
	for _, b := range dk {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			data = append(data, byte(acc>>bits)&31)
		}
	}
	if bits > 0 {
		data = append(data, byte(acc<<(5-bits))&31)
	}

	values := make([]byte, 0, 2*len(hrp)+1+len(data)+6)
	for i := range len(hrp) {
		values = append(values, hrp[i]>>5)
	}
	values = append(values, 0)
	for i := range len(hrp) {
		values = append(values, hrp[i]&31)
	}
	values = append(append(values, data...), 0, 0, 0, 0, 0, 0)
	const bech32mConst = 0x2bc830a3
	checksum := bech32Polymod(values) ^ bech32mConst
	for i := range 6 {
		data = append(data, byte(checksum>>(5*(5-i)))&31)
	}

	out := []byte(hrp + "1")
	for _, v := range data {
		out = append(out, bech32Charset[v])
	}
	return string(out)
}

// DecodeDelegateKey decodes a key's display form. It accepts only the
// canonical form (contracts-v1 §11.2): the exact string EncodeDelegateKey
// writes for the key, so lowercase, with zero padding bits and a valid
// checksum.
func DecodeDelegateKey(s string) ([32]byte, bool) {
	var dk [32]byte
	// 52 data characters hold 256 bits and 4 padding bits; 6 more are the checksum.
	const prefix, dataChars = DelegateKeyHRP + "1", 52
	if len(s) != len(prefix)+dataChars+6 || s[:len(prefix)] != prefix {
		return dk, false
	}
	var acc uint32
	bits, n := 0, 0
	for i := 0; i < dataChars; i++ {
		v := strings.IndexByte(bech32Charset, s[len(prefix)+i])
		if v < 0 {
			return dk, false
		}
		acc = acc<<5 | uint32(v)
		bits += 5
		if bits >= 8 && n < len(dk) {
			bits -= 8
			dk[n] = byte(acc >> bits)
			n++
		}
	}
	return dk, EncodeDelegateKey(dk) == s
}

// bech32Polymod is the BIP-173 checksum polynomial over 5-bit values.
func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i, g := range gen {
			if (top>>i)&1 == 1 {
				chk ^= g
			}
		}
	}
	return chk
}

// IsDelegateHandle reports whether handle is the canonical form of an
// account name at provider (contracts-v1 §2.5), the only form the chain
// stores: for X (1), 1 to 15 bytes of [a-z0-9_]; for GitHub (2), 1 to 39
// bytes of [a-z0-9-] with no leading or trailing hyphen. It mirrors
// x/vote/types.ValidateDelegateHandle, which this package cannot import
// without linking the circuits library; that package's tests hold the two
// together.
func IsDelegateHandle(provider uint32, handle string) bool {
	maxLen, extra := 15, byte('_')
	switch provider {
	case 1:
	case 2:
		maxLen, extra = 39, '-'
	default:
		return false
	}
	if len(handle) < 1 || len(handle) > maxLen {
		return false
	}
	for i := 0; i < len(handle); i++ {
		if c := handle[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == extra) {
			return false
		}
	}
	return provider != 2 || (handle[0] != '-' && handle[len(handle)-1] != '-')
}
