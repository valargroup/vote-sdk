//go:build halo2 && redpallas && proxyenabled

package app_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/mikelodder7/curvey"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"

	voteapi "github.com/valargroup/vote-sdk/api"
	svoteapp "github.com/valargroup/vote-sdk/app"
	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/ffi/redpallas"
	"github.com/valargroup/vote-sdk/ffi/tx1"
	"github.com/valargroup/vote-sdk/ffi/votetree"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	"github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxyprover"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// e2eFullAuthority is voting-circuits' MAX_PROPOSAL_AUTHORITY, the authority
// of a VAN that has not voted.
const e2eFullAuthority uint64 = 1<<51 - 1

// zkp1Mocked verifies ZKP2, ZKP3 and ZKP4 with the real verifier it embeds
// and accepts any ZKP1 proof, for which the test has no prover.
type zkp1Mocked struct{ zkp.Verifier }

// VerifyDelegation accepts any ZKP1 proof.
func (zkp1Mocked) VerifyDelegation([]byte, zkp.DelegationInputs) error { return nil }

// e2eVoter is a test identity: the VAN it registers, the RedPallas key that
// signs its registration's TX1 effects, and the VAN's leaf once registered
// with 0x02.
type e2eVoter struct {
	name  string
	van   proxyprover.VAN
	txKey [32]byte
	leaf  uint64
}

// e2eVote is a proved ZKP2 vote with its choice.
type e2eVote struct {
	*proxyprover.Vote
	proposal, decision uint32
}

// proxyE2E is the app under test and the scenario's shared state.
type proxyE2E struct {
	t        *testing.T
	ta       *testutil.TestApp
	managerA string
	managerB string
	keyB     *secp256k1.PrivKey
	roundID  []byte
	eaPk     [32]byte
	alpha    uint64 // the last spend-auth randomizer handed to a prover
}

