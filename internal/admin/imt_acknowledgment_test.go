package admin

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cosmossdk.io/log"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

func signTestIntent(t *testing.T, payload string) configPRAuth {
	t.Helper()
	priv := secp256k1.GenPrivKey()
	pub := priv.PubKey().Bytes()
	address, err := pubKeyToAddress(pub)
	require.NoError(t, err)
	sig, err := priv.Sign(makeSignArbitraryDoc(address, payload))
	require.NoError(t, err)
	return configPRAuth{SignerAddress: address, Payload: payload, Signature: base64.StdEncoding.EncodeToString(sig), PubKey: base64.StdEncoding.EncodeToString(pub)}
}

func TestConfigPRRequiresSignedIMTAcknowledgment(t *testing.T) {
	for _, mode := range []string{"single missing", "single false", "single unknown version", "batch one false", "tampered after signing"} {
		t.Run(mode, func(t *testing.T) {
			body := validCreateConfigPRRequest(t)
			if strings.HasPrefix(mode, "batch") {
				body = validCreateConfigPRBatchRequest(t, 2)
			}
			payload := body.Auth.Payload
			switch mode {
			case "single missing":
				payload = strings.Replace(payload, `,"imt_verification":{"acknowledged":true,"statement_version":1}`, "", 1)
			case "single false", "batch one false", "tampered after signing":
				payload = strings.Replace(payload, `"acknowledged":true`, `"acknowledged":false`, 1)
			case "single unknown version":
				payload = strings.Replace(payload, `"statement_version":1`, `"statement_version":2`, 1)
			}
			body.Auth = signTestIntent(t, payload)
			if mode == "tampered after signing" {
				body.Auth.Payload = strings.Replace(payload, `"acknowledged":false`, `"acknowledged":true`, 1)
			}
			calls := 0
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "must not contact GitHub", 500) }))
			defer gh.Close()
			a := &Admin{configPR: testConfigPRAutomation(gh.URL), logger: log.NewNopLogger(), checkVoteManager: func(string) bool { return true }}
			router := mux.NewRouter()
			RegisterRoutes(router, func() *Admin { return a }, log.NewNopLogger())
			resp := serveCreateConfigPR(t, router, body)
			require.Equal(t, http.StatusUnauthorized, resp.Code, resp.Body.String())
			require.Zero(t, calls)
		})
	}
}

