package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/gorilla/mux"
)

// newSignedRegisterParts generates a fresh keypair and ADR-036 signature over the
// register payload wire (field order matches registerPayloadWire). Returns the
// derived operator bech32, signature, and pubkey (base64).
func newSignedRegisterParts(t *testing.T, url, moniker string, timestamp int64) (operator, signatureB64, pubKeyB64 string) {
	t.Helper()

	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().(*secp256k1.PubKey)
	pubBytes := pub.Bytes()
	operator, err := pubKeyToAddress(pubBytes)
	if err != nil {
		t.Fatal(err)
	}

	payloadBytes, err := marshalRegisterPayloadWire(registerPayloadWire{
		OperatorAddress: operator,
		URL:             url,
		Moniker:         moniker,
		Timestamp:       timestamp,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Sign exactly the way the production `svoted sign-arbitrary` CLI does:
	// hand priv.Sign the raw amino doc and let it SHA-256 internally. Pre-
	// hashing here would cause the verifier to double-hash and false-pass —
	// which masked the bug that broke `join.sh` for every external operator.
	signDoc := makeSignArbitraryDoc(operator, string(payloadBytes))
	sig, err := priv.Sign(signDoc)
	if err != nil {
		t.Fatal(err)
	}
	return operator, base64.StdEncoding.EncodeToString(sig), base64.StdEncoding.EncodeToString(pubBytes)
}

func TestMarshalRegisterPayloadWireMatchesJoinScriptJSON(t *testing.T) {
	t.Parallel()

	payload, err := marshalRegisterPayloadWire(registerPayloadWire{
		OperatorAddress: "sv1operator",
		URL:             "https://op.example/?a=1&b=<node>",
		Moniker:         `evan.node "west"`,
		Timestamp:       123,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := `{"operator_address":"sv1operator","url":"https://op.example/?a=1&b=<node>","moniker":"evan.node \"west\"","timestamp":123}`
	if string(payload) != want {
		t.Fatalf("payload JSON:\n got %s\nwant %s", payload, want)
	}
}

func TestHandleRegisterValidator_Table(t *testing.T) {
	t.Parallel()

	ts := time.Now().Unix()
	operator, sig, pub := newSignedRegisterParts(t, "https://op.example", "m1", ts)

	newRouter := func(bonded bool) (*mux.Router, *Store) {
		dir := t.TempDir()
		st, err := NewStore(filepath.Join(dir, "r.db"))
		if err != nil {
			t.Fatal(err)
		}
		a := &Admin{
			configURL:   "http://invalid.local",
			logger:      log.NewNopLogger(),
			store:       st,
			checkBonded: func(string) bool { return bonded },
		}
		r := mux.NewRouter()
		RegisterRoutes(r, func() *Admin { return a }, log.NewNopLogger())
		return r, st
	}

	t.Run("stale_timestamp", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(false)
		defer st.Close()
		old := time.Now().Unix() - timestampWindowSecs - 10
		opOld, s, p := newSignedRegisterParts(t, "https://op.example", "m1", old)
		body := map[string]interface{}{
			"operator_address": opOld,
			"url":              "https://op.example",
			"moniker":          "m1",
			"timestamp":        old,
			"signature":        s,
			"pub_key":          p,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("bad_signature", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(false)
		defer st.Close()
		body := map[string]interface{}{
			"operator_address": operator,
			"url":              "https://op.example",
			"moniker":          "m1",
			"timestamp":        ts,
			"signature":        base64.StdEncoding.EncodeToString([]byte{1, 2, 3}),
			"pub_key":          pub,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("unbonded_upserts_pending", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(false)
		defer st.Close()
		body := map[string]interface{}{
			"operator_address": operator,
			"url":              "https://op.example",
			"moniker":          "m1",
			"timestamp":        ts,
			"signature":        sig,
			"pub_key":          pub,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["status"] != "pending" {
			t.Fatalf("want pending, got %#v", resp)
		}
		list, err := st.ListPendingRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].OperatorAddress != operator {
			t.Fatalf("pending list: %+v", list)
		}
	})

	t.Run("dotted_moniker_upserts_pending", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(false)
		defer st.Close()
		tsDotted := time.Now().Unix()
		opDotted, s, p := newSignedRegisterParts(t, "https://op.example", "evan.node", tsDotted)
		body := map[string]interface{}{
			"operator_address": opDotted,
			"url":              "https://op.example",
			"moniker":          "evan.node",
			"timestamp":        tsDotted,
			"signature":        s,
			"pub_key":          p,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		list, err := st.ListPendingRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Moniker != "evan.node" {
			t.Fatalf("pending list: %+v", list)
		}
	})

	t.Run("json_sensitive_moniker_upserts_pending", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(false)
		defer st.Close()
		moniker := `evan.node "west"`
		tsSpecial := time.Now().Unix()
		opSpecial, s, p := newSignedRegisterParts(t, "https://op.example", moniker, tsSpecial)
		body := map[string]interface{}{
			"operator_address": opSpecial,
			"url":              "https://op.example",
			"moniker":          moniker,
			"timestamp":        tsSpecial,
			"signature":        s,
			"pub_key":          p,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		list, err := st.ListPendingRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].Moniker != moniker {
			t.Fatalf("pending list: %+v", list)
		}
	})

	t.Run("empty_url_still_queues_pending", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(false)
		defer st.Close()
		tsEmptyURL := time.Now().Unix()
		opEmptyURL, s, p := newSignedRegisterParts(t, "", "needs-funding", tsEmptyURL)
		body := map[string]interface{}{
			"operator_address": opEmptyURL,
			"url":              "",
			"moniker":          "needs-funding",
			"timestamp":        tsEmptyURL,
			"signature":        s,
			"pub_key":          p,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		list, err := st.ListPendingRegistrations()
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].OperatorAddress != opEmptyURL || list[0].URL != "" {
			t.Fatalf("pending list: %+v", list)
		}
	})

	t.Run("bonded_deletes_pending", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(true)
		defer st.Close()
		now := time.Now().Unix()
		_ = st.UpsertPendingRegistration(PendingRegistration{
			OperatorAddress: operator,
			URL:             "https://x",
			Moniker:         "m1",
			RequestedAt:     now,
			ExpiresAt:       now + 3600,
		})
		body := map[string]interface{}{
			"operator_address": operator,
			"url":              "https://op.example",
			"moniker":          "m1",
			"timestamp":        ts,
			"signature":        sig,
			"pub_key":          pub,
		}
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["status"] != "bonded" {
			t.Fatalf("want bonded, got %#v", resp)
		}
		list, _ := st.ListPendingRegistrations()
		if len(list) != 0 {
			t.Fatalf("want empty pending, got %+v", list)
		}
	})

	t.Run("idempotent_double_bonded", func(t *testing.T) {
		t.Parallel()
		r, st := newRouter(true)
		defer st.Close()
		body := map[string]interface{}{
			"operator_address": operator,
			"url":              "https://op.example",
			"moniker":          "m1",
			"timestamp":        ts,
			"signature":        sig,
			"pub_key":          pub,
		}
		b, _ := json.Marshal(body)
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodPost, "/api/register-validator", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("iter %d: want 200, got %d: %s", i, w.Code, w.Body.String())
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp["status"] != "bonded" {
				t.Fatalf("iter %d: want bonded, got %#v", i, resp)
			}
		}
		_ = st
	})
}
