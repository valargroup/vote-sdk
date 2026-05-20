package types_test

import (
	"bytes"
	cryptorand "crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

func validGenesisCiphertext(t *testing.T) []byte {
	t.Helper()
	_, pk := elgamal.KeyGen(cryptorand.Reader)
	ct, err := elgamal.Encrypt(pk, 42, cryptorand.Reader)
	require.NoError(t, err)
	bz, err := elgamal.MarshalCiphertext(ct)
	require.NoError(t, err)
	return bz
}

func validGenesis(t *testing.T) *types.GenesisState {
	t.Helper()
	roundID := bytes.Repeat([]byte{0xAA}, 32)
	return &types.GenesisState{
		Rounds: []*types.VoteRound{
			{
				VoteRoundId: roundID,
				VoteEndTime: 2_000_000,
				Status:      types.SessionStatus_SESSION_STATUS_ACTIVE,
			},
		},
		Nullifiers: []*types.NullifierEntry{
			{NullifierType: 0, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB1}, 32)},
			{NullifierType: 1, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB2}, 32)},
			{NullifierType: 2, RoundId: roundID, Nullifier: bytes.Repeat([]byte{0xB3}, 32)},
		},
		VoteManagerAddresses: []string{"sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"},
		TallyResults: []*types.TallyResult{
			{VoteRoundId: roundID, ProposalId: 1, VoteDecision: 0, TotalValue: 100},
		},
		PallasKeys: []*types.ValidatorPallasKey{
			{ValidatorAddress: "svvaloper1xyz", PallasPk: elgamal.PallasGenerator().ToAffineCompressed()},
		},
		TallyAccumulators: []*types.GenesisTallyAccumulator{
			{RoundId: roundID, ProposalId: 1, VoteDecision: 0, Ciphertext: validGenesisCiphertext(t)},
		},
		ShareCounts: []*types.GenesisShareCount{
			{RoundId: roundID, ProposalId: 1, VoteDecision: 0, Count: 5},
		},
		PartialDecryptions: []*types.GenesisPartialDecryption{
			{
				RoundId:        roundID,
				ValidatorIndex: 1,
				ProposalId:     1,
				VoteDecision:   0,
				PartialDecrypt: elgamal.PallasGenerator().ToAffineCompressed(),
				DleqProof:      bytes.Repeat([]byte{0x11}, elgamal.DLEQProofSize),
			},
		},
	}
}

func TestValidateGenesisState_Valid(t *testing.T) {
	require.NoError(t, types.ValidateGenesisState(validGenesis(t)))
}

func TestValidateGenesisState_Nil(t *testing.T) {
	require.NoError(t, types.ValidateGenesisState(nil))
}

func TestValidateGenesisState_RoundIDBadLength(t *testing.T) {
	gs := validGenesis(t)
	gs.Rounds[0].VoteRoundId = []byte{0x01, 0x02}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rounds[0].vote_round_id is 2 bytes")
}

func TestValidateGenesisState_DuplicateRoundID(t *testing.T) {
	gs := validGenesis(t)
	gs.Rounds = append(gs.Rounds, &types.VoteRound{
		VoteRoundId: gs.Rounds[0].VoteRoundId,
		VoteEndTime: 2_000_000,
	})
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate vote_round_id")
}

func TestValidateGenesisState_NullifierTypeTooHigh(t *testing.T) {
	gs := validGenesis(t)
	gs.Nullifiers[0].NullifierType = 3
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nullifiers[0].nullifier_type is 3")
}

func TestValidateGenesisState_NullifierRoundIDBadLength(t *testing.T) {
	gs := validGenesis(t)
	gs.Nullifiers[0].RoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nullifiers[0].round_id is 1 bytes")
}

func TestValidateGenesisState_NullifierEmpty(t *testing.T) {
	gs := validGenesis(t)
	gs.Nullifiers[0].Nullifier = nil
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nullifiers[0].nullifier is empty")
}

func TestValidateGenesisState_VoteManagerBadAddress(t *testing.T) {
	gs := validGenesis(t)
	gs.VoteManagerAddresses = []string{"not-a-valid-address"}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a valid bech32 address")
}

func TestValidateGenesisState_VoteManagersEmpty(t *testing.T) {
	gs := validGenesis(t)
	gs.VoteManagerAddresses = nil
	err := types.ValidateGenesisState(gs)
	require.ErrorIs(t, err, types.ErrEmptyVoteManagerSet)
}

