// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoad_HappyPath(t *testing.T) {
	path := writeConfig(t, `
service:
  service_name: calm
  log_level: debug
server:
  address: ":9090"
  request_timeout: 5s
storage:
  dsn: postgres://localhost/calm
  migrate_on_startup: false
namespaces:
  - name: default
    api_key: "[text:dev-key-1234]"
    rate_per_second: 50
  - name: tenant-a
    api_key: "[text:tenant-a-key]"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Service.ServiceName != "calm" || cfg.Service.LogLevel != "debug" {
		t.Errorf("service fields: %+v", cfg.Service)
	}
	if cfg.Server.Address != ":9090" || cfg.Server.RequestTimeout != 5*time.Second {
		t.Errorf("server fields: %+v", cfg.Server)
	}
	if cfg.Storage.MigrateOnStartup {
		t.Errorf("migrate_on_startup should be false; got true")
	}
	if len(cfg.Namespaces) != 2 {
		t.Fatalf("namespace count: want 2, got %d", len(cfg.Namespaces))
	}
	if cfg.Namespaces[0].Name != "default" || string(cfg.Namespaces[0].APIKey) != "[text:dev-key-1234]" {
		t.Errorf("namespaces[0]: %+v", cfg.Namespaces[0])
	}
	if cfg.Namespaces[0].RatePerSecond != 50 {
		t.Errorf("namespaces[0].rate_per_second: want 50, got %d", cfg.Namespaces[0].RatePerSecond)
	}
	if cfg.Namespaces[1].RatePerSecond != 0 {
		t.Errorf("namespaces[1].rate_per_second: want 0 (fall back), got %d", cfg.Namespaces[1].RatePerSecond)
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	path := writeConfig(t, `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Service.ServiceName != "calm" || cfg.Service.LogFormat != "json" {
		t.Errorf("defaults not applied to service: %+v", cfg.Service)
	}
	if cfg.Server.Address != ":8080" || cfg.Server.MaxIngestPayloadKB != 1024 {
		t.Errorf("defaults not applied to server: %+v", cfg.Server)
	}
	if cfg.Sessions.DefaultTTLMinutes != 120 {
		t.Errorf("defaults not applied to sessions: %+v", cfg.Sessions)
	}
	if cfg.Sessions.CacheSize != 10_000 {
		t.Errorf("sessions.cache_size default: want 10000, got %d", cfg.Sessions.CacheSize)
	}
	if !cfg.Storage.MigrateOnStartup {
		t.Errorf("storage.migrate_on_startup default should be true")
	}
}

func TestLoad_SessionsCacheSizeOverride(t *testing.T) {
	path := writeConfig(t, `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	t.Setenv("CALM_SESSIONS_CACHE_SIZE", "500")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sessions.CacheSize != 500 {
		t.Errorf("env override: want 500, got %d", cfg.Sessions.CacheSize)
	}
}

func TestLoad_SessionsCacheSizeNegativeRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  cache_size: -1
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with negative cache_size: want error, got nil")
	}
	if !strings.Contains(err.Error(), "sessions.cache_size") {
		t.Errorf("error should mention sessions.cache_size; got %v", err)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	path := writeConfig(t, `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	t.Setenv("CALM_SERVER_ADDRESS", ":7777")
	t.Setenv("CALM_SERVICE_LOG_LEVEL", "warn")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Address != ":7777" {
		t.Errorf("env override server.address: want :7777, got %q", cfg.Server.Address)
	}
	if cfg.Service.LogLevel != "warn" {
		t.Errorf("env override service.log_level: want warn, got %q", cfg.Service.LogLevel)
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	_, err := Load("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q should mention 'required'", err.Error())
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/dir/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	cases := []struct {
		name          string
		yaml          string
		wantErrSubstr string
	}{
		{
			name: "empty_namespaces",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces: []
`,
			wantErrSubstr: "at least one namespace is required",
		},
		{
			name: "no_namespaces_field",
			yaml: `
storage:
  dsn: postgres://localhost/calm
`,
			wantErrSubstr: "at least one namespace is required",
		},
		{
			name: "missing_dsn",
			yaml: `
namespaces:
  - name: default
    api_key: "[text:x]"
`,
			wantErrSubstr: "storage.dsn is required",
		},
		{
			name: "invalid_namespace_name",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: "has spaces"
    api_key: "[text:x]"
`,
			wantErrSubstr: "invalid name",
		},
		{
			name: "duplicate_namespace_name",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
  - name: default
    api_key: "[text:y]"
`,
			wantErrSubstr: "duplicate name",
		},
		{
			name: "missing_api_key",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
`,
			wantErrSubstr: "api_key is required",
		},
		{
			name: "non_bracketed_api_key",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "raw-key-value"
`,
			wantErrSubstr: "bracketed secret reference",
		},
		{
			name: "duplicate_raw_secret_ref",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:shared]"
  - name: tenant-a
    api_key: "[text:shared]"
`,
			wantErrSubstr: "duplicate api_key reference",
		},
		{
			name: "rate_too_low",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
    rate_per_second: -1
`,
			wantErrSubstr: "rate_per_second must be >= 0",
		},
		{
			name: "rate_too_high",
			yaml: `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
    rate_per_second: 200000
`,
			wantErrSubstr: "above maximum",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.yaml)
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Errorf("error %q missing substring %q", err.Error(), tc.wantErrSubstr)
			}
		})
	}
}
