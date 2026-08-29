package types

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
)

type recordingServiceRegistrar struct {
	desc *grpc.ServiceDesc
}

func (r *recordingServiceRegistrar) RegisterService(desc *grpc.ServiceDesc, _ any) {
	r.desc = desc
}

func TestAtomicVoteBatchFeatureRegistrations(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
	}{
		{name: "disabled", enabled: false},
		{name: "enabled", enabled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			registry := codectypes.NewInterfaceRegistry()
			registerInterfaces(registry, tc.enabled)
			registrationErr := registry.EnsureRegistered(&MsgCastVoteBatch{})
			if tc.enabled {
				require.NoError(t, registrationErr)
			} else {
				require.Error(t, registrationErr)
			}

			registrar := &recordingServiceRegistrar{}
			registerMsgServerWithAtomicVoteBatchGate(registrar, UnimplementedMsgServer{}, tc.enabled)
			require.NotNil(t, registrar.desc)
			require.Equal(t, tc.enabled, serviceHasMethod(registrar.desc, "CastVoteBatch"))
		})
	}

	// Feature filtering must never mutate the generated descriptor.
	require.True(t, serviceHasMethod(&Msg_ServiceDesc, "CastVoteBatch"))
}

func serviceHasMethod(desc *grpc.ServiceDesc, name string) bool {
	for _, method := range desc.Methods {
		if method.MethodName == name {
			return true
		}
	}
	return false
}
