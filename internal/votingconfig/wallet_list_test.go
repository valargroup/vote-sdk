package votingconfig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

// These tests check this package's wallet list code (wallet_list.go), which
// the voting-config CLI and the config repo's CI use, against the shared
// client vectors (docs/proxy-delegation/contracts-v1.md §12.3, §13).

// proxyVectorsPath is vote-sdk's copy of the client vector file. It is read
// rather than embedded through testutil, which links the Rust circuits
// library that the release job's voting-config tests run without.
// x/vote/types/proxy_vectors_test.go pins its hash.
const proxyVectorsPath = "../../testutil/testdata/proxy_delegation_v1.json"

// proxyVectorSection decodes the named section of the vector file into out,
// rejecting fields out does not model.
func proxyVectorSection(t *testing.T, name string, out any) {
	t.Helper()
	raw, err := os.ReadFile(proxyVectorsPath)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatalf("decode vectors: %v", err)
	}
	section, ok := sections[name]
	if !ok {
		t.Fatalf("the vector file has no %s section", name)
	}
	dec := json.NewDecoder(bytes.NewReader(section))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// mustDecodeHex decodes s, which must be exactly n bytes of hex.
func mustDecodeHex(t *testing.T, s string, n int) []byte {
	t.Helper()
	b, err := DecodeHexFixed(s, n, "vector field")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// errorName returns a *WalletListError's name, or "" for no error.
func errorName(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var listErr *WalletListError
	if !errors.As(err, &listErr) {
		t.Fatalf("error %v is not a *WalletListError", err)
	}
	return listErr.Name
}

// TestConfigDomainsVectors checks that the config family's signing
// domains are prefix-free with the wallet list domain among them.
func TestConfigDomainsVectors(t *testing.T) {
	var section struct {
		Domains    []string `json:"domains"`
		PrefixFree bool     `json:"prefix_free"`
	}
	proxyVectorSection(t, "config_domains", &section)
	found := false
	prefixFree := true
	for i, a := range section.Domains {
		found = found || a == WalletListDomainV1
		for j, b := range section.Domains {
			if i != j && strings.HasPrefix(b, a) {
				prefixFree = false
			}
		}
	}
	if !found || !section.PrefixFree || prefixFree != section.PrefixFree {
		t.Fatalf("domains %q: found %v, prefix-free %v", section.Domains, found, prefixFree)
	}
}

// TestWalletListPayloadVectors checks each payload and its rule verdict,
// that the signature verifies and reproduces, and that the published
// document verifies to the same payload.
func TestWalletListPayloadVectors(t *testing.T) {
	type rowInputs struct {
		Index     uint32  `json:"index"`
		Provider  uint32  `json:"provider"`
		Handle    string  `json:"handle"`
		DK        string  `json:"dk"`
		Name      string  `json:"name"`
		Statement *string `json:"statement"`
		Picture   *string `json:"picture"`
	}
	var cases []struct {
		Name   string `json:"name"`
		Inputs struct {
			WalletID         string      `json:"wallet_id"`
			ChainID          string      `json:"chain_id"`
			Version          uint64      `json:"version"`
			IssuedAt         uint64      `json:"issued_at"`
			ReservedNames    []string    `json:"reserved_names"`
			Delegates        []rowInputs `json:"delegates"`
			SignerKeyID      string      `json:"signer_key_id"`
			SignerSecretSeed string      `json:"signer_secret_seed"`
		} `json:"inputs"`
		Expected struct {
			Payload   *string `json:"payload"`
			SignerKey string  `json:"signer_key"`
			Signature *string `json:"signature"`
			Document  *string `json:"document"`
			Error     *string `json:"error"`
		} `json:"expected"`
	}
	proxyVectorSection(t, "wallet_list_payload", &cases)
	if len(cases) == 0 {
		t.Fatal("wallet_list_payload has no cases")
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in, want := c.Inputs, c.Expected
			priv := ed25519.NewKeyFromSeed(mustDecodeHex(t, in.SignerSecretSeed, ed25519.SeedSize))
			pub := priv.Public().(ed25519.PublicKey)
			if got := hex.EncodeToString(pub); got != want.SignerKey {
				t.Fatalf("signer key = %s, want %s", got, want.SignerKey)
			}
			list := WalletList{
				WalletID: in.WalletID, ChainID: in.ChainID, Version: in.Version,
				IssuedAt: in.IssuedAt, ReservedNames: in.ReservedNames,
			}
			for _, row := range in.Delegates {
				r := WalletListRow{
					Index: row.Index, Provider: row.Provider, Handle: row.Handle, DK: row.DK, Name: row.Name,
					Statement: row.Statement,
				}
				if row.Picture != nil {
					picture, err := hex.DecodeString(*row.Picture)
					if err != nil {
						t.Fatal(err)
					}
					r.Picture = picture
				}
				list.Delegates = append(list.Delegates, r)
			}
			payload, err := list.Payload()
			ruleErr := errorName(t, err)
			if want.Error != nil {
				if ruleErr != *want.Error {
					t.Fatalf("error = %q, want %q", ruleErr, *want.Error)
				}
				if want.Payload != nil || want.Signature != nil || want.Document != nil {
					t.Fatal("an error case must not carry a payload, signature or document")
				}
				return
			}
			if ruleErr != "" {
				t.Fatalf("unexpected error %q", ruleErr)
			}
			if want.Payload == nil || want.Signature == nil || want.Document == nil {
				t.Fatal("a success case must carry a payload, signature and document")
			}
			if got := hex.EncodeToString(payload); got != *want.Payload {
				t.Fatalf("payload mismatch\n got: %s\nwant: %s", got, *want.Payload)
			}
			sig := mustDecodeHex(t, *want.Signature, ed25519.SignatureSize)
			if !ed25519.Verify(pub, payload, sig) || !bytes.Equal(ed25519.Sign(priv, payload), sig) {
				t.Fatal("the vector signature does not verify or reproduce")
			}
			signer := TrustedKey{KeyID: in.SignerKeyID, Alg: AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(pub)}
			verified, err := VerifyWalletList([]byte(*want.Document), in.WalletID, in.ChainID, []TrustedKey{signer})
			if err != nil || verified.SignedBy != in.SignerKeyID || !bytes.Equal(verified.Payload, payload) {
				t.Fatalf("document: %+v, %v", verified, err)
			}
			sigs := []Signature{{KeyID: in.SignerKeyID, Alg: AlgEd25519, Sig: base64.StdEncoding.EncodeToString(sig)}}
			if doc, err := list.MarshalDocument(sigs); err != nil || string(doc) != *want.Document {
				t.Fatalf("MarshalDocument = %s, %v; want the vector document byte for byte", doc, err)
			}
		})
	}
}

