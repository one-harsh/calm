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
observability:
  logging:
    level: debug
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
	if cfg.Service.ServiceName != "calm" {
		t.Errorf("service fields: %+v", cfg.Service)
	}
	if cfg.Observability.Logging.Level != "debug" {
		t.Errorf("observability.logging.level: want debug, got %q", cfg.Observability.Logging.Level)
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
	if cfg.Service.ServiceName != "calm" {
		t.Errorf("defaults not applied to service: %+v", cfg.Service)
	}
	if cfg.Observability.Logging.Format != "json" {
		t.Errorf("defaults not applied to observability.logging.format: got %q, want json", cfg.Observability.Logging.Format)
	}
	if cfg.Server.Address != ":8080" || cfg.Server.MaxIngestPayloadKB != 1024 {
		t.Errorf("defaults not applied to server: %+v", cfg.Server)
	}
	if cfg.Sessions.DefaultTTLMinutes != 120 {
		t.Errorf("sessions.default_ttl_minutes default: want 120, got %d", cfg.Sessions.DefaultTTLMinutes)
	}
	if cfg.Sessions.CacheSize != 10_000 {
		t.Errorf("sessions.cache_size default: want 10000, got %d", cfg.Sessions.CacheSize)
	}
	if !cfg.Storage.MigrateOnStartup {
		t.Errorf("storage.migrate_on_startup default should be true")
	}
	if cfg.Storage.MaxOpenConns != 25 || cfg.Storage.MaxIdleConns != 25 {
		t.Errorf("storage pool defaults: want 25/25, got %d/%d", cfg.Storage.MaxOpenConns, cfg.Storage.MaxIdleConns)
	}
	if cfg.Storage.ConnMaxLifetime != 30*time.Minute {
		t.Errorf("storage.conn_max_lifetime default: want 30m, got %v", cfg.Storage.ConnMaxLifetime)
	}
}

func TestLoad_StorageIdleAboveOpenRejected(t *testing.T) {
	path := writeConfig(t, `
storage:
  dsn: postgres://localhost/calm
  max_open_conns: 10
  max_idle_conns: 20
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with max_idle_conns > max_open_conns: want error, got nil")
	}
	if !strings.Contains(err.Error(), "storage.max_idle_conns") {
		t.Errorf("error should mention storage.max_idle_conns; got %v", err)
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

func TestLoad_DefaultTTLMinutesZeroRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  default_ttl_minutes: 0
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "sessions.default_ttl_minutes") {
		t.Errorf("want error mentioning sessions.default_ttl_minutes; got %v", err)
	}
}

func TestLoad_DefaultTTLMinutesNegativeRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  default_ttl_minutes: -5
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "sessions.default_ttl_minutes") {
		t.Errorf("want error mentioning sessions.default_ttl_minutes; got %v", err)
	}
}

func TestLoad_DefaultTTLMinutesAboveOpenAPICapRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  default_ttl_minutes: 20000
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "sessions.default_ttl_minutes") {
		t.Errorf("want error mentioning sessions.default_ttl_minutes for value above OpenAPI cap; got %v", err)
	}
}

func TestLoad_DefaultTTLMinutesAtOpenAPICapAccepted(t *testing.T) {
	path := writeConfig(t, `
sessions:
  default_ttl_minutes: 10080
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load at OpenAPI cap: want pass, got %v", err)
	}
	if cfg.Sessions.DefaultTTLMinutes != 10_080 {
		t.Errorf("got %d; want 10080", cfg.Sessions.DefaultTTLMinutes)
	}
}

func TestLoad_MaxTTLMinutesDefault(t *testing.T) {
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
	if cfg.Sessions.MaxTTLMinutes != 10_080 {
		t.Errorf("sessions.max_ttl_minutes default: want 10080, got %d", cfg.Sessions.MaxTTLMinutes)
	}
}

