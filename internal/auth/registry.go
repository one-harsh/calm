// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

// Registry resolves an API key to a namespace and exposes per-namespace
// rate-limit overrides.
//
// HLD §6: namespace is server-resolved from the API key header, never from
// the request body. HLD §13: registry is config-driven (file), rebuilt on
// process restart.
type Registry interface {
	// Resolve maps an API key to a namespace. Returns ("", false) when the
	// key is unknown.
	Resolve(apiKey string) (namespace string, ok bool)

	// RateFor returns the per-namespace requests-per-second override.
	// The hasOverride flag is true when an explicit rate is set for the
	// namespace; callers fall back to a global default when it is false.
	RateFor(namespace string) (rate int, hasOverride bool)
}

type memoryRegistry struct {
	keys  map[string]string
	rates map[string]int
}

// NewMemoryRegistry constructs an in-memory Registry from the supplied maps.
// Both maps may be nil (yielding an empty registry). The loader at
// LoadRegistry is the primary constructor; this entry point is retained for
// tests and harness setup.
func NewMemoryRegistry(keys map[string]string, rates map[string]int) Registry {
	if keys == nil {
		keys = map[string]string{}
	}
	if rates == nil {
		rates = map[string]int{}
	}
	return &memoryRegistry{keys: keys, rates: rates}
}

func (m *memoryRegistry) Resolve(apiKey string) (string, bool) {
	ns, ok := m.keys[apiKey]
	return ns, ok
}

func (m *memoryRegistry) RateFor(namespace string) (int, bool) {
	rate, ok := m.rates[namespace]
	return rate, ok
}
