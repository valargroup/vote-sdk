package types_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	errorsmod "cosmossdk.io/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests check the chain's side of the proxy delegation golden vectors
// (docs/proxy-delegation/contracts-v1.md §13).

// hexBytes is a byte string that the vector file writes as lowercase hex.
type hexBytes []byte

// UnmarshalJSON decodes a lowercase hex string and rejects null, which the
// vector file uses only for fields these tests decode as *hexBytes.
func (b *hexBytes) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return errors.New("unexpected null hex field")
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s != strings.ToLower(s) {
		return errors.New("hex field is not lowercase: " + s)
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	*b = decoded
	return nil
}

// vectorCase is one {name, inputs, expected} case of a vector section.
type vectorCase[I, E any] struct {
	Name     string `json:"name"`
	Inputs   I      `json:"inputs"`
	Expected E      `json:"expected"`
}

// proxyVectorSections returns the top-level sections of the vector file.
func proxyVectorSections(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var sections map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(svtest.ProxyDelegationV1Vectors(), &sections))
	return sections
}

// decodeVectorSection decodes section name into out, rejecting fields that
// out does not model, and fails if the section is absent.
func decodeVectorSection(t *testing.T, name string, out any) {
	t.Helper()
	raw, ok := proxyVectorSections(t)[name]
	require.Truef(t, ok, "vector section %q is missing", name)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	require.NoErrorf(t, dec.Decode(out), "decode vector section %q", name)
}

// requireHex fails unless got equals want, comparing hex for readable diffs.
func requireHex(t *testing.T, want, got []byte, msgAndArgs ...any) {
	t.Helper()
	require.Equal(t, hex.EncodeToString(want), hex.EncodeToString(got), msgAndArgs...)
}

// checkVectorSignature checks that seed, an RFC 8032 secret seed, derives
// wantKey and signs digest as wantSig, and that the chain accepts that key
// and signature.
func checkVectorSignature(t *testing.T, seed, wantKey, digest, wantSig []byte) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(seed)
	requireHex(t, wantKey, priv.Public().(ed25519.PublicKey), "public key")
	requireHex(t, wantSig, ed25519.Sign(priv, digest), "signature")
	require.NoError(t, types.ValidateEd25519Key(wantKey))
	require.NoError(t, types.VerifyEd25519ZIP215(wantKey, digest, wantSig))
}

// TestProxyVectorSectionsAreAllHandled checks the file format and that the
// vendored client file holds exactly the sections vote-sdk reads.
//
// testutil/testdata/proxy_delegation_v1.json is the one place the shared
// client vector file (zcash_voting, zcash_vote_delegate/test-vectors/) is
// vendored: the sections Go implements, byte for byte. These tests and the
// keeper's read the chain formats; internal/votingconfig, cmd/voting-config
// and the UI read delegate_key, config_domains and the wallet list sections.
func TestProxyVectorSectionsAreAllHandled(t *testing.T) {
	sections := proxyVectorSections(t)
	var format string
	require.NoError(t, json.Unmarshal(sections["format"], &format))
	require.Equal(t, "proxy_delegation_v1", format)

	read := []string{
		"format", "contract", "generator",
		"d1", "handle", "proof_id", "register", "attest", "retire", "ballot",
		"domains", "kv_keys", "entry", "ed25519_keys", "errors",
		"delegate_key", "config_domains", "wallet_list_payload", "wallet_list_document",
	}
	require.ElementsMatch(t, read, slices.Collect(maps.Keys(sections)),
		"vendor exactly the sections vote-sdk reads")
}

// TestProxyVectorFilesAreFrozen pins the vector files vote-sdk holds: its
// copy of the client file, the chain file it generates, and the
// zkp4_instance section of voting-circuits' circuit file.
func TestProxyVectorFilesAreFrozen(t *testing.T) {
	for _, f := range []struct {
		name string
		file []byte
		want string
	}{
		{"proxy_delegation_v1.json", svtest.ProxyDelegationV1Vectors(), "27c89f3c4f9aca80c403ce96738a60bc391eb971795274300eba28720f743b4b"},
		{"proxy_delegation_chain_v1.json", svtest.ProxyDelegationChainV1Vectors(), "fdfe5131df8bb8a37b9025567e3b6d8cd41e04a106f6d2f3102952efa5f3f244"},
		{"proxy_delegation_circuit_v1.json", svtest.ProxyDelegationCircuitV1Vectors(), "9c14cd2ed3ee10e6d8381f6c11c8e5a1fb0d10bb64a6cc367fb4ba2729fc1c69"},
	} {
		sum := sha256.Sum256(f.file)
		require.Equalf(t, f.want, hex.EncodeToString(sum[:]), "%s changed; update every copy and this hash together", f.name)
	}
}

