package types_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func TestDeadlineReached(t *testing.T) {
	require.False(t, types.DeadlineReached(100, 0, 1_000))
	require.False(t, types.DeadlineReached(100, 10, 109))
	require.True(t, types.DeadlineReached(100, 10, 110))
	require.True(t, types.DeadlineReached(100, 10, 111))
	require.False(t, types.DeadlineReached(math.MaxUint64-5, 10, 1_000))
	require.False(t, types.DeadlineReached(math.MaxUint64-5, 10, math.MaxUint64))
}