// TestProxyDelegationEndToEnd runs the whole proxy delegation flow
// (docs/proxy-delegation/contracts-v1.md §5) on one in-process app with the
// gate open: real ZKP2, ZKP3 and ZKP4 proofs, RedPallas and Ed25519
// signatures and a 2-of-3 threshold tally; only ZKP1 is mocked. Every proof
// is made from the Merkle paths the app's own tree gives (testutil/
// proxyprover), and every transaction that lands goes through CheckTx,
// PrepareProposal, ProcessProposal and FinalizeBlock. With one validator in
// the app, the key ceremony's outcome and the two phantom validators' part
// of it are written to state, as the reveal close test does. At the end the
// decrypted results equal a plaintext model: the direct votes, plus each
// balloted delegate's pool on its option of every proposal, and nothing for
// a delegate without a ballot or for shares delegated to an unregistered
// index. Run it with `make test-proxy-e2e`.
func TestProxyDelegationEndToEnd(t *testing.T) {
	require.True(t, types.ProxyDelegationEnabled, "build with the proxyenabled tag")
	require.False(t, halo2.IsMock, "build with the halo2 tag")
	require.False(t, redpallas.IsMock, "build with the redpallas tag")
	started := time.Now()
	e := newProxyE2E(t)

	// Setup: params and three delegates, then the round, both behind the
	// two-approval floor.
	verifier := ed25519.NewKeyFromSeed(e2eSeed("verifier"))
	approved := e.coordinate(&types.MsgSetProxyDelegationParams{
		Creator:           e.managerA,
		Verifiers:         []*types.ProxyVerifier{{VerifierId: 1, Pubkey: verifier.Public().(ed25519.PublicKey)}},
		VerifierThreshold: 1,
		MaxDelegates:      16,
	})
	params := oneEvent(t, approved.Events, types.EventTypeProxyDelegationParams)
	require.Equal(t, "1", attrOf(t, params, types.AttributeKeyVerifierCount))
	require.Equal(t, "1", attrOf(t, params, types.AttributeKeyVerifierThreshold))
	require.Equal(t, "16", attrOf(t, params, types.AttributeKeyMaxDelegates))
	delegates := e.registerDelegates(verifier, 3)
	proposals := []*types.Proposal{
		{Id: 1, Title: "Proposal 1", Options: []*types.VoteOption{{Index: 0, Label: "Yes"}, {Index: 1, Label: "No"}}},
		{Id: 2, Title: "Proposal 2", Options: []*types.VoteOption{{Index: 0, Label: "A"}, {Index: 1, Label: "B"}, {Index: 2, Label: "C"}}},
	}
	eaSk, shares := e.createRound(proposals)

	// Delegators d1 to d3 and a plain direct voter register their VANs with
	// 0x02; delegator d4 registers inside its 0x09 batch.
	voter := func(name string, ballots uint64) *e2eVoter {
		return &e2eVoter{
			name:  name,
			van:   proxyprover.VAN{KeySeed: [32]byte(e2eSeed(name + " key")), Ballots: ballots, Rand: [32]byte(testutil.FpLE(blake2bUint64(name + " rand"))), RoundID: [32]byte(e.roundID)},
			txKey: [32]byte(testutil.FpLE(blake2bUint64(name + " tx key"))),
		}
	}
	d1, d2, d3, d4, plain := voter("d1", 10_000), voter("d2", 4_321), voter("d3", 2_500), voter("d4", 777), voter("plain", 1_234)
	regAnchor := e.register(d1, d2, d3, plain)

	// d1 splits across delegates 1 and 2 and keeps 5,000; d2 gives 3,321 to
	// delegate 3, which never ballots, and 1,000 to unregistered index 9; d3
	// gives everything to delegate 1 in the single-share layout; d4 gives
	// everything to delegate 2 from the VAN its batch registers, at anchor 0.
	d1a := e.delegate(d1.van, e.merklePath(d1.leaf, regAnchor), regAnchor, 1, 3_000, 0, false)
	d1b := e.delegate(successor(d1.van, 7_000), singleLeafPath(t, d1a.VoteAuthorityNoteNew), regAnchor, 2, 2_000, 1, false)
	d2a := e.delegate(d2.van, e.merklePath(d2.leaf, regAnchor), regAnchor, 3, 3_321, 0, false)
	d2b := e.delegate(successor(d2.van, 1_000), singleLeafPath(t, d2a.VoteAuthorityNoteNew), regAnchor, 9, 1_000, 1, false)
	d3a := e.delegate(d3.van, e.merklePath(d3.leaf, regAnchor), regAnchor, 1, 2_500, 0, true)
	d4reg := e.registration(d4)
	d4a := e.delegate(d4.van, singleLeafPath(t, [32]byte(d4reg.VanCmx)), 0, 2, 777, 0, false)
	for _, dc := range []*proxyprover.Delegation{d1a, d2a, d3a} {
		require.Equal(t, e.root(regAnchor), dc.Root[:], "a batch's first DC proves against the chain's root")
	}
	d4root, err := votetree.SingleLeafRoot(d4reg.VanCmx)
	require.NoError(t, err)
	require.Equal(t, d4root, d4a.Root[:], "a registration batch's first DC proves against van_cmx's single-leaf root")
	for prev, dc := range map[*proxyprover.Delegation]*proxyprover.Delegation{d1a: d1b, d2a: d2b} {
		root, err := votetree.SingleLeafRoot(prev.VoteAuthorityNoteNew[:])
		require.NoError(t, err)
		require.Equal(t, root, dc.Root[:], "a later DC proves against its predecessor's single-leaf root")
	}

	// A batch whose expiry has passed is refused with 52 and never lands.
	expired := e.batch(regAnchor, uint64(e.ta.Height)+1, nil, d3a)
	requireCheckCode(t, e.ta, expired, types.ErrProxyBatchExpired)
	requireDeliverCode(t, e.ta, expired, types.ErrProxyBatchExpired)

	// d3 re-signs with a fresh expiry, and all four batches land. The ante
	// checks the D1 signatures and ZKP4 proofs for real: a DC signed by
	// another DC's key, or with a changed proof, is refused.
	batches := []*types.MsgProxyDelegationBatch{
		e.batch(regAnchor, uint64(e.ta.Height)+10, nil, d1a, d1b),
		e.batch(regAnchor, uint64(e.ta.Height)+10, nil, d2a, d2b),
		e.batch(regAnchor, uint64(e.ta.Height)+10, nil, d3a),
		e.batch(0, uint64(e.ta.Height)+10, d4reg, d4a),
	}
	forged := proto.Clone(batches[0]).(*types.MsgProxyDelegationBatch)
	forged.Dcs[0].VoteAuthSig = forged.Dcs[1].VoteAuthSig
	requireCheckCode(t, e.ta, forged, types.ErrInvalidSignature)
	forged = proto.Clone(batches[0]).(*types.MsgProxyDelegationBatch)
	forged.Dcs[1].Proof[100] ^= 1
	requireCheckCode(t, e.ta, forged, types.ErrInvalidProof)
	resp := e.block(encodeAll(batches)...)
	var finalVANs []uint64
	var dcLeaves [][]uint64
	for i, result := range resp.TxResults {
		event := oneEvent(t, result.Events, types.EventTypeProxyDelegationBatch)
		digest, err := types.ComputeProxyDelegationBatchDigest(e.ta.ChainID, batches[i].SighashFields())
		require.NoError(t, err)
		require.Equal(t, hex.EncodeToString(e.roundID), attrOf(t, event, types.AttributeKeyRoundID))
		require.Equal(t, hex.EncodeToString(digest), attrOf(t, event, types.AttributeKeyBatchDigest))
		require.Equal(t, strconv.FormatBool(batches[i].Registration != nil), attrOf(t, event, types.AttributeKeyHasRegistration))
		require.Equal(t, strconv.Itoa(len(batches[i].GetRegistration().GetGovNullifiers())), attrOf(t, event, types.AttributeKeyNullifiers))
		require.Equal(t, strconv.Itoa(len(batches[i].Dcs)), attrOf(t, event, types.AttributeKeyDCCount))
		var vanNullifiers []string
		for _, dc := range batches[i].Dcs {
			vanNullifiers = append(vanNullifiers, hex.EncodeToString(dc.VanNullifier))
		}
		require.Equal(t, strings.Join(vanNullifiers, ","), attrOf(t, event, types.AttributeKeyVANNullifiers))
		finalVAN := parseUints(t, attrOf(t, event, types.AttributeKeyFinalVANLeafIndex))
		leaves := parseUints(t, attrOf(t, event, types.AttributeKeyDCLeafIndices))
		require.Len(t, leaves, len(batches[i].Dcs))
		require.Equal(t, finalVAN[0]+1, leaves[0], "the final VAN, then each DC in order")
		finalVANs = append(finalVANs, finalVAN[0])
		dcLeaves = append(dcLeaves, leaves)
	}
	batchAnchor := uint64(e.ta.Height)
	kv := e.ta.VoteKeeper().OpenKVStore(e.ta.NewUncachedContext(false, cmtproto.Header{Height: e.ta.Height}))
	tree, err := e.ta.VoteKeeper().GetCommitmentTreeState(kv, e.roundID)
	require.NoError(t, err)
	require.Equal(t, dcLeaves[3][0]+1, tree.NextIndex, "the registration's van_cmx is never appended")

	// A 0x09 replayed after it landed is answered 2 in CheckTx and in a block.
	requireCheckCode(t, e.ta, batches[0], types.ErrDuplicateNullifier)
	requireDeliverCode(t, e.ta, batches[0], types.ErrDuplicateNullifier)
	requireCheckCode(t, e.ta, batches[3], types.ErrDuplicateNullifier)

	// d1 votes its kept 5,000 on both proposals in one 0x06 batch, the second
	// vote chained through the first's new VAN; the plain voter votes once.
	kept := successor(d1.van, 5_000)
	keptA := e.vote(kept, e2eFullAuthority, e.merklePath(finalVANs[0], batchAnchor), batchAnchor, 1, 1)
	keptB := e.vote(kept, e2eFullAuthority&^(1<<1), singleLeafPath(t, keptA.VoteAuthorityNoteNew), batchAnchor, 2, 2)
	plainVote := e.vote(plain.van, e2eFullAuthority, e.merklePath(plain.leaf, batchAnchor), batchAnchor, 1, 0)
	require.Equal(t, e.root(batchAnchor), keptA.Root[:])
	require.Equal(t, e.root(batchAnchor), plainVote.Root[:])
	casts := []*types.MsgCastVoteBatch{e.castBatch(batchAnchor, keptA, keptB), e.castBatch(batchAnchor, plainVote)}
	forgedCast := proto.Clone(casts[0]).(*types.MsgCastVoteBatch)
	forgedCast.Votes[1].Proof[100] ^= 1
	requireCheckCode(t, e.ta, forgedCast, types.ErrInvalidProof)
	resp = e.block(encodeAll(casts)...)
	vcLeaves := map[*e2eVote]uint64{}
	for i, votes := range [][]*e2eVote{{keptA, keptB}, {plainVote}} {
		leaves := parseUints(t, attrOf(t, oneEvent(t, resp.TxResults[i].Events, types.EventTypeCastVoteBatch), types.AttributeKeyVoteCommitmentLeafIndices))
		require.Len(t, leaves, len(votes))
		for j, vote := range votes {
			vcLeaves[vote] = leaves[j]
		}
	}
	revealAnchor := uint64(e.ta.Height)

	// Every share of every DC is revealed with a real ZKP3 proof. Those of
	// the DC to index 9 are refused with 53 before their proofs are checked;
	// the others go into their pools. ZKP3 binds the delegate index, so
	// claiming another registered delegate fails it.
	var poolReveals []*types.MsgRevealShare
	for _, dc := range []struct {
		proof *proxyprover.Delegation
		leaf  uint64
		d     uint32
	}{{d1a, dcLeaves[0][0], 1}, {d1b, dcLeaves[0][1], 2}, {d2a, dcLeaves[1][0], 3}, {d3a, dcLeaves[2][0], 1}, {d4a, dcLeaves[3][0], 2}} {
		poolReveals = append(poolReveals, e.reveals(dc.proof.Commitment, dc.leaf, revealAnchor, types.PoolProposalID, dc.d)...)
	}
	claimed := proto.Clone(poolReveals[0]).(*types.MsgRevealShare)
	claimed.VoteDecision = 2
	requireCheckCode(t, e.ta, claimed, types.ErrInvalidProof)
	unregistered := e.reveals(d2b.Commitment, dcLeaves[1][1], revealAnchor, types.PoolProposalID, 9)
	for _, reveal := range unregistered {
		requireCheckCode(t, e.ta, reveal, types.ErrDelegateNotFound)
	}
	for _, result := range e.ta.DeliverVoteTxs(encodeAll(unregistered)) {
		require.Equal(t, types.ErrDelegateNotFound.ABCICode(), result.Code, result.Log)
		require.Equal(t, "vote", result.Codespace)
	}
	resp = e.block(encodeAll(poolReveals)...)
	poolEvents := map[string]int{}
	for _, result := range resp.TxResults {
		event := oneEvent(t, result.Events, types.EventTypeRevealPoolShare)
		require.Empty(t, eventsOf(result.Events, types.EventTypeRevealShare))
		poolEvents[attrOf(t, event, types.AttributeKeyDelegateIndex)]++
	}
	require.Equal(t, map[string]int{"1": 32, "2": 32, "3": 16}, poolEvents)

	// Every share of every vote commitment is revealed as a normal share.
	var voteReveals []*types.MsgRevealShare
	for _, vote := range []*e2eVote{keptA, keptB, plainVote} {
		voteReveals = append(voteReveals, e.reveals(vote.Commitment, vcLeaves[vote], revealAnchor, vote.proposal, vote.decision)...)
	}
	resp = e.block(encodeAll(voteReveals)...)
	for _, result := range resp.TxResults {
		oneEvent(t, result.Events, types.EventTypeRevealShare)
	}

	// Delegates 1 and 2 submit complete ballots; delegate 3 submits none. A
	// second ballot is refused with 57.
	ballots := map[uint32][]uint32{1: {0, 2}, 2: {0, 0}}
	resp = e.block(e.ballot(delegates[0], 1, ballots[1]), e.ballot(delegates[1], 2, ballots[2]))
	for i, result := range resp.TxResults {
		event := oneEvent(t, result.Events, types.EventTypeDelegateBallot)
		require.Equal(t, strconv.Itoa(i+1), attrOf(t, event, types.AttributeKeyDelegateIndex))
		require.Equal(t, joinUints(ballots[uint32(i+1)]), attrOf(t, event, types.AttributeKeyOptions))
	}
	second := e.ballot(delegates[0], 1, []uint32{1, 1})
	requireCheckCode(t, e.ta, second, types.ErrDelegateBallotExists)
	requireDeliverCode(t, e.ta, second, types.ErrDelegateBallotExists)

	// The plaintext model, from the shares' plaintexts: the direct votes,
	// plus each balloted pool on its options. Pool 3 has no ballot.
	direct := map[[2]uint32]uint64{}
	for _, vote := range []*e2eVote{keptA, keptB, plainVote} {
		direct[[2]uint32{vote.proposal, vote.decision}] += shareSum(vote.Commitment)
	}
	require.Equal(t, map[[2]uint32]uint64{{1, 1}: 5_000, {2, 2}: 5_000, {1, 0}: 1_234}, direct, "the direct votes by hand")
	pools := map[uint32]uint64{
		1: shareSum(d1a.Commitment) + shareSum(d3a.Commitment),
		2: shareSum(d1b.Commitment) + shareSum(d4a.Commitment),
		3: shareSum(d2a.Commitment),
	}
	require.Equal(t, map[uint32]uint64{1: 5_500, 2: 2_777, 3: 3_321}, pools, "the pools by hand")
	want := maps.Clone(direct)
	for d, options := range ballots {
		for i, option := range options {
			want[[2]uint32{uint32(i + 1), option}] += pools[d]
		}
	}
	require.Equal(t, map[[2]uint32]uint64{
		{1, 0}: 1_234 + 5_500 + 2_777, {1, 1}: 5_000,
		{2, 0}: 2_777, {2, 2}: 5_000 + 5_500,
	}, want, "the model by hand")

	// Before the close, the proposals' buckets hold the direct votes only,
	// and the pools hold their delegations: pool 3 too, but nothing for
	// index 9.
	round := e.ta.MustGetVoteRound(e.roundID)
	e.requireDecrypts(eaSk, optionBuckets(round), direct)
	e.requireDecrypts(eaSk, [][2]uint32{{0, 1}, {0, 2}, {0, 3}, {0, 9}}, map[[2]uint32]uint64{{0, 1}: pools[1], {0, 2}: pools[2], {0, 3}: pools[3]})

	// Close: the block at vote_end_time routes the pools before the round
	// turns TALLYING.
	resp = e.finalizeAt(time.Unix(int64(round.VoteEndTime), 0).UTC())
	var routed []abci.Event
	for _, event := range resp.Events {
		if event.Type == types.EventTypeProxyPoolsRouted || event.Type == types.EventTypeRoundStatusChange {
			routed = append(routed, event)
		}
	}
	require.Len(t, routed, 2)
	require.Equal(t, types.EventTypeProxyPoolsRouted, routed[0].Type, "pools are routed before the status change")
	require.Equal(t, hex.EncodeToString(e.roundID), attrOf(t, routed[0], types.AttributeKeyRoundID))
	require.Equal(t, "2", attrOf(t, routed[0], types.AttributeKeyBallots))
	require.Equal(t, "2", attrOf(t, routed[0], types.AttributeKeyRoutedPools))
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING.String(), attrOf(t, routed[1], types.AttributeKeyNewStatus))
	round = e.ta.MustGetVoteRound(e.roundID)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_TALLYING, round.Status)
	require.True(t, round.ProxyDelegation.PoolsRouted)
	e.requireDecrypts(eaSk, optionBuckets(round), want)

	// Tally: the proposer's partial decryption and then MsgSubmitTally are
	// injected by its PrepareProposal, after the phantom validator 2's.
	e.seedPartials(round, shares[1])
	shareBytes, err := elgamal.MarshalSecretKey(&elgamal.SecretKey{Scalar: shares[0].Value})
	require.NoError(t, err)
	e.ta.WriteShareForRound(e.roundID, shareBytes)
	partial := e.proposerBlock(voteapi.TagSubmitPartialDecryption)
	oneEvent(t, partial.TxResults[0].Events, types.EventTypeSubmitPartialDecryption)
	tally := e.proposerBlock(voteapi.TagSubmitTally)
	oneEvent(t, tally.TxResults[0].Events, types.EventTypeSubmitTally)
	require.Equal(t, types.SessionStatus_SESSION_STATUS_FINALIZED, e.ta.MustGetVoteRound(e.roundID).Status)

	// The results equal the model, and no query exposes proposal 0.
	ctx := e.ta.NewUncachedContext(false, cmtproto.Header{Height: e.ta.Height})
	qs := keeper.NewQueryServerImpl(e.ta.VoteKeeper())
	results, err := qs.TallyResults(ctx, &types.QueryTallyResultsRequest{VoteRoundId: e.roundID})
	require.NoError(t, err)
	got := map[[2]uint32]uint64{}
	for _, result := range results.Results {
		got[[2]uint32{result.ProposalId, result.VoteDecision}] = result.TotalValue
	}
	require.Equal(t, want, got)
	for _, id := range []uint32{types.PoolProposalID, uint32(len(proposals) + 1)} {
		_, err := qs.ProposalTally(ctx, &types.QueryProposalTallyRequest{VoteRoundId: e.roundID, ProposalId: id})
		require.Equal(t, codes.InvalidArgument, status.Code(err), "proposal %d", id)
	}
	summary, err := qs.VoteSummary(ctx, &types.QueryVoteSummaryRequest{VoteRoundId: e.roundID})
	require.NoError(t, err)
	require.Len(t, summary.Proposals, len(proposals))
	for i, proposal := range summary.Proposals {
		require.Equal(t, uint32(i+1), proposal.Id)
		for _, option := range proposal.Options {
			require.Equal(t, want[[2]uint32{proposal.Id, option.Index}], option.TotalValue)
		}
	}
	partials, err := qs.PartialDecryptions(ctx, &types.QueryPartialDecryptionsRequest{VoteRoundId: e.roundID})
	require.NoError(t, err)
	require.Len(t, partials.Entries, 2*len(want), "both validators decrypt every bucket")
	for _, partial := range partials.Entries {
		require.NotEqual(t, types.PoolProposalID, partial.ProposalId)
	}
	stored, err := qs.DelegateBallots(ctx, &types.QueryDelegateBallotsRequest{VoteRoundId: e.roundID})
	require.NoError(t, err)
	require.Len(t, stored.Ballots, 2)
	registry, err := qs.ProxyDelegationParams(ctx, &types.QueryProxyDelegationParamsRequest{})
	require.NoError(t, err)
	require.Len(t, registry.Params.Verifiers, 1)
	require.Equal(t, uint32(4), registry.NextDelegateIndex)
	capabilities, err := qs.ProtocolCapabilities(ctx, &types.QueryProtocolCapabilitiesRequest{})
	require.NoError(t, err)
	require.Equal(t, types.ProxyDelegationVersion, capabilities.ProxyDelegationVersion)
	t.Logf("proxy delegation end-to-end flow: %d blocks in %s", e.ta.Height, time.Since(started).Round(time.Millisecond))
}

