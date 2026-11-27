package votingconfig

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The vectors are shared with the Rust wallet library
// (zcash_voting/test-vectors/ballot_digest_v1.json); both implementations
// must reproduce them byte for byte.
type ballotVectors struct {
	BallotDigestV1 []struct {
		Name        string `json:"name"`
		Ballot      Ballot `json:"ballot"`
		EncodingHex string `json:"encoding_hex"`
		DigestHex   string `json:"digest_hex"`
	} `json:"ballot_digest_v1"`
	RoundAuthV3 []struct {
		RoundIDHex      string    `json:"round_id_hex"`
		EaPKHex         string    `json:"ea_pk_hex"`
		PIRLayout       PIRLayout `json:"pir_layout"`
		BallotDigestHex string    `json:"ballot_digest_hex"`
		PayloadHex      string    `json:"payload_hex"`
	} `json:"round_auth_v3"`
}

func loadBallotVectors(t *testing.T) ballotVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/ballot_digest_v1.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vectors ballotVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(vectors.BallotDigestV1) == 0 || len(vectors.RoundAuthV3) == 0 {
		t.Fatalf("vector file is empty")
	}
	return vectors
}

func TestBallotEncodingMatchesSharedVectors(t *testing.T) {
	for _, vector := range loadBallotVectors(t).BallotDigestV1 {
		if got := hex.EncodeToString(vector.Ballot.CanonicalEncoding()); got != vector.EncodingHex {
			t.Errorf("%s: encoding = %s, want %s", vector.Name, got, vector.EncodingHex)
		}
		digest := vector.Ballot.Digest()
		if got := hex.EncodeToString(digest[:]); got != vector.DigestHex {
			t.Errorf("%s: digest = %s, want %s", vector.Name, got, vector.DigestHex)
		}
	}
}

func TestCanonicalPayloadV3MatchesSharedVector(t *testing.T) {
	for _, vector := range loadBallotVectors(t).RoundAuthV3 {
		var eaPK, digest [32]byte
		copy(eaPK[:], mustHex(t, vector.EaPKHex))
		copy(digest[:], mustHex(t, vector.BallotDigestHex))
		payload, err := CanonicalPayloadV3(vector.RoundIDHex, eaPK, vector.PIRLayout, digest)
		if err != nil {
			t.Fatalf("payload: %v", err)
		}
		if got := hex.EncodeToString(payload); got != vector.PayloadHex {
			t.Fatalf("payload = %s, want %s", got, vector.PayloadHex)
		}
	}
}

