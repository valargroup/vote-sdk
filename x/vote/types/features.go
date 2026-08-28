package types

import "google.golang.org/grpc"

// AtomicVoteBatchesEnabled must remain false in this state-compatible
// groundwork release. Set it to true only when the coordinated activation
// upgrade handler is registered; this lets the implementation merge while old
// and new validators continue accepting the same transaction set.
const AtomicVoteBatchesEnabled = false

// RegisterMsgServerWithAtomicVoteBatchGate omits the batch Msg-service method
// while its type URL is intentionally absent from the interface registry.
// Replace this wrapper with RegisterMsgServer when the feature gate is retired.
func RegisterMsgServerWithAtomicVoteBatchGate(s grpc.ServiceRegistrar, srv MsgServer) {
	registerMsgServerWithAtomicVoteBatchGate(s, srv, AtomicVoteBatchesEnabled)
}

func registerMsgServerWithAtomicVoteBatchGate(s grpc.ServiceRegistrar, srv MsgServer, enabled bool) {
	RegisterMsgServer(atomicVoteBatchServiceRegistrar{
		ServiceRegistrar: s,
		enabled:          enabled,
	}, srv)
}

type atomicVoteBatchServiceRegistrar struct {
	grpc.ServiceRegistrar
	enabled bool
}

func (r atomicVoteBatchServiceRegistrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	if r.enabled {
		r.ServiceRegistrar.RegisterService(desc, impl)
		return
	}

	filtered := *desc
	filtered.Methods = make([]grpc.MethodDesc, 0, len(desc.Methods))
	for _, method := range desc.Methods {
		if method.MethodName != "CastVoteBatch" {
			filtered.Methods = append(filtered.Methods, method)
		}
	}
	r.ServiceRegistrar.RegisterService(&filtered, impl)
}