// TestProxyVectorsD1 checks D1 and batch_digest (contracts-v1 §4.1).
func TestProxyVectorsD1(t *testing.T) {
	type dc struct {
		RVpk                      hexBytes `json:"r_vpk"`
		VanNullifier              hexBytes `json:"van_nullifier"`
		VoteAuthorityNoteNew      hexBytes `json:"vote_authority_note_new"`
		ProxyDelegationCommitment hexBytes `json:"proxy_delegation_commitment"`
		DCSlotNullifier           hexBytes `json:"dc_slot_nullifier"`
	}
	type inputs struct {
		ChainID                  string    `json:"chain_id"`
		VoteRoundID              hexBytes  `json:"vote_round_id"`
		VoteCommTreeAnchorHeight uint64    `json:"vote_comm_tree_anchor_height"`
		ExpiryHeight             uint64    `json:"expiry_height"`
		RegistrationVanCmx       *hexBytes `json:"registration_van_cmx"`
		DCs                      []dc      `json:"dcs"`
	}
	type expected struct {
		D1          hexBytes `json:"d1"`
		BatchDigest hexBytes `json:"batch_digest"`
	}
	var cases []vectorCase[inputs, expected]
	decodeVectorSection(t, "d1", &cases)
	require.NotEmpty(t, cases)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Inputs
			fields := types.ProxyBatchSighashFields{
				VoteRoundID:              in.VoteRoundID,
				VoteCommTreeAnchorHeight: in.VoteCommTreeAnchorHeight,
				ExpiryHeight:             in.ExpiryHeight,
				HasRegistration:          in.RegistrationVanCmx != nil,
			}
			if in.RegistrationVanCmx != nil {
				fields.RegistrationVanCmx = *in.RegistrationVanCmx
			}
			for _, d := range in.DCs {
				fields.DCs = append(fields.DCs, types.ProxyDCSighashFields{
					RVpk:                      d.RVpk,
					VanNullifier:              d.VanNullifier,
					VoteAuthorityNoteNew:      d.VoteAuthorityNoteNew,
					ProxyDelegationCommitment: d.ProxyDelegationCommitment,
					DCSlotNullifier:           d.DCSlotNullifier,
				})
			}

			d1, err := types.ComputeProxyDelegationBatchSighash(in.ChainID, fields)
			require.NoError(t, err)
			requireHex(t, c.Expected.D1, d1, "d1")
			batchDigest, err := types.ComputeProxyDelegationBatchDigest(in.ChainID, fields)
			require.NoError(t, err)
			requireHex(t, c.Expected.BatchDigest, batchDigest, "batch_digest")

			// The chain binds a message's fields through SighashFields.
			msg := &types.MsgProxyDelegationBatch{
				VoteRoundId:              in.VoteRoundID,
				VoteCommTreeAnchorHeight: in.VoteCommTreeAnchorHeight,
				ExpiryHeight:             in.ExpiryHeight,
			}
			if in.RegistrationVanCmx != nil {
				msg.Registration = &types.MsgDelegateVote{VanCmx: *in.RegistrationVanCmx}
			}
			for _, d := range in.DCs {
				msg.Dcs = append(msg.Dcs, &types.ProxyDelegationAction{
					VanNullifier:              d.VanNullifier,
					RVpk:                      d.RVpk,
					VoteAuthorityNoteNew:      d.VoteAuthorityNoteNew,
					ProxyDelegationCommitment: d.ProxyDelegationCommitment,
					DcSlotNullifier:           d.DCSlotNullifier,
				})
			}
			d1, err = types.ComputeProxyDelegationBatchSighash(in.ChainID, msg.SighashFields())
			require.NoError(t, err)
			requireHex(t, c.Expected.D1, d1, "d1 of the message")
			batchDigest, err = types.ComputeProxyDelegationBatchDigest(in.ChainID, msg.SighashFields())
			require.NoError(t, err)
			requireHex(t, c.Expected.BatchDigest, batchDigest, "batch_digest of the message")
		})
	}
}