// newProxyE2E returns an app whose ante handler mocks only ZKP1, with the
// genesis validator's Pallas key registered and two vote managers: the
// genesis validator's account and a second, unfunded account.
func newProxyE2E(t *testing.T) *proxyE2E {
	t.Cleanup(svoteapp.SetAnteZKPVerifier(zkp1Mocked{halo2.NewVerifier()}))
	ta, _, pallasPk, _, _ := testutil.SetupTestAppWithPallasKey(t)
	ta.RegisterPallasKey(pallasPk)

	keyB := secp256k1.GenPrivKeyFromSecret([]byte("vote-sdk proxy e2e/manager B"))
	addrB := sdk.AccAddress(keyB.PubKey().Address())
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	ta.AccountKeeper.SetAccount(ctx, ta.AccountKeeper.NewAccountWithAddress(ctx, addrB))
	ta.SeedVoteManagers(ta.ValidatorAccAddr(), addrB.String())
	return &proxyE2E{t: t, ta: ta, managerA: ta.ValidatorAccAddr(), managerB: addrB.String(), keyB: keyB}
}

// block runs txs as a node does: CheckTx admits each, PrepareProposal
// proposes exactly them, ProcessProposal accepts the proposal, and every tx
// succeeds in FinalizeBlock before Commit. It returns the block's response.
func (e *proxyE2E) block(txs ...[]byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	for i, tx := range txs {
		check := e.ta.CheckTxSync(tx)
		require.Zero(e.t, check.Code, "CheckTx of tx %d: %s", i, check.Log)
	}
	prepared := e.ta.CallPrepareProposalWithTxs(txs).Txs
	require.Equal(e.t, txs, prepared, "PrepareProposal proposes exactly the txs")
	resp := e.finalize(prepared)
	for i, result := range resp.TxResults {
		require.Zero(e.t, result.Code, "tx %d: %s", i, result.Log)
	}
	return resp
}

