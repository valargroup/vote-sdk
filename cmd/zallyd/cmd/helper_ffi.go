package cmd

import "github.com/z-cale/zally/crypto/zkp/halo2"

func helperHalo2Available() bool {
	return true
}

func halo2GenerateShareRevealProof(
	merklePath []byte,
	allEncShares [8][32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
	sharesHash [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	return halo2.GenerateShareRevealProof(
		merklePath, allEncShares, shareIndex,
		proposalID, voteDecision, roundID, sharesHash,
	)
}