// TestProxyVectorsCanonicalForms checks the canonical handle and proof id
// rules (contracts-v1 §2.5).
func TestProxyVectorsCanonicalForms(t *testing.T) {
	type expected struct {
		Canonical bool `json:"canonical"`
	}
	var handles []vectorCase[struct {
		Provider uint32 `json:"provider"`
		Handle   string `json:"handle"`
	}, expected]
	decodeVectorSection(t, "handle", &handles)
	require.NotEmpty(t, handles)
	for _, c := range handles {
		t.Run("handle/"+c.Name, func(t *testing.T) {
			err := types.ValidateDelegateHandle(c.Inputs.Provider, c.Inputs.Handle)
			require.Equal(t, c.Expected.Canonical, err == nil, "%v", err)
			if err != nil {
				require.ErrorIs(t, err, types.ErrInvalidField)
			}
		})
	}

	var proofIDs []vectorCase[struct {
		Provider uint32 `json:"provider"`
		ProofID  string `json:"proof_id"`
	}, expected]
	decodeVectorSection(t, "proof_id", &proofIDs)
	require.NotEmpty(t, proofIDs)
	for _, c := range proofIDs {
		t.Run("proof_id/"+c.Name, func(t *testing.T) {
			// A registration checks its provider with the handle first.
			err := types.ValidateDelegateHandle(c.Inputs.Provider, "a")
			if err == nil {
				err = types.ValidateDelegateProofID(c.Inputs.Provider, c.Inputs.ProofID)
			}
			require.Equal(t, c.Expected.Canonical, err == nil, "%v", err)
			if err != nil {
				require.ErrorIs(t, err, types.ErrInvalidField)
			}
		})
	}
}

// TestProxyVectorsRegister checks REGISTER and its dk_sig (contracts-v1 §4.2).
func TestProxyVectorsRegister(t *testing.T) {
	type inputs struct {
		ChainID  string   `json:"chain_id"`
		Provider uint32   `json:"provider"`
		Handle   string   `json:"handle"`
		ProofID  string   `json:"proof_id"`
		DKSecret hexBytes `json:"dk_sk"`
	}
	type expected struct {
		DK     hexBytes `json:"dk"`
		Digest hexBytes `json:"digest"`
		DKSig  hexBytes `json:"dk_sig"`
	}
	var cases []vectorCase[inputs, expected]
	decodeVectorSection(t, "register", &cases)
	require.NotEmpty(t, cases)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in, want := c.Inputs, c.Expected
			digest, err := types.ComputeDelegateRegisterDigest(in.ChainID, types.DelegateRegistrationFields{
				DK: want.DK, Provider: in.Provider, Handle: in.Handle, ProofID: in.ProofID,
			})
			require.NoError(t, err)
			requireHex(t, want.Digest, digest)
			checkVectorSignature(t, in.DKSecret, want.DK, digest, want.DKSig)
		})
	}
}

// TestProxyVectorsAttest checks ATTEST and the verifier signature (contracts-v1 §4.2).
func TestProxyVectorsAttest(t *testing.T) {
	type inputs struct {
		ChainID            string   `json:"chain_id"`
		DK                 hexBytes `json:"dk"`
		Provider           uint32   `json:"provider"`
		Handle             string   `json:"handle"`
		ProofID            string   `json:"proof_id"`
		NotBefore          uint64   `json:"not_before"`
		ExpiresAt          uint64   `json:"expires_at"`
		VerifierSecretSeed hexBytes `json:"verifier_secret_seed"`
	}
	type expected struct {
		Digest      hexBytes `json:"digest"`
		VerifierKey hexBytes `json:"verifier_key"`
		Sig         hexBytes `json:"sig"`
	}
	var cases []vectorCase[inputs, expected]
	decodeVectorSection(t, "attest", &cases)
	require.NotEmpty(t, cases)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in, want := c.Inputs, c.Expected
			digest, err := types.ComputeDelegateAttestDigest(in.ChainID, types.DelegateRegistrationFields{
				DK: in.DK, Provider: in.Provider, Handle: in.Handle, ProofID: in.ProofID,
			}, in.NotBefore, in.ExpiresAt)
			require.NoError(t, err)
			requireHex(t, want.Digest, digest)
			checkVectorSignature(t, in.VerifierSecretSeed, want.VerifierKey, digest, want.Sig)
		})
	}
}

