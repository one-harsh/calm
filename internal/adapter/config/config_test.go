// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/config"
)

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "adapter.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return p
}

func TestLoad_DefaultsWhenNoFile(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Calm.URL != "http://localhost:8080" || cfg.Calm.Client != "calm-adapter" || cfg.Calm.SessionTTLMinutes != 120 {
		t.Errorf("defaults = %+v", cfg.Calm)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "json" {
		t.Errorf("log defaults = %+v", cfg.Log)
	}
}

func TestLoad_FileOverrides(t *testing.T) {
	p := writeYAML(t, `
calm:
  url: http://calm:9090
  api_key: "[env:CALM_DEFAULT_KEY]"
  client: claude-code
  session_ttl_minutes: 30
log:
  level: debug
`)
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Calm.URL != "http://calm:9090" || cfg.Calm.Client != "claude-code" || cfg.Calm.SessionTTLMinutes != 30 {
		t.Errorf("calm = %+v", cfg.Calm)
	}
	// api_key is left as a raw reference for main to resolve.
	if cfg.Calm.APIKey != "[env:CALM_DEFAULT_KEY]" {
		t.Errorf("api_key = %q; want the raw [env:…] reference", cfg.Calm.APIKey)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("log.level = %q; want debug", cfg.Log.Level)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("CALM_ADAPTER_CALM_URL", "http://env-override:1234")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Calm.URL != "http://env-override:1234" {
		t.Errorf("calm.url = %q; want env override", cfg.Calm.URL)
	}
}

func TestLoad_UnknownKeyRejected(t *testing.T) {
	p := writeYAML(t, "bogus_key: 1\n")
	if _, err := config.Load(p); err == nil {
		t.Fatal("Load: want error for unknown key, got nil")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	urlEmpty := writeYAML(t, "calm:\n  url: \"\"\n")
	if _, err := config.Load(urlEmpty); err == nil {
		t.Error("Load: want error for empty calm.url")
	}
	badTTL := writeYAML(t, "calm:\n  session_ttl_minutes: 0\n")
	if _, err := config.Load(badTTL); err == nil {
		t.Error("Load: want error for non-positive session_ttl_minutes")
	}
}

func TestLoad_ReadError(t *testing.T) {
	if _, err := config.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("Load: want error for missing file path")
	}
}
