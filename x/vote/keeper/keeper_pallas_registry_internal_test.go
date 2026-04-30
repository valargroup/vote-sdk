package keeper

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
)

func pallasRegistryTestValAddr(seed byte) string {
	addr := make([]byte, 20)
	addr[0] = seed
	return sdk.ValAddress(addr).String()
}

type pallasRegistryStakingKeeper struct {
	validators map[string]stakingtypes.Validator
}

func newPallasRegistryStakingKeeper(vals ...stakingtypes.Validator) *pallasRegistryStakingKeeper {
	mk := &pallasRegistryStakingKeeper{validators: make(map[string]stakingtypes.Validator, len(vals))}
	for _, val := range vals {
		mk.validators[val.OperatorAddress] = val
	}
	return mk
}

func (mk *pallasRegistryStakingKeeper) GetValidator(_ context.Context, addr sdk.ValAddress) (stakingtypes.Validator, error) {
	val, ok := mk.validators[addr.String()]
	if !ok {
		return stakingtypes.Validator{}, fmt.Errorf("validator %s not found", addr)
	}
	return val, nil
}

func (mk *pallasRegistryStakingKeeper) GetValidatorByConsAddr(_ context.Context, _ sdk.ConsAddress) (stakingtypes.Validator, error) {
	return stakingtypes.Validator{}, fmt.Errorf("not implemented")
}

func (mk *pallasRegistryStakingKeeper) Jail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

func (mk *pallasRegistryStakingKeeper) Unjail(_ context.Context, _ sdk.ConsAddress) error {
	return nil
}

func TestKeeperGetActiveCeremonyValidator(t *testing.T) {
	activeAddr := pallasRegistryTestValAddr(1)
	jailedAddr := pallasRegistryTestValAddr(2)
	unbondedAddr := pallasRegistryTestValAddr(3)
	missingAddr := pallasRegistryTestValAddr(4)

	activeVal := stakingtypes.Validator{
		OperatorAddress: activeAddr,
		Status:          stakingtypes.Bonded,
	}
	jailedVal := stakingtypes.Validator{
		OperatorAddress: jailedAddr,
		Status:          stakingtypes.Bonded,
		Jailed:          true,
	}
	unbondedVal := stakingtypes.Validator{
		OperatorAddress: unbondedAddr,
		Status:          stakingtypes.Unbonded,
	}

	tests := []struct {
		name        string
		keeper      Keeper
		valoperAddr string
		wantAddr    string
		wantErr     string
	}{
		{
			name:        "nil staking keeper",
			keeper:      Keeper{},
			valoperAddr: activeAddr,
			wantErr:     "staking keeper is not configured",
		},
		{
			name:        "invalid validator address",
			keeper:      Keeper{stakingKeeper: newPallasRegistryStakingKeeper(activeVal)},
			valoperAddr: "not-a-validator-address",
			wantErr:     "decoding bech32 failed",
		},
		{
			name:        "validator not found",
			keeper:      Keeper{stakingKeeper: newPallasRegistryStakingKeeper(activeVal)},
			valoperAddr: missingAddr,
			wantErr:     "not found",
		},
		{
			name:        "non-bonded validator rejected",
			keeper:      Keeper{stakingKeeper: newPallasRegistryStakingKeeper(unbondedVal)},
			valoperAddr: unbondedAddr,
			wantErr:     "is not active",
		},
		{
			name:        "jailed validator rejected",
			keeper:      Keeper{stakingKeeper: newPallasRegistryStakingKeeper(jailedVal)},
			valoperAddr: jailedAddr,
			wantErr:     "is not active",
		},
		{
			name:        "bonded unjailed validator accepted",
			keeper:      Keeper{stakingKeeper: newPallasRegistryStakingKeeper(activeVal)},
			valoperAddr: activeAddr,
			wantAddr:    activeAddr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			val, err := tc.keeper.getActiveCeremonyValidator(context.Background(), tc.valoperAddr)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Truef(t, strings.Contains(err.Error(), tc.wantErr), "error %q should contain %q", err.Error(), tc.wantErr)
				require.Nil(t, val)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, val)
			require.Equal(t, tc.wantAddr, val.OperatorAddress)
		})
	}
}
