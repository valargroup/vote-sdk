package ante_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"cosmossdk.io/log"
	"github.com/valargroup/vote-sdk/x/vote/ante"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func (s *ValidateTestSuite) TestDelegationFailureLogsInputSources() {
	for _, combined := range []bool{false, true} {
		for _, outcome := range []string{"proof failure", "success", "signature failure", "recheck"} {
			s.Run(fmt.Sprintf("combined=%t/%s", combined, outcome), func() {
				var logs bytes.Buffer
				s.setupWithLogger(log.NewLogger(&logs, log.OutputJSONOption()))
				s.setupActiveRound()
				var msg types.VoteMessage = newValidMsgDelegateVote()
				delegation := msg.(*types.MsgDelegateVote)
				if combined {
					composite := newValidMsgDelegateAndCastVoteBatch()
					msg, delegation = composite, composite.Delegation
				}
				opts := mockOpts()
				switch outcome {
				case "proof failure":
					opts = failZKPOpts()
				case "signature failure":
					opts = failSigOpts()
				case "recheck":
					opts = recheckOpts()
				}
				err := ante.ValidateVoteTx(s.ctx, msg, s.keeper, opts)
				if outcome == "proof failure" {
					s.Require().ErrorIs(err, types.ErrInvalidProof)
				} else if outcome == "signature failure" {
					s.Require().ErrorIs(err, types.ErrInvalidSignature)
				} else {
					s.Require().NoError(err)
				}

				var events []map[string]interface{}
				for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
					if len(line) == 0 {
						continue
					}
					var event map[string]interface{}
					s.Require().NoError(json.Unmarshal(line, &event))
					if event["message"] == "delegation proof verification failed" {
						events = append(events, event)
					}
					// The former unconditional input log must also be absent.
					s.Require().NotEqual("VerifyDelegation inputs", event["message"])
				}
				if outcome != "proof failure" {
					s.Require().Empty(events)
					return
				}
				s.Require().Len(events, 1)
				event := events[0]
				var nullifiers []interface{}
				for _, nf := range delegation.GovNullifiers {
					nullifiers = append(nullifiers, hex.EncodeToString(nf))
				}
				s.Require().Equal(map[string]interface{}{
					"rk":                    hex.EncodeToString(delegation.Rk),
					"signed_note_nullifier": hex.EncodeToString(delegation.SignedNoteNullifier),
					"cmx_new":               hex.EncodeToString(delegation.CmxNew),
					"van_cmx":               hex.EncodeToString(delegation.VanCmx),
					"gov_nullifiers":        nullifiers,
					"vote_round_id":         hex.EncodeToString(delegation.VoteRoundId),
				}, event["user_provided_fields"])
				round, err := s.keeper.GetVoteRound(s.keeper.OpenKVStore(s.ctx), delegation.VoteRoundId)
				s.Require().NoError(err)
				s.Require().Equal(map[string]interface{}{
					types.SessionKeyNcRoot:           hex.EncodeToString(round.NcRoot),
					types.SessionKeyNullifierImtRoot: hex.EncodeToString(round.NullifierImtRoot),
				}, event["server_provided_fields"])
				s.Require().NotEmpty(event["error"])
				s.Require().NotContains(event, "proof")
				s.Require().NotContains(event, "spend_auth_sig")
			})
		}
	}
}
