package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// TestEncodeRoundManifestKnownAnswer pins the canonical byte encoding for a
// fixed input. Wallets re-implementing the encoding (Swift, Rust) MUST
// reproduce this output byte-for-byte; otherwise signatures from this binary
// will fail to verify in those wallets.
func TestEncodeRoundManifestKnownAnswer(t *testing.T) {
	roundID, _ := hex.DecodeString("6823028ccc36f2fffc5a6d9af3e62a918a33913a8f37c2d3efe962a0357aa03f")
	eaPK, _ := hex.DecodeString("01020304050607080910111213141516171819202122232425262728293031aa")
	valset, _ := hex.DecodeString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	got, err := EncodeRoundManifest(RoundManifestPayload{
		ChainID:    "svote-1",
		RoundID:    roundID,
		EaPK:       eaPK,
		ValsetHash: valset,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Layout:
	//   u16(31) || "shielded-vote/round-manifest/v1"  (33 bytes)
	//   u16(7)  || "svote-1"                          ( 9 bytes)
	//   u16(32) || round_id                           (34 bytes)
	//   u16(32) || ea_pk                              (34 bytes)
	//   u16(32) || valset_hash                        (34 bytes)
	//   total: 144 bytes
	expectedHex := "001f" + hex.EncodeToString([]byte(roundManifestDomainSep)) +
		"0007" + hex.EncodeToString([]byte("svote-1")) +
		"0020" + "6823028ccc36f2fffc5a6d9af3e62a918a33913a8f37c2d3efe962a0357aa03f" +
		"0020" + "01020304050607080910111213141516171819202122232425262728293031aa" +
		"0020" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	expected, err2 := hex.DecodeString(expectedHex)
	if err2 != nil {
		t.Fatalf("decode expected hex: %v", err2)
	}

	if !equalBytes(got, expected) {
		t.Fatalf("EncodeRoundManifest mismatch:\n got %s\nwant %s", hex.EncodeToString(got), hex.EncodeToString(expected))
	}
	if len(got) != 144 {
		t.Fatalf("expected 144 bytes, got %d", len(got))
	}
}

// TestEncodeCheckpointKnownAnswer pins the checkpoint canonical encoding.
func TestEncodeCheckpointKnownAnswer(t *testing.T) {
	header, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	valset, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000002")
	app, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000003")

	got, err := EncodeCheckpoint(CheckpointPayload{
		ChainID:    "svote-1",
		Height:     0x01020304,
		HeaderHash: header,
		ValsetHash: valset,
		AppHash:    app,
		IssuedAt:   0x55667788,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Layout:
	//   u16(27) || "shielded-vote/checkpoint/v1" (29 bytes)
	//   u16(7)  || "svote-1"                     ( 9 bytes)
	//   u64_be(0x01020304)                       ( 8 bytes)
	//   u16(32) || header_hash                   (34 bytes)
	//   u16(32) || valset_hash                   (34 bytes)
	//   u16(32) || app_hash                      (34 bytes)
	//   u64_be(0x55667788)                       ( 8 bytes)
	//   total: 156 bytes
	expectedHex := "001b" + hex.EncodeToString([]byte(checkpointDomainSep)) +
		"0007" + hex.EncodeToString([]byte("svote-1")) +
		"0000000001020304" +
		"0020" + "0000000000000000000000000000000000000000000000000000000000000001" +
		"0020" + "0000000000000000000000000000000000000000000000000000000000000002" +
		"0020" + "0000000000000000000000000000000000000000000000000000000000000003" +
		"0000000055667788"
	expected, err2 := hex.DecodeString(expectedHex)
	if err2 != nil {
		t.Fatalf("decode expected hex: %v", err2)
	}
	if !equalBytes(got, expected) {
		t.Fatalf("EncodeCheckpoint mismatch:\n got %s\nwant %s", hex.EncodeToString(got), hex.EncodeToString(expected))
	}
	if len(got) != 156 {
		t.Fatalf("expected 156 bytes, got %d", len(got))
	}
}

// TestSignVerifyRoundtrip exercises the same signing path the CLI uses and
// confirms verify.go's verification logic accepts a freshly produced signature.
func TestSignVerifyRoundtrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	roundID, _ := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	eaPK, _ := hex.DecodeString("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899")
	valset, _ := hex.DecodeString("ffffeeeeddddccccbbbbaaaa9999888877776666555544443333222211110000")

	payload, err := EncodeRoundManifest(RoundManifestPayload{
		ChainID:    "svote-1",
		RoundID:    roundID,
		EaPK:       eaPK,
		ValsetHash: valset,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sig := ed25519.Sign(priv, payload)
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatalf("self-verify failed (impossible if Go's stdlib is intact)")
	}

	// Tampering anywhere in the payload must invalidate the signature.
	tampered := make([]byte, len(payload))
	copy(tampered, payload)
	tampered[len(tampered)-1] ^= 0x01
	if ed25519.Verify(pub, tampered, sig) {
		t.Fatalf("signature verified over tampered payload (impossible)")
	}

	// And the digest matches what we'd publish as signed_payload_hash.
	digest := PayloadDigest(payload)
	if hex.EncodeToString(digest[:]) == strings.Repeat("0", 64) {
		t.Fatalf("digest is all zeros, can't be right")
	}
	_ = base64.StdEncoding.EncodeToString(sig) // smoke test
}

// TestRoundManifestKnownAnswerSignature is a deterministic test vector other
// implementations (Swift wallet, future Rust port) can pin against. The seed
// is a fixed all-zero 32 bytes; the inputs are stable; therefore the
// signature bytes are stable and any cross-language reproduction of
// EncodeRoundManifest + ed25519_sign(seed, payload) MUST produce the same
// signature.
//
// If this changes, EVERY existing wallet will fail to verify EVERY
// already-published round_signatures.json — coordinated wallet release
// required.
func TestRoundManifestKnownAnswerSignature(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize) // all zeros
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)

	roundID, _ := hex.DecodeString("6823028ccc36f2fffc5a6d9af3e62a918a33913a8f37c2d3efe962a0357aa03f")
	eaPK, _ := hex.DecodeString("01020304050607080910111213141516171819202122232425262728293031aa")
	valset, _ := hex.DecodeString("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	payload, err := EncodeRoundManifest(RoundManifestPayload{
		ChainID:    "svote-1",
		RoundID:    roundID,
		EaPK:       eaPK,
		ValsetHash: valset,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sig := ed25519.Sign(priv, payload)

	// pubkey for an all-zero seed is the canonical RFC 8032 §7.1 vector.
	const expectedPubB64 = "O2onvM62pC1io6jQKm8Nc2UyFXcd4kOmOsBIoYtZ2ik="
	if got := base64.StdEncoding.EncodeToString(pub); got != expectedPubB64 {
		t.Fatalf("derived pubkey from zero seed mismatch:\n got %s\nwant %s", got, expectedPubB64)
	}

	if !ed25519.Verify(pub, payload, sig) {
		t.Fatalf("self-verify failed")
	}

	// Pinned deterministic signature for the seed/inputs above. ed25519
	// PureEdDSA is deterministic in the seed and message bytes; cross-language
	// implementations of EncodeRoundManifest can use this vector to confirm
	// their canonical encoding matches byte-for-byte.
	const expectedSigB64 = "vge064r69glI/HQ+ZJPM8n+RcUarjddpjQIHqwyl+SXEfOE8khWJsCuQvCew3/mImqmhP2f8EMFNT4RCIipzCg=="
	if got := base64.StdEncoding.EncodeToString(sig); got != expectedSigB64 {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", got, expectedSigB64)
	}
}

// TestEncodeRoundManifestRejectsTooLarge guards the u16 length cap; a
// pathological future field longer than 65535 bytes would silently truncate
// without this check.
func TestEncodeRoundManifestRejectsTooLarge(t *testing.T) {
	huge := make([]byte, 0x10000) // 65536 bytes, exceeds u16 cap by 1
	_, err := EncodeRoundManifest(RoundManifestPayload{
		ChainID:    "svote-1",
		RoundID:    huge,
		EaPK:       []byte{1},
		ValsetHash: []byte{2},
	})
	if err == nil {
		t.Fatalf("expected error for oversized field")
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
