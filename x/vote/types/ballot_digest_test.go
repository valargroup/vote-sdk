package types_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// The vectors are shared with the zcash_voting wallet library and the
// voting-config signer; all implementations must reproduce them byte for byte.
type ballotDigestVectors struct {
	BallotDigestV1 []struct {
		Name   string `json:"name"`
		Ballot struct {
			Title         string            `json:"title"`
			Description   string            `json:"description"`
			DiscussionURL string            `json:"discussion_url"`
			Proposals     []*types.Proposal `json:"proposals"`
		} `json:"ballot"`
		EncodingHex string `json:"encoding_hex"`
		DigestHex   string `json:"digest_hex"`
	} `json:"ballot_digest_v1"`
}

func TestBallotDigestMatchesSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/ballot_digest_v1.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vectors ballotDigestVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(vectors.BallotDigestV1) == 0 {
		t.Fatalf("no vectors")
	}
	for _, vector := range vectors.BallotDigestV1 {
		b := vector.Ballot
		encoding := types.CanonicalBallotEncoding(b.Title, b.Description, b.DiscussionURL, b.Proposals)
		if got := hex.EncodeToString(encoding); got != vector.EncodingHex {
			t.Errorf("%s: encoding = %s, want %s", vector.Name, got, vector.EncodingHex)
		}
		digest := types.BallotDigest(b.Title, b.Description, b.DiscussionURL, b.Proposals)
		if got := hex.EncodeToString(digest[:]); got != vector.DigestHex {
			t.Errorf("%s: digest = %s, want %s", vector.Name, got, vector.DigestHex)
		}
	}
}

func TestCreateVotingSessionRequiresProposalsHashToBeTheBallotDigest(t *testing.T) {
	msg := validCreateSession()
	if err := msg.ValidateBasic(); err != nil {
		t.Fatalf("message with the ballot digest rejected: %v", err)
	}

	edits := map[string]func(*types.MsgCreateVotingSession){
		"arbitrary hash":     func(m *types.MsgCreateVotingSession) { m.ProposalsHash = bytes.Repeat([]byte{0x02}, 32) },
		"round title":        func(m *types.MsgCreateVotingSession) { m.Title = "Changed" },
		"round description":  func(m *types.MsgCreateVotingSession) { m.Description = "Changed" },
		"discussion url":     func(m *types.MsgCreateVotingSession) { m.DiscussionUrl = "https://example.org" },
		"proposal title":     func(m *types.MsgCreateVotingSession) { m.Proposals[0].Title = "Changed" },
		"zip number":         func(m *types.MsgCreateVotingSession) { m.Proposals[0].ZipNumber = "ZIP-1" },
		"forum url":          func(m *types.MsgCreateVotingSession) { m.Proposals[0].ForumUrl = "https://example.org" },
		"option label":       func(m *types.MsgCreateVotingSession) { m.Proposals[0].Options[0].Label = "Changed" },
		"option description": func(m *types.MsgCreateVotingSession) { m.Proposals[0].Options[0].Description = "Changed" },
	}
	for name, edit := range edits {
		msg := validCreateSession()
		edit(msg)
		err := msg.ValidateBasic()
		if err == nil {
			t.Errorf("%s: a proposals_hash that is not the ballot digest was accepted", name)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("ballot digest")) {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

func TestBallotDigestToleratesNilProposalsAndOptions(t *testing.T) {
	// ValidateBasic rejects these, but computing the digest first must not panic.
	_ = types.BallotDigest("", "", "", []*types.Proposal{nil, {Id: 2, Options: []*types.VoteOption{nil}}})
}
