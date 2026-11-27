//go:build !proxyenabled

package types

// ProxyDelegationEnabled gates every proxy delegation path until the v1.7.0
// activation commit sets it (docs/proxy-delegation/contracts-v1.md §9): the
// 0x09 and 0x0B tags and Msg handlers, the registry queries and capability
// fields, and the round flag. Code reads this constant directly. The
// test-only proxyenabled build tag opens it (proxy_delegation_gate_enabled.go),
// and CI runs the Go suites both ways.
const ProxyDelegationEnabled = false
