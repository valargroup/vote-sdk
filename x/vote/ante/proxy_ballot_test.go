package ante_test

import (
	"bytes"

	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// proxyAnteRound is the round of the batch, reveal and tree capacity ante
// tests.
var proxyAnteRound = bytes.Repeat([]byte{0xC1}, 32)

// setupProxyRound stores proxyAnteRound, changed by mutate: an ACTIVE proxy
// round created an hour before proxyAnteTime and ending a day after it, with
// two proposals of two options.
func (s *ValidateTestSuite) setupProxyRound(mutate func(*types.VoteRound)) {
	round := proxytest.Round(proxyAnteRound, 2, 2)
	round.CreatedAtTime = proxyAnteTime - 3600
	round.VoteEndTime = proxyAnteTime + 86_400
	round.ProxyDelegation.NextDelegateIndexAtCreation = 3
	mutate(round)
	s.Require().NoError(s.keeper.SetVoteRound(s.keeper.OpenKVStore(s.ctx), round))
}
