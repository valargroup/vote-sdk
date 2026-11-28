//go:build proxyenabled

package vote_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// TestPoolsRoutedBeforeAnyPartialDecryption pins plan §4.2 "Reveal close":
// routing runs in the EndBlock that makes a proxy round TALLYING, and a
// partial decryption is accepted only for a TALLYING round, so none is ever
// accepted for an unrouted bucket. In the transition block, before
// EndBlock, a partial decryption is refused because the round is still
// ACTIVE. After it, one computed before routing is refused, and so is a
// complete one with a single entry for a bucket's unrouted C1, while those
// over the routed buckets are accepted. A tally of the plaintext model then
// finalizes the round: results are decrypted once and count every counted
// pool on every proposal.
func TestPoolsRoutedBeforeAnyPartialDecryption(t *testing.T) {
	c := newRoutingChain(t)
	addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3)}
	staking := newModuleMockStakingKeeper(addrs...)
	c.keeper.SetStakingKeeper(staking)
	shares, coeffs, err := shamir.Split(routingEaSK().Scalar, 2, len(addrs))
	require.NoError(t, err)
	commitments, err := shamir.FeldmanCommit(elgamal.PallasGenerator(), coeffs)
	require.NoError(t, err)
	round := routingRound()
	round.Threshold = 2
	for i, addr := range addrs {
		round.CeremonyValidators = append(round.CeremonyValidators, &types.ValidatorPallasKey{
			ValidatorAddress: addr, PallasPk: elgamal.PallasGenerator().ToAffineCompressed(), ShamirIndex: uint32(i + 1),
		})
	}
	for _, commitment := range commitments {
		round.FeldmanCommitments = append(round.FeldmanCommitments, commitment.ToAffineCompressed())
	}
	var want map[[2]uint32]uint64
	require.NoError(t, c.block(routingEnd-10, func(ctx sdk.Context) error {
		if err := c.keeper.SetVoteRound(c.keeper.OpenKVStore(ctx), round); err != nil {
			return err
		}
		want = seedRoutingState(t, c.keeper, ctx, routingRoundID, false)
		return nil
	}))

	msgServer := keeper.NewMsgServerImpl(c.keeper)
	// asProposer returns ctx with validator v (1-based) as the block proposer.
	asProposer := func(ctx sdk.Context, v int) sdk.Context {
		consAddr, err := staking.validators[addrs[v-1]].GetConsAddr()
		require.NoError(t, err)
		return ctx.WithProposer(sdk.ConsAddress(consAddr))
	}
	// partials returns validator v's partial decryption of every non-empty
	// accumulator in ctx's state.
	partials := func(ctx sdk.Context, v int) *types.MsgSubmitPartialDecryption {
		kv := c.keeper.OpenKVStore(ctx)
		stored, err := c.keeper.GetVoteRound(kv, routingRoundID)
		require.NoError(t, err)
		accumulators, err := c.keeper.CollectNonEmptyAccumulators(kv, stored)
		require.NoError(t, err)
		msg := &types.MsgSubmitPartialDecryption{VoteRoundId: routingRoundID, Creator: addrs[v-1], ValidatorIndex: uint32(v)}
		for _, b := range slices.SortedFunc(maps.Keys(accumulators), func(a, b [2]uint32) int {
			return slices.Compare(a[:], b[:])
		}) {
			bz, err := c.keeper.GetTally(kv, routingRoundID, b[0], b[1])
			require.NoError(t, err)
			ct, err := elgamal.UnmarshalCiphertext(bz)
			require.NoError(t, err)
			proof, err := elgamal.GeneratePartialDecryptDLEQ(shares[v-1].Value, ct.C1)
			require.NoError(t, err)
			msg.Entries = append(msg.Entries, &types.PartialDecryptionEntry{
				ProposalId:     b[0],
				VoteDecision:   b[1],
				PartialDecrypt: ct.C1.Mul(shares[v-1].Value).ToAffineCompressed(),
				DleqProof:      proof,
			})
		}
		return msg
	}
	// submit delivers msg from its validator as a transaction does: in a
	// cache written only on success.
	submit := func(ctx sdk.Context, msg *types.MsgSubmitPartialDecryption) error {
		cacheCtx, write := asProposer(ctx, int(msg.ValidatorIndex)).CacheContext()
		_, err := msgServer.SubmitPartialDecryption(cacheCtx, msg)
		if err == nil {
			write()
		}
		return err
	}

	var early *types.MsgSubmitPartialDecryption
	require.NoError(t, c.block(routingEnd, func(ctx sdk.Context) error {
		early = partials(ctx, 1)
		require.ErrorIs(t, submit(ctx, early), types.ErrRoundNotTallying)
		return c.module.EndBlock(ctx)
	}))
	routed := c.round(routingRoundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, routed.Status)
	require.True(t, routed.ProxyDelegation.PoolsRouted)

	require.NoError(t, c.block(routingEnd+1, func(ctx sdk.Context) error {
		require.ErrorIs(t, submit(ctx, early), types.ErrInvalidField)
		stale := partials(ctx, 1)
		for i, entry := range stale.Entries {
			if entry.ProposalId == 1 && entry.VoteDecision == 0 {
				require.Equal(t, uint32(1), early.Entries[0].ProposalId)
				require.Equal(t, uint32(0), early.Entries[0].VoteDecision)
				stale.Entries[i] = early.Entries[0]
			}
		}
		err := submit(ctx, stale)
		require.ErrorIs(t, err, types.ErrInvalidField)
		require.ErrorContains(t, err, "DLEQ verification failed")
		require.NoError(t, submit(ctx, partials(ctx, 1)))
		require.NoError(t, submit(ctx, partials(ctx, 2)))
		return nil
	}))

	require.NoError(t, c.block(routingEnd+2, func(ctx sdk.Context) error {
		msg := &types.MsgSubmitTally{VoteRoundId: routingRoundID, Creator: addrs[0]}
		for _, b := range slices.SortedFunc(maps.Keys(want), func(a, b [2]uint32) int { return slices.Compare(a[:], b[:]) }) {
			msg.Entries = append(msg.Entries, &types.TallyEntry{ProposalId: b[0], VoteDecision: b[1], TotalValue: want[b]})
		}
		_, err := msgServer.SubmitTally(asProposer(ctx, 1), msg)
		return err
	}))
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, c.round(routingRoundID).Status)
	results, err := c.keeper.GetAllTallyResults(c.kv(), routingRoundID)
	require.NoError(t, err)
	got := map[[2]uint32]uint64{}
	for _, result := range results {
		got[[2]uint32{result.ProposalId, result.VoteDecision}] = result.TotalValue
	}
	require.Equal(t, want, got)
}
