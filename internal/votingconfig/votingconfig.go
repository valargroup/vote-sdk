package votingconfig

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	ConfigVersionV1       = 1
	StaticConfigVersionV1 = 1
	AuthVersionV1         = 1
	AuthVersionV2         = 2
	AlgEd25519            = "ed25519"

	// Allowed YPIR polynomial degrees bound into auth_version 2 signatures.
	PolyLen2048 uint32 = 2048
	PolyLen4096 uint32 = 4096

	// DomainTagV2 prefixes the auth_version 2 signed preimage:
	// DomainTagV2 || round_id (32 raw bytes) || ea_pk (32 bytes)
	//             || pir_depth (u32 LE) || tier0_layers (u32 LE) || tier1_layers (u32 LE)
	//             || poly_len (u32 LE).
	// Binding the round id stops a signed ea_pk from being replayed under a
	// different rounds-map key; binding the PIR layout (including poly_len)
	// stops a config host from swapping those parameters under already-attested
	// rounds. Wallet verifiers (librustvoting) hardcode the same tag; bytes
	// must match verbatim.
	DomainTagV2 = "zcash-shielded-vote:round-auth:v2"
)

type SignedConfig struct {
	ConfigVersion     int                   `json:"config_version"`
	VoteServers       []Endpoint            `json:"vote_servers"`
	PIREndpoints      []Endpoint            `json:"pir_endpoints"`
	PIRLayout         PIRLayout             `json:"pir_layout"`
	SupportedVersions SupportedVersions     `json:"supported_versions"`
	Rounds            map[string]RoundEntry `json:"rounds"`
}

// PIRLayout describes how the PIR circuit depth is split across the two data
// tiers and the YPIR polynomial degree bound into auth_version 2 signatures.
type PIRLayout struct {
	PIRDepth    uint32 `json:"pir_depth"`
	Tier0Layers uint32 `json:"tier0_layers"`
	Tier1Layers uint32 `json:"tier1_layers"`
	PolyLen     uint32 `json:"poly_len"`
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

type StaticConfig struct {
	StaticConfigVersion int          `json:"static_config_version"`
	DynamicConfigURL    string       `json:"dynamic_config_url"`
	TrustedKeys         []TrustedKey `json:"trusted_keys"`
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

// CanonicalPayloadV2 builds the auth_version 2 signed preimage:
// DomainTagV2 || round_id (32 raw bytes) || ea_pk (32 bytes)
//
//	|| pir_depth (u32 LE) || tier0_layers (u32 LE) || tier1_layers (u32 LE)
//	|| poly_len (u32 LE).
func CanonicalPayloadV2(roundID string, eaPK [32]byte, layout PIRLayout) ([]byte, error) {
	if err := ValidateRoundID(roundID); err != nil {
		return nil, err
	}
	if err := ValidatePIRLayout(layout); err != nil {
		return nil, err
	}
	roundIDBytes, err := hex.DecodeString(roundID)
	if err != nil {
		return nil, fmt.Errorf("round id %q is invalid hex: %w", roundID, err)
	}
	out := make([]byte, 0, len(DomainTagV2)+len(roundIDBytes)+len(eaPK)+16)
	out = append(out, DomainTagV2...)
	out = append(out, roundIDBytes...)
	out = append(out, eaPK[:]...)
	out = binary.LittleEndian.AppendUint32(out, layout.PIRDepth)
	out = binary.LittleEndian.AppendUint32(out, layout.Tier0Layers)
	out = binary.LittleEndian.AppendUint32(out, layout.Tier1Layers)
	out = binary.LittleEndian.AppendUint32(out, layout.PolyLen)
	return out, nil
}

func SignV2(priv ed25519.PrivateKey, roundID string, eaPK [32]byte, layout PIRLayout) ([]byte, error) {
	payload, err := CanonicalPayloadV2(roundID, eaPK, layout)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, payload), nil
}

func VerifyV2(pub ed25519.PublicKey, roundID string, eaPK [32]byte, layout PIRLayout, sig []byte) bool {
	payload, err := CanonicalPayloadV2(roundID, eaPK, layout)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, payload, sig)
}