// TestProxyVectorsRetire checks RETIRE and its dk_sig (contracts-v1 §4.2).
func TestProxyVectorsRetire(t *testing.T) {
	type inputs struct {
		ChainID       string   `json:"chain_id"`
		DelegateIndex uint32   `json:"delegate_index"`
		DKSecret      hexBytes `json:"dk_sk"`
	}
	type expected struct {
		DK     hexBytes `json:"dk"`
		Digest hexBytes `json:"digest"`
		DKSig  hexBytes `json:"dk_sig"`
	}
	var cases []vectorCase[inputs, expected]
	decodeVectorSection(t, "retire", &cases)
	require.NotEmpty(t, cases)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in, want := c.Inputs, c.Expected
			digest, err := types.ComputeDelegateRetireDigest(in.ChainID, in.DelegateIndex, want.DK)
			require.NoError(t, err)
			requireHex(t, want.Digest, digest)
			checkVectorSignature(t, in.DKSecret, want.DK, digest, want.DKSig)
		})
	}
}

// TestProxyVectorsBallot checks BALLOT and its dk_sig (contracts-v1 §4.2).
func TestProxyVectorsBallot(t *testing.T) {
	type inputs struct {
		ChainID       string   `json:"chain_id"`
		VoteRoundID   hexBytes `json:"vote_round_id"`
		DelegateIndex uint32   `json:"delegate_index"`
		Options       []uint32 `json:"options"`
		DKSecret      hexBytes `json:"dk_sk"`
	}
	type expected struct {
		DK     hexBytes `json:"dk"`
		Digest hexBytes `json:"digest"`
		DKSig  hexBytes `json:"dk_sig"`
	}
	var cases []vectorCase[inputs, expected]
	decodeVectorSection(t, "ballot", &cases)
	require.NotEmpty(t, cases)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in, want := c.Inputs, c.Expected
			digest, err := types.ComputeDelegateBallotDigest(in.ChainID, in.VoteRoundID, in.DelegateIndex, in.Options)
			require.NoError(t, err)
			requireHex(t, want.Digest, digest)
			checkVectorSignature(t, in.DKSecret, want.DK, digest, want.DKSig)
		})
	}
}

// TestProxyVectorsDomains checks that the Go domains are the vector file's.
func TestProxyVectorsDomains(t *testing.T) {
	var domains struct {
		Existing        []string `json:"existing"`
		ProxyDelegation []string `json:"proxy_delegation"`
		PrefixFree      bool     `json:"prefix_free"`
	}
	decodeVectorSection(t, "domains", &domains)
	require.ElementsMatch(t, svoteDomains, slices.Concat(domains.Existing, domains.ProxyDelegation))
	require.True(t, domains.PrefixFree)
}

// TestProxyVectorsKVKeys checks the entry, used-key, handle, next-index,
// params, ballot and pool share count store keys (contracts-v1 §3.2).
func TestProxyVectorsKVKeys(t *testing.T) {
	type inputs struct {
		Kind          string   `json:"kind"`
		DelegateIndex uint32   `json:"delegate_index"`
		DK            hexBytes `json:"dk"`
		Provider      uint32   `json:"provider"`
		Handle        string   `json:"handle"`
		VoteRoundID   hexBytes `json:"vote_round_id"`
	}
	type expected struct {
		Key hexBytes `json:"key"`
	}
	var cases []vectorCase[inputs, expected]
	decodeVectorSection(t, "kv_keys", &cases)
	require.NotEmpty(t, cases)

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			in := c.Inputs
			var (
				key []byte
				err error
			)
			switch in.Kind {
			case "entry":
				key = types.ProxyDelegateEntryKey(in.DelegateIndex)
			case "used_key":
				key, err = types.ProxyUsedDelegateKeyKey(in.DK)
			case "handle":
				key = types.ProxyDelegateHandleKey(in.Provider, in.Handle)
			case "next_index":
				key = types.ProxyNextDelegateIndexKey
			case "params":
				key = types.ProxyDelegationParamsKey
			case "ballot":
				key, err = types.DelegateBallotKey(in.VoteRoundID, in.DelegateIndex)
			case "pool_share_count":
				key, err = types.ShareCountKey(in.VoteRoundID, types.PoolProposalID, in.DelegateIndex)
			default:
				t.Fatalf("unknown key kind %q", in.Kind)
			}
			require.NoError(t, err)
			requireHex(t, c.Expected.Key, key)
		})
	}
}

