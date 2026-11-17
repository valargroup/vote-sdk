package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/valargroup/vote-sdk/ffi/zkp"
	"github.com/valargroup/vote-sdk/ffi/zkp/halo2"
	"github.com/valargroup/vote-sdk/internal/delegationreplay"
)

// ConfigureDelegationCapture is called once by start's PostSetup. Its returned
// worker belongs to the server errgroup and releases the storage lock on exit.
// An empty directory disables capture without opening files or hashing a binary.
func (app *SvoteApp) ConfigureDelegationCapture(dir string) (func(context.Context) error, error) {
	if dir == "" {
		return nil, nil
	}
	if halo2.IsMock {
		return nil, errors.New("delegation capture requires a real Halo2 build")
	}
	identity, err := delegationreplay.ExecutableIdentity()
	if err != nil {
		return nil, err
	}
	r, err := delegationreplay.New(dir, identity, app.Logger())
	if err != nil {
		return nil, err
	}
	app.delegationCapture.Store(r)
	return r.Run, nil
}

func (app *SvoteApp) captureDelegationFailure(ctx context.Context, proof []byte, inputs zkp.DelegationInputs, failure error) {
	r := app.delegationCapture.Load()
	if r == nil {
		return
	}
	c, ok := ctx.(sdk.Context)
	if !ok {
		return
	}
	phase := "finalize_block"
	if c.IsCheckTx() {
		phase = "check_tx"
	}
	txHash := ""
	if tx := c.TxBytes(); len(tx) > 0 {
		h := sha256.Sum256(tx)
		txHash = hex.EncodeToString(h[:])
	}
	r.TryCapture(proof, inputs, delegationreplay.Metadata{ChainID: c.ChainID(), Height: c.BlockHeight(), Phase: phase, TxHash: txHash}, failure)
}
