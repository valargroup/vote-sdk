package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

func TestMergeConfigPREntryPreservesHistoricalLayoutSignatures(t *testing.T) {
	t.Parallel()

	oldRoundID := strings.Repeat("b", 64)
	oldEntry := signedRoundEntry(t, oldRoundID, bytes.Repeat([]byte{2}, 32), "valar-test")
	dynamic, static := configDocuments(t, map[string]votingconfig.RoundEntry{oldRoundID: oldEntry})
	var cfg votingconfig.SignedConfig
	require.NoError(t, json.Unmarshal(dynamic, &cfg))
	var trusted votingconfig.StaticConfig
	require.NoError(t, json.Unmarshal(static, &trusted))
	require.True(t, votingconfig.VerifyEntrySignatures(oldRoundID, oldEntry, trusted.TrustedKeys, cfg.PIRLayout))
	cfg.PIRLayout.PIRDepth = 20
	cfg.PIRLayout.Tier1Layers = 8
	require.False(t, votingconfig.VerifyEntrySignatures(oldRoundID, oldEntry, trusted.TrustedKeys, cfg.PIRLayout))
	dynamic, err := json.Marshal(cfg)
	require.NoError(t, err)

	for _, tc := range []struct {
		name             string
		invalidSignature bool
		wrongLayout      bool
	}{
		{name: "new signature preserves historical round"},
		{name: "invalid new signature is rejected", invalidSignature: true},
		{name: "different signed layout is rejected", wrongLayout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := validCreateConfigPRRequest(t)
			body.PIRLayout = cfg.PIRLayout
			var eaPK [32]byte
			copy(eaPK[:], bytes.Repeat([]byte{1}, 32))
			sig, err := votingconfig.SignV2(testEd25519PrivateKey(), body.RoundID, eaPK, body.PIRLayout)
			require.NoError(t, err)
			if tc.invalidSignature {
				sig = make([]byte, 64)
			}
			body.Entry.Signatures[0].Sig = base64.StdEncoding.EncodeToString(sig)
			if tc.wrongLayout {
				body.PIRLayout = testConfigPIRLayout()
			}
			merged, _, _, err := mergeConfigPREntry(dynamic, static, body.RoundID, body.Entry, body.PIRLayout)
			if tc.invalidSignature || tc.wrongLayout {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			var result votingconfig.SignedConfig
			require.NoError(t, json.Unmarshal(merged, &result))
			require.Equal(t, oldEntry, result.Rounds[oldRoundID])
			require.True(t, votingconfig.VerifyEntrySignatures(body.RoundID, result.Rounds[body.RoundID], trusted.TrustedKeys, result.PIRLayout))
		})
	}
}
