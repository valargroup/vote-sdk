package helper

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryAwareCaptureDecision(t *testing.T) {
	t.Run("retriable captures first and final", func(t *testing.T) {
		assert.True(t, retryAwareCaptureDecision(true, 1, 5))
		assert.False(t, retryAwareCaptureDecision(true, 2, 5))
		assert.False(t, retryAwareCaptureDecision(true, 4, 5))
		assert.True(t, retryAwareCaptureDecision(true, 5, 5))
	})

	t.Run("non retriable captures final only", func(t *testing.T) {
		assert.False(t, retryAwareCaptureDecision(false, 1, 5))
		assert.False(t, retryAwareCaptureDecision(false, 4, 5))
		assert.True(t, retryAwareCaptureDecision(false, 5, 5))
	})

	t.Run("unknown attempts captures by default", func(t *testing.T) {
		assert.True(t, retryAwareCaptureDecision(true, 0, 5))
		assert.True(t, retryAwareCaptureDecision(false, 0, 0))
	})
}

func TestFailureMetadataFromError(t *testing.T) {
	wrapped := wrapShareProcessingError(failureStageProofGenerate, false, errors.New("proof failed"))
	meta := failureMetadataFromError(wrapped, failureStageProcessShare, true)
	assert.Equal(t, failureStageProofGenerate, meta.stage)
	assert.False(t, meta.retriable)
	assert.Nil(t, meta.chainCode)

	chainErr := newChainRejectError(42, "rejected")
	meta = failureMetadataFromError(chainErr, failureStageProcessShare, true)
	require.NotNil(t, meta.chainCode)
	assert.Equal(t, uint32(42), *meta.chainCode)
	assert.Equal(t, failureStageSubmitChain, meta.stage)
	assert.False(t, meta.retriable)
}

func TestBuildFailureTagsIncludesChainCode(t *testing.T) {
	code := uint32(9)
	share := QueuedShare{
		Payload: SharePayload{
			VoteRoundID: "round-1",
			EncShare: EncryptedShareWire{
				ShareIndex: 3,
			},
		},
	}
	tags := buildFailureTags(share, failureMetadata{
		stage:     failureStageSubmitChain,
		retriable: false,
		chainCode: &code,
	}, 5, 5)
	assert.Equal(t, "round-1", tags["round_id"])
	assert.Equal(t, "3", tags["share_index"])
	assert.Equal(t, failureStageSubmitChain, tags["stage"])
	assert.Equal(t, "false", tags["retriable"])
	assert.Equal(t, "5", tags["attempt"])
	assert.Equal(t, "5", tags["max_attempts"])
	assert.Equal(t, "9", tags["chain_code"])
}