// proposerBlock runs a block with no submitted txs, whose PrepareProposal
// must inject exactly one tx with tag, and returns the block's response.
func (e *proxyE2E) proposerBlock(tag byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	prepared := e.ta.CallPrepareProposal().Txs
	require.Len(e.t, prepared, 1)
	require.Equal(e.t, tag, prepared[0][0])
	resp := e.finalize(prepared)
	require.Zero(e.t, resp.TxResults[0].Code, resp.TxResults[0].Log)
	return resp
}

// finalize checks that ProcessProposal accepts txs, then runs them as the
// next block, five seconds on, and commits it.
func (e *proxyE2E) finalize(txs [][]byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	require.Equal(e.t, abci.ResponseProcessProposal_ACCEPT, e.ta.CallProcessProposal(txs).Status)
	return e.finalizeAt(e.ta.Time.Add(5*time.Second), txs...)
}

// finalizeAt runs txs as the next block at time at and commits it.
func (e *proxyE2E) finalizeAt(at time.Time, txs ...[]byte) *abci.ResponseFinalizeBlock {
	e.t.Helper()
	e.ta.Height++
	e.ta.Time = at
	resp, err := e.ta.FinalizeBlock(&abci.RequestFinalizeBlock{Height: e.ta.Height, Time: at, Txs: txs, ProposerAddress: e.ta.ProposerAddress})
	require.NoError(e.t, err)
	_, err = e.ta.Commit()
	require.NoError(e.t, err)
	return resp
}

