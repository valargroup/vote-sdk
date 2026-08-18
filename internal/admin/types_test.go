package admin

import "testing"

func TestDefaultConfigUsesVotingConfigGateway(t *testing.T) {
	const want = "https://voting.valargroup.dev/prod/"
	if got := DefaultConfig().ConfigURL; got != want {
		t.Fatalf("DefaultConfig().ConfigURL = %q, want %q", got, want)
	}
}
