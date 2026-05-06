package keeper

import (
	"fmt"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func coordinatorActionRequired(action string) error {
	return fmt.Errorf("%w: %s requires coordinator action approval", types.ErrCoordinatorActionRequired, action)
}