// coordinate proposes payload as manager A, checks that the approval floor
// holds it, and approves it as manager B, which executes it. It returns the
// approval's result.
func (e *proxyE2E) coordinate(payload proto.Message) *abci.ExecTxResult {
	e.t.Helper()
	proposed := e.block(e.ta.MustBuildSignedCoordinatorActionTx(e.managerA, payload)).TxResults[0]
	require.Empty(e.t, eventsOf(proposed.Events, types.EventTypeExecuteCoordinatorAction), "one approval is below the floor")
	actionID, err := strconv.ParseUint(attrOf(e.t, oneEvent(e.t, proposed.Events, types.EventTypeProposeCoordinatorAction), types.AttributeKeyActionID), 10, 64)
	require.NoError(e.t, err)
	approved := e.block(e.ta.MustBuildSignedTx(e.keyB, &types.MsgApproveCoordinatorAction{Creator: e.managerB, ActionId: actionID})).TxResults[0]
	require.Equal(e.t, "2", attrOf(e.t, oneEvent(e.t, approved.Events, types.EventTypeExecuteCoordinatorAction), types.AttributeKeyApprovalCount))
	return approved
}

// registerDelegates registers n delegates in one block of 0x0B ops, each
// signed over REGISTER by its own key and attested over ATTEST by verifier,
// and returns their keys. They get indices 1 to n in that order.
func (e *proxyE2E) registerDelegates(verifier ed25519.PrivateKey, n int) []ed25519.PrivateKey {
	e.t.Helper()
	notBefore := uint64(e.ta.Time.Unix()) - 60
	expiresAt := notBefore + 86_400
	var keys []ed25519.PrivateKey
	var txs [][]byte
	for i := range n {
		dk := ed25519.NewKeyFromSeed(e2eSeed(fmt.Sprintf("delegate %d", i+1)))
		fields := types.DelegateRegistrationFields{
			DK:       dk.Public().(ed25519.PublicKey),
			Provider: types.DelegateProviderX,
			Handle:   fmt.Sprintf("delegate_%d", i+1),
			ProofID:  strconv.Itoa(1_790_000_000 + i),
		}
		register, err := types.ComputeDelegateRegisterDigest(e.ta.ChainID, fields)
		require.NoError(e.t, err)
		attest, err := types.ComputeDelegateAttestDigest(e.ta.ChainID, fields, notBefore, expiresAt)
		require.NoError(e.t, err)
		txs = append(txs, testutil.MustEncodeVoteTx(&types.MsgDelegateOp{Register: &types.DelegateRegister{
			Dk:           fields.DK,
			Provider:     fields.Provider,
			Handle:       fields.Handle,
			ProofId:      fields.ProofID,
			Attestations: []*types.DelegateAttestation{{VerifierId: 1, NotBefore: notBefore, ExpiresAt: expiresAt, Sig: ed25519.Sign(verifier, attest)}},
			DkSig:        ed25519.Sign(dk, register),
		}}))
		keys = append(keys, dk)
	}
	for i, result := range e.block(txs...).TxResults {
		event := oneEvent(e.t, result.Events, types.EventTypeDelegateRegistered)
		require.Equal(e.t, strconv.Itoa(i+1), attrOf(e.t, event, types.AttributeKeyDelegateIndex))
		require.Equal(e.t, fmt.Sprintf("delegate_%d", i+1), attrOf(e.t, event, types.AttributeKeyHandle))
	}
	return keys
}

