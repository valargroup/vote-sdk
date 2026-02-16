package testutil

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"

	abci "github.com/cometbft/cometbft/abci/types"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"

	"github.com/z-cale/zally/app"
	"github.com/z-cale/zally/crypto/elgamal"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
)

const testChainID = "zally-test-1"

// TestApp wraps ZallyApp with helpers for driving the ABCI lifecycle
// in integration tests. No CometBFT process or network is involved —
// tests call FinalizeBlock/Commit/CheckTx directly.
type TestApp struct {
	*app.ZallyApp

	t      *testing.T
	Height int64
	Time   time.Time

	// ProposerAddress is the consensus address of the genesis validator,
	// passed in every FinalizeBlock request so the ante handler can verify
	// that MsgSubmitTally creators match the block proposer.
	ProposerAddress []byte
}

// SetupTestApp creates a fresh ZallyApp backed by an in-memory database,
// initializes the chain with a proper genesis (including a genesis validator),
// and returns a TestApp ready for integration testing.
func SetupTestApp(t *testing.T) *TestApp {
	t.Helper()
	return setupTestApp(t, simtestutil.EmptyAppOptions{})
}

// SetupTestAppWithEAKey creates a TestApp with a real ElGamal keypair for the
// EA (Election Authority). The secret key is written to a temp file and passed
// via the "vote.ea_sk_path" app option so that PrepareProposal can decrypt
// tallies. Returns both the TestApp and the public key for encrypting shares.
func SetupTestAppWithEAKey(t *testing.T) (*TestApp, *elgamal.PublicKey) {
	t.Helper()

	sk, pk := elgamal.KeyGen(rand.Reader)

	skBytes, err := elgamal.MarshalSecretKey(sk)
	require.NoError(t, err)

	skPath := filepath.Join(t.TempDir(), "ea.sk")
	require.NoError(t, os.WriteFile(skPath, skBytes, 0600))

	appOpts := simtestutil.AppOptionsMap{
		"vote.ea_sk_path": skPath,
	}

	return setupTestApp(t, appOpts), pk
}

// setupTestApp is the shared implementation for SetupTestApp and SetupTestAppWithEAKey.
func setupTestApp(t *testing.T, appOpts servertypes.AppOptions) *TestApp {
	t.Helper()

	db := dbm.NewMemDB()
	logger := log.NewNopLogger()

	zallyApp := app.NewZallyApp(
		logger, db, nil, true, appOpts,
		baseapp.SetChainID(testChainID),
	)

	// Create a genesis validator set.
	valSet, err := simtestutil.CreateRandomValidatorSet()
	require.NoError(t, err)

	// Create a genesis account with enough funds for the validator's self-delegation.
	privKey := secp256k1.GenPrivKey()
	genAcc := authtypes.NewBaseAccount(
		privKey.PubKey().Address().Bytes(),
		privKey.PubKey(),
		0, 0,
	)
	balance := banktypes.Balance{
		Address: genAcc.GetAddress().String(),
		Coins:   sdk.NewCoins(sdk.NewCoin(sdk.DefaultBondDenom, sdkmath.NewInt(1_000_000_000_000))),
	}

	// Build genesis state with the validator set and funded account.
	genesisState, err := simtestutil.GenesisStateWithValSet(
		zallyApp.AppCodec(),
		zallyApp.DefaultGenesis(),
		valSet,
		[]authtypes.GenesisAccount{genAcc},
		balance,
	)
	require.NoError(t, err)

	stateBytes, err := json.MarshalIndent(genesisState, "", " ")
	require.NoError(t, err)

	now := time.Unix(1_000_000, 0).UTC()

	// Initialize the chain.
	_, err = zallyApp.InitChain(&abci.RequestInitChain{
		ChainId:         testChainID,
		AppStateBytes:   stateBytes,
		ConsensusParams: simtestutil.DefaultConsensusParams,
		Validators:      []abci.ValidatorUpdate{},
		Time:            now,
		InitialHeight:   1,
	})
	require.NoError(t, err)

	// The genesis validator's consensus address, used as ProposerAddress
	// in all FinalizeBlock calls.
	proposerAddr := valSet.Validators[0].Address.Bytes()

	// Finalize the genesis block (height 1) so the app is fully initialized.
	_, err = zallyApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:             1,
		Time:               now,
		NextValidatorsHash: valSet.Hash(),
		ProposerAddress:    proposerAddr,
	})
	require.NoError(t, err)

	_, err = zallyApp.Commit()
	require.NoError(t, err)

	return &TestApp{
		ZallyApp:        zallyApp,
		t:               t,
		Height:          1,
		Time:            now,
		ProposerAddress: proposerAddr,
	}
}

// VoteKeeper returns the vote module keeper for querying state in tests.
func (ta *TestApp) VoteKeeper() votekeeper.Keeper {
	return ta.ZallyApp.VoteKeeper
}