// keyRuleVectorErrors and zip215VectorErrors map the vector file's Ed25519
// failure names to the Go errors.
var (
	keyRuleVectorErrors = map[string]error{
		"NotOnCurve":     types.ErrEd25519KeyNotOnCurve,
		"NonCanonical":   types.ErrEd25519KeyNonCanonical,
		"SmallOrder":     types.ErrEd25519KeySmallOrder,
		"NotTorsionFree": types.ErrEd25519KeyNotTorsionFree,
	}
	zip215VectorErrors = map[string]error{
		"PublicKeyNotOnCurve":  types.ErrEd25519KeyNotOnCurve,
		"ScalarNotCanonical":   types.ErrEd25519ScalarNotCanonical,
		"CommitmentNotOnCurve": types.ErrEd25519CommitmentNotOnCurve,
		"EquationMismatch":     types.ErrEd25519EquationMismatch,
	}
)

// requireVectorError fails unless got is the error that want names in
// names, or nil when want is nil.
func requireVectorError(t *testing.T, names map[string]error, want *string, got error) {
	t.Helper()
	if want == nil {
		require.NoError(t, got)
		return
	}
	wantErr, ok := names[*want]
	require.Truef(t, ok, "unknown vector error %q", *want)
	require.ErrorIs(t, got, wantErr)
}

// TestProxyVectorsEd25519Keys checks the key rule and ZIP-215 verification,
// including which check fails (contracts-v1 §0).
func TestProxyVectorsEd25519Keys(t *testing.T) {
	type keyRuleInputs struct {
		Key hexBytes `json:"key"`
	}
	type keyRuleExpected struct {
		Error *string `json:"error"`
	}
	type zip215Inputs struct {
		PublicKey hexBytes `json:"public_key"`
		Message   hexBytes `json:"message"`
		Signature hexBytes `json:"signature"`
	}
	type zip215Expected struct {
		Error        *string `json:"error"`
		KeyRuleError *string `json:"key_rule_error"`
	}
	var sections struct {
		KeyRule []vectorCase[keyRuleInputs, keyRuleExpected] `json:"key_rule"`
		ZIP215  []vectorCase[zip215Inputs, zip215Expected]   `json:"zip215"`
	}
	decodeVectorSection(t, "ed25519_keys", &sections)
	require.NotEmpty(t, sections.KeyRule)
	require.NotEmpty(t, sections.ZIP215)

	for _, c := range sections.KeyRule {
		t.Run("key_rule/"+c.Name, func(t *testing.T) {
			requireVectorError(t, keyRuleVectorErrors, c.Expected.Error, types.ValidateEd25519Key(c.Inputs.Key))
		})
	}
	for _, c := range sections.ZIP215 {
		t.Run("zip215/"+c.Name, func(t *testing.T) {
			in := c.Inputs
			requireVectorError(t, zip215VectorErrors, c.Expected.Error, types.VerifyEd25519ZIP215(in.PublicKey, in.Message, in.Signature))
			requireVectorError(t, keyRuleVectorErrors, c.Expected.KeyRuleError, types.ValidateEd25519Key(in.PublicKey))
		})
	}
}