// createRound creates the proxy round with proposals through the
// coordinator flow, then stands in for its key ceremony as the reveal close
// test does: it installs a 2-of-3 Shamir split of a test election key over
// the genesis validator, which proposes every block, and two phantom
// validators. It returns the key and the shares, the proposer's first.
func (e *proxyE2E) createRound(proposals []*types.Proposal) (*elgamal.SecretKey, []shamir.Share) {
	e.t.Helper()
	approved := e.coordinate(&types.MsgCreateVotingSession{
		Creator:           e.managerA,
		SnapshotHeight:    100,
		SnapshotBlockhash: bytes.Repeat([]byte{0xAA}, 32),
		ProposalsHash:     bytes.Repeat([]byte{0xBB}, 32),
		VoteEndTime:       uint64(e.ta.Time.Add(time.Hour).Unix()),
		NullifierImtRoot:  bytes.Repeat([]byte{0x01}, 32),
		NcRoot:            bytes.Repeat([]byte{0x02}, 32),
		Proposals:         proposals,
		Title:             "Proxy delegation end to end",
		ProxyDelegation:   true,
	})
	roundID, err := hex.DecodeString(attrOf(e.t, oneEvent(e.t, approved.Events, types.EventTypeCreateVotingSession), types.AttributeKeyRoundID))
	require.NoError(e.t, err)
	e.roundID = roundID
	round := e.ta.MustGetVoteRound(roundID)
	require.Equal(e.t, types.SessionStatus_SESSION_STATUS_PENDING, round.Status)
	require.True(e.t, proto.Equal(&types.ProxyDelegationRoundState{Enabled: true, NextDelegateIndexAtCreation: 4}, round.ProxyDelegation), "field 31: %v", round.ProxyDelegation)
	require.Len(e.t, round.CeremonyValidators, 1)
	require.Equal(e.t, e.ta.ValidatorOperAddr(), round.CeremonyValidators[0].ValidatorAddress)
	require.Equal(e.t, uint32(1), round.CeremonyValidators[0].ShamirIndex)

	eaSk := &elgamal.SecretKey{Scalar: new(curvey.ScalarPallas).Hash([]byte("vote-sdk proxy e2e/ea_sk"))}
	shares, coeffs, err := shamir.Split(eaSk.Scalar, 2, 3)
	require.NoError(e.t, err)
	commitments, err := shamir.FeldmanCommit(elgamal.PallasGenerator(), coeffs)
	require.NoError(e.t, err)
	for i := uint32(2); i <= 3; i++ {
		_, pk := elgamal.KeyGen(rand.Reader)
		round.CeremonyValidators = append(round.CeremonyValidators, &types.ValidatorPallasKey{
			ValidatorAddress: testutil.TestValAddr(byte(0xF0 + i)), PallasPk: pk.Point.ToAffineCompressed(), ShamirIndex: i,
		})
	}
	for _, commitment := range commitments {
		round.FeldmanCommitments = append(round.FeldmanCommitments, commitment.ToAffineCompressed())
	}
	round.EaPk = elgamal.PallasGenerator().Mul(eaSk.Scalar).ToAffineCompressed()
	round.Threshold = 2
	round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED
	round.Status = types.SessionStatus_SESSION_STATUS_ACTIVE
	ctx := e.ta.NewUncachedContext(false, cmtproto.Header{Height: e.ta.Height})
	require.NoError(e.t, e.ta.VoteKeeper().SetVoteRound(e.ta.VoteKeeper().OpenKVStore(ctx), round))
	e.ta.NextBlock()
	e.eaPk = [32]byte(round.EaPk)
	return eaSk, shares
}

// registration returns voter's ZKP1 registration: the proof is a mock, but
// van_cmx is the voter's VAN commitment and the TX1 signature real.
func (e *proxyE2E) registration(voter *e2eVoter) *types.MsgDelegateVote {
	e.t.Helper()
	van, err := voter.van.Commitment()
	require.NoError(e.t, err)
	rk, _, err := proxyprover.Sign(voter.txKey, nil)
	require.NoError(e.t, err)
	nf, cmx := testutil.FpLE(blake2bUint64(voter.name+" nf")), testutil.FpLE(blake2bUint64(voter.name+" cmx"))
	effects := testutil.DelegationTX1Effects(rk[:], nf, cmx)
	sighash, err := tx1.ComputeDelegationSighash(effects)
	require.NoError(e.t, err)
	_, sig, err := proxyprover.Sign(voter.txKey, sighash)
	require.NoError(e.t, err)
	return &types.MsgDelegateVote{
		Rk:                  rk[:],
		SpendAuthSig:        sig[:],
		SignedNoteNullifier: nf,
		CmxNew:              cmx,
		VanCmx:              van[:],
		GovNullifiers:       [][]byte{e2eSeed(voter.name + " gov a"), e2eSeed(voter.name + " gov b")},
		Proof:               []byte("mock-zkp1-proof"),
		VoteRoundId:         e.roundID,
		Tx1Effects:          effects,
	}
}

// register registers voters' VANs with 0x02 in one block, records each
// VAN's leaf and returns the block's height, whose root covers them.
func (e *proxyE2E) register(voters ...*e2eVoter) uint64 {
	e.t.Helper()
	var txs [][]byte
	for _, voter := range voters {
		txs = append(txs, testutil.MustEncodeVoteTx(e.registration(voter)))
	}
	for i, result := range e.block(txs...).TxResults {
		voters[i].leaf = parseUints(e.t, attrOf(e.t, oneEvent(e.t, result.Events, types.EventTypeDelegateVote), types.AttributeKeyLeafIndex))[0]
	}
	return uint64(e.ta.Height)
}

// delegate proves a DC of ballots from van, the leaf that path authenticates
// under the root at anchor, to delegate d in DC slot slot.
func (e *proxyE2E) delegate(van proxyprover.VAN, path []byte, anchor uint64, d uint32, ballots uint64, slot uint8, singleShare bool) *proxyprover.Delegation {
	e.t.Helper()
	e.alpha++
	dc, err := proxyprover.ProveDelegation(proxyprover.DelegationWitness{
		VAN:           van,
		MerklePath:    path,
		AnchorHeight:  anchor,
		EaPk:          e.eaPk,
		Alpha:         e.alpha,
		DelegateIndex: d,
		Ballots:       ballots,
		Slot:          slot,
		Seed:          [32]byte(e2eSeed(fmt.Sprintf("dc_seed %d", e.alpha))),
		SingleShare:   singleShare,
	})
	require.NoError(e.t, err)
	return dc
}

// batch returns the 0x09 batch of dcs at anchor, carrying registration if
// it is set, rejected at expiry, with every DC's rsk signing its D1.
func (e *proxyE2E) batch(anchor, expiry uint64, registration *types.MsgDelegateVote, dcs ...*proxyprover.Delegation) *types.MsgProxyDelegationBatch {
	e.t.Helper()
	msg := &types.MsgProxyDelegationBatch{VoteRoundId: e.roundID, VoteCommTreeAnchorHeight: anchor, Registration: registration, ExpiryHeight: expiry}
	for _, dc := range dcs {
		msg.Dcs = append(msg.Dcs, &types.ProxyDelegationAction{
			VanNullifier:              bytes.Clone(dc.VanNullifier[:]),
			RVpk:                      bytes.Clone(dc.RVpk[:]),
			VoteAuthorityNoteNew:      bytes.Clone(dc.VoteAuthorityNoteNew[:]),
			ProxyDelegationCommitment: bytes.Clone(dc.DC[:]),
			DcSlotNullifier:           bytes.Clone(dc.DcSlotNullifier[:]),
			Proof:                     dc.Proof,
		})
	}
	d1, err := types.ComputeProxyDelegationBatchSighash(e.ta.ChainID, msg.SighashFields())
	require.NoError(e.t, err)
	for i, dc := range dcs {
		_, sig, err := proxyprover.Sign(dc.Rsk, d1)
		require.NoError(e.t, err)
		msg.Dcs[i].VoteAuthSig = sig[:]
	}
	return msg
}

