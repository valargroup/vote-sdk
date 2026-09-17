package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	rpcclient "github.com/cometbft/cometbft/rpc/client"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/stretchr/testify/require"
	"github.com/valargroup/vote-sdk/ffi/roundid"
	"github.com/valargroup/vote-sdk/x/vote/types"
	"google.golang.org/protobuf/proto"
)

func TestLatestVerificationRoundIncludesPending(t *testing.T) {
	old := &types.VoteRound{CreatedAtHeight: 50, Status: types.SessionStatus_SESSION_STATUS_ACTIVE}
	newest := &types.VoteRound{CreatedAtHeight: 60, Status: types.SessionStatus_SESSION_STATUS_PENDING}
	got, err := latestVerificationRound([]*types.VoteRound{newest, old})
	require.NoError(t, err)
	require.Same(t, newest, got)
	_, err = latestVerificationRound(nil)
	require.ErrorContains(t, err, "no registered")
	_, err = latestVerificationRound([]*types.VoteRound{newest, {CreatedAtHeight: 60}})
	require.ErrorContains(t, err, "multiple rounds")
}

func TestCheckRoundIdentityUsesCreationHeight(t *testing.T) {
	round := &types.VoteRound{
		CreatedAtHeight: 123, SnapshotHeight: 3459350, VoteEndTime: 2000000000,
		SnapshotBlockhash: bytes.Repeat([]byte{2}, 32), ProposalsHash: bytes.Repeat([]byte{3}, 32),
		NullifierImtRoot: append([]byte{4}, make([]byte, 31)...), NcRoot: append([]byte{5}, make([]byte, 31)...),
		Status: types.SessionStatus_SESSION_STATUS_PENDING,
	}
	id, err := roundid.DeriveRoundID(round.CreatedAtHeight, round.SnapshotBlockhash, round.ProposalsHash, round.VoteEndTime, round.NullifierImtRoot, round.NcRoot)
	require.NoError(t, err)
	round.VoteRoundId = id[:]
	report, err := checkRoundIdentity(round)
	require.NoError(t, err)
	require.True(t, report.RoundIDMatches)
	// No EA key or attestation is necessary, but every committed field matters.
	for _, mutate := range []func(*types.VoteRound){
		func(round *types.VoteRound) { round.CreatedAtHeight = round.SnapshotHeight },
		func(round *types.VoteRound) { round.SnapshotBlockhash[0] ^= 1 },
		func(round *types.VoteRound) { round.ProposalsHash[0] ^= 1 },
		func(round *types.VoteRound) { round.VoteEndTime++ },
		func(round *types.VoteRound) { round.NullifierImtRoot[0] ^= 1 },
		func(round *types.VoteRound) { round.NcRoot[0] ^= 1 },
	} {
		changed := proto.Clone(round).(*types.VoteRound)
		mutate(changed)
		report, err := checkRoundIdentity(changed)
		require.NoError(t, err)
		require.False(t, report.RoundIDMatches)
	}
	round.NullifierImtRoot = bytes.Repeat([]byte{255}, 32)
	_, err = checkRoundIdentity(round)
	require.Error(t, err)
}

type verificationNode struct {
	client.CometRPC
	status coretypes.ResultStatus
	query  func(string, cmtbytes.HexBytes, rpcclient.ABCIQueryOptions) (*coretypes.ResultABCIQuery, error)
}

func (n verificationNode) Status(context.Context) (*coretypes.ResultStatus, error) {
	return &n.status, nil
}
func (n verificationNode) ABCIQueryWithOptions(_ context.Context, path string, data cmtbytes.HexBytes, opts rpcclient.ABCIQueryOptions) (*coretypes.ResultABCIQuery, error) {
	return n.query(path, data, opts)
}

func TestVerifyRoundCommand(t *testing.T) {
	round := &types.VoteRound{CreatedAtHeight: 123, SnapshotHeight: 3428150, VoteEndTime: 2000000000,
		SnapshotBlockhash: bytes.Repeat([]byte{2}, 32), ProposalsHash: bytes.Repeat([]byte{3}, 32),
		NullifierImtRoot: append([]byte{4}, make([]byte, 31)...), NcRoot: append([]byte{5}, make([]byte, 31)...),
		Status: types.SessionStatus_SESSION_STATUS_PENDING}
	id, err := roundid.DeriveRoundID(round.CreatedAtHeight, round.SnapshotBlockhash, round.ProposalsHash, round.VoteEndTime, round.NullifierImtRoot, round.NcRoot)
	require.NoError(t, err)
	round.VoteRoundId = id[:]
	for _, scenario := range []string{"latest pending", "explicit", "wrong chain", "syncing", "wrong returned round", "bad binding"} {
		t.Run(scenario, func(t *testing.T) {
			node := verificationNode{}
			node.status.NodeInfo.Network = "test-chain"
			node.status.SyncInfo.LatestBlockHeight = 456
			if scenario == "wrong chain" {
				node.status.NodeInfo.Network = "other-chain"
			}
			if scenario == "syncing" {
				node.status.SyncInfo.CatchingUp = true
			}
			queried := false
			node.query = func(path string, data cmtbytes.HexBytes, opts rpcclient.ABCIQueryOptions) (*coretypes.ResultABCIQuery, error) {
				queried = true
				require.EqualValues(t, 456, opts.Height)
				responseRound := proto.Clone(round).(*types.VoteRound)
				if scenario == "wrong returned round" {
					responseRound.VoteRoundId[0] ^= 1
				}
				if scenario == "bad binding" {
					responseRound.NullifierImtRoot[0] ^= 1
				}
				var response proto.Message
				if scenario == "explicit" || scenario == "wrong returned round" {
					require.Equal(t, "/svote.v1.Query/VoteRound", path)
					var request types.QueryVoteRoundRequest
					require.NoError(t, proto.Unmarshal(data, &request))
					require.Equal(t, round.VoteRoundId, request.VoteRoundId)
					response = &types.QueryVoteRoundResponse{Round: responseRound}
				} else {
					require.Equal(t, "/svote.v1.Query/ListRounds", path)
					response = &types.QueryListRoundsResponse{Rounds: []*types.VoteRound{{CreatedAtHeight: 122, Status: types.SessionStatus_SESSION_STATUS_ACTIVE}, responseRound}}
				}
				encoded, err := proto.Marshal(response)
				require.NoError(t, err)
				return &coretypes.ResultABCIQuery{Response: abci.ResponseQuery{Value: encoded, Height: opts.Height}}, nil
			}
			cmd := CmdVerifyRound()
			cmd.SetContext(context.Background())
			require.NoError(t, client.SetCmdClientContext(cmd, client.Context{}.WithClient(node)))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&bytes.Buffer{})
			args := []string{"--expected-chain-id", "test-chain", "--output", "json"}
			if scenario == "explicit" || scenario == "wrong returned round" {
				args = append(args, hex.EncodeToString(round.VoteRoundId))
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			switch scenario {
			case "wrong chain", "syncing":
				require.Error(t, err)
				require.False(t, queried)
			case "wrong returned round":
				require.ErrorContains(t, err, "requested round")
			case "bad binding":
				require.ErrorContains(t, err, "does not match")
			default:
				require.NoError(t, err)
				var report roundIdentityReport
				require.NoError(t, json.Unmarshal(out.Bytes(), &report))
				require.True(t, report.RoundIDMatches)
				require.Equal(t, hex.EncodeToString(round.VoteRoundId), report.RoundID)
				require.EqualValues(t, 456, report.QueryHeight)
				require.Equal(t, "SESSION_STATUS_PENDING", report.Status)
			}
		})
	}
}
