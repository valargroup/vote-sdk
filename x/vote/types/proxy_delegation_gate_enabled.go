//go:build proxyenabled

package types

// ProxyDelegationEnabled is open under the proxyenabled build tag, which only
// tests set (`make test-proxyenabled`). Production builds never set the tag;
// see proxy_delegation_gate.go.
const ProxyDelegationEnabled = true
