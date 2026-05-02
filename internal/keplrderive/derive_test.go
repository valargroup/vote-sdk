package keplrderive

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

type parityVector struct {
	Mnemonic                 string `json:"mnemonic"`
	ChainID                  string `json:"chain_id"`
	Bech32Prefix             string `json:"bech32_prefix"`
	BIP44Path                string `json:"bip44_path"`
	Purpose                  string `json:"purpose"`
	ExpectedAddress          string `json:"expected_address"`
	ExpectedSecp256k1PubB64  string `json:"expected_secp256k1_pub_b64"`
	ExpectedAminoSignDocB64  string `json:"expected_amino_signdoc_b64"`
	ExpectedSignatureB64     string `json:"expected_signature_b64"`
	ExpectedEd25519SeedB64   string `json:"expected_ed25519_seed_b64"`
	ExpectedEd25519PubKeyB64 string `json:"expected_ed25519_pub_b64"`
}

func TestDeriveSignerMatchesParityVector(t *testing.T) {
	v := loadParityVector(t)
	signer, err := DeriveSigner(DerivationParams{
		Mnemonic:     v.Mnemonic,
		BIP44Path:    v.BIP44Path,
		Bech32Prefix: v.Bech32Prefix,
		ChainID:      v.ChainID,
		Purpose:      v.Purpose,
	})
	if err != nil {
		t.Fatalf("derive signer: %v", err)
	}

	assertEqual(t, "address", signer.Address, v.ExpectedAddress)
	assertB64Equal(t, "secp256k1 pubkey", signer.Secp256k1Pub, v.ExpectedSecp256k1PubB64)
	assertB64Equal(t, "amino sign doc", signer.AminoSignDoc, v.ExpectedAminoSignDocB64)
	assertB64Equal(t, "signature", signer.ArbitrarySignature, v.ExpectedSignatureB64)
	assertB64Equal(t, "ed25519 seed", signer.Ed25519Seed, v.ExpectedEd25519SeedB64)
	assertB64Equal(t, "ed25519 pubkey", signer.Ed25519Pub, v.ExpectedEd25519PubKeyB64)
}

func loadParityVector(t *testing.T) parityVector {
	t.Helper()
	raw, err := os.ReadFile("testdata/parity_vector.json")
	if err != nil {
		t.Fatalf("read parity vector: %v", err)
	}
	var v parityVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode parity vector: %v", err)
	}
	return v
}

func assertB64Equal(t *testing.T, field string, got []byte, wantB64 string) {
	t.Helper()
	want, err := base64.StdEncoding.DecodeString(wantB64)
	if err != nil {
		t.Fatalf("%s fixture is invalid base64: %v", field, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s mismatch:\n got:  %s\n want: %s", field, base64.StdEncoding.EncodeToString(got), wantB64)
	}
}

func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s mismatch: got %q, want %q", field, got, want)
	}
}
