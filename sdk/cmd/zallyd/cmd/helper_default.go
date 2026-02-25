//go:build !halo2

package cmd

import "fmt"

func helperHalo2Available() bool {
	return false
}

func halo2GenerateShareRevealProof(
	merklePath []byte,
	allEncShares [32][32]byte,
	shareBlinds [16][32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
	sharesHash [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	return nil, nullifier, treeRoot, fmt.Errorf("share reveal proof generation requires the 'halo2' build tag")
}
