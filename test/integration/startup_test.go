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
// to start under operator-misconfiguration conditions, which is the kind of
// claim that's only meaningful end-to-end.

// TestServiceRefusesWithoutKeysFile asserts that the service exits non-zero
// with a clear error when CALM_API_KEYS_FILE is unset/empty.
func TestServiceRefusesWithoutKeysFile(t *testing.T) {
	stderr, err := spawnCALM(t, map[string]string{
		"CALM_API_KEYS_FILE": "",
	})
	if err == nil {
		t.Fatalf("expected non-zero exit; service started with empty keys path. stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "CALM_API_KEYS_FILE is required") {
		t.Errorf("stderr missing required-variable message. got: %s", stderr)
	}
}

// TestServiceRefusesMalformedKeysFile asserts that the service exits non-zero
// with a clear parse error (including line number) on a malformed keys file.
func TestServiceRefusesMalformedKeysFile(t *testing.T) {
	dir := t.TempDir()
	keysPath := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(keysPath, []byte(":[text:no-namespace]\n"), 0o600); err != nil {
		t.Fatalf("write temp keys file: %v", err)
	}

	stderr, err := spawnCALM(t, map[string]string{
		"CALM_API_KEYS_FILE": keysPath,
	})
	if err == nil {
		t.Fatalf("expected non-zero exit on malformed file. stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "line 1") {
		t.Errorf("stderr should reference line 1. got: %s", stderr)
	}
	if !strings.Contains(stderr, "empty namespace") {
		t.Errorf("stderr should describe the failure ('empty namespace'). got: %s", stderr)
	}
}

// spawnCALM runs `go run ./cmd/calm` with the supplied env overrides applied
// on top of the test process's environment. Returns the binary's combined
// stdout+stderr (logger writes structured JSON to stdout; `go run` writes
// "exit status 1" to stderr) and the run error (non-nil means the process
// exited with a non-zero status).
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
