package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// loadPrivateKey resolves the signer's private ed25519 key from one of three
// inputs (in priority order): --privkey-file, MANIFEST_SIGNER_PRIVKEY env var,
// --privkey-stdin. Exactly one must be provided.
//
// The on-disk format is base64(32-byte seed). The full ed25519 private key
// is derived via ed25519.NewKeyFromSeed, matching what `keygen` writes.
func loadPrivateKey(file string, stdin bool, stdinReader io.Reader) (ed25519.PrivateKey, error) {
	envKey := os.Getenv("MANIFEST_SIGNER_PRIVKEY")
	sources := 0
	if file != "" {
		sources++
	}
	if envKey != "" {
		sources++
	}
	if stdin {
		sources++
	}
	if sources == 0 {
		return nil, errors.New("no signer key provided: use --privkey-file, MANIFEST_SIGNER_PRIVKEY, or --privkey-stdin")
	}
	if sources > 1 {
		return nil, errors.New("multiple signer keys provided: pick exactly one of --privkey-file, MANIFEST_SIGNER_PRIVKEY, --privkey-stdin")
	}

	var b64 string
	switch {
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read privkey-file %q: %w", file, err)
		}
		b64 = strings.TrimSpace(string(raw))
	case envKey != "":
		b64 = strings.TrimSpace(envKey)
	case stdin:
		raw, err := io.ReadAll(stdinReader)
		if err != nil {
			return nil, fmt.Errorf("read privkey from stdin: %w", err)
		}
		b64 = strings.TrimSpace(string(raw))
	}

	seed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode privkey base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("decoded privkey is %d bytes, expected %d", len(seed), ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// loadPublicKey reads a base64-encoded 32-byte ed25519 public key from a file.
func loadPublicKey(file string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read pubkey-file %q: %w", file, err)
	}
	b64 := strings.TrimSpace(string(raw))
	pk, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode pubkey base64: %w", err)
	}
	if len(pk) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("decoded pubkey is %d bytes, expected %d", len(pk), ed25519.PublicKeySize)
	}
	return pk, nil
}
