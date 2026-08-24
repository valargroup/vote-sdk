package app

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestShouldCaptureVoteAnteError(t *testing.T) {
	checkTxCtx := sdk.Context{}.WithIsCheckTx(true)
	recheckTxCtx := sdk.Context{}.WithIsReCheckTx(true)
	finalizeBlockCtx := sdk.Context{}
	committedAt100 := func() int64 { return 100 }
	uninitializedHeight := func() int64 { return -1 }

	futureRoot := &types.CommitmentRootUnavailableError{AnchorHeight: 101}
	committedRootMissing := &types.CommitmentRootUnavailableError{AnchorHeight: 100}
	proofFailure := fmt.Errorf("%w: vote share: verifier failed", types.ErrInvalidProof)
	signatureFailure := fmt.Errorf("%w: verification failed", types.ErrInvalidSignature)

	tests := []struct {
		name            string
		ctx             sdk.Context
		err             error
		committedHeight func() int64
		want            bool
	}{
		{
			name:            "future root during CheckTx is not captured",
			ctx:             checkTxCtx,
			err:             futureRoot,
			committedHeight: committedAt100,
		},
		{
			name:            "future root during ReCheckTx is not captured",
			ctx:             recheckTxCtx,
			err:             futureRoot,
			committedHeight: committedAt100,
		},
		{
			name:            "missing committed root during CheckTx is captured",
			ctx:             checkTxCtx,
			err:             committedRootMissing,
			committedHeight: committedAt100,
			want:            true,
		},
		{
			name:            "future root during block execution is captured",
			ctx:             finalizeBlockCtx,
			err:             futureRoot,
			committedHeight: committedAt100,
			want:            true,
		},
		{
			name:            "proof verifier failure during CheckTx is captured",
			ctx:             checkTxCtx,
			err:             proofFailure,
			committedHeight: committedAt100,
			want:            true,
		},
		{
			name:            "signature failure during CheckTx is captured",
			ctx:             checkTxCtx,
			err:             signatureFailure,
			committedHeight: committedAt100,
			want:            true,
		},
		{
			name:            "missing height provider preserves capture behavior",
			ctx:             checkTxCtx,
			err:             futureRoot,
			committedHeight: nil,
			want:            true,
		},
		{
			name:            "uninitialized committed height preserves capture behavior",
			ctx:             checkTxCtx,
			err:             futureRoot,
			committedHeight: uninitializedHeight,
			want:            true,
		},
		{
			name:            "unrelated error is not captured",
			ctx:             checkTxCtx,
			err:             fmt.Errorf("round unavailable"),
			committedHeight: committedAt100,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldCaptureVoteAnteError(tc.ctx, tc.err, tc.committedHeight))
		})
	}
}
