package admin

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"golang.org/x/crypto/ripemd160"
)

const bech32Prefix = "sv"
const valoperPrefix = "svvaloper"

// VerifyArbitrarySignature verifies a Keplr-style ADR-036 signArbitrary
// signature. It checks that:
//  1. The secp256k1 signature is valid over the amino sign doc.
//  2. The public key derives to the claimed signer address.
func VerifyArbitrarySignature(signerAddress, payload string, signatureB64, pubKeyB64 string) error {
	sigBytes, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyB64)
	if err != nil {
		return fmt.Errorf("decode pubkey: %w", err)
	}

	signDoc := makeSignArbitraryDoc(signerAddress, payload)
	msgHash := sha256.Sum256(signDoc)

	pk := &secp256k1.PubKey{Key: pubKeyBytes}
	if !pk.VerifySignature(msgHash[:], sigBytes) {
		return fmt.Errorf("invalid signature")
	}

	derived, err := pubKeyToAddress(pubKeyBytes)
	if err != nil {
		return fmt.Errorf("derive address: %w", err)
	}
	if derived != signerAddress {
		return fmt.Errorf("public key does not match signer address")
	}

	return nil
}

// makeSignArbitraryDoc reconstructs the amino sign doc used by Keplr's
// signArbitrary. The data is base64-encoded in the sign doc.
func makeSignArbitraryDoc(signer, data string) []byte {
	doc := map[string]interface{}{
		"account_number": "0",
		"chain_id":       "",
		"fee":            map[string]interface{}{"amount": []interface{}{}, "gas": "0"},
		"memo":           "",
		"msgs": []interface{}{
			map[string]interface{}{
				"type": "sign/MsgSignData",
				"value": map[string]interface{}{
					"data":   base64.StdEncoding.EncodeToString([]byte(data)),
					"signer": signer,
				},
			},
		},
		"sequence": "0",
	}
	// Amino signing uses sorted, deterministic JSON.
	bz, _ := json.Marshal(doc)
	return bz
}

// pubKeyToAddress derives a bech32 address from a compressed secp256k1 public key.
func pubKeyToAddress(compressedPubKey []byte) (string, error) {
	s := sha256.Sum256(compressedPubKey)
	r := ripemd160.New()
	r.Write(s[:])
	hash := r.Sum(nil)
	return bech32.ConvertAndEncode(bech32Prefix, hash)
}

// AddressToValoper converts an account bech32 address to a valoper address.
func AddressToValoper(address string) (string, error) {
	_, bz, err := bech32.DecodeAndConvert(address)
	if err != nil {
		return "", fmt.Errorf("decode address: %w", err)
	}
	return sdk.Bech32ifyAddressBytes(valoperPrefix, bz)
}
