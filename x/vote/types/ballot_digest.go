package types

import (
	"crypto/sha256"
	"encoding/binary"
)

// BallotDomainTagV1 prefixes the canonical ballot encoding.
const BallotDomainTagV1 = "zcash-shielded-vote:ballot:v1"

// CanonicalBallotEncoding returns the bytes hashed by BallotDigest:
//
//	BallotDomainTagV1
//	  || str(title) || str(description) || str(discussion_url)
//	  || u64(len(proposals))
//	  || for each proposal: u32(id) || str(title) || str(description)
//	       || str(zip_number) || str(forum_url) || u64(len(options))
//	       || for each option: u32(index) || str(label) || str(description)
//
// where str(s) = u64(len(s)) || s and integers are little-endian. This is the
// encoding wallets and config signers use for the round's displayed ballot
// (zcash_voting docs/ballot_authentication.md). A nil proposal or option
// encodes as its zero value; ValidateBasic rejects both separately.
func CanonicalBallotEncoding(title, description, discussionURL string, proposals []*Proposal) []byte {
	out := []byte(BallotDomainTagV1)
	out = appendBallotString(out, title)
	out = appendBallotString(out, description)
	out = appendBallotString(out, discussionURL)
	out = binary.LittleEndian.AppendUint64(out, uint64(len(proposals)))
	for _, proposal := range proposals {
		if proposal == nil {
			proposal = &Proposal{}
		}
		out = binary.LittleEndian.AppendUint32(out, proposal.Id)
		out = appendBallotString(out, proposal.Title)
		out = appendBallotString(out, proposal.Description)
		out = appendBallotString(out, proposal.ZipNumber)
		out = appendBallotString(out, proposal.ForumUrl)
		out = binary.LittleEndian.AppendUint64(out, uint64(len(proposal.Options)))
		for _, option := range proposal.Options {
			if option == nil {
				option = &VoteOption{}
			}
			out = binary.LittleEndian.AppendUint32(out, option.Index)
			out = appendBallotString(out, option.Label)
			out = appendBallotString(out, option.Description)
		}
	}
	return out
}

// BallotDigest is SHA-256 of CanonicalBallotEncoding.
func BallotDigest(title, description, discussionURL string, proposals []*Proposal) [32]byte {
	return sha256.Sum256(CanonicalBallotEncoding(title, description, discussionURL, proposals))
}

// BallotDigest returns the digest MsgCreateVotingSession.ProposalsHash must
// equal: the ballot digest of the round's title, description, discussion link,
// and proposals.
func (msg *MsgCreateVotingSession) BallotDigest() [32]byte {
	return BallotDigest(msg.Title, msg.Description, msg.DiscussionUrl, msg.Proposals)
}

func appendBallotString(out []byte, s string) []byte {
	out = binary.LittleEndian.AppendUint64(out, uint64(len(s)))
	return append(out, s...)
}
