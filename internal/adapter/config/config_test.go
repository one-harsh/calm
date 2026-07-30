// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/config"
)

// clearAdapterEnv isolates the loader's env overlay from the developer's
// shell: MCP hosts export CALM_ADAPTER_* for the registered adapter, and an
// inherited value contaminates default/override assertions. Viper treats an
// empty env value as unset (AllowEmptyEnv is off), so blanking suffices, and
// t.Setenv restores the shell value afterward.
func clearAdapterEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "CALM_ADAPTER_") {
			t.Setenv(k, "")
		}
	}
}

func writeYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "adapter.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return p
}

func TestLoad_DefaultsWhenNoFile(t *testing.T) {
	clearAdapterEnv(t)
	cfg, err := config.Load("", "")
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
	clearAdapterEnv(t)
	p := writeYAML(t, `
calm:
  url: http://calm:9090
  api_key: "[env:CALM_DEFAULT_KEY]"
  client: claude-code
  session_ttl_minutes: 30
log:
  level: debug
`)
	cfg, err := config.Load(p, "")
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
	clearAdapterEnv(t)
	t.Setenv("CALM_ADAPTER_CALM_URL", "http://env-override:1234")
	cfg, err := config.Load("", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Calm.URL != "http://env-override:1234" {
		t.Errorf("calm.url = %q; want env override", cfg.Calm.URL)
	}
}

func TestLoad_UnknownKeyRejected(t *testing.T) {
	clearAdapterEnv(t)
	p := writeYAML(t, "bogus_key: 1\n")
	if _, err := config.Load(p, ""); err == nil {
		t.Fatal("Load: want error for unknown key, got nil")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	clearAdapterEnv(t)
	urlEmpty := writeYAML(t, "calm:\n  url: \"\"\n")
	if _, err := config.Load(urlEmpty, ""); err == nil {
		t.Error("Load: want error for empty calm.url")
	}
	badTTL := writeYAML(t, "calm:\n  session_ttl_minutes: 0\n")
	if _, err := config.Load(badTTL, ""); err == nil {
		t.Error("Load: want error for non-positive session_ttl_minutes")
	}
}

func TestLoad_ReadError(t *testing.T) {
	clearAdapterEnv(t)
	if _, err := config.Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"), ""); err == nil {
		t.Fatal("Load: want error for missing file path")
	}
}

// The workspace surface is discovery-driven (DESIGN.md §5): any workspace
// key in config is an unknown key and fails loudly.
func TestLoad_WorkspaceKeysRejected(t *testing.T) {
	clearAdapterEnv(t)
	oldKey := writeYAML(t, "calm:\n  workspace_root: /repos/alpha\n")
	if _, err := config.Load(oldKey, ""); err == nil {
		t.Error("Load: want error for retired workspace_root key")
	}
	listKey := writeYAML(t, "calm:\n  workspaces:\n    - root: /repos/alpha\n")
	if _, err := config.Load(listKey, ""); err == nil {
		t.Error("Load: want error for retired workspaces key")
	}
}

// writeFallback plants adapter.yaml in a fresh root and returns the root, so a
// cwd-independent `$CALM_HOME` fallback can be exercised.
func writeFallback(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.AdapterConfigFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("write fallback: %v", err)
	}
	return root
}

// The config-delivery gate: with no config-file env, Load reads
// `$CALM_HOME/adapter.yaml`; an explicit path still wins, a missing fallback is
// defaults not error, and env vars still override file values.
func TestLoad_RootFallbackMatrix(t *testing.T) {
	t.Run("unset+fallback-present uses it", func(t *testing.T) {
		clearAdapterEnv(t)
		root := writeFallback(t, "calm:\n  url: http://from-fallback:1\n")
		cfg, err := config.Load("", root)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Calm.URL != "http://from-fallback:1" {
			t.Errorf("calm.url = %q; want the fallback file's value", cfg.Calm.URL)
		}
	})

	t.Run("explicit path beats fallback", func(t *testing.T) {
		clearAdapterEnv(t)
		root := writeFallback(t, "calm:\n  url: http://from-fallback:1\n")
		explicit := writeYAML(t, "calm:\n  url: http://from-explicit:2\n")
		cfg, err := config.Load(explicit, root)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Calm.URL != "http://from-explicit:2" {
			t.Errorf("calm.url = %q; want the explicit path's value", cfg.Calm.URL)
		}
	})

	t.Run("unset+absent falls to defaults", func(t *testing.T) {
		clearAdapterEnv(t)
		cfg, err := config.Load("", t.TempDir())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Calm.URL != "http://localhost:8080" {
			t.Errorf("calm.url = %q; want default", cfg.Calm.URL)
		}
	})

	t.Run("env var overrides fallback file value", func(t *testing.T) {
		clearAdapterEnv(t)
		root := writeFallback(t, "calm:\n  url: http://from-fallback:1\n")
		t.Setenv("CALM_ADAPTER_CALM_URL", "http://from-env:3")
		cfg, err := config.Load("", root)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Calm.URL != "http://from-env:3" {
			t.Errorf("calm.url = %q; want env override of the fallback file", cfg.Calm.URL)
		}
	})
}
