package app_test

import (
	"crypto/rand"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	voteapi "github.com/valargroup/vote-sdk/api"
	"github.com/valargroup/vote-sdk/crypto/ecies"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestDKGRejectsLastDealerKeyCaptureThroughBlockExecution(t *testing.T) {
	ta := testutil.SetupTestApp(t)
	firstAddr := ta.ValidatorOperAddr()
	lastAddr := testutil.TestValAddr(2)
	_, firstPK := elgamal.KeyGen(rand.Reader)
	_, lastPK := elgamal.KeyGen(rand.Reader)
	seedBondedValidatorWithPallasKey(t, ta, lastAddr, lastPK.Point.ToAffineCompressed())
	roundID := ta.SeedRegisteringCeremony([]*types.ValidatorPallasKey{
		{ValidatorAddress: firstAddr, PallasPk: firstPK.Point.ToAffineCompressed()},
		{ValidatorAddress: lastAddr, PallasPk: lastPK.Point.ToAffineCompressed()},
	})
	G := elgamal.PallasGenerator()
	makeContribution := func(creator, recipient string, recipientPK curvey.Point, recipientIndex int, secret curvey.Scalar) *types.MsgContributeDKG {
		shares, coefficients, err := shamir.Split(secret, 2, 2)
		require.NoError(t, err)
		points, err := shamir.FeldmanCommit(G, coefficients)
		require.NoError(t, err)
		commitments := [][]byte{points[0].ToAffineCompressed(), points[1].ToAffineCompressed()}
		proof, err := elgamal.GenerateConstantTermProof(secret, ta.ChainID, roundID, creator, commitments)
		require.NoError(t, err)
		envelope, err := ecies.Encrypt(G, recipientPK, shares[recipientIndex].Value.Bytes(), rand.Reader)
		require.NoError(t, err)
		return &types.MsgContributeDKG{
			Creator: creator, VoteRoundId: roundID,
			FeldmanCommitments: commitments, ConstantTermProof: proof,
			Payloads: []*types.DealerPayload{{
				ValidatorAddress: recipient,
				EphemeralPk:      envelope.Ephemeral.ToAffineCompressed(), Ciphertext: envelope.Ciphertext,
			}},
		}
	}
	deliver := func(msg *types.MsgContributeDKG) *abci.ExecTxResult {
		tx, err := voteapi.EncodeCeremonyTx(msg, voteapi.TagContributeDKG)
		require.NoError(t, err)
		// Proposal checks authenticate the proposer. Proof verification happens
		// during execution, so a bad contribution must leave ceremony state intact.
		require.Equal(t, abci.ResponseProcessProposal_ACCEPT, ta.CallProcessProposal([][]byte{tx}).Status)
		return ta.DeliverVoteTx(tx)
	}

	honestSecret := new(curvey.ScalarPallas).New(17)
	honest := makeContribution(firstAddr, lastAddr, lastPK.Point, 1, honestSecret)
	result := deliver(honest)
	require.Zero(t, result.Code, result.Log)
	before := ta.MustGetVoteRound(roundID)
	require.Len(t, before.DkgContributions, 1)
	require.Empty(t, before.EaPk)

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	validator, err := ta.StakingKeeper.GetValidator(ctx, sdk.MustValAddressFromBech32(lastAddr))
	require.NoError(t, err)
	consAddr, err := validator.GetConsAddr()
	require.NoError(t, err)
	ta.ProposerAddress = consAddr

	// The last dealer can compute [42]G - C_honest from public points alone.
	// Accepting it would make the combined election secret the known value 42.
	chosenSecret := new(curvey.ScalarPallas).New(42)
	chosenKey := G.Mul(chosenSecret)
	priorConstant, err := elgamal.DecompressPallasPoint(honest.FeldmanCommitments[0])
	require.NoError(t, err)
	rogueConstant := chosenKey.Sub(priorConstant)
	require.True(t, priorConstant.Add(rogueConstant).Equal(chosenKey))
	attack := makeContribution(lastAddr, firstAddr, firstPK.Point, 0, chosenSecret)
	attack.FeldmanCommitments[0] = rogueConstant.ToAffineCompressed()
	for _, missingProof := range []bool{true, false} {
		msg := proto.Clone(attack).(*types.MsgContributeDKG)
		if missingProof {
			msg.ConstantTermProof = nil
		}
		result = deliver(msg)
		require.NotZero(t, result.Code, "key-cancellation contribution must fail")
		require.Contains(t, result.Log, "constant-term proof")
		require.True(t, proto.Equal(before, ta.MustGetVoteRound(roundID)), "rejected contribution must not alter the round")
	}

	lastSecret := new(curvey.ScalarPallas).New(23)
	result = deliver(makeContribution(lastAddr, firstAddr, firstPK.Point, 0, lastSecret))
	require.Zero(t, result.Code, result.Log)
	after := ta.MustGetVoteRound(roundID)
	require.Len(t, after.DkgContributions, 2)
	require.Equal(t, types.CeremonyStatus_CEREMONY_STATUS_DEALT, after.CeremonyStatus)
	require.Equal(t, G.Mul(honestSecret.Add(lastSecret)).ToAffineCompressed(), after.EaPk)
	require.NotEqual(t, chosenKey.ToAffineCompressed(), after.EaPk)
}
