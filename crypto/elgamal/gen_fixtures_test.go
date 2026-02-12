package elgamal

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/crypto/chacha20"

	"github.com/stretchr/testify/require"
)

// deterministicReader returns an io.Reader that produces a deterministic byte
// stream from a fixed seed using ChaCha20. This makes the fixture output
// stable across runs so the JSON file only changes when we intentionally
// re-generate it.
func deterministicReader() *chacha20.Cipher {
	// 32-byte key, 12-byte nonce — all derived from a human-readable seed.
	key := [32]byte{}
	copy(key[:], "zally-elgamal-fixture-seed-00001")
	nonce := [12]byte{}
	copy(nonce[:], "fixture-nonce")
	stream, err := chacha20.NewUnauthenticatedCipher(key[:], nonce[:])
	if err != nil {
		panic("chacha20: " + err.Error())
	}
	return stream
}

// chachaReader wraps a ChaCha20 cipher as an io.Reader (XOR zero bytes).
type chachaReader struct{ c *chacha20.Cipher }

func (r *chachaReader) Read(p []byte) (int, error) {
	// Zero the buffer, then XOR with keystream — equivalent to reading
	// the raw keystream.
	for i := range p {
		p[i] = 0
	}
	r.c.XORKeyStream(p, p)
	return len(p), nil
}

// FixtureShare is one encrypted vote share in the fixture.
type FixtureShare struct {
	Value    int    `json:"value"`
	EncShare string `json:"enc_share"` // base64, 64 bytes
}

// ElGamalFixtures is the top-level fixture file layout.
type ElGamalFixtures struct {
	EaPk                string         `json:"ea_pk"`                // base64, 32 bytes
	Shares              []FixtureShare `json:"shares"`               // encrypted shares
	ExpectedAccumulated string         `json:"expected_accumulated"` // base64, 64 bytes
	ExpectedTotal       int            `json:"expected_total"`       // sum of all share values
}

// TestGenerateElGamalFixtures creates the elgamal_tally.json fixture file
// consumed by the TypeScript E2E tests.
//
// Run:
//
//	go test -run TestGenerateElGamalFixtures -v ./crypto/elgamal/
//
// Output: sdk/tests/api/fixtures/elgamal_tally.json
func TestGenerateElGamalFixtures(t *testing.T) {
	rng := &chachaReader{deterministicReader()}
	sk, pk := KeyGen(rng)

	// Compress the public key.
	pkBytes := pk.Point.ToAffineCompressed()
	require.Len(t, pkBytes, 32)

	// Encrypt individual values.
	values := []int{5, 10}
	shares := make([]FixtureShare, len(values))
	var accumulated *Ciphertext

	for i, v := range values {
		ct, err := Encrypt(pk, uint64(v), rng)
		require.NoError(t, err)

		ctBytes, err := MarshalCiphertext(ct)
		require.NoError(t, err)
		require.Len(t, ctBytes, 64)

		shares[i] = FixtureShare{
			Value:    v,
			EncShare: base64.StdEncoding.EncodeToString(ctBytes),
		}

		if accumulated == nil {
			accumulated = ct
		} else {
			accumulated = HomomorphicAdd(accumulated, ct)
		}
	}

	// Verify decryption of the accumulated ciphertext.
	expectedTotal := 0
	for _, v := range values {
		expectedTotal += v
	}

	decPoint := DecryptToPoint(sk, accumulated)
	table := NewBSGSTable(uint64(expectedTotal + 1000))
	solved, err := table.Solve(decPoint)
	require.NoError(t, err, "BSGS should find the discrete log")
	require.Equal(t, uint64(expectedTotal), solved, "decrypted total should match")

	// Serialize the accumulated ciphertext.
	accBytes, err := MarshalCiphertext(accumulated)
	require.NoError(t, err)

	fixtures := ElGamalFixtures{
		EaPk:                base64.StdEncoding.EncodeToString(pkBytes),
		Shares:              shares,
		ExpectedAccumulated: base64.StdEncoding.EncodeToString(accBytes),
		ExpectedTotal:       expectedTotal,
	}

	data, err := json.MarshalIndent(fixtures, "", "  ")
	require.NoError(t, err)

	// Write to sdk/tests/api/fixtures/elgamal_tally.json.
	_, thisFile, _, _ := runtime.Caller(0)
	fixturesDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "tests", "api", "fixtures")
	err = os.MkdirAll(fixturesDir, 0o755)
	require.NoError(t, err)

	outPath := filepath.Join(fixturesDir, "elgamal_tally.json")
	err = os.WriteFile(outPath, data, 0o644)
	require.NoError(t, err)

	t.Logf("Wrote %d bytes to %s", len(data), outPath)
	t.Logf("EA PK:    %s", fixtures.EaPk)
	t.Logf("Share[0]: value=%d enc=%s...", shares[0].Value, shares[0].EncShare[:20])
	t.Logf("Share[1]: value=%d enc=%s...", shares[1].Value, shares[1].EncShare[:20])
	t.Logf("Expected: total=%d acc=%s...", expectedTotal, fixtures.ExpectedAccumulated[:20])
}
