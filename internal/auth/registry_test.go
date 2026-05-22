// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/config"
	"github.com/one-harsh/calm/internal/secrets"
)

func TestMemoryRegistry_Resolve(t *testing.T) {
	reg := NewMemoryRegistry(
		map[string]string{
			"k1": "production",
			"k2": "production",
			"k3": "staging",
		},
		map[string]int{
			"production": 500,
		},
	)

	cases := []struct {
		key    string
		wantNS string
		wantOK bool
	}{
		{"k1", "production", true},
		{"k2", "production", true},
		{"k3", "staging", true},
		{"unknown", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			ns, ok := reg.Resolve(tc.key)
			if ns != tc.wantNS || ok != tc.wantOK {
				t.Errorf("Resolve(%q) = (%q, %v); want (%q, %v)", tc.key, ns, ok, tc.wantNS, tc.wantOK)
			}
		})
	}
}

func TestMemoryRegistry_RateFor(t *testing.T) {
	reg := NewMemoryRegistry(
		map[string]string{
			"k1": "production",
			"k2": "staging",
		},
		map[string]int{
			"production": 500,
		},
	)

	cases := []struct {
		ns           string
		wantRate     int
		wantOverride bool
	}{
		{"production", 500, true},
		{"staging", 0, false},
		{"unknown", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.ns, func(t *testing.T) {
			rate, has := reg.RateFor(tc.ns)
			if rate != tc.wantRate || has != tc.wantOverride {
				t.Errorf("RateFor(%q) = (%d, %v); want (%d, %v)", tc.ns, rate, has, tc.wantRate, tc.wantOverride)
			}
		})
	}
}

func TestMemoryRegistry_NilMapsAcceptable(t *testing.T) {
	reg := NewMemoryRegistry(nil, nil)

	if ns, ok := reg.Resolve("anything"); ok {
		t.Errorf("Resolve on empty registry returned (%q, true); want (\"\", false)", ns)
	}
	if rate, has := reg.RateFor("anything"); has {
		t.Errorf("RateFor on empty registry returned (%d, true); want (0, false)", rate)
	}
}

func TestBuildRegistry_Happy(t *testing.T) {
	reader := secrets.NewMockSecretReader(t)
	reader.EXPECT().ReadSecret(mock.Anything, secrets.Secret("[text:default-key]")).Return("default-key").Once()
	reader.EXPECT().ReadSecret(mock.Anything, secrets.Secret("[text:tenant-a-key]")).Return("tenant-a-key").Once()

	reg, err := buildRegistry(context.Background(), []config.NamespaceConfig{
		{Name: "default", APIKey: "[text:default-key]", RatePerSecond: 100},
		{Name: "tenant-a", APIKey: "[text:tenant-a-key]"},
	}, reader, logging.Nop())
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}

	if ns, ok := reg.Resolve("default-key"); !ok || ns != "default" {
		t.Errorf("Resolve(default-key) = (%q, %v); want (default, true)", ns, ok)
	}
	if ns, ok := reg.Resolve("tenant-a-key"); !ok || ns != "tenant-a" {
		t.Errorf("Resolve(tenant-a-key) = (%q, %v); want (tenant-a, true)", ns, ok)
	}
	if rate, has := reg.RateFor("default"); !has || rate != 100 {
		t.Errorf("RateFor(default) = (%d, %v); want (100, true)", rate, has)
	}
	if rate, has := reg.RateFor("tenant-a"); has {
		t.Errorf("RateFor(tenant-a) = (%d, %v); want no override", rate, has)
	}
}

// TestBuildRegistry_EmptyResolvedKeyRejected guards against the auth-bypass
// where CALM_DEFAULT_KEY="" resolves to empty (env-var resolver permits it
// by design) and would otherwise authenticate `Authorization: Bearer `.
func TestBuildRegistry_EmptyResolvedKeyRejected(t *testing.T) {
	reader := secrets.NewMockSecretReader(t)
	reader.EXPECT().ReadSecret(mock.Anything, secrets.Secret("[env:CALM_UNSET]")).Return("").Once()

	_, err := buildRegistry(context.Background(), []config.NamespaceConfig{
		{Name: "default", APIKey: "[env:CALM_UNSET]"},
	}, reader, logging.Nop())
	if err == nil {
		t.Fatal("expected error for empty resolved api_key")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q should mention 'empty'", err.Error())
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error %q should name the offending namespace", err.Error())
	}
}

func TestBuildRegistry_DuplicateResolvedKeyRejected(t *testing.T) {
	reader := secrets.NewMockSecretReader(t)
	reader.EXPECT().ReadSecret(mock.Anything, secrets.Secret("[text:shared]")).Return("shared-key").Once()
	reader.EXPECT().ReadSecret(mock.Anything, secrets.Secret("[env:OTHER_REF]")).Return("shared-key").Once()

	_, err := buildRegistry(context.Background(), []config.NamespaceConfig{
		{Name: "tenant-a", APIKey: "[text:shared]"},
		{Name: "tenant-b", APIKey: "[env:OTHER_REF]"},
	}, reader, logging.Nop())
	if err == nil {
		t.Fatal("expected error for two namespaces resolving to same value")
	}
	if !strings.Contains(err.Error(), "same value") {
		t.Errorf("error %q should mention 'same value'", err.Error())
	}
	if !strings.Contains(err.Error(), "tenant-a") || !strings.Contains(err.Error(), "tenant-b") {
		t.Errorf("error %q should name both namespaces", err.Error())
	}
}

func TestBuildRegistry_EmptyNamespaceList(t *testing.T) {
	reader := secrets.NewMockSecretReader(t)
	reg, err := buildRegistry(context.Background(), nil, reader, logging.Nop())
	if err != nil {
		t.Fatalf("empty list should build an empty registry, not error: %v", err)
	}
	if _, ok := reg.Resolve(""); ok {
		t.Error("empty registry should not resolve the empty string")
	}
}
