// Command proxy-chain-vectors writes the proxy delegation golden vectors that
// vote-sdk generates (docs/proxy-delegation/contracts-v1.md §13) to
// testutil/testdata/proxy_delegation_chain_v1.json. Its output depends only on
// the literals and labels below, so a rerun reproduces the file byte for
// byte. It computes routing through
// x/vote/keeper, so it links the circuits library (make circuits).
//
// Usage, from the repository root:
//
//	make proxy-chain-vectors
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/mikelodder7/curvey"
	"golang.org/x/crypto/blake2b"
	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// seedDomain prefixes every label that seeded derives bytes from.
const seedDomain = "vote-sdk proxy chain vectors v1/"

// generatorNote is the file's generator field, which documents the derivation.
const generatorNote = "vote-sdk: scripts/proxy-chain-vectors, written by `make proxy-chain-vectors`. " +
	"Seeded bytes are BLAKE2b-256(\"" + seedDomain + "\" || label), unkeyed, for a label naming the section, case and field " +
	"(for example \"entry_value/active_x/dk\"). Each dk is the Ed25519 public key of its seeded bytes " +
	"taken as an RFC 8032 secret seed; each routing vote_round_id is its seeded bytes " +
	"with the top two bits cleared, a canonical Pallas field encoding; and routing's ea_sk and ciphertext randomness are " +
	"curvey's ScalarPallas.Hash of their seeded bytes. The rest are literals. " +
	"entry_value.expected.value is the entry's deterministic protobuf encoding, the bytes the chain stores at 0x1A 0x01 u32be(index) (contracts-v1 §3.2). " +
	"routing.expected is keeper.RerandomizeRoutedBucket's output (contracts-v1 §5.6). combined and final are MarshalCiphertext " +
	"encodings (compressed C1 || C2, the identity as 32 zero bytes), ea_pk is a compressed point, and ea_sk and rho are " +
	"32-byte little-endian scalars; ea_sk is given only so tests can decrypt."

// entrySpec is one entry_value case; the generator derives its dk from name.
type entrySpec struct {
	name             string
	index            uint32
	provider         uint32
	handle           string
	proofID          string
	status           uint32
	statusReason     uint32
	registeredHeight uint64
}

// entrySpecs are the entry_value cases (contracts-v1 §3.1).
var entrySpecs = []entrySpec{{
	name:             "active_x",
	index:            1,
	provider:         types.DelegateProviderX,
	handle:           "alice",
	proofID:          "1790000000000000000",
	registeredHeight: 1_204_517,
}, {
	name:             "active_github",
	index:            2,
	provider:         types.DelegateProviderGitHub,
	handle:           "alice-example",
	proofID:          "0123456789abcdef0123456789abcdef",
	registeredHeight: 1_204_900,
}, {
	// Suspended for impersonation (reason 1).
	name:             "suspended",
	index:            3,
	provider:         types.DelegateProviderX,
	handle:           "bob_01",
	proofID:          "1790000000000000001",
	status:           types.DelegateStatusSuspended,
	statusReason:     types.DelegateStatusReasonImpersonation,
	registeredHeight: 1_150_000,
}, {
	// Retired with its own key, so the reason is 0.
	name:             "retired_by_key",
	index:            4,
	provider:         types.DelegateProviderX,
	handle:           "carol",
	proofID:          "1790000000000000002",
	status:           types.DelegateStatusRetired,
	registeredHeight: 1_100_000,
}, {
	// Retired by coordinators after a lost key (reason 2).
	name:             "retired_by_coordinators",
	index:            5,
	provider:         types.DelegateProviderGitHub,
	handle:           "dave",
	proofID:          "abcdef",
	status:           types.DelegateStatusRetired,
	statusReason:     types.DelegateStatusReasonKeyLost,
	registeredHeight: 1_100_001,
}, {
	// The longest strings and the largest values, for multi-byte varints.
	name:             "max_values",
	index:            types.MaxDelegates,
	provider:         types.DelegateProviderGitHub,
	handle:           "a123456789012345678901234567890123456-z",
	proofID:          "0123456789abcdef0123456789abcdef01234567",
	status:           types.DelegateStatusRetired,
	statusReason:     types.DelegateStatusReasonOther,
	registeredHeight: math.MaxUint64,
}}

