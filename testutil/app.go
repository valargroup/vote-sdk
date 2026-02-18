package testutil

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

	dbm "github.com/cosmos/cosmos-db"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log"
	sdkmath "cosmossdk.io/math"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"

	"github.com/cosmos/cosmos-sdk/baseapp"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/cosmos/cosmos-sdk/codec"

	"github.com/z-cale/zally/app"
	"github.com/z-cale/zally/crypto/elgamal"
	votekeeper "github.com/z-cale/zally/x/vote/keeper"
	"github.com/z-cale/zally/x/vote/types"
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

	// ValPrivKey is the secp256k1 private key of the genesis validator's
	// operator account. Used for signing ceremony messages in tests.
	ValPrivKey *secp256k1.PrivKey
}

// SetupTestApp creates a fresh ZallyApp backed by an in-memory database,
// initializes the chain with a proper genesis (including a genesis validator),
// and returns a TestApp ready for integration testing.
func SetupTestApp(t *testing.T) *TestApp {
	t.Helper()
	ta := setupTestApp(t, simtestutil.EmptyAppOptions{})

	// Seed a confirmed ceremony with a dummy EA keypair so that
	// CreateVotingSession (which requires a confirmed ceremony) works.
	_, pk := elgamal.KeyGen(rand.Reader)
	ta.SeedConfirmedCeremony(pk.Point.ToAffineCompressed())

	// Seed the vote manager so CreateVotingSession passes authorization.
	ta.SeedVoteManager("zvote1admin")

	return ta
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

	ta := setupTestApp(t, appOpts)
	ta.SeedConfirmedCeremony(pk.Point.ToAffineCompressed())
	ta.SeedVoteManager("zvote1admin")

	return ta, pk
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
	// This key is also used for signing ceremony messages in tests.
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

	// Build genesis state. GenesisStateWithValSet sets the validator operator
	// address to sdk.ValAddress(val.Address) (from the CometBFT key), and the
	// genesis account as the delegator. We then patch the staking genesis to
	// use the genesis account as operator so that ceremony message signing
	// (which requires the operator's account key) works in tests.
	genesisState, err := genesisStateWithAccountOperator(
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
		ValPrivKey:      privKey,
	}
}

// SetupTestAppWithPallasKey creates a TestApp with both an EA keypair and a
// Pallas keypair written to temp files and passed via app options. This enables
// testing the auto-ack PrepareProposal handler. Returns the TestApp, the
// validator's Pallas secret key, the EA secret key, and the EA public key.
func SetupTestAppWithPallasKey(t *testing.T) (ta *TestApp, pallasSk *elgamal.SecretKey, pallasPk *elgamal.PublicKey, eaSk *elgamal.SecretKey, eaPk *elgamal.PublicKey) {
	t.Helper()

	pallasSk, pallasPk = elgamal.KeyGen(rand.Reader)
	eaSk, eaPk = elgamal.KeyGen(rand.Reader)

	pallasSkBytes, err := elgamal.MarshalSecretKey(pallasSk)
	require.NoError(t, err)

	tmpDir := t.TempDir()
	pallasSkPath := filepath.Join(tmpDir, "pallas.sk")
	require.NoError(t, os.WriteFile(pallasSkPath, pallasSkBytes, 0600))

	eaSkPath := filepath.Join(tmpDir, "ea.sk")
	// ea.sk is intentionally NOT pre-written — the ceremony ack handler
	// writes it after successful decryption.

	appOpts := simtestutil.AppOptionsMap{
		"vote.pallas_sk_path": pallasSkPath,
		"vote.ea_sk_path":     eaSkPath,
	}

	ta = setupTestApp(t, appOpts)
	return ta, pallasSk, pallasPk, eaSk, eaPk
}

// VoteKeeper returns the vote module keeper for querying state in tests.
func (ta *TestApp) VoteKeeper() votekeeper.Keeper {
	return ta.ZallyApp.VoteKeeper
}

