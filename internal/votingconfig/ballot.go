package votingconfig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// BallotDomainTagV1 prefixes the canonical ballot encoding.
	BallotDomainTagV1 = "zcash-shielded-vote:ballot:v1"
	// DomainTagV3 prefixes the ballot attestation preimage: the v2 preimage
	// under its own tag, followed by the 32-byte ballot digest.
	DomainTagV3 = "zcash-shielded-vote:round-auth:v3"
)

// BallotOption is one selectable answer, as stored on chain.
type BallotOption struct {
	Index       uint32 `json:"index"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// BallotProposal is one question on the ballot, as stored on chain.
type BallotProposal struct {
	ID          uint32         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	ZipNumber   string         `json:"zip_number"`
	ForumURL    string         `json:"forum_url"`
	Options     []BallotOption `json:"options"`
}

// Ballot is everything a wallet displays about a round before the voter
// chooses. Proposals and options are digested in the order given; strings are
// digested as their exact UTF-8 bytes.
type Ballot struct {
	Title         string           `json:"title"`
	Description   string           `json:"description"`
	DiscussionURL string           `json:"discussion_url"`
	Proposals     []BallotProposal `json:"proposals"`
}

// BallotAttestation binds a round entry to its ballot digest. Signatures are
// over CanonicalPayloadV3. Wallets that predate it ignore the field.
type BallotAttestation struct {
	BallotDigest string      `json:"ballot_digest"`
	Signatures   []Signature `json:"signatures"`
}

// CanonicalEncoding returns the bytes hashed by Digest:
//
//	BallotDomainTagV1
//	  || str(title) || str(description) || str(discussion_url)
//	  || u64(len(proposals))
//	  || for each proposal: u32(id) || str(title) || str(description)
//	       || str(zip_number) || str(forum_url) || u64(len(options))
//	       || for each option: u32(index) || str(label) || str(description)
//
// where str(s) = u64(len(s)) || s and integers are little-endian. The
// specification and shared vectors live in the zcash_voting repository
// (docs/ballot_authentication.md).
func (b Ballot) CanonicalEncoding() []byte {
	out := []byte(BallotDomainTagV1)
	out = appendString(out, b.Title)
	out = appendString(out, b.Description)
	out = appendString(out, b.DiscussionURL)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(b.Proposals)))
	for _, proposal := range b.Proposals {
		out = binary.LittleEndian.AppendUint32(out, proposal.ID)
		out = appendString(out, proposal.Title)
		out = appendString(out, proposal.Description)
		out = appendString(out, proposal.ZipNumber)
		out = appendString(out, proposal.ForumURL)
		out = binary.LittleEndian.AppendUint64(out, uint64(len(proposal.Options)))
		for _, option := range proposal.Options {
			out = binary.LittleEndian.AppendUint32(out, option.Index)
			out = appendString(out, option.Label)
			out = appendString(out, option.Description)
		}
	}
	return out
}

// Digest is SHA-256 of CanonicalEncoding.
func (b Ballot) Digest() [32]byte {
	return sha256.Sum256(b.CanonicalEncoding())
}

func appendString(out []byte, s string) []byte {
	out = binary.LittleEndian.AppendUint64(out, uint64(len(s)))
	return append(out, s...)
}

// CanonicalPayloadV3 builds the ballot attestation preimage:
// DomainTagV3 || round_id || ea_pk || pir_layout (4 x u32 LE) || ballot_digest.
func CanonicalPayloadV3(roundID string, eaPK [32]byte, layout PIRLayout, ballotDigest [32]byte) ([]byte, error) {
	v2, err := CanonicalPayloadV2(roundID, eaPK, layout)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(DomainTagV3)+len(v2)-len(DomainTagV2)+len(ballotDigest))
	out = append(out, DomainTagV3...)
	out = append(out, v2[len(DomainTagV2):]...)
	return append(out, ballotDigest[:]...), nil
}

// SignBallotAttestation signs CanonicalPayloadV3.
func SignBallotAttestation(priv ed25519.PrivateKey, roundID string, eaPK [32]byte, layout PIRLayout, ballotDigest [32]byte) ([]byte, error) {
	payload, err := CanonicalPayloadV3(roundID, eaPK, layout, ballotDigest)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(priv, payload), nil
}

// ValidateBallotAttestation checks an entry's ballot attestation the way
// wallets do. An absent attestation is valid. A present one must carry a
// 64-character lowercase-hex digest and at least one signature over
// CanonicalPayloadV3 by a trusted key; wallets skip a round whose attestation
// fails, so publishing one would make the round unusable for them.
func ValidateBallotAttestation(roundID string, entry RoundEntry, trusted []TrustedKey, layout PIRLayout) error {
	attestation := entry.BallotAttestation
	if attestation == nil {
		return nil
	}
	if err := validateLowercaseHex32(attestation.BallotDigest); err != nil {
		return fmt.Errorf("round %s: ballot_attestation.ballot_digest %w", roundID, err)
	}
	digestBytes, err := hex.DecodeString(attestation.BallotDigest)
	if err != nil {
		return fmt.Errorf("round %s: ballot_attestation.ballot_digest: %w", roundID, err)
	}
	var digest [32]byte
	copy(digest[:], digestBytes)
	eaPKBytes, err := DecodeBase64Fixed(entry.EaPK, 32, "ea_pk")
	if err != nil {
		return fmt.Errorf("round %s: %w", roundID, err)
	}
	var eaPK [32]byte
	copy(eaPK[:], eaPKBytes)
	payload, err := CanonicalPayloadV3(roundID, eaPK, layout, digest)
	if err != nil {
		return fmt.Errorf("round %s: %w", roundID, err)
	}
	if !anyTrustedSignature(payload, attestation.Signatures, trusted) {
		return fmt.Errorf("round %s: ballot_attestation has no valid signature by a trusted key", roundID)
	}
	return nil
}

// MergeBallotAttestations combines the attestation already in a config with
// one being merged in. An entry merged without an attestation keeps the
// existing one, so re-signing a round's v2 entry never drops its ballot
// binding. Attestations over different digests are refused: a round has one
// ballot, so a mismatch means one side was computed from the wrong content.
func MergeBallotAttestations(existing, incoming *BallotAttestation) (*BallotAttestation, error) {
	switch {
	case incoming == nil:
		return existing, nil
	case existing == nil:
		return incoming, nil
	case existing.BallotDigest != incoming.BallotDigest:
		return nil, fmt.Errorf("ballot_attestation digest mismatch: existing %s, incoming %s", existing.BallotDigest, incoming.BallotDigest)
	}
	merged := &BallotAttestation{
		BallotDigest: existing.BallotDigest,
		Signatures:   append([]Signature(nil), existing.Signatures...),
	}
	for _, sig := range incoming.Signatures {
		replaced := false
		for i := range merged.Signatures {
			if merged.Signatures[i].KeyID == sig.KeyID {
				merged.Signatures[i] = sig
				replaced = true
				break
			}
		}
		if !replaced {
			merged.Signatures = append(merged.Signatures, sig)
		}
	}
	return merged, nil
}

// ServedRound is the identity and ballot of a round as the chain's REST
// endpoint returns it.
type ServedRound struct {
	RoundID string
	EaPK    [32]byte
	Ballot  Ballot
}

// ParseServedRound reads the JSON of GET /shielded-vote/v1/round/{round_id}
// (either the full response or its "round" object). That handler encodes the
// proto struct with encoding/json, so byte fields are base64 and zero values
// such as option index 0 are omitted; both decode to their Go zero values.
func ParseServedRound(raw []byte) (ServedRound, error) {
	var wrapper struct {
		Round json.RawMessage `json:"round"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return ServedRound{}, fmt.Errorf("parse round JSON: %w", err)
	}
	if len(bytes.TrimSpace(wrapper.Round)) > 0 {
		raw = wrapper.Round
	}
	var round struct {
		Ballot
		VoteRoundID string `json:"vote_round_id"`
		EaPK        string `json:"ea_pk"`
	}
	if err := json.Unmarshal(raw, &round); err != nil {
		return ServedRound{}, fmt.Errorf("parse round JSON: %w", err)
	}
	roundIDBytes, err := DecodeBase64Fixed(round.VoteRoundID, 32, "round.vote_round_id")
	if err != nil {
		return ServedRound{}, err
	}
	eaPKBytes, err := DecodeBase64Fixed(round.EaPK, 32, "round.ea_pk")
	if err != nil {
		return ServedRound{}, err
	}
	served := ServedRound{RoundID: hex.EncodeToString(roundIDBytes), Ballot: round.Ballot}
	copy(served.EaPK[:], eaPKBytes)
	return served, nil
}

func validateLowercaseHex32(s string) error {
	if len(s) != 64 {
		return errors.New("must be 64 lowercase hex characters")
	}
	for _, ch := range s {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return errors.New("must be 64 lowercase hex characters")
		}
	}
	return nil
}

// anyTrustedSignature reports whether at least one signature verifies payload
// under the trusted key it names.
func anyTrustedSignature(payload []byte, signatures []Signature, trusted []TrustedKey) bool {
	keys := map[string]TrustedKey{}
	for _, key := range trusted {
		keys[key.KeyID] = key
	}
	for _, sigRef := range signatures {
		key, ok := keys[sigRef.KeyID]
		if !ok || sigRef.Alg != key.Alg || key.Alg != AlgEd25519 {
			continue
		}
		pub, err := DecodeBase64Fixed(key.Pubkey, ed25519.PublicKeySize, "trusted_keys.pubkey")
		if err != nil {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(sigRef.Sig)
		if err != nil || len(sig) != ed25519.SignatureSize {
			continue
		}
		if ed25519.Verify(ed25519.PublicKey(pub), payload, sig) {
			return true
		}
	}
	return false
}