// entryInputs is a ProxyDelegateEntry as the file writes it.
type entryInputs struct {
	Index            uint32 `json:"index"`
	DK               string `json:"dk"`
	Provider         uint32 `json:"provider"`
	Handle           string `json:"handle"`
	ProofID          string `json:"proof_id"`
	Status           uint32 `json:"status"`
	StatusReason     uint32 `json:"status_reason"`
	RegisteredHeight uint64 `json:"registered_height"`
}

// entryExpected is an entry case's expected output.
type entryExpected struct {
	Value string `json:"value"`
}

// entryCase is one {name, inputs, expected} case of entry_value.
type entryCase struct {
	Name     string        `json:"name"`
	Inputs   entryInputs   `json:"inputs"`
	Expected entryExpected `json:"expected"`
}

// routingInputs is a routed bucket as the file writes it.
type routingInputs struct {
	VoteRoundID  string `json:"vote_round_id"`
	ProposalID   uint32 `json:"proposal_id"`
	VoteDecision uint32 `json:"vote_decision"`
	Combined     string `json:"combined"`
	EaPK         string `json:"ea_pk"`
	EaSK         string `json:"ea_sk"`
}

// routingExpected is a routing case's expected output.
type routingExpected struct {
	Rho   string `json:"rho"`
	Final string `json:"final"`
}

// routingCase is one {name, inputs, expected} case of routing.
type routingCase struct {
	Name     string          `json:"name"`
	Inputs   routingInputs   `json:"inputs"`
	Expected routingExpected `json:"expected"`
}

// vectorFile is the file's top level.
type vectorFile struct {
	Format     string        `json:"format"`
	Contract   string        `json:"contract"`
	Generator  string        `json:"generator"`
	EntryValue []entryCase   `json:"entry_value"`
	Routing    []routingCase `json:"routing"`
}

// seeded returns the 32 bytes derived from label.
func seeded(label string) []byte {
	sum := blake2b.Sum256([]byte(seedDomain + label))
	return sum[:]
}

// seededScalar returns ScalarPallas.Hash of label's seeded bytes.
func seededScalar(label string) curvey.Scalar {
	return new(curvey.ScalarPallas).Hash(seeded(label))
}

// seededRoundID returns label's seeded bytes with the top two bits cleared,
// so the little-endian value is below 2^254 < p: a canonical Pallas base field
// encoding, as every vote_round_id is.
func seededRoundID(label string) []byte {
	id := seeded(label)
	id[31] &= 0x3f
	return id
}

// routingCases builds the routing cases (contracts-v1 §5.6) under one seeded
// election key and computes each case's expected output with
// keeper.RerandomizeRoutedBucket.
func routingCases() ([]routingCase, error) {
	eaSK := &elgamal.SecretKey{Scalar: seededScalar("routing/ea_sk")}
	eaPK := &elgamal.PublicKey{Point: elgamal.PallasGenerator().Mul(eaSK.Scalar)}
	roundA := seededRoundID("routing/vote_round_id/a")
	roundB := seededRoundID("routing/vote_round_id/b")

	// enc encrypts v under eaPK with randomness r; it cannot fail for a
	// valid key and a non-zero r, which every seeded scalar is.
	enc := func(v uint64, r curvey.Scalar) *elgamal.Ciphertext {
		ct, err := elgamal.EncryptWithRandomness(eaPK, v, r)
		if err != nil {
			panic(err)
		}
		return ct
	}
	directAndPool := elgamal.HomomorphicAdd(
		enc(120, seededScalar("routing/direct_tally_and_one_pool/direct")),
		enc(40, seededScalar("routing/direct_tally_and_one_pool/pool")),
	)
	// The griefing pools of the plan's routing prototype: their randomness
	// sums to zero, so the naive sum's C1 is the identity while its plaintext
	// is 15.
	r1 := seededScalar("routing/identity_c1_griefing/r1")
	r2 := seededScalar("routing/identity_c1_griefing/r2")
	griefingPool1 := elgamal.HomomorphicAdd(enc(5, r1), enc(3, r2))
	griefingPool2 := enc(7, r1.Add(r2).Neg())
	// Two pools of plaintext 0 whose randomness cancels sum to the identity
	// ciphertext.
	r := seededScalar("routing/identity_ciphertext_griefing/r")

	buckets := []struct {
		name       string
		roundID    []byte
		proposalID uint32
		decision   uint32
		combined   *elgamal.Ciphertext
	}{
		{"direct_tally_and_one_pool", roundA, 1, 0, directAndPool},
		{"pools_without_direct_tally", roundA, 2, 1, elgamal.HomomorphicAdd(
			enc(40, seededScalar("routing/pools_without_direct_tally/pool_1")),
			enc(25, seededScalar("routing/pools_without_direct_tally/pool_2")),
		)},
		{"last_proposal_last_option", roundA, types.MaxProposals, types.MaxVoteOptions - 1, elgamal.HomomorphicAdd(
			elgamal.HomomorphicAdd(
				enc(3, seededScalar("routing/last_proposal_last_option/direct")),
				enc(1000, seededScalar("routing/last_proposal_last_option/pool_1")),
			),
			enc(64, seededScalar("routing/last_proposal_last_option/pool_2")),
		)},
		{"same_bucket_other_round", roundB, 1, 0, directAndPool},
		{"identity_c1_griefing", roundA, 1, 1, elgamal.HomomorphicAdd(griefingPool1, griefingPool2)},
		{"identity_ciphertext_griefing", roundA, 3, 2, elgamal.HomomorphicAdd(enc(0, r), enc(0, r.Neg()))},
	}

	eaSKBytes, err := elgamal.MarshalSecretKey(eaSK)
	if err != nil {
		return nil, err
	}
	var cases []routingCase
	for _, b := range buckets {
		final, rho, err := keeper.RerandomizeRoutedBucket(eaPK, b.roundID, b.proposalID, b.decision, b.combined)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.name, err)
		}
		combinedBytes, err := elgamal.MarshalCiphertext(b.combined)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.name, err)
		}
		finalBytes, err := elgamal.MarshalCiphertext(final)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.name, err)
		}
		cases = append(cases, routingCase{
			Name: b.name,
			Inputs: routingInputs{
				VoteRoundID:  hex.EncodeToString(b.roundID),
				ProposalID:   b.proposalID,
				VoteDecision: b.decision,
				Combined:     hex.EncodeToString(combinedBytes),
				EaPK:         hex.EncodeToString(eaPK.Point.ToAffineCompressed()),
				EaSK:         hex.EncodeToString(eaSKBytes),
			},
			Expected: routingExpected{Rho: hex.EncodeToString(rho.Bytes()), Final: hex.EncodeToString(finalBytes)},
		})
	}
	return cases, nil
}

