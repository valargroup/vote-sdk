package pirupdate

import (
	"encoding/base64"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestSharedVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/pir-update-vector.json")
	require.NoError(t, err)
	var v struct {
		Scope        string
		Config       string
		Payload      Payload
		Message      string `json:"message_base64"`
		Key          Key
		Attestations Attestations
	}
	require.NoError(t, json.Unmarshal(raw, &v))
	message, err := SigningBytes(v.Scope, v.Payload)
	require.NoError(t, err)
	require.Equal(t, v.Message, base64.StdEncoding.EncodeToString(message))
	_, err = verify([]byte(v.Config), v.Attestations, v.Scope, []Key{v.Key})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, scope, config string
		keys                []Key
	}{
		{"wrong scope", "stage", v.Config, []Key{v.Key}}, {"changed config", "prod", v.Config + " ", []Key{v.Key}}, {"unknown key", "prod", v.Config, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verify([]byte(tc.config), v.Attestations, tc.scope, tc.keys)
			require.Error(t, err)
		})
	}
	for _, sig := range []string{"", "!!!", "AA=="} {
		a := v.Attestations
		a.Signatures = []Signature{{KeyID: v.Key.KeyID, Alg: "ed25519", Sig: sig}}
		_, err := verify([]byte(v.Config), a, v.Scope, []Key{v.Key})
		require.Error(t, err)
	}
	for _, field := range []string{"linux_amd64_sha256", "linux_arm64_sha256", "snapshot_manifest_sha256", "service_sha256"} {
		var a map[string]any
		bytes, _ := json.Marshal(v.Attestations)
		require.NoError(t, json.Unmarshal(bytes, &a))
		a["payload"].(map[string]any)[field] = string(make([]byte, 64))
		bytes, _ = json.Marshal(a)
		var changed Attestations
		require.NoError(t, json.Unmarshal(bytes, &changed))
		_, err := verify([]byte(v.Config), changed, v.Scope, []Key{v.Key})
		require.Error(t, err)
	}
}

// The pinned coordinator keys must differ per scope. Sharing one key across
// prod and stage would leave SigningBytes' domain prefix as the only thing
// separating the environments, and would force a stage coordinator to
// re-derive the prod key against the prod chain ID.
func TestTrustedKeysDifferPerScope(t *testing.T) {
	prod := TrustedKeys("prod")
	stage := TrustedKeys("stage")
	require.NotEmpty(t, prod)
	require.NotEmpty(t, stage)

	prodKeys := map[string]bool{}
	for _, k := range prod {
		require.NotEmpty(t, k.Pubkey)
		prodKeys[k.Pubkey] = true
	}
	for _, k := range stage {
		require.NotEmpty(t, k.Pubkey)
		require.False(t, prodKeys[k.Pubkey],
			"stage pins a key that is also trusted for prod: %s", k.Pubkey)
	}

	require.Empty(t, TrustedKeys("nonsense"))
}