func TestParseServedRoundAcceptsQueryResponseAndBareRound(t *testing.T) {
	roundID, eaPK := testRoundIdentity()
	round := servedRoundJSON(roundID, eaPK)
	for name, raw := range map[string]string{
		"query response": `{"round":` + round + `}`,
		"bare round":     round,
	} {
		served, err := ParseServedRound([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if served.RoundID != roundID || served.EaPK != eaPK {
			t.Fatalf("%s: identity = %s/%x", name, served.RoundID, served.EaPK)
		}
		if served.Ballot.Digest() != testBallot().Digest() {
			t.Fatalf("%s: ballot = %+v", name, served.Ballot)
		}
	}
}

func TestParseServedRoundRejectsMissingIdentity(t *testing.T) {
	if _, err := ParseServedRound([]byte(`{"round":{"title":"x"}}`)); err == nil {
		t.Fatalf("expected a round without vote_round_id to be rejected")
	}
}

func TestBallotAttestationSignsAndValidates(t *testing.T) {
	pub, priv := testKeypair(t)
	roundID, eaPK := testRoundIdentity()
	layout := testBallotLayout()
	entry := attestedEntry(t, priv, roundID, eaPK, layout, testBallot())
	trusted := []TrustedKey{{KeyID: "key-1", Alg: AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(pub)}}

	if err := ValidateBallotAttestation(roundID, entry, trusted, layout); err != nil {
		t.Fatalf("valid attestation rejected: %v", err)
	}

	entry.BallotAttestation = nil
	if err := ValidateBallotAttestation(roundID, entry, trusted, layout); err != nil {
		t.Fatalf("absent attestation must be accepted: %v", err)
	}
}

func TestBallotAttestationRejectsUntrustedMalformedAndWrongLayout(t *testing.T) {
	pub, priv := testKeypair(t)
	_, otherPriv := testKeypair(t)
	roundID, eaPK := testRoundIdentity()
	layout := testBallotLayout()
	trusted := []TrustedKey{{KeyID: "key-1", Alg: AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(pub)}}

	untrusted := attestedEntry(t, otherPriv, roundID, eaPK, layout, testBallot())
	if err := ValidateBallotAttestation(roundID, untrusted, trusted, layout); err == nil {
		t.Fatalf("attestation by an untrusted key accepted")
	}

	malformed := attestedEntry(t, priv, roundID, eaPK, layout, testBallot())
	malformed.BallotAttestation.BallotDigest = strings.ToUpper(malformed.BallotAttestation.BallotDigest)
	if err := ValidateBallotAttestation(roundID, malformed, trusted, layout); err == nil {
		t.Fatalf("non-lowercase digest accepted")
	}

	otherLayout := layout
	otherLayout.PolyLen = PolyLen2048
	if err := ValidateBallotAttestation(roundID, attestedEntry(t, priv, roundID, eaPK, layout, testBallot()), trusted, otherLayout); err == nil {
		t.Fatalf("attestation verified under a different pir_layout")
	}
}

func TestMergeBallotAttestations(t *testing.T) {
	sig := func(key string) Signature { return Signature{KeyID: key, Alg: AlgEd25519, Sig: key + "-sig"} }
	a := &BallotAttestation{BallotDigest: strings.Repeat("a", 64), Signatures: []Signature{sig("k1")}}
	b := &BallotAttestation{BallotDigest: strings.Repeat("a", 64), Signatures: []Signature{sig("k2")}}
	other := &BallotAttestation{BallotDigest: strings.Repeat("b", 64), Signatures: []Signature{sig("k2")}}

	if got, err := MergeBallotAttestations(nil, nil); err != nil || got != nil {
		t.Fatalf("nil+nil = %+v, %v", got, err)
	}
	if got, err := MergeBallotAttestations(a, nil); err != nil || got != a {
		t.Fatalf("existing must survive an incoming entry without attestation: %+v, %v", got, err)
	}
	if got, err := MergeBallotAttestations(nil, b); err != nil || got != b {
		t.Fatalf("incoming attestation must be kept: %+v, %v", got, err)
	}
	merged, err := MergeBallotAttestations(a, b)
	if err != nil || len(merged.Signatures) != 2 {
		t.Fatalf("same digest must merge signatures: %+v, %v", merged, err)
	}
	if _, err := MergeBallotAttestations(a, other); err == nil {
		t.Fatalf("differing digests must not merge")
	}
}

func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

func testRoundIdentity() (string, [32]byte) {
	var eaPK [32]byte
	for i := range eaPK {
		eaPK[i] = 7
	}
	return strings.Repeat("01", 32), eaPK
}

func testBallotLayout() PIRLayout {
	return PIRLayout{PIRDepth: 19, Tier0Layers: 12, Tier1Layers: 7, PolyLen: PolyLen4096}
}

// testBallot is the full_ballot shared vector.
func testBallot() Ballot {
	return Ballot{
		Title:         "Round 1",
		Description:   "Two questions.",
		DiscussionURL: "https://forum.example.org/t/1",
		Proposals: []BallotProposal{
			{
				ID: 1, Title: "Adopt ZIP-1", Description: "First question.", ZipNumber: "ZIP-1",
				ForumURL: "https://forum.example.org/t/2",
				Options: []BallotOption{
					{Index: 0, Label: "Support", Description: "In favor"},
					{Index: 1, Label: "Oppose"},
					{Index: 2, Label: "Abstain"},
				},
			},
			{ID: 2, Title: "Second", Options: []BallotOption{{Index: 0, Label: "A"}, {Index: 1, Label: "B"}}},
		},
	}
}

// servedRoundJSON renders a round the way the vote server's REST handler does:
// encoding/json over the proto struct, so bytes are base64 and zero values
// are omitted.
func servedRoundJSON(roundID string, eaPK [32]byte) string {
	roundIDBytes, _ := hex.DecodeString(roundID)
	return `{
		"vote_round_id": "` + base64.StdEncoding.EncodeToString(roundIDBytes) + `",
		"ea_pk": "` + base64.StdEncoding.EncodeToString(eaPK[:]) + `",
		"snapshot_height": 3000000,
		"title": "Round 1",
		"description": "Two questions.",
		"discussion_url": "https://forum.example.org/t/1",
		"proposals": [
			{"id": 1, "title": "Adopt ZIP-1", "description": "First question.", "zip_number": "ZIP-1",
			 "forum_url": "https://forum.example.org/t/2",
			 "options": [{"label": "Support", "description": "In favor"}, {"index": 1, "label": "Oppose"}, {"index": 2, "label": "Abstain"}]},
			{"id": 2, "title": "Second", "options": [{"label": "A"}, {"index": 1, "label": "B"}]}
		]
	}`
}

func attestedEntry(t *testing.T, priv ed25519.PrivateKey, roundID string, eaPK [32]byte, layout PIRLayout, ballot Ballot) RoundEntry {
	t.Helper()
	digest := ballot.Digest()
	sig, err := SignBallotAttestation(priv, roundID, eaPK, layout, digest)
	if err != nil {
		t.Fatalf("sign attestation: %v", err)
	}
	return RoundEntry{
		AuthVersion: AuthVersionV2,
		EaPK:        base64.StdEncoding.EncodeToString(eaPK[:]),
		BallotAttestation: &BallotAttestation{
			BallotDigest: hex.EncodeToString(digest[:]),
			Signatures:   []Signature{{KeyID: "key-1", Alg: AlgEd25519, Sig: base64.StdEncoding.EncodeToString(sig)}},
		},
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	out, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return out
}