// entryCaseFor builds spec's entry, encodes it as the chain does, and returns
// the case.
func entryCaseFor(spec entrySpec) (entryCase, error) {
	dk := ed25519.NewKeyFromSeed(seeded("entry_value/" + spec.name + "/dk")).Public().(ed25519.PublicKey)
	entry := &types.ProxyDelegateEntry{
		Index:            spec.index,
		Dk:               dk,
		Provider:         spec.provider,
		Handle:           spec.handle,
		ProofId:          spec.proofID,
		Status:           spec.status,
		StatusReason:     spec.statusReason,
		RegisteredHeight: spec.registeredHeight,
	}
	// Only canonical entries are stored (contracts-v1 §2.5).
	if err := types.ValidateDelegateHandle(entry.Provider, entry.Handle); err != nil {
		return entryCase{}, fmt.Errorf("%s: %w", spec.name, err)
	}
	if err := types.ValidateDelegateProofID(entry.Provider, entry.ProofId); err != nil {
		return entryCase{}, fmt.Errorf("%s: %w", spec.name, err)
	}
	value, err := proto.MarshalOptions{Deterministic: true}.Marshal(entry)
	if err != nil {
		return entryCase{}, fmt.Errorf("%s: %w", spec.name, err)
	}
	return entryCase{
		Name: spec.name,
		Inputs: entryInputs{
			Index:            entry.Index,
			DK:               hex.EncodeToString(dk),
			Provider:         entry.Provider,
			Handle:           entry.Handle,
			ProofID:          entry.ProofId,
			Status:           entry.Status,
			StatusReason:     entry.StatusReason,
			RegisteredHeight: entry.RegisteredHeight,
		},
		Expected: entryExpected{Value: hex.EncodeToString(value)},
	}, nil
}

// main writes the vector file to -out.
func main() {
	out := flag.String("out", "testutil/testdata/proxy_delegation_chain_v1.json", "vector file to write")
	flag.Parse()

	file := vectorFile{
		Format:    "proxy_delegation_chain_v1",
		Contract:  "Proxy delegation contracts, version 1, section 13",
		Generator: generatorNote,
	}
	for _, spec := range entrySpecs {
		c, err := entryCaseFor(spec)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		file.EntryValue = append(file.EntryValue, c)
	}
	routing, err := routingCases()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	file.Routing = routing

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
