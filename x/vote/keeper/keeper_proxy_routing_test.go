package keeper_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
)

// TestRoutingVectors recomputes every routing case of the chain's vector file
// (docs/proxy-delegation/contracts-v1.md §5.6, §13). RerandomizeRoutedBucket
// must reproduce rho and final, rho must be the hash of the contract's
// preimage, built here by hand, and final must decrypt under ea_sk to
// combined's value with a C1 that a partial-decryption DLEQ accepts. In the
// griefing cases combined's C1 is the identity, which that DLEQ rejects.
func TestRoutingVectors(t *testing.T) {
	cases := routingVectorCases(t)
	griefing := map[string]bool{"identity_c1_griefing": true, "identity_ciphertext_griefing": true}
	names := make(map[string]bool, len(cases))
	for _, c := range cases {
		require.Falsef(t, names[c.Name], "%s is listed twice", c.Name)
		names[c.Name] = true
		t.Run(c.Name, func(t *testing.T) {
			in := c.Inputs
			roundID := routingVectorHex(t, in.VoteRoundID)
			combinedBytes := routingVectorHex(t, in.Combined)
			combined, err := elgamal.UnmarshalCiphertext(combinedBytes)
			require.NoError(t, err)
			eaPK, err := elgamal.UnmarshalPublicKey(routingVectorHex(t, in.EaPK))
			require.NoError(t, err)
			eaSK, err := elgamal.UnmarshalSecretKey(routingVectorHex(t, in.EaSK))
			require.NoError(t, err)
			require.True(t, elgamal.PallasGenerator().Mul(eaSK.Scalar).Equal(eaPK.Point), "ea_pk is ea_sk * G")

			final, rho, err := keeper.RerandomizeRoutedBucket(eaPK, roundID, in.ProposalID, in.VoteDecision, combined)
			require.NoError(t, err)
			finalBytes, err := elgamal.MarshalCiphertext(final)
			require.NoError(t, err)
			require.Equal(t, c.Expected.Rho, hex.EncodeToString(rho.Bytes()), "rho")
			require.Equal(t, c.Expected.Final, hex.EncodeToString(finalBytes), "final")

			preimage := append([]byte("svote-route-rerand-v1"), roundID...)
			preimage = binary.BigEndian.AppendUint32(preimage, in.ProposalID)
			preimage = binary.BigEndian.AppendUint32(preimage, in.VoteDecision)
			preimage = append(preimage, combinedBytes...)
			require.Equal(t, new(curvey.ScalarPallas).Hash(preimage).Bytes(), rho.Bytes(), "rho is the preimage's hash")
			zero, err := elgamal.EncryptWithRandomness(eaPK, 0, rho)
			require.NoError(t, err)
			recomputed, err := elgamal.MarshalCiphertext(elgamal.HomomorphicAdd(combined, zero))
			require.NoError(t, err)
			require.Equal(t, finalBytes, recomputed, "final is combined plus Enc(0, rho)")

			require.False(t, final.C1.IsIdentity(), "final C1")
			require.False(t, final.C2.IsIdentity(), "final C2")
			require.True(t, elgamal.DecryptToPoint(eaSK, final).Equal(elgamal.DecryptToPoint(eaSK, combined)),
				"final decrypts to combined's value")
			require.True(t, partialDecryptionVerifies(eaSK, eaPK, final.C1))
			if griefing[c.Name] {
				require.True(t, combined.C1.IsIdentity(), "the naive sum's C1 is the identity")
				require.False(t, partialDecryptionVerifies(eaSK, eaPK, combined.C1))
			}
		})
	}
	for name := range griefing {
		require.Truef(t, names[name], "routing has no %s case", name)
	}
	require.Greater(t, len(names), len(griefing), "routing has ordinary cases too")
}

// routingVectorCase is one case of the routing section of the chain's vector
// file.
type routingVectorCase struct {
	Name   string `json:"name"`
	Inputs struct {
		VoteRoundID  string `json:"vote_round_id"`
		ProposalID   uint32 `json:"proposal_id"`
		VoteDecision uint32 `json:"vote_decision"`
		Combined     string `json:"combined"`
		EaPK         string `json:"ea_pk"`
		EaSK         string `json:"ea_sk"`
	} `json:"inputs"`
	Expected struct {
		Rho   string `json:"rho"`
		Final string `json:"final"`
	} `json:"expected"`
}

// routingVectorCases decodes the routing section of the chain's vector file,
// refusing unknown fields.
func routingVectorCases(t *testing.T) []routingVectorCase {
	t.Helper()
	var sections map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(svtest.ProxyDelegationChainV1Vectors(), &sections))
	dec := json.NewDecoder(bytes.NewReader(sections["routing"]))
	dec.DisallowUnknownFields()
	var cases []routingVectorCase
	require.NoError(t, dec.Decode(&cases))
	return cases
}

// routingVectorHex decodes a lowercase hex field of the vector file.
func routingVectorHex(t *testing.T, s string) []byte {
	t.Helper()
	require.Equal(t, strings.ToLower(s), s, "hex is lowercase")
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

// partialDecryptionVerifies reports whether a partial-decryption DLEQ by sk
// over c1 verifies, as the tally requires of every non-empty bucket.
func partialDecryptionVerifies(sk *elgamal.SecretKey, pk *elgamal.PublicKey, c1 curvey.Point) bool {
	proof, err := elgamal.GeneratePartialDecryptDLEQ(sk.Scalar, c1)
	if err != nil {
		return false
	}
	return elgamal.VerifyPartialDecryptDLEQ(proof, pk.Point, c1, c1.Mul(sk.Scalar)) == nil
}
