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

func TestReadHelperConfigProofConcurrencyV2(t *testing.T) {
	tests := []struct {
		name            string
		toml            string
		want            int
		wantLogContains []string
	}{
		{
			name: "no setting uses safe v2 default",
			want: 1,
		},
		{
			name: "legacy setting is ignored",
			toml: "[helper]\n" +
				"max_concurrent_proofs = 8\n",
			want: 1,
			wantLogContains: []string{
				"deprecated helper proof concurrency setting ignored",
				"helper.max_concurrent_proofs_v2",
			},
		},
		{
			name: "explicit v2 setting is honored",
			toml: "[helper]\n" +
				"max_concurrent_proofs_v2 = 3\n",
			want: 3,
		},
		{
			name: "v2 setting wins when both are present",
			toml: "[helper]\n" +
				"max_concurrent_proofs = 8\n" +
				"max_concurrent_proofs_v2 = 2\n",
			want: 2,
			wantLogContains: []string{
				"deprecated helper proof concurrency setting ignored",
			},
		},
		{
			name: "invalid v2 setting falls back to one",
			toml: "[helper]\n" +
				"max_concurrent_proofs_v2 = 0\n",
			want: 1,
			wantLogContains: []string{
				"invalid helper proof concurrency, using fallback",
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
