package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/log"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadHelperConfigProofConcurrencyV3(t *testing.T) {
	tests := []struct {
		name            string
		toml            string
		want            int
		wantLogContains []string
	}{
		{
			name: "no setting uses v3 default",
			want: 2,
			wantLogContains: []string{
				"source=\"v3 default\"",
			},
		},
		{
			name: "v1 setting is ignored",
			toml: "[helper]\n" +
				"max_concurrent_proofs = 8\n",
			want: 2,
			wantLogContains: []string{
				"deprecated helper proof concurrency setting ignored",
				"key=helper.max_concurrent_proofs ",
				"replacement=helper.max_concurrent_proofs_v3",
			},
		},
		{
			name: "v2 setting is ignored",
			toml: "[helper]\n" +
				"max_concurrent_proofs_v2 = 1\n",
			want: 2,
			wantLogContains: []string{
				"deprecated helper proof concurrency setting ignored",
				"key=helper.max_concurrent_proofs_v2",
				"replacement=helper.max_concurrent_proofs_v3",
			},
		},
		{
			name: "explicit v3 setting is honored",
			toml: "[helper]\n" +
				"max_concurrent_proofs_v3 = 3\n",
			want: 3,
			wantLogContains: []string{
				"source=helper.max_concurrent_proofs_v3",
			},
		},
		{
			name: "v3 setting wins when all versions are present",
			toml: "[helper]\n" +
				"max_concurrent_proofs = 8\n" +
				"max_concurrent_proofs_v2 = 1\n" +
				"max_concurrent_proofs_v3 = 4\n",
			want: 4,
			wantLogContains: []string{
				"deprecated helper proof concurrency setting ignored",
				"key=helper.max_concurrent_proofs ",
				"key=helper.max_concurrent_proofs_v2",
				"source=helper.max_concurrent_proofs_v3",
			},
		},
		{
			name: "invalid v3 setting falls back to two",
			toml: "[helper]\n" +
				"max_concurrent_proofs_v3 = 0\n",
			want: 2,
			wantLogContains: []string{
				"invalid helper proof concurrency, using fallback",
				"fallback=2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := viper.New()
			if tt.toml != "" {
				v.SetConfigType("toml")
				require.NoError(t, v.ReadConfig(strings.NewReader(tt.toml)))
			}

			var logs bytes.Buffer
			cfg := readHelperConfig(v, log.NewLogger(&logs, log.ColorOption(false)))

			assert.Equal(t, tt.want, cfg.MaxConcurrentProofs)
			assert.Contains(t, logs.String(), "helper proof concurrency configured")
			assert.Contains(t, logs.String(), fmt.Sprintf("effective=%d", tt.want))
			for _, want := range tt.wantLogContains {
				assert.Contains(t, logs.String(), want)
			}
		})
	}
}
