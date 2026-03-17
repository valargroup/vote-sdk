//go:build !halo2

package halo2

import "fmt"

// GenerateShareRevealProof is a stub that returns an error when built without
// the "halo2" build tag. Use `go build -tags halo2` for real proof generation.
func GenerateShareRevealProof(
	merklePath []byte,
	shareComms [16][32]byte,
	primaryBlind [32]byte,
	encC1X [32]byte,
	encC2X [32]byte,
	shareIndex uint32,
	proposalID, voteDecision uint32,
	roundID [32]byte,
) (proof []byte, nullifier [32]byte, treeRoot [32]byte, err error) {
	return nil, nullifier, treeRoot, fmt.Errorf("share reveal proof generation requires the 'halo2' build tag")
}
