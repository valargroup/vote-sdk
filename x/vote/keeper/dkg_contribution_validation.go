package keeper

import (
	"fmt"

	"github.com/mikelodder7/curvey"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/crypto/shamir"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

const dkgShareCiphertextLen = elgamal.CompressedPointSize + 16

// ValidateDKGContributionShapeAndPoK checks the parts of MsgContributeDKG that
// are independent of block-proposer identity and duplicate-contribution state.
func ValidateDKGContributionShapeAndPoK(round *types.VoteRound, msg *types.MsgContributeDKG) (int, error) {
	if round == nil {
		return 0, fmt.Errorf("%w: round must not be nil", types.ErrInvalidField)
	}
	if msg == nil {
		return 0, fmt.Errorf("%w: message must not be nil", types.ErrInvalidField)
	}

	nValidators := len(round.CeremonyValidators)
	if nValidators == 0 {
		return 0, fmt.Errorf("%w: no validators in round ceremony", types.ErrCeremonyWrongStatus)
	}

	expectedThreshold, err := ThresholdForN(nValidators)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", types.ErrInvalidThreshold, err)
	}
	if len(msg.FeldmanCommitments) != expectedThreshold {
		return expectedThreshold, fmt.Errorf("%w: expected %d Feldman commitments, got %d",
			types.ErrInvalidThreshold, expectedThreshold, len(msg.FeldmanCommitments))
	}

	commitments := make([]curvey.Point, len(msg.FeldmanCommitments))
	for i, c := range msg.FeldmanCommitments {
		pk, err := elgamal.UnmarshalPublicKey(c)
		if err != nil {
			return expectedThreshold, fmt.Errorf("%w: feldman_commitment[%d]: %v",
				types.ErrInvalidPallasPoint, i, err)
		}
		commitments[i] = pk.Point
	}

	expectedPayloads := nValidators - 1
	if len(msg.Payloads) != expectedPayloads {
		return expectedThreshold, fmt.Errorf("%w: got %d payloads, expected %d (all validators except contributor)",
			types.ErrPayloadMismatch, len(msg.Payloads), expectedPayloads)
	}

	covered := make(map[string]bool, expectedPayloads)
	for _, p := range msg.Payloads {
		if p.ValidatorAddress == msg.Creator {
			return expectedThreshold, fmt.Errorf("%w: payload must not include contributor's own address %s",
				types.ErrPayloadMismatch, msg.Creator)
		}
		if _, found := FindValidatorInRoundCeremony(round, p.ValidatorAddress); !found {
			return expectedThreshold, fmt.Errorf("%w: payload references unknown validator %s",
				types.ErrNotRegisteredValidator, p.ValidatorAddress)
		}
		if covered[p.ValidatorAddress] {
			return expectedThreshold, fmt.Errorf("%w: duplicate payload for validator %s",
				types.ErrPayloadMismatch, p.ValidatorAddress)
		}
		covered[p.ValidatorAddress] = true

		if _, err := elgamal.UnmarshalPublicKey(p.EphemeralPk); err != nil {
			return expectedThreshold, fmt.Errorf("%w: ephemeral_pk for %s: %v",
				types.ErrInvalidPallasPoint, p.ValidatorAddress, err)
		}
		if len(p.Ciphertext) != dkgShareCiphertextLen {
			return expectedThreshold, fmt.Errorf("%w: ciphertext for %s must be %d bytes, got %d",
				types.ErrPayloadMismatch, p.ValidatorAddress, dkgShareCiphertextLen, len(p.Ciphertext))
		}
	}

	if err := shamir.VerifyFeldmanOpeningProof(
		elgamal.PallasGenerator(),
		commitments,
		msg.VoteRoundId,
		msg.Creator,
		msg.FeldmanOpeningProof,
	); err != nil {
		return expectedThreshold, fmt.Errorf("%w: feldman_opening_proof: %v", types.ErrInvalidField, err)
	}

	return expectedThreshold, nil
}