// SeedVoteManager writes the vote manager address directly into the module's
// KV store and commits via an empty block. Must be called before any
// CreateVotingSession, since that handler requires the creator to be the
// vote manager.
func (ta *TestApp) SeedVoteManager(addr string) {
	ta.t.Helper()

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	err := ta.VoteKeeper().SetVoteManager(kvStore, &types.VoteManagerState{Address: addr})
	require.NoError(ta.t, err)

	ta.NextBlock()
}

// SeedConfirmedCeremony writes a confirmed ceremony state (with ea_pk) directly
// into the module's KV store and commits it via an empty block. This must be
// called before any CreateVotingSession call, since that handler now requires
// a confirmed ceremony.
func (ta *TestApp) SeedConfirmedCeremony(eaPk []byte) {
	ta.t.Helper()

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	state := &types.CeremonyState{
		Status: types.CeremonyStatus_CEREMONY_STATUS_CONFIRMED,
		EaPk:   eaPk,
	}
	err := ta.VoteKeeper().SetCeremonyState(kvStore, state)
	require.NoError(ta.t, err)

	// Commit via an empty block so the IAVL working set changes are persisted.
	ta.NextBlock()
}

// SeedVotingSession creates a VoteRound directly in the KV store from a
// MsgCreateVotingSession, bypassing the ABCI pipeline. The round is committed
// via an empty block. Returns the derived vote_round_id.
//
// MsgCreateVotingSession is now a standard Cosmos SDK tx (signed by the vote
// manager), so it can no longer be submitted via the custom vote tx wire
// format. For integration tests that need an active round to test other vote
// messages (delegation, cast, reveal, tally), this helper seeds the round
// directly — the session creation itself is not under test here.
func (ta *TestApp) SeedVotingSession(msg *types.MsgCreateVotingSession) []byte {
	ta.t.Helper()

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	// Read ea_pk from confirmed ceremony state (if present).
	var eaPk []byte
	ceremony, err := ta.VoteKeeper().GetCeremonyState(kvStore)
	if err == nil && ceremony != nil && len(ceremony.EaPk) > 0 {
		eaPk = ceremony.EaPk
	} else {
		eaPk = make([]byte, 32)
	}

	roundID := deriveRoundID(msg)

	round := &types.VoteRound{
		VoteRoundId:       roundID,
		SnapshotHeight:    msg.SnapshotHeight,
		SnapshotBlockhash: msg.SnapshotBlockhash,
		ProposalsHash:     msg.ProposalsHash,
		VoteEndTime:       msg.VoteEndTime,
		NullifierImtRoot:  msg.NullifierImtRoot,
		NcRoot:            msg.NcRoot,
		Creator:           msg.Creator,
		Status:            types.SessionStatus_SESSION_STATUS_ACTIVE,
		EaPk:              eaPk,
		VkZkp1:            msg.VkZkp1,
		VkZkp2:            msg.VkZkp2,
		VkZkp3:            msg.VkZkp3,
		Proposals:         msg.Proposals,
		Description:       msg.Description,
	}

	err = ta.VoteKeeper().SetVoteRound(kvStore, round)
	require.NoError(ta.t, err)

	ta.NextBlock()
	return roundID
}

// deriveRoundID computes Blake2b-256(snapshot_height || snapshot_blockhash ||
// proposals_hash || vote_end_time || nullifier_imt_root || nc_root).
func deriveRoundID(msg *types.MsgCreateVotingSession) []byte {
	h, _ := blake2b.New256(nil)
	var buf [8]byte

	binary.BigEndian.PutUint64(buf[:], msg.SnapshotHeight)
	h.Write(buf[:])
	h.Write(msg.SnapshotBlockhash)
	h.Write(msg.ProposalsHash)
	binary.BigEndian.PutUint64(buf[:], msg.VoteEndTime)
	h.Write(buf[:])
	h.Write(msg.NullifierImtRoot)
	h.Write(msg.NcRoot)

	return h.Sum(nil)
}

