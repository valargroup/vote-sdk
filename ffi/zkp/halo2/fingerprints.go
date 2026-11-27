//go:build halo2

package halo2

/*
#cgo LDFLAGS: -L${SRCDIR}/../../../circuits/target/release -lshielded_vote_circuits -ldl -lm -lpthread
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include "../../../circuits/include/shielded_vote_circuits.h"
*/
import "C"

import (
	"bytes"
	"fmt"
	"unsafe"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

// circuitNames names the circuits in sv_circuit_fingerprints order, as
// capability field 5 names them (contracts-v1 §8).
var circuitNames = [...]string{"zkp1", "zkp2", "zkp3", "zkp4"}

// CircuitFingerprints returns the verifying-key fingerprints of ZKP #1 to #4,
// named zkp1 to zkp4, for QueryProtocolCapabilitiesResponse field 5
// (contracts-v1 §8). Each is BLAKE2b-256 of the circuit's pinned verifying key
// (§10). The first call generates any keys not yet cached.
func CircuitFingerprints() ([]*types.CircuitFingerprint, error) {
	var buf [len(circuitNames) * 32]byte
	rc := C.sv_circuit_fingerprints((*C.uint8_t)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	if rc != 0 {
		return nil, fmt.Errorf("halo2: circuit fingerprints failed with code %d: %s", rc, rustLastError())
	}

	fingerprints := make([]*types.CircuitFingerprint, len(circuitNames))
	for i, name := range circuitNames {
		fingerprints[i] = &types.CircuitFingerprint{Name: name, VkBlake2B: bytes.Clone(buf[i*32 : (i+1)*32])}
	}
	return fingerprints, nil
}