// CanonicalPayload builds the signed preimage for the given auth_version.
// The layout (including poly_len) is bound by v2 payloads and ignored by
// legacy v1 payloads.
func CanonicalPayload(authVersion int, roundID string, eaPK [32]byte, layout PIRLayout) ([]byte, error) {
	switch authVersion {
	case AuthVersionV1:
		return CanonicalPayloadV1(eaPK), nil
	case AuthVersionV2:
		return CanonicalPayloadV2(roundID, eaPK, layout)
	default:
		return nil, fmt.Errorf("unsupported auth_version %d", authVersion)
	}
}

func Authenticate(cfg *SignedConfig, trusted []TrustedKey, roundID string, eaPKFromChain [32]byte) AuthStatus {
	if cfg == nil || cfg.Rounds == nil {
		return AuthMissingRound
	}
	entry, ok := cfg.Rounds[roundID]
	if !ok {
		return AuthMissingRound
	}
	if entry.AuthVersion != AuthVersionV1 && entry.AuthVersion != AuthVersionV2 {
		return AuthUnknownVersion
	}

	entryEaPK, err := DecodeBase64Fixed(entry.EaPK, 32, "ea_pk")
	if err != nil {
		return AuthInvalidSignatures
	}
	var entryEaPKArray [32]byte
	copy(entryEaPKArray[:], entryEaPK)

	if len(entry.Signatures) == 0 || !VerifyEntrySignatures(roundID, entry, trusted, cfg.PIRLayout) {
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
	if err := ValidatePIRLayout(cfg.PIRLayout); err != nil {
		return err
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

// ValidatePIRLayout requires the two tiers to cover the configured circuit
// depth and poly_len to be an allowed YPIR polynomial degree.
func ValidatePIRLayout(layout PIRLayout) error {
	if layout.PIRDepth == 0 {
		return errors.New("pir_layout.pir_depth must be greater than zero")
	}
	layers := uint64(layout.Tier0Layers) + uint64(layout.Tier1Layers)
	if layers != uint64(layout.PIRDepth) {
		return fmt.Errorf(
			"pir_layout.pir_depth %d must equal tier0_layers %d + tier1_layers %d",
			layout.PIRDepth,
			layout.Tier0Layers,
			layout.Tier1Layers,
		)
	}
	if err := ValidatePolyLen(layout.PolyLen); err != nil {
		return err
	}
	return nil
}

// ValidatePolyLen requires an allowed YPIR polynomial degree.
func ValidatePolyLen(polyLen uint32) error {
	switch polyLen {
	case PolyLen2048, PolyLen4096:
		return nil
	default:
		return fmt.Errorf("poly_len must be %d or %d", PolyLen2048, PolyLen4096)
	}
}

func ValidateStaticConfig(cfg *StaticConfig) error {
	if cfg == nil {
		return errors.New("static config is nil")
	}
	if cfg.StaticConfigVersion != StaticConfigVersionV1 {
		return fmt.Errorf("unsupported static_config_version %d", cfg.StaticConfigVersion)
	}
	if strings.TrimSpace(cfg.DynamicConfigURL) == "" {
		return errors.New("dynamic_config_url is required")
	}
	if len(cfg.TrustedKeys) == 0 {
		return errors.New("trusted_keys must contain at least one entry")
	}
	return nil
}

// VerifyEntrySignatures reports whether at least one trusted key signed the
// entry's canonical payload for its auth_version. v1 signatures cover only the
// raw ea_pk (legacy, kept for mixed-version files during migration); v2
// signatures cover DomainTagV2 || round_id || ea_pk || pir_layout fields
// including poly_len.
func VerifyEntrySignatures(roundID string, entry RoundEntry, trusted []TrustedKey, layout PIRLayout) bool {
	if entry.AuthVersion != AuthVersionV1 && entry.AuthVersion != AuthVersionV2 {
		return false
	}
	if len(entry.Signatures) == 0 {
		return false
	}
	entryEaPK, err := DecodeBase64Fixed(entry.EaPK, 32, "ea_pk")
	if err != nil {
		return false
	}
	var eaPK [32]byte
	copy(eaPK[:], entryEaPK)
	return hasValidSignature(roundID, entry, trusted, eaPK, layout)
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

func hasValidSignature(roundID string, entry RoundEntry, trusted []TrustedKey, eaPK [32]byte, layout PIRLayout) bool {
	payload, err := CanonicalPayload(entry.AuthVersion, roundID, eaPK, layout)
	if err != nil {
		return false
	}
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
		if ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
			return true
		}
	}
	return false
}
