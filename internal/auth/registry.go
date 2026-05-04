// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

const LocalNamespace = "local"

// Registry resolves an API key to a namespace.
//
// HLD §6: namespace is server-resolved from the API key header, never from
// the request body. HLD §13: registry is config-driven (file or env), not a
// table; rebuilt on process restart.
type Registry interface {
	Resolve(apiKey string) (namespace string, ok bool)
	IsLocalMode() bool
}

type memoryRegistry struct {
	keys map[string]string
}

func NewMemoryRegistry(keys map[string]string) Registry {
	return &memoryRegistry{keys: keys}
}

func (m *memoryRegistry) Resolve(apiKey string) (string, bool) {
	if m.keys == nil {
		return "", false
	}
	ns, ok := m.keys[apiKey]
	return ns, ok
}

func (m *memoryRegistry) IsLocalMode() bool {
	return len(m.keys) == 0
}