// ValidatorOperAddr returns the operator (valoper) address of the genesis
// validator. This queries the staking keeper for all validators and returns
// the first one's operator address.
func (ta *TestApp) ValidatorOperAddr() string {
	ta.t.Helper()
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	vals, err := ta.StakingKeeper.GetAllValidators(ctx)
	require.NoError(ta.t, err)
	require.NotEmpty(ta.t, vals, "expected at least one genesis validator")
	return vals[0].OperatorAddress
}

// NextBlock commits an empty block, advancing height and time by 5 seconds.
// Triggers EndBlocker (commitment tree root computation).
func (ta *TestApp) NextBlock() {
	ta.t.Helper()

	ta.Height++
	ta.Time = ta.Time.Add(5 * time.Second)

	_, err := ta.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          ta.Height,
		Time:            ta.Time,
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)

	_, err = ta.Commit()
	require.NoError(ta.t, err)
}

// NextBlockAtTime commits an empty block at a specific time, advancing height by 1.
// Triggers EndBlocker (commitment tree root computation, round status transitions).
func (ta *TestApp) NextBlockAtTime(t time.Time) {
	ta.t.Helper()

	ta.Height++
	ta.Time = t

	_, err := ta.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          ta.Height,
		Time:            ta.Time,
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)

	_, err = ta.Commit()
	require.NoError(ta.t, err)
}

// DeliverVoteTx submits a single raw vote tx through FinalizeBlock + Commit
// and returns the ExecTxResult. The block height and time are advanced.
func (ta *TestApp) DeliverVoteTx(txBytes []byte) *abci.ExecTxResult {
	ta.t.Helper()

	ta.Height++
	ta.Time = ta.Time.Add(5 * time.Second)

	resp, err := ta.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          ta.Height,
		Time:            ta.Time,
		Txs:             [][]byte{txBytes},
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)

	_, err = ta.Commit()
	require.NoError(ta.t, err)

	require.Len(ta.t, resp.TxResults, 1, "expected exactly 1 tx result")
	return resp.TxResults[0]
}

// DeliverVoteTxs submits multiple raw vote txs in a single block through
// FinalizeBlock + Commit and returns all ExecTxResults.
func (ta *TestApp) DeliverVoteTxs(txs [][]byte) []*abci.ExecTxResult {
	ta.t.Helper()

	ta.Height++
	ta.Time = ta.Time.Add(5 * time.Second)

	resp, err := ta.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          ta.Height,
		Time:            ta.Time,
		Txs:             txs,
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)

	_, err = ta.Commit()
	require.NoError(ta.t, err)

	return resp.TxResults
}

// CheckTxSync runs CheckTx (type New) on raw tx bytes and returns the response.
func (ta *TestApp) CheckTxSync(txBytes []byte) *abci.ResponseCheckTx {
	ta.t.Helper()

	resp, err := ta.CheckTx(&abci.RequestCheckTx{
		Tx:   txBytes,
		Type: abci.CheckTxType_New,
	})
	require.NoError(ta.t, err)
	return resp
}

// RecheckTxSync runs CheckTx (type Recheck) on raw tx bytes and returns the response.
func (ta *TestApp) RecheckTxSync(txBytes []byte) *abci.ResponseCheckTx {
	ta.t.Helper()

	resp, err := ta.CheckTx(&abci.RequestCheckTx{
		Tx:   txBytes,
		Type: abci.CheckTxType_Recheck,
	})
	require.NoError(ta.t, err)
	return resp
}

// CallPrepareProposal builds a RequestPrepareProposal for the next block
// (current height+1, time+5s) and calls PrepareProposal on the app.
// Returns the response containing any auto-injected txs.
func (ta *TestApp) CallPrepareProposal() *abci.ResponsePrepareProposal {
	ta.t.Helper()

	resp, err := ta.ZallyApp.PrepareProposal(&abci.RequestPrepareProposal{
		Height:          ta.Height + 1,
		Time:            ta.Time.Add(5 * time.Second),
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)
	return resp
}

// NextBlockWithPrepareProposal calls PrepareProposal to collect any auto-injected
// txs, then feeds them into FinalizeBlock + Commit. This simulates a real block
// production cycle where PrepareProposal injects tally txs.
func (ta *TestApp) NextBlockWithPrepareProposal() {
	ta.t.Helper()

	ta.Height++
	ta.Time = ta.Time.Add(5 * time.Second)

	ppResp, err := ta.ZallyApp.PrepareProposal(&abci.RequestPrepareProposal{
		Height:          ta.Height,
		Time:            ta.Time,
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)

	_, err = ta.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height:          ta.Height,
		Time:            ta.Time,
		Txs:             ppResp.Txs,
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)

	_, err = ta.Commit()
	require.NoError(ta.t, err)
}