// TestProxyVectorsEntry checks that every entry the client decoder accepts
// is the chain's own encoding: the entry's deterministic protobuf is the
// case's value, and the value decodes back to it (contracts-v1 §3.1, §11.6).
// The refused values exercise the client's strict decoder, which the chain
// has no counterpart of.
func TestProxyVectorsEntry(t *testing.T) {
	type entry struct {
		Index            uint32   `json:"index"`
		DK               hexBytes `json:"dk"`
		Provider         uint32   `json:"provider"`
		Handle           string   `json:"handle"`
		ProofID          string   `json:"proof_id"`
		Status           uint32   `json:"status"`
		StatusReason     uint32   `json:"status_reason"`
		RegisteredHeight uint64   `json:"registered_height"`
	}
	var cases []vectorCase[struct {
		Value hexBytes `json:"value"`
	}, struct {
		Entry *entry  `json:"entry"`
		Error *string `json:"error"`
	}]
	decodeVectorSection(t, "entry", &cases)
	accepted := 0
	for _, c := range cases {
		if c.Expected.Entry == nil {
			require.NotNil(t, c.Expected.Error, c.Name)
			continue
		}
		accepted++
		t.Run(c.Name, func(t *testing.T) {
			want := c.Expected.Entry
			msg := &types.ProxyDelegateEntry{
				Index: want.Index, Dk: want.DK, Provider: want.Provider, Handle: want.Handle, ProofId: want.ProofID,
				Status: want.Status, StatusReason: want.StatusReason, RegisteredHeight: want.RegisteredHeight,
			}
			encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
			require.NoError(t, err)
			requireHex(t, c.Inputs.Value, encoded)
			var decoded types.ProxyDelegateEntry
			require.NoError(t, proto.Unmarshal(c.Inputs.Value, &decoded))
			require.True(t, proto.Equal(msg, &decoded))
		})
	}
	require.Positive(t, accepted)
}

// TestProxyVectorsErrors checks each error's registered name, code and
// codespace (contracts-v1 §6).
func TestProxyVectorsErrors(t *testing.T) {
	registered := map[string]*errorsmod.Error{
		"ErrDuplicateNullifier":       types.ErrDuplicateNullifier,
		"ErrRoundNotFound":            types.ErrRoundNotFound,
		"ErrRoundNotActive":           types.ErrRoundNotActive,
		"ErrInvalidProof":             types.ErrInvalidProof,
		"ErrInvalidSignature":         types.ErrInvalidSignature,
		"ErrInvalidAnchorHeight":      types.ErrInvalidAnchorHeight,
		"ErrInvalidField":             types.ErrInvalidField,
		"ErrCommitmentTreeFull":       types.ErrCommitmentTreeFull,
		"ErrProxyDelegationDisabled":  types.ErrProxyDelegationDisabled,
		"ErrProxyDelegationPaused":    types.ErrProxyDelegationPaused,
		"ErrProxyBatchExpired":        types.ErrProxyBatchExpired,
		"ErrDelegateNotFound":         types.ErrDelegateNotFound,
		"ErrDelegateSuspended":        types.ErrDelegateSuspended,
		"ErrDelegateRetired":          types.ErrDelegateRetired,
		"ErrDelegateBallotExists":     types.ErrDelegateBallotExists,
		"ErrInvalidBallot":            types.ErrInvalidBallot,
		"ErrRegistrationClosed":       types.ErrRegistrationClosed,
		"ErrRegistryFull":             types.ErrRegistryFull,
		"ErrDelegateKeyReused":        types.ErrDelegateKeyReused,
		"ErrHandleTaken":              types.ErrHandleTaken,
		"ErrInvalidAttestation":       types.ErrInvalidAttestation,
		"ErrInsufficientAttestations": types.ErrInsufficientAttestations,
		"ErrInvalidDelegateKey":       types.ErrInvalidDelegateKey,
		"ErrInvalidProxyParams":       types.ErrInvalidProxyParams,
	}
	var cases []struct {
		Name      string `json:"name"`
		Code      uint32 `json:"code"`
		Codespace string `json:"codespace"`
	}
	decodeVectorSection(t, "errors", &cases)

	seen := map[string]bool{}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			require.Falsef(t, seen[c.Name], "%s is listed twice", c.Name)
			seen[c.Name] = true
			err, ok := registered[c.Name]
			require.Truef(t, ok, "no Go error is registered as %s", c.Name)
			codespace, code, _ := errorsmod.ABCIInfo(err, false)
			require.Equal(t, c.Codespace, codespace)
			require.Equal(t, c.Code, code)
		})
	}
	require.Len(t, seen, len(registered), "the vector file and the Go table list different errors")
}
