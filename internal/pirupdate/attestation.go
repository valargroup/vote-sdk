// Package pirupdate defines the coordinator authorization shared by the admin UI and PIR hosts.
package pirupdate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

//go:embed keys.json
var keysJSON []byte

type Key struct {
	KeyID  string `json:"key_id"`
	Pubkey string `json:"pubkey"`
}
type Config struct {
	SchemaVersion  int    `json:"schema_version"`
	SnapshotHeight uint64 `json:"snapshot_height"`
	BinaryTag      string `json:"binary_tag"`
}
type Payload struct {
	ConfigSHA256           string `json:"config_sha256"`
	LinuxAMD64SHA256       string `json:"linux_amd64_sha256"`
	LinuxARM64SHA256       string `json:"linux_arm64_sha256"`
	SnapshotManifestSHA256 string `json:"snapshot_manifest_sha256"`
	ServiceSHA256          string `json:"service_sha256"`
}
type Signature struct {
	KeyID string `json:"key_id"`
	Alg   string `json:"alg"`
	Sig   string `json:"sig"`
}
type Attestations struct {
	SchemaVersion int         `json:"schema_version"`
	Payload       Payload     `json:"payload"`
	Signatures    []Signature `json:"signatures"`
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var tagPattern = regexp.MustCompile(`^v[0-9A-Za-z.+-]{1,127}$`)

func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func TrustedKeys(scope string) []Key {
	var keys map[string][]Key
	_ = json.Unmarshal(keysJSON, &keys)
	return keys[scope]
}
func SigningBytes(scope string, p Payload) ([]byte, error) {
	if scope != "prod" && scope != "stage" {
		return nil, fmt.Errorf("invalid PIR signing scope")
	}
	fields := []string{p.ConfigSHA256, p.LinuxAMD64SHA256, p.LinuxARM64SHA256, p.SnapshotManifestSHA256, p.ServiceSHA256}
	for _, h := range fields {
		if !hashPattern.MatchString(h) {
			return nil, fmt.Errorf("invalid SHA-256")
		}
	}
	return []byte("valargroup/pir-update/v1\n" + scope + "\n" + strings.Join(fields, "\n") + "\n"), nil
}
func Decode(data []byte, out any) error {
	if len(data) > 65536 {
		return fmt.Errorf("PIR metadata too large")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
func ValidateConfig(cfg Config) error {
	if cfg.SchemaVersion != 1 || cfg.SnapshotHeight == 0 || cfg.SnapshotHeight > 9007199254740991 || cfg.SnapshotHeight%10 != 0 || !tagPattern.MatchString(cfg.BinaryTag) {
		return fmt.Errorf("invalid PIR config")
	}
	return nil
}
func Verify(config []byte, att Attestations, scope string) (Config, error) {
	return verify(config, att, scope, TrustedKeys(scope))
}
func verify(config []byte, att Attestations, scope string, keys []Key) (Config, error) {
	var cfg Config
	if err := Decode(config, &cfg); err != nil {
		return cfg, err
	}
	if err := ValidateConfig(cfg); err != nil {
		return cfg, err
	}
	if att.SchemaVersion != 1 || Hash(config) != att.Payload.ConfigSHA256 {
		return cfg, fmt.Errorf("attestation does not match PIR config")
	}
	message, err := SigningBytes(scope, att.Payload)
	if err != nil {
		return cfg, err
	}
	for _, s := range att.Signatures {
		if s.Alg != "ed25519" {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil || len(sig) != ed25519.SignatureSize {
			continue
		}
		for _, k := range keys {
			if s.KeyID != k.KeyID {
				continue
			}
			pk, err := base64.StdEncoding.DecodeString(k.Pubkey)
			if err == nil && len(pk) == ed25519.PublicKeySize && ed25519.Verify(pk, message, sig) {
				return cfg, nil
			}
		}
	}
	return cfg, fmt.Errorf("no pinned coordinator signature matches PIR update")
}
