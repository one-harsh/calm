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

// Registry resolves an API key to a namespace (DL10).
type Registry interface {
	Resolve(apiKey string) (namespace string, ok bool)

	// RateFor returns the per-namespace override; hasOverride=false → caller uses global default.
	RateFor(namespace string) (rate int, hasOverride bool)

	RequiresClientCredentials(namespace string) bool

	// FeedbackTTLFor returns the per-namespace feedback-acceptance window override;
	// hasOverride=false → caller uses the system default.
	FeedbackTTLFor(namespace string) (minutes int, hasOverride bool)

	// DefaultAllocatorFor returns the per-namespace default search allocator
	// (raw string; auth stays search-agnostic); hasOverride=false → caller uses
	// the system default variant.
	DefaultAllocatorFor(namespace string) (allocator string, hasOverride bool)

	// AllowAllocatorOverride reports whether the namespace honors a per-request
	// X-CALM-Allocator-Variant header.
	AllowAllocatorOverride(namespace string) bool
}

type memoryRegistry struct {
	keys                map[string]string
	rates               map[string]int
	credentialsRequired map[string]bool
	feedbackTTLs        map[string]int
	defaultAllocators   map[string]string
	allocatorOverride   map[string]bool
}

func NewMemoryRegistry(
	keys map[string]string,
	rates map[string]int,
	credentialsRequired map[string]bool,
	feedbackTTLs map[string]int,
	defaultAllocators map[string]string,
	allocatorOverride map[string]bool,
) Registry {
	if keys == nil {
		keys = map[string]string{}
	}
	if rates == nil {
		rates = map[string]int{}
	}
	if credentialsRequired == nil {
		credentialsRequired = map[string]bool{}
	}
	if feedbackTTLs == nil {
		feedbackTTLs = map[string]int{}
	}
	if defaultAllocators == nil {
		defaultAllocators = map[string]string{}
	}
	if allocatorOverride == nil {
		allocatorOverride = map[string]bool{}
	}
	return &memoryRegistry{
		keys:                keys,
		rates:               rates,
		credentialsRequired: credentialsRequired,
		feedbackTTLs:        feedbackTTLs,
		defaultAllocators:   defaultAllocators,
		allocatorOverride:   allocatorOverride,
	}
}

func (m *memoryRegistry) Resolve(apiKey string) (string, bool) {
	ns, ok := m.keys[apiKey]
	return ns, ok
}

func (m *memoryRegistry) RateFor(namespace string) (int, bool) {
	rate, ok := m.rates[namespace]
	return rate, ok
}

func (m *memoryRegistry) RequiresClientCredentials(namespace string) bool {
	return m.credentialsRequired[namespace]
}

func (m *memoryRegistry) FeedbackTTLFor(namespace string) (int, bool) {
	minutes, ok := m.feedbackTTLs[namespace]
	return minutes, ok
}

func (m *memoryRegistry) DefaultAllocatorFor(namespace string) (string, bool) {
	allocator, ok := m.defaultAllocators[namespace]
	return allocator, ok
}

func (m *memoryRegistry) AllowAllocatorOverride(namespace string) bool {
	return m.allocatorOverride[namespace]
}

// BuildRegistry is Fatal on resolution failure, empty resolved key, or two namespaces resolving to the same value.
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
	credentialsRequired := make(map[string]bool, len(namespaces))
	feedbackTTLs := make(map[string]int, len(namespaces))
	defaultAllocators := make(map[string]string, len(namespaces))
	allocatorOverride := make(map[string]bool, len(namespaces))
	keyOrigin := make(map[string]string, len(namespaces))

	for _, ns := range namespaces {
		// Bind namespace before ReadSecret so any Fatal log names the failing namespace.
		nsCtx := logging.Bind(ctx, obs.Namespace(ns.Name))
		apiKey := reader.ReadSecret(nsCtx, ns.APIKey)

		if apiKey == "" {
			// Empty value would authenticate empty X-CALM-API-Key headers.
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
		if ns.RequireClientCredentials {
			credentialsRequired[ns.Name] = true
		}
		if ns.FeedbackTTLMinutes > 0 {
			feedbackTTLs[ns.Name] = ns.FeedbackTTLMinutes
		}
		if ns.Search.DefaultAllocator != "" {
			defaultAllocators[ns.Name] = ns.Search.DefaultAllocator
		}
		if ns.Search.AllowAllocatorOverride {
			allocatorOverride[ns.Name] = true
		}
	}

	logger.WithContext(ctx).Info(
		"registry loaded",
		logging.IntField("namespaces.loaded", len(namespaces)),
		logging.IntField("namespaces.with_rate_override", len(rates)),
		logging.IntField("namespaces.credentialed", len(credentialsRequired)),
		logging.IntField("namespaces.with_feedback_ttl_override", len(feedbackTTLs)),
		logging.IntField("namespaces.with_allocator_override", len(allocatorOverride)),
	)
	return NewMemoryRegistry(keys, rates, credentialsRequired, feedbackTTLs, defaultAllocators, allocatorOverride), nil
}
