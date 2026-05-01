package votingconfig

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	ConfigVersionV1 = 1
	AuthVersionV1   = 1
	AlgEd25519      = "ed25519"
)

type SignedConfig struct {
	ConfigVersion     int                   `json:"config_version"`
	VoteServers       []Endpoint            `json:"vote_servers"`
	PIREndpoints      []Endpoint            `json:"pir_endpoints"`
	SupportedVersions SupportedVersions     `json:"supported_versions"`
	Rounds            map[string]RoundEntry `json:"rounds"`
}

type Endpoint struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

type SupportedVersions struct {
	PIR          []string `json:"pir"`
	VoteProtocol string   `json:"vote_protocol"`
	Tally        string   `json:"tally"`
	VoteServer   string   `json:"vote_server"`
}

type RoundEntry struct {
	AuthVersion int         `json:"auth_version"`
	EaPK        string      `json:"ea_pk"`
	Signatures  []Signature `json:"signatures"`
}

type Signature struct {
	KeyID string `json:"key_id"`
	Alg   string `json:"alg"`
	Sig   string `json:"sig"`
}

type TrustedKey struct {
	KeyID  string `json:"key_id"`
	Alg    string `json:"alg"`
	Pubkey string `json:"pubkey"`
	Notes  string `json:"notes,omitempty"`
}

type TrustedKeysFile struct {
	TrustedKeys []TrustedKey `json:"trusted_keys"`
}

type AuthStatus string

const (
	AuthAuthenticated     AuthStatus = "authenticated"
	AuthMissingRound      AuthStatus = "missing_round"
	AuthUnknownVersion    AuthStatus = "unknown_auth_version"
	AuthInvalidSignatures AuthStatus = "invalid_signatures"
	AuthEaPKMismatch      AuthStatus = "ea_pk_mismatch"
)

func CanonicalPayloadV1(eaPK [32]byte) []byte {
	out := make([]byte, len(eaPK))
	copy(out, eaPK[:])
	return out
}

func SignedPayloadHash(payload []byte) [32]byte {
	return sha256.Sum256(payload)
}

func SignV1(priv ed25519.PrivateKey, eaPK [32]byte) []byte {
	return ed25519.Sign(priv, CanonicalPayloadV1(eaPK))
}

func VerifyV1(pub ed25519.PublicKey, eaPK [32]byte, sig []byte) bool {
	return ed25519.Verify(pub, CanonicalPayloadV1(eaPK), sig)
}

func Authenticate(cfg *SignedConfig, trusted []TrustedKey, roundID string, eaPKFromChain [32]byte) AuthStatus {
	if cfg == nil || cfg.Rounds == nil {
		return AuthMissingRound
	}
	entry, ok := cfg.Rounds[roundID]
	if !ok {
		return AuthMissingRound
	}
	if entry.AuthVersion != AuthVersionV1 {
		return AuthUnknownVersion
	}

	entryEaPK, err := DecodeBase64Fixed(entry.EaPK, 32, "ea_pk")
	if err != nil {
		return AuthInvalidSignatures
	}
	var entryEaPKArray [32]byte
	copy(entryEaPKArray[:], entryEaPK)

	if len(entry.Signatures) == 0 || !VerifyEntrySignatures(entry, trusted) {
		return AuthInvalidSignatures
	}
	if entryEaPKArray != eaPKFromChain {
		return AuthEaPKMismatch
	}
	return AuthAuthenticated
}

func ValidateWrapper(cfg *SignedConfig) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	if cfg.ConfigVersion != ConfigVersionV1 {
		return fmt.Errorf("unsupported config_version %d", cfg.ConfigVersion)
	}
	if len(cfg.VoteServers) == 0 {
		return errors.New("vote_servers must contain at least one entry")
	}
	if len(cfg.PIREndpoints) == 0 {
		return errors.New("pir_endpoints must contain at least one entry")
	}
	if cfg.Rounds == nil {
		return errors.New("rounds must be an object")
	}
	for roundID := range cfg.Rounds {
		if err := ValidateRoundID(roundID); err != nil {
			return err
		}
	}
	return nil
}

func VerifyEntrySignatures(entry RoundEntry, trusted []TrustedKey) bool {
	if entry.AuthVersion != AuthVersionV1 || len(entry.Signatures) == 0 {
		return false
	}
	entryEaPK, err := DecodeBase64Fixed(entry.EaPK, 32, "ea_pk")
	if err != nil {
		return false
	}
	var eaPK [32]byte
	copy(eaPK[:], entryEaPK)
	return hasValidSignature(entry, trusted, eaPK)
}

func ValidateRoundID(roundID string) error {
	if len(roundID) != 64 {
		return fmt.Errorf("round id %q is %d chars, expected 64", roundID, len(roundID))
	}
	for _, ch := range roundID {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		default:
			return fmt.Errorf("round id %q must be lowercase hex", roundID)
		}
	}
	if _, err := hex.DecodeString(roundID); err != nil {
		return fmt.Errorf("round id %q is invalid hex: %w", roundID, err)
	}
	return nil
}

func DecodeBase64Fixed(s string, expectedLen int, field string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	out, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid base64: %w", field, err)
	}
	if expectedLen > 0 && len(out) != expectedLen {
		return nil, fmt.Errorf("%s: decoded %d bytes, expected %d", field, len(out), expectedLen)
	}
	return out, nil
}

func DecodeHexFixed(s string, expectedLen int, field string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid hex: %w", field, err)
	}
	if expectedLen > 0 && len(out) != expectedLen {
		return nil, fmt.Errorf("%s: decoded %d bytes, expected %d", field, len(out), expectedLen)
	}
	return out, nil
}

func hasValidSignature(entry RoundEntry, trusted []TrustedKey, eaPK [32]byte) bool {
	keys := map[string]TrustedKey{}
	for _, key := range trusted {
		keys[key.KeyID] = key
	}
	for _, sigRef := range entry.Signatures {
		key, ok := keys[sigRef.KeyID]
		if !ok || sigRef.Alg != key.Alg || key.Alg != AlgEd25519 {
			continue
		}
		pub, err := DecodeBase64Fixed(key.Pubkey, ed25519.PublicKeySize, "trusted_keys.pubkey")
		if err != nil {
			continue
		}
		sig, err := DecodeBase64Fixed(sigRef.Sig, ed25519.SignatureSize, "signatures.sig")
		if err != nil {
			continue
		}
		if VerifyV1(ed25519.PublicKey(pub), eaPK, sig) {
			return true
		}
	}
	return false
}