func TestLoad_MaxTTLMinutesOverride(t *testing.T) {
	path := writeConfig(t, `
sessions:
  max_ttl_minutes: 240
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	t.Setenv("CALM_SESSIONS_DEFAULT_TTL_MINUTES", "120")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sessions.MaxTTLMinutes != 240 {
		t.Errorf("got %d; want 240", cfg.Sessions.MaxTTLMinutes)
	}
}

func TestLoad_MaxTTLMinutesZeroRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  max_ttl_minutes: 0
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "sessions.max_ttl_minutes") {
		t.Errorf("want error mentioning sessions.max_ttl_minutes; got %v", err)
	}
}

func TestLoad_MaxTTLMinutesAboveOpenAPICapRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  max_ttl_minutes: 20000
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "sessions.max_ttl_minutes") {
		t.Errorf("want error mentioning sessions.max_ttl_minutes for value above OpenAPI cap; got %v", err)
	}
}

func TestLoad_DefaultTTLMinutesAboveMaxRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  default_ttl_minutes: 5000
  max_ttl_minutes: 240
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "must be <= sessions.max_ttl_minutes") {
		t.Errorf("want error about inactivity > max; got %v", err)
	}
}

func TestLoad_TTLScannerJitterDefault(t *testing.T) {
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
	if cfg.Sessions.TTLScannerJitterMS != 10_000 {
		t.Errorf("ttl_scanner_jitter_ms default: want 10000, got %d", cfg.Sessions.TTLScannerJitterMS)
	}
}

func TestLoad_TTLScannerJitterOverride(t *testing.T) {
	path := writeConfig(t, `
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	t.Setenv("CALM_SESSIONS_TTL_SCANNER_JITTER_MS", "2000")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Sessions.TTLScannerJitterMS != 2000 {
		t.Errorf("env override: want 2000, got %d", cfg.Sessions.TTLScannerJitterMS)
	}
}

func TestLoad_TTLScannerJitterNegativeRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  ttl_scanner_jitter_ms: -1
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "ttl_scanner_jitter_ms") {
		t.Errorf("want negative-jitter error mentioning ttl_scanner_jitter_ms; got %v", err)
	}
}

func TestLoad_TTLScannerIntervalNegativeRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  ttl_scanner_interval_ms: -1
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "ttl_scanner_interval_ms") {
		t.Errorf("want negative-interval error mentioning ttl_scanner_interval_ms; got %v", err)
	}
}

func TestLoad_TTLScannerJitterEqualToIntervalRejected(t *testing.T) {
	path := writeConfig(t, `
sessions:
  ttl_scanner_interval_ms: 5000
  ttl_scanner_jitter_ms: 5000
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "must be <") {
		t.Errorf("want jitter>=interval error; got %v", err)
	}
}

func TestLoad_TTLScannerIntervalZeroSkipsJitterValidation(t *testing.T) {
	// Interval=0 disables the scanner; jitter is irrelevant in that case
	// and must not cause validation to fail.
	path := writeConfig(t, `
sessions:
  ttl_scanner_interval_ms: 0
  ttl_scanner_jitter_ms: 999999
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with interval=0 + large jitter: want pass, got %v", err)
	}
	if cfg.Sessions.TTLScannerIntervalMS != 0 {
		t.Errorf("interval: want 0, got %d", cfg.Sessions.TTLScannerIntervalMS)
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
	t.Setenv("CALM_OBSERVABILITY_LOGGING_LEVEL", "warn")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Address != ":7777" {
		t.Errorf("env override server.address: want :7777, got %q", cfg.Server.Address)
	}
	if cfg.Observability.Logging.Level != "warn" {
		t.Errorf("env override observability.logging.level: want warn, got %q", cfg.Observability.Logging.Level)
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

func TestLoad_TLSEnabledRequiresCertAndKey(t *testing.T) {
	path := writeConfig(t, `
server:
  tls:
    enabled: true
    cert_file: "[file:/etc/calm/tls.crt]"
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("tls.enabled with missing key_file: want error, got nil")
	}
	if !strings.Contains(err.Error(), "server.tls") {
		t.Errorf("error should mention server.tls; got %v", err)
	}
}

func TestLoad_TLSEnabledRawPathRejected(t *testing.T) {
	path := writeConfig(t, `
server:
  tls:
    enabled: true
    cert_file: "/etc/calm/tls.crt"
    key_file: "/etc/calm/tls.key"
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("tls with raw (unbracketed) cert path: want error, got nil")
	}
	if !strings.Contains(err.Error(), "bracketed secret reference") {
		t.Errorf("error should mention bracketed secret reference; got %v", err)
	}
}

func TestLoad_TLSEnabledWithCertAndKeyAccepted(t *testing.T) {
	path := writeConfig(t, `
server:
  tls:
    enabled: true
    cert_file: "[file:/etc/calm/tls.crt]"
    key_file: "[file:/etc/calm/tls.key]"
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with valid tls config: %v", err)
	}
	if !cfg.Server.TLS.Enabled {
		t.Error("server.tls.enabled: want true")
	}
	if string(cfg.Server.TLS.CertFile) != "[file:/etc/calm/tls.crt]" || string(cfg.Server.TLS.KeyFile) != "[file:/etc/calm/tls.key]" {
		t.Errorf("tls cert/key not bound: %+v", cfg.Server.TLS)
	}
}

func TestLoad_TLSDisabledIgnoresCertKey(t *testing.T) {
	path := writeConfig(t, `
server:
  tls:
    enabled: false
storage:
  dsn: postgres://localhost/calm
namespaces:
  - name: default
    api_key: "[text:x]"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load with tls disabled: %v", err)
	}
	if cfg.Server.TLS.Enabled {
		t.Error("server.tls.enabled: want false")
	}
}
