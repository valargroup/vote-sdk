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

func TestAtomicVoteBatchRegistrations(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	RegisterInterfaces(registry)
	require.NoError(t, registry.EnsureRegistered(&MsgCastVoteBatch{}))
	require.NoError(t, registry.EnsureRegistered(&MsgDelegateAndCastVoteBatch{}))

	registrar := &recordingServiceRegistrar{}
	RegisterMsgServer(registrar, UnimplementedMsgServer{})
	require.NotNil(t, registrar.desc)
	require.True(t, serviceHasMethod(registrar.desc, "CastVoteBatch"))
	require.True(t, serviceHasMethod(registrar.desc, "DelegateAndCastVoteBatch"))
}

func serviceHasMethod(desc *grpc.ServiceDesc, name string) bool {
	for _, method := range desc.Methods {
		if method.MethodName == name {
			return true
		}
	}
	return false
}
