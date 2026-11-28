//go:build proxyenabled

package types

// ProxyDelegationEnabled is open under the proxyenabled build tag, which only
// tests, the proxy e2e and isolated localnets set (`make test-proxyenabled`,
// `make test-proxy-e2e`, `PROXY=1`). Production builds never set the tag; see
// proxy_delegation_gate.go.
const ProxyDelegationEnabled = true