// TestWalletListDocumentVectors checks a wallet's verdict on each fetched
// document under its pinned keys.
func TestWalletListDocumentVectors(t *testing.T) {
	var cases []struct {
		Name   string `json:"name"`
		Inputs struct {
			Document   string `json:"document"`
			WalletID   string `json:"wallet_id"`
			ChainID    string `json:"chain_id"`
			PinnedKeys []struct {
				KeyID     string `json:"key_id"`
				PublicKey string `json:"public_key"`
			} `json:"pinned_keys"`
		} `json:"inputs"`
		Expected struct {
			Payload  *string `json:"payload"`
			SignedBy *string `json:"signed_by"`
			Error    *string `json:"error"`
		} `json:"expected"`
	}
	proxyVectorSection(t, "wallet_list_document", &cases)
	if len(cases) == 0 {
		t.Fatal("wallet_list_document has no cases")
	}
	// The ZIP-215 case pins Go's verifier to the wallets' rule (contracts-v1 §0).
	const zip215Case = "torsion_in_commitment_verifies"
	sawZIP215 := false
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in, want := c.Inputs, c.Expected
			var pinned []TrustedKey
			for _, key := range in.PinnedKeys {
				pub := mustDecodeHex(t, key.PublicKey, ed25519.PublicKeySize)
				pinned = append(pinned, TrustedKey{KeyID: key.KeyID, Alg: AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(pub)})
			}
			if c.Name == zip215Case {
				sawZIP215 = true
				requireStdlibRefuses(t, in.Document, pinned)
			}
			verified, err := VerifyWalletList([]byte(in.Document), in.WalletID, in.ChainID, pinned)
			errName := errorName(t, err)
			if want.Error != nil {
				if errName != *want.Error {
					t.Fatalf("error = %q, want %q", errName, *want.Error)
				}
				return
			}
			if errName != "" {
				t.Fatalf("unexpected error %q", errName)
			}
			if want.Payload == nil || want.SignedBy == nil {
				t.Fatal("a success case must carry a payload and signer")
			}
			if got := hex.EncodeToString(verified.Payload); got != *want.Payload || verified.SignedBy != *want.SignedBy {
				t.Fatalf("payload or signer mismatch: signed by %q, want %q", verified.SignedBy, *want.SignedBy)
			}
		})
	}
	if !sawZIP215 {
		t.Fatalf("wallet_list_document has no %s case", zip215Case)
	}
}

