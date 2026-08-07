//go:build redpallas

package tx1

/*
#cgo LDFLAGS: -L${SRCDIR}/../../circuits/target/release -lshielded_vote_circuits -ldl -lm -lpthread
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include "../../circuits/include/shielded_vote_circuits.h"
*/
import "C"

import (
	"fmt"
	"runtime"
	"unsafe"
)

// ComputeDelegationSighash computes the canonical V6/NU6.3 shielded signature
// digest for the fixed delegation transaction profile.
func ComputeDelegationSighash(effects []byte) ([]byte, error) {
	if err := ValidateEffectsFraming(effects); err != nil {
		return nil, err
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	out := make([]byte, 32)
	rc := C.sv_compute_delegation_sighash(
		(*C.uint8_t)(unsafe.Pointer(&effects[0])),
		C.size_t(len(effects)),
		(*C.uint8_t)(unsafe.Pointer(&out[0])),
		C.size_t(len(out)),
	)
	if rc != 0 {
		message := C.GoString(C.sv_last_error())
		if message == "" {
			message = "no error detail returned"
		}
		return nil, fmt.Errorf("delegation sighash failed with code %d: %s", int32(rc), message)
	}
	return out, nil
}
