//go:build halo2

package cmd

import "github.com/z-cale/zally/crypto/zkp/halo2"

func helperHalo2Available() bool {
	return true
}

func halo2GenerateShareRevealProof(
	merklePath []byte,
	shareComms [16][32]byte,
	primaryBlind [32]byte,
	encC1X [32]byte,
	encC2X [32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	return halo2.GenerateShareRevealProof(
		merklePath, shareComms, primaryBlind, encC1X, encC2X,
		shareIndex, proposalID, voteDecision, roundID,
	)
}
