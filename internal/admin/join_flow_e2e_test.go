package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/gorilla/mux"
)

// Simulates operator join admin flow: register while unbonded (pending row),
// then register again after bonding (row cleared, status bonded). Funding and
// create-val-tx happen on-chain outside this package.
func TestJoinAdminFlow_PendingThenBonded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := NewStore(filepath.Join(dir, "join.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var bonded atomic.Bool
	check := func(string) bool { return bonded.Load() }

	a := &Admin{
		configURL:   "http://invalid.local",
		logger:      log.NewNopLogger(),
		store:       st,
		checkBonded: check,
	}
	r := mux.NewRouter()
	RegisterRoutes(r, func() *Admin { return a }, log.NewNopLogger())

	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().(*secp256k1.PubKey)
	operator, err := pubKeyToAddress(pub.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	ts := time.Now().Unix()
	payloadBytes, err := json.Marshal(registerPayloadWire{
		OperatorAddress: operator,
		URL:             "https://op.example",
		Moniker:         "join-e2e",
		Timestamp:       ts,
	})
	if err != nil {
		t.Fatal(err)
	}
	signDoc := makeSignArbitraryDoc(operator, string(payloadBytes))
	msgHash := sha256.Sum256(signDoc)
	sig, err := priv.Sign(msgHash[:])
	if err != nil {
		t.Fatal(err)
	}
	sigB64 := base64.StdEncoding.EncodeToString(sig)
	pubB64 := base64.StdEncoding.EncodeToString(pub.Bytes())

	post := func() *httptest.ResponseRecorder {
		body := map[string]interface{}{
			"operator_address": operator,
			"url":              "https://op.example",
			"moniker":          "join-e2e",
			"timestamp":        ts,
			"signature":        sigB64,
			"pub_key":          pubB64,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	getPending := func() int {
		req := httptest.NewRequest(http.MethodGet, "/api/pending-validators", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET pending: %d %s", w.Code, w.Body.String())
		}
		var rows []PendingValidatorPublic
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}

	w1 := post()
	if w1.Code != http.StatusOK {
		t.Fatalf("first POST: %d %s", w1.Code, w1.Body.String())
	}
	var r1 map[string]interface{}
	if err := json.Unmarshal(w1.Body.Bytes(), &r1); err != nil {
		t.Fatal(err)
	}
	if r1["status"] != "pending" {
		t.Fatalf("want pending first, got %#v", r1)
	}
	if getPending() != 1 {
		t.Fatalf("want 1 pending row")
	}

	bonded.Store(true)
	w2 := post()
	if w2.Code != http.StatusOK {
		t.Fatalf("second POST: %d %s", w2.Code, w2.Body.String())
	}
	var r2 map[string]string
	if err := json.Unmarshal(w2.Body.Bytes(), &r2); err != nil {
		t.Fatal(err)
	}
	if r2["status"] != "bonded" {
		t.Fatalf("want bonded, got %#v", r2)
	}
	if getPending() != 0 {
		t.Fatalf("want 0 pending rows after bond")
	}

	w3 := post()
	if w3.Code != http.StatusOK {
		t.Fatalf("third POST: %d %s", w3.Code, w3.Body.String())
	}
	var r3 map[string]string
	if err := json.Unmarshal(w3.Body.Bytes(), &r3); err != nil {
		t.Fatal(err)
	}
	if r3["status"] != "bonded" {
		t.Fatalf("want idempotent bonded, got %#v", r3)
	}
}