// vote proves a vote for decision on proposal by van, holding authority,
// the leaf that path authenticates under the root at anchor.
func (e *proxyE2E) vote(van proxyprover.VAN, authority uint64, path []byte, anchor uint64, proposal, decision uint32) *e2eVote {
	e.t.Helper()
	e.alpha++
	vote, err := proxyprover.ProveVote(proxyprover.VoteWitness{
		VAN:          van,
		Authority:    authority,
		MerklePath:   path,
		AnchorHeight: anchor,
		EaPk:         e.eaPk,
		Alpha:        e.alpha,
		ProposalID:   proposal,
		Decision:     decision,
	})
	require.NoError(e.t, err)
	return &e2eVote{Vote: vote, proposal: proposal, decision: decision}
}

// castBatch returns the 0x06 batch of votes at anchor, each signed by its
// rsk over the batch sighash.
func (e *proxyE2E) castBatch(anchor uint64, votes ...*e2eVote) *types.MsgCastVoteBatch {
	e.t.Helper()
	msg := &types.MsgCastVoteBatch{}
	for _, vote := range votes {
		msg.Votes = append(msg.Votes, &types.MsgCastVote{
			VanNullifier:             bytes.Clone(vote.VanNullifier[:]),
			VoteAuthorityNoteNew:     bytes.Clone(vote.VoteAuthorityNoteNew[:]),
			VoteCommitment:           bytes.Clone(vote.VoteCommitment[:]),
			ProposalId:               vote.proposal,
			Proof:                    vote.Proof,
			VoteRoundId:              e.roundID,
			VoteCommTreeAnchorHeight: anchor,
			RVpk:                     bytes.Clone(vote.RVpk[:]),
		})
	}
	digest := types.ComputeCastVoteBatchSighash(msg)
	for i, vote := range votes {
		_, sig, err := proxyprover.Sign(vote.Rsk, digest)
		require.NoError(e.t, err)
		msg.Votes[i].VoteAuthSig = sig[:]
	}
	return msg
}

// reveals returns 0x04 reveals of every share of commitment c, the leaf at
// position, proved with real ZKP3 against the root at anchor. A pool share
// carries proposal 0 and the delegate index as its decision.
func (e *proxyE2E) reveals(c proxyprover.Commitment, position, anchor uint64, proposal, decision uint32) []*types.MsgRevealShare {
	e.t.Helper()
	path := e.merklePath(position, anchor)
	root := e.root(anchor)
	var msgs []*types.MsgRevealShare
	for i, share := range c.Shares {
		proof, nullifier, treeRoot, err := halo2.GenerateShareRevealProof(path, c.ShareComms, share.Blind, share.C1, share.C2, uint32(i), proposal, decision, [32]byte(e.roundID))
		require.NoError(e.t, err)
		require.Equal(e.t, root, treeRoot[:])
		msgs = append(msgs, &types.MsgRevealShare{
			ShareNullifier:           nullifier[:],
			EncShare:                 share.EncShare(),
			ProposalId:               proposal,
			VoteDecision:             decision,
			Proof:                    proof,
			VoteRoundId:              e.roundID,
			VoteCommTreeAnchorHeight: anchor,
		})
	}
	return msgs
}

// ballot returns delegate d's 0x0B ballot of options, signed over BALLOT by
// dk.
func (e *proxyE2E) ballot(dk ed25519.PrivateKey, d uint32, options []uint32) []byte {
	e.t.Helper()
	digest, err := types.ComputeDelegateBallotDigest(e.ta.ChainID, e.roundID, d, options)
	require.NoError(e.t, err)
	return testutil.MustEncodeVoteTx(&types.MsgDelegateOp{Ballot: &types.DelegateBallot{
		VoteRoundId: e.roundID, DelegateIndex: d, Options: options, DkSig: ed25519.Sign(dk, digest),
	}})
}

// merklePath reads the Merkle path of the leaf at position under the root
// at height from the app's committed tree, as the helper does.
func (e *proxyE2E) merklePath(position, height uint64) []byte {
	e.t.Helper()
	kv := e.ta.VoteKeeper().OpenKVStore(e.ta.NewUncachedContext(false, cmtproto.Header{Height: e.ta.Height}))
	state, err := e.ta.VoteKeeper().GetCommitmentTreeState(kv, e.roundID)
	require.NoError(e.t, err)
	handle, err := votetree.NewTreeHandleWithKV(&votetree.KvStoreProxy{Current: kv, Prefix: types.RoundTreeKey(e.roundID)}, state.NextIndex)
	require.NoError(e.t, err)
	defer handle.Close()
	path, err := handle.Path(position, uint32(height))
	require.NoError(e.t, err)
	return path
}

// root returns the round's committed tree root at height.
func (e *proxyE2E) root(height uint64) []byte {
	e.t.Helper()
	kv := e.ta.VoteKeeper().OpenKVStore(e.ta.NewUncachedContext(false, cmtproto.Header{Height: e.ta.Height}))
	root, err := e.ta.VoteKeeper().GetCommitmentRootAtHeight(kv, e.roundID, height)
	require.NoError(e.t, err)
	require.NotNil(e.t, root, "no root at height %d", height)
	return root
}

// requireDecrypts decrypts each (proposal, option) bucket of the round with
// the whole election key and checks it against want, where a bucket missing
// from want must be absent.
func (e *proxyE2E) requireDecrypts(eaSk *elgamal.SecretKey, buckets [][2]uint32, want map[[2]uint32]uint64) {
	e.t.Helper()
	kv := e.ta.VoteKeeper().OpenKVStore(e.ta.NewUncachedContext(false, cmtproto.Header{Height: e.ta.Height}))
	for _, bucket := range buckets {
		bz, err := e.ta.VoteKeeper().GetTally(kv, e.roundID, bucket[0], bucket[1])
		require.NoError(e.t, err)
		value, ok := want[bucket]
		if !ok {
			require.Nil(e.t, bz, "bucket %v", bucket)
			continue
		}
		ct, err := elgamal.UnmarshalCiphertext(bz)
		require.NoError(e.t, err)
		require.True(e.t, elgamal.DecryptToPoint(eaSk, ct).Equal(elgamal.ValuePoint(value)), "bucket %v decrypts to %d", bucket, value)
	}
}

// optionBuckets returns every (proposal, option) bucket of round.
func optionBuckets(round *types.VoteRound) [][2]uint32 {
	var buckets [][2]uint32
	for _, proposal := range round.Proposals {
		for _, option := range proposal.Options {
			buckets = append(buckets, [2]uint32{proposal.Id, option.Index})
		}
	}
	return buckets
}

