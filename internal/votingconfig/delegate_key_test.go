package votingconfig

import "testing"

// TestDelegateKeyVectors checks the zvdk1 form of each key in the
// delegate_key vectors (contracts-v1 §11.2) and which strings decode.
// DecodeDelegateKey checks the string's form only, as a wallet list row
// needs (§12.3), so it also decodes the canonical string of a key that
// fails the key rule, which the vectors' full decoder refuses as InvalidKey.
func TestDelegateKeyVectors(t *testing.T) {
	var section struct {
		Encoding []struct {
			Name     string         `json:"name"`
			Inputs   map[string]any `json:"inputs"`
			Expected struct {
				DK          string `json:"dk"`
				Zvdk        string `json:"zvdk"`
				Zvdsk       string `json:"zvdsk"`
				Fingerprint string `json:"fingerprint"`
				Display     string `json:"display"`
			} `json:"expected"`
		} `json:"encoding"`
		Decode []struct {
			Name   string `json:"name"`
			Inputs struct {
				Text string `json:"text"`
			} `json:"inputs"`
			Expected struct {
				DK    *string `json:"dk"`
				Error *string `json:"error"`
			} `json:"expected"`
		} `json:"decode"`
		Import []any `json:"import"`
	}
	proxyVectorSection(t, "delegate_key", &section)
	if len(section.Encoding) == 0 || len(section.Decode) == 0 {
		t.Fatal("delegate_key has no cases")
	}
	for _, c := range section.Encoding {
		t.Run("encoding/"+c.Name, func(t *testing.T) {
			dk := [32]byte(mustDecodeHex(t, c.Expected.DK, 32))
			if got := EncodeDelegateKey(dk); got != c.Expected.Zvdk {
				t.Fatalf("EncodeDelegateKey = %s, want %s", got, c.Expected.Zvdk)
			}
		})
	}
	for _, c := range section.Decode {
		t.Run("decode/"+c.Name, func(t *testing.T) {
			dk, ok := DecodeDelegateKey(c.Inputs.Text)
			formOnly := c.Expected.Error != nil && *c.Expected.Error == "InvalidKey"
			if want := c.Expected.DK != nil || formOnly; ok != want {
				t.Fatalf("DecodeDelegateKey(%q) ok = %v, want %v", c.Inputs.Text, ok, want)
			}
			if c.Expected.DK != nil && dk != [32]byte(mustDecodeHex(t, *c.Expected.DK, 32)) {
				t.Fatalf("DecodeDelegateKey(%q) = %x, want %s", c.Inputs.Text, dk, *c.Expected.DK)
			}
			if ok && EncodeDelegateKey(dk) != c.Inputs.Text {
				t.Fatalf("%q does not re-encode to itself", c.Inputs.Text)
			}
		})
	}
}