// SeedDealtCeremony writes a DEALT ceremony state into the KV store. The
// ceremony includes a single validator (the genesis validator) with an ECIES
// payload encrypting eaSkBytes under pallasPk. Commits via an empty block.
func (ta *TestApp) SeedDealtCeremony(pallasPkBytes, eaPkBytes []byte, payloads []*types.DealerPayload, validators []*types.ValidatorPallasKey) {
	ta.t.Helper()

	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	kvStore := ta.VoteKeeper().OpenKVStore(ctx)

	state := &types.CeremonyState{
		Status:       types.CeremonyStatus_CEREMONY_STATUS_DEALT,
		EaPk:         eaPkBytes,
		Dealer:       "dealer",
		Validators:   validators,
		Payloads:     payloads,
		PhaseStart:   uint64(ta.Time.Unix()),
		PhaseTimeout: 30,
	}
	err := ta.VoteKeeper().SetCeremonyState(kvStore, state)
	require.NoError(ta.t, err)

	// Commit via an empty block so the IAVL working set changes are persisted.
	ta.NextBlock()
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

// CallPrepareProposalWithTxs builds a RequestPrepareProposal for the next block
// with the given mempool txs and calls PrepareProposal. Returns the response.
func (ta *TestApp) CallPrepareProposalWithTxs(txs [][]byte) *abci.ResponsePrepareProposal {
	ta.t.Helper()

	resp, err := ta.ZallyApp.PrepareProposal(&abci.RequestPrepareProposal{
		Height:          ta.Height + 1,
		Time:            ta.Time.Add(5 * time.Second),
		Txs:             txs,
		ProposerAddress: ta.ProposerAddress,
	})
	require.NoError(ta.t, err)
	return resp
}

// CallProcessProposal calls ProcessProposal with the given txs at the next
// block height (current height+1, time+5s). Returns the response.
func (ta *TestApp) CallProcessProposal(txs [][]byte) *abci.ResponseProcessProposal {
	ta.t.Helper()

	resp, err := ta.ZallyApp.ProcessProposal(&abci.RequestProcessProposal{
		Height:          ta.Height + 1,
		Time:            ta.Time.Add(5 * time.Second),
		Txs:             txs,
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

// MustBuildSignedCeremonyTx builds a standard Cosmos SDK transaction containing
// the ceremony message, signs it with the genesis validator's secp256k1 key,
// and returns the encoded tx bytes. Panics on failure (safe for tests).
func (ta *TestApp) MustBuildSignedCeremonyTx(msg sdk.Msg) []byte {
	ta.t.Helper()

	txConfig := ta.ZallyApp.TxConfig()
	privKey := ta.ValPrivKey
	accAddr := sdk.AccAddress(privKey.PubKey().Address())

	// Query the account for the current sequence number.
	ctx := ta.NewUncachedContext(false, cmtproto.Header{Height: ta.Height})
	acc := ta.AccountKeeper.GetAccount(ctx, accAddr)
	require.NotNil(ta.t, acc, "validator account not found at address %s", accAddr)

	accNum := acc.GetAccountNumber()
	accSeq := acc.GetSequence()

	// Build the unsigned tx.
	txBuilder := txConfig.NewTxBuilder()
	err := txBuilder.SetMsgs(msg)
	require.NoError(ta.t, err)
	txBuilder.SetGasLimit(200000)
	txBuilder.SetFeeAmount(sdk.NewCoins())

	// Determine sign mode.
	signMode, err := authsigning.APISignModeToInternal(txConfig.SignModeHandler().DefaultMode())
	require.NoError(ta.t, err)

	// Set empty signature first (required for SIGN_MODE_DIRECT).
	sigData := signing.SingleSignatureData{
		SignMode:  signMode,
		Signature: nil,
	}
	sig := signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	err = txBuilder.SetSignatures(sig)
	require.NoError(ta.t, err)

	// Generate sign bytes and sign.
	signerData := authsigning.SignerData{
		ChainID:       testChainID,
		AccountNumber: accNum,
		Sequence:      accSeq,
		PubKey:        privKey.PubKey(),
		Address:       accAddr.String(),
	}

	signBytes, err := authsigning.GetSignBytesAdapter(
		context.Background(), txConfig.SignModeHandler(), signMode, signerData, txBuilder.GetTx())
	require.NoError(ta.t, err)

	sigBytes, err := privKey.Sign(signBytes)
	require.NoError(ta.t, err)

	// Set the real signature.
	sigData = signing.SingleSignatureData{
		SignMode:  signMode,
		Signature: sigBytes,
	}
	sig = signing.SignatureV2{
		PubKey:   privKey.PubKey(),
		Data:     &sigData,
		Sequence: accSeq,
	}
	err = txBuilder.SetSignatures(sig)
	require.NoError(ta.t, err)

	// Encode.
	txBytes, err := txConfig.TxEncoder()(txBuilder.GetTx())
	require.NoError(ta.t, err)

	// Sanity check: verify the tx can be decoded.
	_, err = txConfig.TxDecoder()(txBytes)
	require.NoError(ta.t, err, "self-decode check failed for signed ceremony tx")

	return txBytes
}

// genesisStateWithAccountOperator is a variant of simtestutil.GenesisStateWithValSet
// that sets the validator's operator address to the genesis account's address
// (instead of deriving it from the CometBFT consensus key). This allows test
// ceremony messages to be signed by the genesis account's secp256k1 key.
func genesisStateWithAccountOperator(
	cdc codec.Codec,
	genesisState map[string]json.RawMessage,
	valSet *cmttypes.ValidatorSet,
	genAccs []authtypes.GenesisAccount,
	balances ...banktypes.Balance,
) (map[string]json.RawMessage, error) {
	authGenesis := authtypes.NewGenesisState(authtypes.DefaultParams(), genAccs)
	genesisState[authtypes.ModuleName] = cdc.MustMarshalJSON(authGenesis)

	validators := make([]stakingtypes.Validator, 0, len(valSet.Validators))
	delegations := make([]stakingtypes.Delegation, 0, len(valSet.Validators))

	bondAmt := sdk.DefaultPowerReduction

	// Use the genesis account as the validator operator so that the
	// operator's private key (secp256k1) is available for signing.
	operAddr := sdk.ValAddress(genAccs[0].GetAddress()).String()

	for _, val := range valSet.Validators {
		pk, err := cryptocodec.FromCmtPubKeyInterface(val.PubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to convert pubkey: %w", err)
		}

		pkAny, err := codectypes.NewAnyWithValue(pk)
		if err != nil {
			return nil, fmt.Errorf("failed to create new any: %w", err)
		}

		validator := stakingtypes.Validator{
			OperatorAddress:   operAddr,
			ConsensusPubkey:   pkAny,
			Jailed:            false,
			Status:            stakingtypes.Bonded,
			Tokens:            bondAmt,
			DelegatorShares:   sdkmath.LegacyOneDec(),
			Description:       stakingtypes.Description{},
			UnbondingHeight:   int64(0),
			UnbondingTime:     time.Unix(0, 0).UTC(),
			Commission:        stakingtypes.NewCommission(sdkmath.LegacyZeroDec(), sdkmath.LegacyZeroDec(), sdkmath.LegacyZeroDec()),
			MinSelfDelegation: sdkmath.ZeroInt(),
		}
		validators = append(validators, validator)
		delegations = append(delegations, stakingtypes.NewDelegation(
			genAccs[0].GetAddress().String(), operAddr, sdkmath.LegacyOneDec()))
	}

	stakingGenesis := stakingtypes.NewGenesisState(stakingtypes.DefaultParams(), validators, delegations)
	genesisState[stakingtypes.ModuleName] = cdc.MustMarshalJSON(stakingGenesis)

	totalSupply := sdk.NewCoins()
	for _, b := range balances {
		totalSupply = totalSupply.Add(b.Coins...)
	}
	for range delegations {
		totalSupply = totalSupply.Add(sdk.NewCoin(sdk.DefaultBondDenom, bondAmt))
	}

	balances = append(balances, banktypes.Balance{
		Address: authtypes.NewModuleAddress(stakingtypes.BondedPoolName).String(),
		Coins:   sdk.Coins{sdk.NewCoin(sdk.DefaultBondDenom, bondAmt)},
	})

	bankGenesis := banktypes.NewGenesisState(banktypes.DefaultGenesisState().Params, balances, totalSupply, []banktypes.Metadata{}, []banktypes.SendEnabled{})
	genesisState[banktypes.ModuleName] = cdc.MustMarshalJSON(bankGenesis)

	return genesisState, nil
}