// seedPartials stores the phantom validator 2's partial decryption of every
// non-empty accumulator of round with its share, with DLEQ proofs, as its
// own MsgSubmitPartialDecryption would; it cannot propose a block here.
func (e *proxyE2E) seedPartials(round *types.VoteRound, share shamir.Share) {
	e.t.Helper()
	require.Equal(e.t, 2, share.Index)
	ctx := e.ta.NewUncachedContext(false, cmtproto.Header{Height: e.ta.Height})
	kv := e.ta.VoteKeeper().OpenKVStore(ctx)
	accumulators, err := e.ta.VoteKeeper().CollectNonEmptyAccumulators(kv, round)
	require.NoError(e.t, err)
	var entries []*types.PartialDecryptionEntry
	for _, bucket := range slices.SortedFunc(maps.Keys(accumulators), func(a, b [2]uint32) int { return slices.Compare(a[:], b[:]) }) {
		require.NotEqual(e.t, types.PoolProposalID, bucket[0])
		bz, err := e.ta.VoteKeeper().GetTally(kv, e.roundID, bucket[0], bucket[1])
		require.NoError(e.t, err)
		ct, err := elgamal.UnmarshalCiphertext(bz)
		require.NoError(e.t, err)
		proof, err := elgamal.GeneratePartialDecryptDLEQ(share.Value, ct.C1)
		require.NoError(e.t, err)
		entries = append(entries, &types.PartialDecryptionEntry{
			ProposalId: bucket[0], VoteDecision: bucket[1], PartialDecrypt: ct.C1.Mul(share.Value).ToAffineCompressed(), DleqProof: proof,
		})
	}
	require.NoError(e.t, e.ta.VoteKeeper().SetPartialDecryptions(kv, e.roundID, 2, entries))
	e.ta.NextBlock()
}

// successor returns van holding ballots, as a ZKP4 successor VAN does.
func successor(van proxyprover.VAN, ballots uint64) proxyprover.VAN {
	van.Ballots = ballots
	return van
}

// singleLeafPath returns the Merkle path of leaf in a tree that holds only
// it, which proves against votetree.SingleLeafRoot(leaf).
func singleLeafPath(t *testing.T, leaf [32]byte) []byte {
	t.Helper()
	handle, err := votetree.NewEphemeralTreeHandle()
	require.NoError(t, err)
	defer handle.Close()
	require.NoError(t, handle.AppendBatch([][]byte{leaf[:]}))
	require.NoError(t, handle.Checkpoint(1))
	path, err := handle.Path(0, 1)
	require.NoError(t, err)
	return path
}

// requireCheckCode checks that CheckTx refuses msg with err's code in
// codespace vote.
func requireCheckCode(t *testing.T, ta *testutil.TestApp, msg any, err interface{ ABCICode() uint32 }) {
	t.Helper()
	check := ta.CheckTxSync(encodeOne(t, msg))
	require.Equal(t, err.ABCICode(), check.Code, check.Log)
	require.Equal(t, "vote", check.Codespace)
}

// requireDeliverCode checks that a block refuses msg with err's code in
// codespace vote.
func requireDeliverCode(t *testing.T, ta *testutil.TestApp, msg any, err interface{ ABCICode() uint32 }) {
	t.Helper()
	result := ta.DeliverVoteTx(encodeOne(t, msg))
	require.Equal(t, err.ABCICode(), result.Code, result.Log)
	require.Equal(t, "vote", result.Codespace)
}

// encodeOne returns msg's wire bytes; raw bytes pass through.
func encodeOne(t *testing.T, msg any) []byte {
	t.Helper()
	switch m := msg.(type) {
	case []byte:
		return m
	case types.VoteMessage:
		return testutil.MustEncodeVoteTx(m)
	}
	t.Fatalf("cannot encode %T", msg)
	return nil
}

// encodeAll returns the wire bytes of msgs.
func encodeAll[M types.VoteMessage](msgs []M) [][]byte {
	var txs [][]byte
	for _, msg := range msgs {
		txs = append(txs, testutil.MustEncodeVoteTx(msg))
	}
	return txs
}

// shareSum returns the plaintext weight of c's shares.
func shareSum(c proxyprover.Commitment) uint64 {
	var sum uint64
	for _, share := range c.Shares {
		sum += share.Value
	}
	return sum
}

// e2eSeed derives 32 fixed bytes from label.
func e2eSeed(label string) []byte {
	sum := blake2b.Sum256([]byte("vote-sdk proxy e2e/" + label))
	return sum[:]
}

// blake2bUint64 derives a fixed nonzero 62-bit integer from label, which is
// canonical as a Pallas Fp or scalar.
func blake2bUint64(label string) uint64 {
	seed := e2eSeed(label)
	var v uint64
	for _, b := range seed[:8] {
		v = v<<8 | uint64(b)
	}
	return v>>2 | 1
}

// eventsOf returns the events of type typ.
func eventsOf(events []abci.Event, typ string) []abci.Event {
	var out []abci.Event
	for _, event := range events {
		if event.Type == typ {
			out = append(out, event)
		}
	}
	return out
}

// oneEvent returns the only event of type typ.
func oneEvent(t *testing.T, events []abci.Event, typ string) abci.Event {
	t.Helper()
	matching := eventsOf(events, typ)
	require.Len(t, matching, 1, "events of type %s", typ)
	return matching[0]
}

// attrOf returns the value of event's attribute key.
func attrOf(t *testing.T, event abci.Event, key string) string {
	t.Helper()
	for _, attribute := range event.Attributes {
		if attribute.Key == key {
			return attribute.Value
		}
	}
	t.Fatalf("event %s has no attribute %s", event.Type, key)
	return ""
}

// parseUints parses a comma-joined list of decimal integers.
func parseUints(t *testing.T, list string) []uint64 {
	t.Helper()
	var out []uint64
	for _, field := range strings.Split(list, ",") {
		v, err := strconv.ParseUint(field, 10, 64)
		require.NoError(t, err, list)
		out = append(out, v)
	}
	return out
}

// joinUints joins values as an event attribute does.
func joinUints(values []uint32) string {
	fields := make([]string, len(values))
	for i, v := range values {
		fields[i] = strconv.FormatUint(uint64(v), 10)
	}
	return strings.Join(fields, ",")
}
