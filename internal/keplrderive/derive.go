package keplrderive

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

const (
	DefaultBIP44Path    = "m/44'/118'/0'/0/0"
	DefaultBech32Prefix = "sv"
	DefaultPurpose      = "shielded-vote/ea-pk-signer/v1"
	ed25519SeedSaltText = "shielded-vote/ed25519-seed/v1"
)

type DerivationParams struct {
	Mnemonic        string
	BIP39Passphrase string
	BIP44Path       string
	Bech32Prefix    string
	ChainID         string
	Purpose         string
}

type Signer struct {
	Address            string
	Secp256k1Pub       []byte
	AminoSignDoc       []byte
	ArbitrarySignature []byte
	Ed25519Seed        []byte
	Ed25519Pub         []byte
}

func (p DerivationParams) normalized() (DerivationParams, error) {
	p.Mnemonic = strings.TrimSpace(p.Mnemonic)
	p.BIP44Path = strings.TrimSpace(p.BIP44Path)
	p.Bech32Prefix = strings.TrimSpace(p.Bech32Prefix)
	p.ChainID = strings.TrimSpace(p.ChainID)
	p.Purpose = strings.TrimSpace(p.Purpose)

	if p.Mnemonic == "" {
		return p, errors.New("mnemonic is required")
	}
	if p.ChainID == "" {
		return p, errors.New("chain-id is required")
	}
	if p.BIP44Path == "" {
		p.BIP44Path = DefaultBIP44Path
	}
	if p.Bech32Prefix == "" {
		p.Bech32Prefix = DefaultBech32Prefix
	}
	if p.Purpose == "" {
		p.Purpose = DefaultPurpose
	}
	return p, nil
}

func DeriveSecp256k1(params DerivationParams) ([]byte, []byte, string, error) {
	p, err := params.normalized()
	if err != nil {
		return nil, nil, "", err
	}

	privBytes, err := hd.Secp256k1.Derive()(p.Mnemonic, p.BIP39Passphrase, p.BIP44Path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("derive secp256k1 key: %w", err)
	}
	if len(privBytes) != secp256k1.PrivKeySize {
		return nil, nil, "", fmt.Errorf("derived private key is %d bytes, expected %d", len(privBytes), secp256k1.PrivKeySize)
	}

	priv := &secp256k1.PrivKey{Key: privBytes}
	pub, ok := priv.PubKey().(*secp256k1.PubKey)
	if !ok {
		return nil, nil, "", errors.New("derived public key has unexpected type")
	}
	address, err := bech32.ConvertAndEncode(p.Bech32Prefix, pub.Address())
	if err != nil {
		return nil, nil, "", fmt.Errorf("encode bech32 address: %w", err)
	}

	return append([]byte(nil), privBytes...), append([]byte(nil), pub.Key...), address, nil
}

func CanonicalAminoSignDoc(signer, data string) ([]byte, error) {
	doc := map[string]any{
		"account_number": "0",
		"chain_id":       "",
		"fee":            map[string]any{"amount": []any{}, "gas": "0"},
		"memo":           "",
		"msgs": []any{
			map[string]any{
				"type": "sign/MsgSignData",
				"value": map[string]any{
					"data":   base64.StdEncoding.EncodeToString([]byte(data)),
					"signer": signer,
				},
			},
		},
		"sequence": "0",
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("marshal sign doc: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func SignArbitrary(privKey []byte, signer, purpose string) ([]byte, []byte, error) {
	if len(privKey) != secp256k1.PrivKeySize {
		return nil, nil, fmt.Errorf("secp256k1 private key is %d bytes, expected %d", len(privKey), secp256k1.PrivKeySize)
	}
	signDoc, err := CanonicalAminoSignDoc(signer, purpose)
	if err != nil {
		return nil, nil, err
	}
	sig, err := (&secp256k1.PrivKey{Key: privKey}).Sign(signDoc)
	if err != nil {
		return nil, nil, fmt.Errorf("sign arbitrary payload: %w", err)
	}
	if len(sig) != 64 {
		return nil, nil, fmt.Errorf("secp256k1 signature is %d bytes, expected 64", len(sig))
	}
	return signDoc, sig, nil
}

func DeriveEd25519Seed(signature []byte, chainID, address string) ([]byte, error) {
	if len(signature) != 64 {
		return nil, fmt.Errorf("secp256k1 signature is %d bytes, expected 64", len(signature))
	}
	salt := sha256.Sum256([]byte(ed25519SeedSaltText))
	seed, err := hkdf.Key(sha256.New, signature, salt[:], strings.TrimSpace(chainID)+"|"+strings.TrimSpace(address), ed25519.SeedSize)
	if err != nil {
		return nil, fmt.Errorf("derive ed25519 seed: %w", err)
	}
	return seed, nil
}

func DeriveSigner(params DerivationParams) (*Signer, error) {
	p, err := params.normalized()
	if err != nil {
		return nil, err
	}

	priv, pub, address, err := DeriveSecp256k1(p)
	if err != nil {
		return nil, err
	}
	signDoc, sig, err := SignArbitrary(priv, address, p.Purpose)
	if err != nil {
		return nil, err
	}
	seed, err := DeriveEd25519Seed(sig, p.ChainID, address)
	if err != nil {
		return nil, err
	}

	return &Signer{
		Address:            address,
		Secp256k1Pub:       pub,
		AminoSignDoc:       signDoc,
		ArbitrarySignature: sig,
		Ed25519Seed:        seed,
		Ed25519Pub:         ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey),
	}, nil
}
