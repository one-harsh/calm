// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/config"
	"github.com/one-harsh/calm/internal/obs"
	"github.com/one-harsh/calm/internal/secrets"
)

// Registry resolves an API key to a namespace. Namespace is server-resolved
// from the API key header per DL10, never from the request body. Built once
// at startup from operator config; rebuilt on process restart.
type Registry interface {
	Resolve(apiKey string) (namespace string, ok bool)

	// RateFor returns the per-namespace requests-per-second override.
	// Callers fall back to a global default when hasOverride is false.
	RateFor(namespace string) (rate int, hasOverride bool)
}

type memoryRegistry struct {
	keys  map[string]string
	rates map[string]int
}

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

// BuildRegistry resolves each namespace's bracketed api_key reference via
// the SecretReader and returns the in-memory Registry. Fatal on resolution
// failure, an empty resolved key, or two namespaces resolving to the same
// value. buildRegistry is the testable core.
func BuildRegistry(ctx context.Context, namespaces []config.NamespaceConfig, reader secrets.SecretReader, logger *logging.Logger) Registry {
	reg, err := buildRegistry(ctx, namespaces, reader, logger)
	if err != nil {
		logger.WithContext(ctx).Fatal("build registry", logging.ErrorField(err))
	}
	return reg
}

func buildRegistry(ctx context.Context, namespaces []config.NamespaceConfig, reader secrets.SecretReader, logger *logging.Logger) (Registry, error) {
	keys := make(map[string]string, len(namespaces))
	rates := make(map[string]int, len(namespaces))
	keyOrigin := make(map[string]string, len(namespaces))

	for _, ns := range namespaces {
		// Bind namespace before ReadSecret so the SecretReader's Fatal log
		// (on a bad ref) carries which namespace's secret failed.
		nsCtx := logging.Bind(ctx, obs.Namespace(ns.Name))
		apiKey := reader.ReadSecret(nsCtx, ns.APIKey)

		if apiKey == "" {
			// An empty bearer would authenticate `Authorization: Bearer ` —
			// the SecretReader allows empty env-var values by design; the
			// consumer enforces non-empty for credential use.
			return nil, fmt.Errorf("namespace %q: api_key resolved to empty value (secret %q)", ns.Name, ns.APIKey)
		}

		if existing, dup := keys[apiKey]; dup {
			return nil, fmt.Errorf("namespace %q: api_key resolves to the same value as namespace %q (secret %q vs %q)",
				ns.Name, existing, ns.APIKey, keyOrigin[apiKey])
		}

		keys[apiKey] = ns.Name
		keyOrigin[apiKey] = string(ns.APIKey)
		if ns.RatePerSecond > 0 {
			rates[ns.Name] = ns.RatePerSecond
		}
	}

	logger.WithContext(ctx).Info(
		"registry loaded",
		logging.IntField("namespace_count", len(namespaces)),
		logging.IntField("rate_override_count", len(rates)),
	)
	return NewMemoryRegistry(keys, rates), nil
}