func TestConfigPRReusePreservesSignaturesAndAcknowledgments(t *testing.T) {
	for _, name := range []string{"append and retry", "failed content update", "rotated trusted key"} {
		t.Run(name, func(t *testing.T) {
			rejectUpdate := name == "failed content update"
			body := validCreateConfigPRRequest(t)
			dynamic, static := configDocuments(t, nil)
			previous := body.Entry
			previous.Signatures = nil
			otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{42}, 32))
			eaPKBytes, err := base64.StdEncoding.DecodeString(body.Entry.EaPK)
			require.NoError(t, err)
			var eaPK [32]byte
			copy(eaPK[:], eaPKBytes)
			sig, err := votingconfig.SignV2(otherKey, body.RoundID, eaPK, body.PIRLayout)
			require.NoError(t, err)
			previous.Signatures = []votingconfig.Signature{{KeyID: "previous", Alg: votingconfig.AlgEd25519, Sig: base64.StdEncoding.EncodeToString(sig)}}
			branchContent, _ := configDocuments(t, map[string]votingconfig.RoundEntry{body.RoundID: previous})
			var trusted votingconfig.StaticConfig
			require.NoError(t, json.Unmarshal(static, &trusted))
			if name != "rotated trusted key" {
				trusted.TrustedKeys = append(trusted.TrustedKeys, votingconfig.TrustedKey{KeyID: "previous", Alg: votingconfig.AlgEd25519, Pubkey: base64.StdEncoding.EncodeToString(otherKey.Public().(ed25519.PublicKey))})
			}
			static, err = json.Marshal(trusted)
			require.NoError(t, err)
			priorBody := "Existing reviewer notes.\n\nPrevious manager acknowledged verifying the IMT."
			savedBody := priorBody
			patches, creates, updates := 0, 0, 0
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/"):
					writeJSON(t, w, map[string]any{"object": map[string]string{"sha": "main-sha"}})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git/refs"):
					http.Error(w, `{"message":"already exists"}`, 422)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/"+staticConfigName):
					writeContent(t, w, static, "static-sha")
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/"+dynamicConfigName):
					if r.URL.Query().Get("ref") == "main" {
						writeContent(t, w, dynamic, "main-file-sha")
					} else {
						writeContent(t, w, branchContent, "branch-file-sha")
					}
				case r.Method == http.MethodPut:
					updates++
					if rejectUpdate {
						http.Error(w, `{"message":"update rejected"}`, 422)
						return
					}
					var request struct {
						Content string `json:"content"`
						SHA     string `json:"sha"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					require.Equal(t, "branch-file-sha", request.SHA)
					updated, err := base64.StdEncoding.DecodeString(request.Content)
					require.NoError(t, err)
					if bytes.Equal(updated, branchContent) {
						http.Error(w, `{"message":"redundant content update"}`, 422)
						return
					}
					branchContent = updated
					var cfg votingconfig.SignedConfig
					require.NoError(t, json.Unmarshal(branchContent, &cfg))
					require.Len(t, cfg.Rounds[body.RoundID].Signatures, 2)
					require.True(t, votingconfig.VerifyEntrySignatures(body.RoundID, cfg.Rounds[body.RoundID], trusted.TrustedKeys, body.PIRLayout))
					writeJSON(t, w, map[string]any{"commit": map[string]string{"sha": "updated-sha"}})
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
					creates++
					http.Error(w, `{"message":"PR already exists"}`, 422)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
					writeJSON(t, w, []githubPullRequest{{Number: 123, Body: savedBody, HTMLURL: "https://github.com/valargroup/token-holder-voting-config/pull/123"}})
				case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/pulls/123"):
					var request struct {
						Body string `json:"body"`
					}
					require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					savedBody = request.Body
					patches++
					writeJSON(t, w, map[string]string{})
				default:
					t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected", 500)
				}
			}))
			defer gh.Close()
			a := &Admin{configPR: testConfigPRAutomation(gh.URL), logger: log.NewNopLogger(), checkVoteManager: func(string) bool { return true }}
			router := mux.NewRouter()
			RegisterRoutes(router, func() *Admin { return a }, log.NewNopLogger())
			resp := serveCreateConfigPR(t, router, body)
			if rejectUpdate {
				require.Equal(t, http.StatusBadGateway, resp.Code, resp.Body.String())
				require.Zero(t, creates)
				require.Zero(t, patches)
				require.Equal(t, priorBody, savedBody)
				return
			}
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			require.Contains(t, savedBody, priorBody)
			require.Contains(t, savedBody, body.Auth.SignerAddress)
			require.Contains(t, savedBody, body.RoundID)
			require.Contains(t, savedBody, body.SignedPayloadHash)
			require.Equal(t, 1, patches)
			resp = serveCreateConfigPR(t, router, body)
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			require.Equal(t, 1, patches, "retry must not duplicate acknowledgment")
			require.Equal(t, 1, updates, "retry must not rewrite unchanged content")
			var retry createConfigPRResponse
			require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &retry))
			require.Empty(t, retry.CommitSHA, "no new commit was created")

			firstManager := body.Auth.SignerAddress
			body.Auth = signTestIntent(t, body.Auth.Payload)
			require.NotEqual(t, firstManager, body.Auth.SignerAddress)
			resp = serveCreateConfigPR(t, router, body)
			require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
			require.Equal(t, 1, updates, "another manager's acknowledgment does not change the config")
			require.Equal(t, 2, patches)
			require.Contains(t, savedBody, firstManager)
			require.Contains(t, savedBody, body.Auth.SignerAddress)
		})
	}
}

func TestMergeConfigPREntryValidatesCombinedSignatures(t *testing.T) {
	for _, mode := range []string{"replacement trusted", "same key ID", "no trusted signature", "EA key changed", "unsupported version", "legacy signature"} {
		t.Run(mode, func(t *testing.T) {
			body := validCreateConfigPRRequest(t)
			dynamic, static := configDocuments(t, nil)
			// The branch was attested by a key that is no longer trusted.
			prior := body.Entry
			prior.Signatures = []votingconfig.Signature{{KeyID: "retired-key", Alg: votingconfig.AlgEd25519, Sig: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 64))}}
			switch mode {
			case "same key ID":
				prior.Signatures[0].KeyID = body.Entry.Signatures[0].KeyID
			case "no trusted signature":
				body.Entry.Signatures = prior.Signatures
			case "EA key changed":
				prior.EaPK = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
			case "unsupported version":
				prior.AuthVersion = 3
			case "legacy signature":
				prior.AuthVersion = votingconfig.AuthVersionV1
			}
			merged, existed, _, err := mergeConfigPREntry(dynamic, static, body.RoundID, body.Entry, body.PIRLayout, prior)
			switch mode {
			case "no trusted signature":
				require.ErrorContains(t, err, "no valid signature")
			case "EA key changed":
				require.ErrorContains(t, err, "ea_pk mismatch")
			case "unsupported version":
				require.ErrorContains(t, err, "cannot merge")
			default:
				require.NoError(t, err)
				require.True(t, existed)
				var cfg votingconfig.SignedConfig
				require.NoError(t, json.Unmarshal(merged, &cfg))
				var trust votingconfig.StaticConfig
				require.NoError(t, json.Unmarshal(static, &trust))
				require.True(t, votingconfig.VerifyEntrySignatures(body.RoundID, cfg.Rounds[body.RoundID], trust.TrustedKeys, body.PIRLayout))
				if mode == "same key ID" || mode == "legacy signature" {
					require.Equal(t, body.Entry.Signatures, cfg.Rounds[body.RoundID].Signatures)
				} else {
					require.Len(t, cfg.Rounds[body.RoundID].Signatures, 2)
				}
			}
		})
	}
}