func TestValidateGenesisState_VoteManagersDuplicate(t *testing.T) {
	gs := validGenesis(t)
	addr := "sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"
	gs.VoteManagerAddresses = []string{addr, addr}
	err := types.ValidateGenesisState(gs)
	require.ErrorIs(t, err, types.ErrDuplicateVoteManager)
}

func TestValidateAndNormalizeVoteManagerPolicy_DefaultThreshold(t *testing.T) {
	addr := "sv1mqts0klc9768rns9h2ykeaka5tve6ts39c2zu3"
	normalized, threshold, err := types.ValidateAndNormalizeVoteManagerPolicy([]string{addr}, 0)
	require.NoError(t, err)
	require.Equal(t, []string{addr}, normalized)
	require.Equal(t, uint32(1), threshold)
}

func TestValidateGenesisState_VoteManagerThresholdTooHigh(t *testing.T) {
	gs := validGenesis(t)
	gs.VoteManagerThreshold = 2
	err := types.ValidateGenesisState(gs)
	require.ErrorIs(t, err, types.ErrInvalidThreshold)
	require.Contains(t, err.Error(), "exceeds manager count")
}

func TestValidateGenesisState_TallyResultBadRoundID(t *testing.T) {
	gs := validGenesis(t)
	gs.TallyResults[0].VoteRoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tally_results[0].vote_round_id")
}

func TestValidateGenesisState_TallyResultExceedsBound(t *testing.T) {
	gs := validGenesis(t)
	gs.TallyResults[0].TotalValue = types.TallyBSGSBound
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds BSGS bound")
}

func TestValidateGenesisState_PallasKeyEmptyAddress(t *testing.T) {
	gs := validGenesis(t)
	gs.PallasKeys[0].ValidatorAddress = ""
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pallas_keys[0].validator_address is empty")
}

func TestValidateGenesisState_PallasKeyBadPK(t *testing.T) {
	gs := validGenesis(t)
	gs.PallasKeys[0].PallasPk = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pallas_keys[0].pallas_pk is 1 bytes")
}

func TestValidateGenesisState_PallasKeyInvalidPoint(t *testing.T) {
	gs := validGenesis(t)
	gs.PallasKeys[0].PallasPk = bytes.Repeat([]byte{0x00}, 32)
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pallas_keys[0].pallas_pk is invalid")
}

func TestValidateGenesisState_TallyAccumulatorBadRoundID(t *testing.T) {
	gs := validGenesis(t)
	gs.TallyAccumulators[0].RoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tally_accumulators[0].round_id")
}

func TestValidateGenesisState_TallyAccumulatorBadCiphertext(t *testing.T) {
	gs := validGenesis(t)
	gs.TallyAccumulators[0].Ciphertext = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tally_accumulators[0].ciphertext is 1 bytes")
}

func TestValidateGenesisState_TallyAccumulatorInvalidPoint(t *testing.T) {
	gs := validGenesis(t)
	gs.TallyAccumulators[0].Ciphertext = bytes.Repeat([]byte{0xFF}, 64)
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tally_accumulators[0].ciphertext is invalid")
}

func TestValidateGenesisState_TallyAccumulatorIdentity(t *testing.T) {
	gs := validGenesis(t)
	gs.TallyAccumulators[0].Ciphertext = elgamal.IdentityCiphertextBytes()
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "contains identity point")
}

func TestValidateGenesisState_ShareCountBadRoundID(t *testing.T) {
	gs := validGenesis(t)
	gs.ShareCounts[0].RoundId = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "share_counts[0].round_id")
}

func TestValidateGenesisState_PartialDecryptionInvalidPoint(t *testing.T) {
	gs := validGenesis(t)
	gs.PartialDecryptions[0].PartialDecrypt = bytes.Repeat([]byte{0xFF}, 32)
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "partial_decryptions[0].partial_decrypt is invalid")
}

func TestValidateGenesisState_PartialDecryptionBadDLEQProofSize(t *testing.T) {
	gs := validGenesis(t)
	gs.PartialDecryptions[0].DleqProof = []byte{0x01}
	err := types.ValidateGenesisState(gs)
	require.Error(t, err)
	require.Contains(t, err.Error(), "partial_decryptions[0].dleq_proof")
}