// requireStdlibRefuses fails unless crypto/ed25519 rejects every signature
// in document under the pinned key its key_id names, so the case still
// tells ZIP-215 apart from RFC 8032's cofactorless rule.
func requireStdlibRefuses(t *testing.T, document string, pinned []TrustedKey) {
	t.Helper()
	list, sigs, err := ParseWalletList([]byte(document))
	if err != nil || len(sigs) == 0 {
		t.Fatalf("ParseWalletList: %v, %d signatures", err, len(sigs))
	}
	payload, err := list.Payload()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sigs {
		for _, key := range pinned {
			if key.KeyID != s.KeyID {
				continue
			}
			pub, _ := base64.StdEncoding.DecodeString(key.Pubkey)
			sig, _ := base64.StdEncoding.DecodeString(s.Sig)
			if ed25519.Verify(pub, payload, sig) {
				t.Fatalf("crypto/ed25519 accepts the %s signature", s.KeyID)
			}
		}
	}
}

// vizorVectorList returns the vizor_list vector's published document and its
// signer, the deterministic test key the vector signs with.
func vizorVectorList(t *testing.T) ([]byte, ed25519.PrivateKey) {
	t.Helper()
	var cases []struct {
		Name   string `json:"name"`
		Inputs struct {
			SignerSecretSeed string `json:"signer_secret_seed"`
		} `json:"inputs"`
		Expected struct {
			Document *string `json:"document"`
		} `json:"expected"`
	}
	raw, err := os.ReadFile(proxyVectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sections["wallet_list_payload"], &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Name == "vizor_list" {
			return []byte(*c.Expected.Document), ed25519.NewKeyFromSeed(mustDecodeHex(t, c.Inputs.SignerSecretSeed, ed25519.SeedSize))
		}
	}
	t.Fatal("no vizor_list vector")
	return nil, nil
}

