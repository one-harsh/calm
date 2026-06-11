// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Startup scenarios spawn the calm binary via `go run ./cmd/calm` with a
// controlled environment. These are slower than in-process tests because
// each invocation triggers a Go build — but they prove the service refuses
// to start under operator-misconfiguration conditions, which is the kind
// of claim that's only meaningful end-to-end.

// The service fails fast (non-zero exit, clear message) when no config file is
// supplied — operator misconfiguration never silently falls back to defaults.
func TestServiceRefusesWithoutConfigFile(t *testing.T) {
	stderr, err := spawnCALM(t, map[string]string{
		"CALM_CONFIG_FILE": "",
	})
	if err == nil {
		t.Fatalf("expected non-zero exit; service started with empty config path. stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "CALM_CONFIG_FILE is required") {
		t.Errorf("stderr missing required-variable message. got: %s", stderr)
	}
}

// The service refuses to start with an empty namespace list, failing startup
// with an explanatory validation error rather than booting unusable.
func TestServiceRefusesMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	content := `
service:
  service_name: calm
storage:
  dsn: postgres://nowhere:5432/calm
namespaces: []
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	stderr, err := spawnCALM(t, map[string]string{
		"CALM_CONFIG_FILE": cfgPath,
	})
	if err == nil {
		t.Fatalf("expected non-zero exit on malformed config. stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "at least one namespace is required") {
		t.Errorf("stderr should explain the validation failure. got: %s", stderr)
	}
}

func spawnCALM(t *testing.T, envOverrides map[string]string) (string, error) {
	t.Helper()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	cmd := exec.Command("go", "run", "./cmd/calm")
	cmd.Dir = repoRoot

	env := os.Environ()
	for k, v := range envOverrides {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	return combined.String(), runErr
}
