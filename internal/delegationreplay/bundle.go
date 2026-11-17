// Package delegationreplay retains bounded, local-only evidence of rejected
// delegation proofs. It never decides whether a transaction is valid.
package delegationreplay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/cosmos/cosmos-sdk/version"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

const (
	Schema         = "svote-delegation-replay-v1"
	MaxBundleBytes = 64 * 1024
)

// Identity pins the executable, including its statically linked Rust verifier.
// Exact replay requires the same executable; source-equivalent rebuilds are
// comparisons because compiler, linker, and crypto artifacts can differ.
type Identity struct {
	ExecutableSHA256 string `json:"executable_sha256"`
	Version          string `json:"version"`
	Commit           string `json:"commit"`
	BuildInfo        string `json:"build_info"`
}

func ExecutableIdentity() (Identity, error) {
	path, err := os.Executable()
	if err != nil {
		return Identity{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Identity{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return Identity{}, err
	}
	i := Identity{ExecutableSHA256: hex.EncodeToString(h.Sum(nil)), Version: version.Version, Commit: version.Commit}
	if info, ok := debug.ReadBuildInfo(); ok {
		// Keep only public build settings. Full module graphs are unnecessary
		// for exact replay, and arbitrary linker flags can contain local data.
		public := debug.BuildInfo{GoVersion: info.GoVersion, Main: info.Main}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "-tags", "GOOS", "GOARCH", "CGO_ENABLED", "vcs", "vcs.revision", "vcs.modified":
				public.Settings = append(public.Settings, setting)
			}
		}
		i.BuildInfo = public.String()
	}
	if len(i.BuildInfo) > 24*1024 {
		return Identity{}, errors.New("build identity exceeds capture limit")
	}
	return i, nil
}

// Metadata contains only server context, never the incoming request or headers.
type Metadata struct {
	ChainID string `json:"chain_id"`
	Height  int64  `json:"height"`
	Phase   string `json:"phase"`
	TxHash  string `json:"tx_hash,omitempty"`
}

// Bundle is an allowlist of verifier-visible evidence. Byte slices use JSON
// base64 encoding. Inputs retain the Go verifier's named fields and are replayed
// through the existing Go-to-Rust conversion, including padding and derivation.
type Bundle struct {
	Schema     string               `json:"schema"`
	ID         string               `json:"id"`
	Integrity  string               `json:"integrity_sha256"`
	CapturedAt time.Time            `json:"captured_at"`
	Metadata   Metadata             `json:"metadata"`
	Identity   Identity             `json:"identity"`
	Proof      []byte               `json:"proof"`
	Inputs     zkp.DelegationInputs `json:"inputs"`
	Error      string               `json:"error"`
}

func digest(v any) string {
	b, _ := json.Marshal(v) // Only concrete structs, strings and byte slices.
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (b Bundle) captureID() string {
	return digest(struct {
		Schema     string
		Executable string
		Proof      []byte
		Inputs     zkp.DelegationInputs
	}{Schema, b.Identity.ExecutableSHA256, b.Proof, b.Inputs})
}

func (b Bundle) checksum() string { b.Integrity = ""; return digest(b) }

func validHash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == hex.EncodeToString(b)
}

func validateEvidence(proof []byte, inputs zkp.DelegationInputs) error {
	if len(proof) == 0 || len(proof) > types.MaxProofSize {
		return errors.New("proof length outside capture limit")
	}
	fields := [][]byte{inputs.Rk, inputs.SignedNoteNullifier, inputs.CmxNew, inputs.VanCmx, inputs.VoteRoundId, inputs.NcRoot, inputs.NullifierImtRoot}
	if len(inputs.GovNullifiers) > 5 {
		return errors.New("too many governance nullifiers")
	}
	fields = append(fields, inputs.GovNullifiers...)
	for _, field := range fields {
		if len(field) != 32 {
			return errors.New("public input must be 32 bytes")
		}
	}
	return nil
}

func (b Bundle) Validate() error {
	if b.Schema != Schema {
		return errors.New("unsupported replay schema")
	}
	if !validHash(b.Identity.ExecutableSHA256) || !validHash(b.ID) || !validHash(b.Integrity) {
		return errors.New("invalid replay fingerprint")
	}
	if err := validateEvidence(b.Proof, b.Inputs); err != nil {
		return err
	}
	if b.ID != b.captureID() || b.Integrity != b.checksum() {
		return errors.New("replay integrity mismatch")
	}
	if b.CapturedAt.IsZero() || b.Error == "" {
		return errors.New("missing rejection metadata")
	}
	return nil
}

// Read limits allocation before decoding and rejects partial or extra JSON data.
func Read(r io.Reader) (Bundle, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBundleBytes+1))
	if err != nil {
		return Bundle{}, err
	}
	if len(data) > MaxBundleBytes {
		return Bundle{}, errors.New("replay bundle too large")
	}
	var b Bundle
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return Bundle{}, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Bundle{}, errors.New("extra replay data")
	}
	return b, b.Validate()
}

// Replay returns a classification rather than treating any rejection as a match.
// The caller must supply the real verifier, never the mock build.
func Replay(b Bundle, identity Identity, verifier zkp.Verifier, compare bool) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	match := b.Identity.ExecutableSHA256 == identity.ExecutableSHA256
	if !match && !compare {
		return "", errors.New("executable differs; use --compare to explicitly test a different build")
	}
	err := verifier.VerifyDelegation(b.Proof, b.Inputs)
	prefix := ""
	if !match {
		prefix = "comparison: "
	}
	if err == nil {
		return prefix + "verified unexpectedly", nil
	}
	if err.Error() != b.Error {
		return prefix + "rejected with a different error", nil
	}
	return prefix + "rejection reproduced", nil
}

// newBundle copies only the validated, bounded verifier inputs.
func newBundle(proof []byte, inputs zkp.DelegationInputs, metadata Metadata, identity Identity, failure error, now time.Time) (Bundle, error) {
	if err := validateEvidence(proof, inputs); err != nil {
		return Bundle{}, err
	}
	if failure == nil || len(failure.Error()) > 2048 || len(metadata.ChainID) > 128 || len(metadata.Phase) > 32 || len(metadata.TxHash) > 64 {
		return Bundle{}, errors.New("capture metadata outside limit")
	}
	b := Bundle{Schema: Schema, CapturedAt: now.UTC(), Metadata: metadata, Identity: identity, Proof: proof, Inputs: inputs, Error: failure.Error()}
	b.ID = b.captureID()
	b.Integrity = b.checksum()
	data, err := json.Marshal(b)
	if err != nil {
		return Bundle{}, err
	}
	if len(data) > MaxBundleBytes {
		return Bundle{}, fmt.Errorf("capture exceeds %d bytes", MaxBundleBytes)
	}
	// A JSON round-trip gives the writer ownership of every byte slice.
	var owned Bundle
	err = json.Unmarshal(data, &owned)
	return owned, err
}