// TestParseWalletListDraft checks that tooling reads an unsigned draft and
// re-signs it to the same payload, while a wallet refuses it as Malformed.
func TestParseWalletListDraft(t *testing.T) {
	document, signer := vizorVectorList(t)
	signed, sigs, err := ParseWalletList(document)
	if err != nil || len(sigs) != 1 {
		t.Fatalf("ParseWalletList: %v, %d signatures", err, len(sigs))
	}
	draft, err := signed.MarshalDocument(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(draft, []byte(`"signatures"`)) {
		t.Fatalf("a draft must carry no signatures member:\n%s", draft)
	}
	list, sigs, err := ParseWalletList(draft)
	if err != nil || len(sigs) != 0 {
		t.Fatalf("ParseWalletList(draft): %v, %d signatures", err, len(sigs))
	}
	payload, err := list.Payload()
	if err != nil {
		t.Fatal(err)
	}
	pinned := []TrustedKey{{KeyID: "valargroup", Alg: AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(signer.Public().(ed25519.PublicKey))}}
	if _, err := VerifyWalletList(draft, "vizor", "zvote-1", pinned); errorName(t, err) != "Malformed" {
		t.Fatalf("an unsigned list verified: %v", err)
	}
	resigned, err := list.MarshalDocument([]Signature{{KeyID: "valargroup", Alg: AlgEd25519, Sig: base64.StdEncoding.EncodeToString(ed25519.Sign(signer, payload))}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resigned, document) {
		t.Fatalf("re-signing the draft changed the document:\n%s", resigned)
	}
	if _, _, err := ParseWalletList([]byte(strings.Replace(string(draft), `"version": 1`, `"version": 0`, 1))); errorName(t, err) != "InvalidVersion" {
		t.Fatalf("a draft with version 0: %v", err)
	}
}

// TestWalletListRefusesWhatReadersDisagreeOn checks inputs Go's decoder
// would read differently from a wallet's parser.
func TestWalletListRefusesWhatReadersDisagreeOn(t *testing.T) {
	document, _ := vizorVectorList(t)
	for name, edit := range map[string]func(string) string{
		"invalid UTF-8":       func(d string) string { return strings.Replace(d, "Alice Example", "Alice \xffxample", 1) },
		"lone high surrogate": func(d string) string { return strings.Replace(d, "Alice Example", `Alice \ud83dxample`, 1) },
		"lone low surrogate":  func(d string) string { return strings.Replace(d, "Alice Example", `Alice \ude00xample`, 1) },
		"null member":         func(d string) string { return strings.Replace(d, `"handle": "alice"`, `"handle": null`, 1) },
		"miscased member":     func(d string) string { return strings.Replace(d, `"handle": "alice"`, `"Handle": "alice"`, 1) },
		"missing name":        func(d string) string { return strings.Replace(d, `"name": "Alice Example",`, "", 1) },
		"fractional index":    func(d string) string { return strings.Replace(d, `"index": 17`, `"index": 17.0`, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ParseWalletList([]byte(edit(string(document)))); errorName(t, err) != "Malformed" {
				t.Fatalf("err = %v, want Malformed", err)
			}
		})
	}
	paired := strings.Replace(string(document), "Alice Example", `Alice \ud83d\ude00`, 1)
	if _, _, err := ParseWalletList([]byte(paired)); err != nil {
		t.Fatalf("a surrogate pair was refused: %v", err)
	}
}

// TestCheckWalletListPictures checks the 128 x 128 rule config CI applies.
func TestCheckWalletListPictures(t *testing.T) {
	document, _ := vizorVectorList(t)
	list, _, err := ParseWalletList(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckWalletListPictures(list); err != nil {
		t.Fatalf("the vector picture: %v", err)
	}
	// A RIFF header around a truncated VP8L stream passes the field rules
	// but does not decode.
	broken := append([]byte(nil), list.Delegates[0].Picture[:30]...)
	broken[4], broken[5], broken[6], broken[7] = 22, 0, 0, 0
	list.Delegates[0].Picture = broken
	if _, err := list.Payload(); err != nil {
		t.Fatalf("the truncated picture should pass the field rules: %v", err)
	}
	if err := CheckWalletListPictures(list); err == nil {
		t.Fatal("a picture that does not decode passed")
	}
}

// TestCheckWalletListPicturesReadsDimensionsFirst checks that a small
// picture whose bitstream declares a 16384 x 16384 image is refused from its
// headers, without decoding the gigabyte of pixels it describes, also when a
// VP8X canvas claims 128 x 128.
func TestCheckWalletListPicturesReadsDimensionsFirst(t *testing.T) {
	document, _ := vizorVectorList(t)
	list, _, err := ParseWalletList(document)
	if err != nil {
		t.Fatal(err)
	}
	// A VP8L stream of 16384 x 16384 with no transforms and five one-symbol
	// Huffman codes, so every pixel decodes from zero bits.
	stream := []byte{0x2f, 0xff, 0xff, 0xff, 0x0f, 0x88, 0x88, 0x08}
	vp8l := append(append([]byte("VP8L"), byte(len(stream)), 0, 0, 0), stream...)
	// A VP8X chunk with no flags and a 128 x 128 canvas.
	vp8x := append([]byte("VP8X"), 10, 0, 0, 0, 0, 0, 0, 0, 127, 0, 0, 127, 0, 0)
	riff := func(chunks ...[]byte) []byte {
		body := []byte("WEBP")
		for _, c := range chunks {
			body = append(body, c...)
		}
		return append(append([]byte("RIFF"), byte(len(body)), 0, 0, 0), body...)
	}
	// A VP8 key frame header of 16383 x 16383 with a one-byte first partition.
	frame := []byte{0x30, 0, 0, 0x9d, 0x01, 0x2a, 0xff, 0x3f, 0xff, 0x3f, 0, 0}
	vp8 := append(append([]byte("VP8 "), byte(len(frame)), 0, 0, 0), frame...)
	for name, tc := range map[string]struct {
		picture []byte
		size    string
	}{
		"VP8L":                    {riff(vp8l), "16384x16384"},
		"VP8X canvas 128px, VP8L": {riff(vp8x, vp8l), "16384x16384"},
		"VP8X canvas 128px, VP8":  {riff(vp8x, vp8), "16383x16383"},
	} {
		picture := tc.picture
		t.Run(name, func(t *testing.T) {
			list.Delegates[0].Picture = picture
			if _, err := list.Payload(); err != nil {
				t.Fatalf("the picture should pass the field rules: %v", err)
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err := CheckWalletListPictures(list)
			runtime.ReadMemStats(&after)
			if err == nil || !strings.Contains(err.Error(), tc.size) {
				t.Fatalf("err = %v, want the declared size refused", err)
			}
			if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
				t.Fatalf("checking the picture allocated %d bytes", grew)
			}
		})
	}
}

// TestCheckWalletListPicturesMissingPadding checks that a file ending in an
// odd-length chunk without its padding byte is refused rather than read past
// its end.
func TestCheckWalletListPicturesMissingPadding(t *testing.T) {
	document, _ := vizorVectorList(t)
	list, _, err := ParseWalletList(document)
	if err != nil {
		t.Fatal(err)
	}
	picture := []byte("RIFF\x0d\x00\x00\x00WEBPXXXX\x01\x00\x00\x00z")
	if len(picture) != 21 {
		t.Fatalf("the picture is %d bytes", len(picture))
	}
	list.Delegates[0].Picture = picture
	if _, err := list.Payload(); err != nil {
		t.Fatalf("the picture should pass the field rules: %v", err)
	}
	if err := CheckWalletListPictures(list); err == nil || !strings.Contains(err.Error(), "does not decode") {
		t.Fatalf("err = %v, want the picture refused", err)
	}
}

// TestCheckWalletListVersion checks that a changed list needs a higher
// version and an unchanged one does not.
func TestCheckWalletListVersion(t *testing.T) {
	document, _ := vizorVectorList(t)
	previous, _, err := ParseWalletList(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckWalletListVersion(previous, previous); err != nil {
		t.Fatalf("an unchanged list: %v", err)
	}
	next := previous
	next.Delegates = previous.Delegates[:1]
	if err := CheckWalletListVersion(previous, next); err == nil {
		t.Fatal("a changed list kept its version")
	}
	next.Version = previous.Version + 1
	if err := CheckWalletListVersion(previous, next); err != nil {
		t.Fatalf("a changed list with a higher version: %v", err)
	}
	next.Version = previous.Version
	next.Delegates = previous.Delegates
	next.IssuedAt++
	if err := CheckWalletListVersion(previous, next); err == nil {
		t.Fatal("a re-signed list with a new issued_at kept its version")
	}
}
