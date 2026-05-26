// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	logging "github.com/one-harsh/context-logging"
)

// Tests target the unexported `resolve` because the public ReadSecret Fatals
// on error (by design). The Fatal paths of ReadSecret itself are exercised by
// re-executing the test binary as a subprocess (see runReadSecretFatalSubprocess
// + TestReadSecretFatalHelper at the bottom of this file).

func TestResolve_Text(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want string
	}{
		{"simple", "[text:hello]", "hello"},
		{"with_spaces", "[text:hello world]", "hello world"},
		{"colons_inside_payload", "[text:postgres://user:pass@host/db]", "postgres://user:pass@host/db"},
		{"hex_key", "[text:0123456789abcdef0123456789abcdef]", "0123456789abcdef0123456789abcdef"},
		{"trailing_bracket_in_payload", "[text:value]extra]", ""}, // see below — only the regex-matched form is valid
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolve(tc.ref)
			if tc.want == "" {
				// Cases where the regex doesn't accept the whole input.
				if err == nil {
					t.Errorf("resolve(%q) succeeded; want error", tc.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve(%q) unexpected error: %v", tc.ref, err)
			}
			if got != tc.want {
				t.Errorf("resolve(%q) = %q; want %q", tc.ref, got, tc.want)
			}
		})
	}
}

func TestResolve_Env(t *testing.T) {
	const setVar = "CALM_TEST_RESOLVE_SET"
	const emptyVar = "CALM_TEST_RESOLVE_EMPTY"
	const unsetVar = "CALM_TEST_RESOLVE_UNSET_xyz_never_set"

	t.Setenv(setVar, "resolved-value")
	t.Setenv(emptyVar, "")

	t.Run("set_env_var", func(t *testing.T) {
		got, err := resolve("[env:" + setVar + "]")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "resolved-value" {
			t.Errorf("got %q; want resolved-value", got)
		}
	})

	t.Run("empty_env_var_returns_empty_value", func(t *testing.T) {
		// resolve treats an empty-string env var as a valid (empty) value —
		// the resolver only fails when the var is unset. Consumers that need
		// the value to be non-empty (e.g., the auth loader treating it as an
		// API key) must enforce that invariant themselves; a "bearer key is
		// the empty string" check belongs in the consumer, not the resolver.
		got, err := resolve("[env:" + emptyVar + "]")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("got %q; want empty string", got)
		}
	})

	t.Run("unset_env_var_errors", func(t *testing.T) {
		_, err := resolve("[env:" + unsetVar + "]")
		if err == nil {
			t.Fatal("expected error for unset env var")
		}
		if !strings.Contains(err.Error(), "not set") {
			t.Errorf("error %q should mention 'not set'", err.Error())
		}
	})
}

func TestResolve_File(t *testing.T) {
	dir := t.TempDir()

	t.Run("file_with_content", func(t *testing.T) {
		path := filepath.Join(dir, "key.txt")
		if err := os.WriteFile(path, []byte("file-key-value"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolve("[file:" + path + "]")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "file-key-value" {
			t.Errorf("got %q; want file-key-value", got)
		}
	})

	t.Run("trailing_newline_stripped", func(t *testing.T) {
		// Vault Agent / ESO / `echo` all leave trailing newlines; service
		// would otherwise compare with a Bearer-without-newline token and 401.
		path := filepath.Join(dir, "trailing.txt")
		if err := os.WriteFile(path, []byte("key-with-newline\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolve("[file:" + path + "]")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "key-with-newline" {
			t.Errorf("got %q; want key-with-newline (trimmed)", got)
		}
	})

	t.Run("surrounding_whitespace_stripped", func(t *testing.T) {
		path := filepath.Join(dir, "ws.txt")
		if err := os.WriteFile(path, []byte("\n\t  key-padded  \n  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolve("[file:" + path + "]")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "key-padded" {
			t.Errorf("got %q; want key-padded (trimmed)", got)
		}
	})

	t.Run("missing_file_errors", func(t *testing.T) {
		_, err := resolve("[file:/nonexistent/path/key.txt]")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("empty_file_errors", func(t *testing.T) {
		path := filepath.Join(dir, "empty.txt")
		if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := resolve("[file:" + path + "]")
		if err == nil {
			t.Fatal("expected error for empty file")
		}
		if !strings.Contains(err.Error(), "empty") {
			t.Errorf("error %q should mention 'empty'", err.Error())
		}
	})

	t.Run("whitespace_only_file_errors", func(t *testing.T) {
		path := filepath.Join(dir, "whitespace.txt")
		if err := os.WriteFile(path, []byte("\n\t  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := resolve("[file:" + path + "]")
		if err == nil {
			t.Fatal("expected error for whitespace-only file (trims to empty)")
		}
	})
}

func TestResolve_Malformed(t *testing.T) {
	cases := []struct {
		name string
		ref  string
	}{
		{"empty_string", ""},
		{"no_brackets", "text:value"},
		{"only_open_bracket", "[text:value"},
		{"only_close_bracket", "text:value]"},
		{"unknown_scheme", "[vault:my-secret]"},
		{"missing_colon", "[textvalue]"},
		{"bare_literal", "bare-string-no-brackets"},
		{"empty_payload", "[text:]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolve(tc.ref)
			if err == nil {
				t.Errorf("resolve(%q) succeeded; want malformed error", tc.ref)
			}
		})
	}
}

// TestSecretReader_ReadSecret_HappyPath exercises the public Fatal-wrapping
// API for cases that succeed. The Fatal paths can't be unit-tested directly
// (Fatal calls os.Exit and kills the test process — by design), but resolve's
// tests cover all failure modes the wrapper would Fatal on.
func TestSecretReader_ReadSecret_HappyPath(t *testing.T) {
	t.Setenv("CALM_TEST_SECRET_READER", "via-reader")

	reader := New(logging.Nop())
	ctx := context.Background()

	cases := []struct {
		name   string
		secret Secret
		want   string
	}{
		{"text", "[text:literal-value]", "literal-value"},
		{"env", "[env:CALM_TEST_SECRET_READER]", "via-reader"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reader.ReadSecret(ctx, tc.secret)
			if got != tc.want {
				t.Errorf("ReadSecret(%q) = %q; want %q", tc.secret, got, tc.want)
			}
		})
	}
}

func TestSecret_String(t *testing.T) {
	s := Secret("[text:hello]")
	if s.String() != "[text:hello]" {
		t.Errorf("Secret.String() = %q; want [text:hello]", s.String())
	}
}

// ---------- ReadSecret Fatal-path coverage (subprocess) ----------

// Pattern: each Fatal-path test re-executes this test binary with
// FATAL_SECRET=<reference>, hits TestReadSecretFatalHelper, which calls
// ReadSecret on the bad reference and exits non-zero via logger.Fatal. The
// parent process captures stderr and asserts that the JSON log line carries
// `"level":"fatal"` and the expected substring (proves both that Fatal fired
// and that it logged the right context).

func TestReadSecret_FatalOnMalformedReference(t *testing.T) {
	out := runReadSecretFatalSubprocess(t, "not-a-bracketed-reference")
	requireFatalContains(t, out, "malformed secret reference")
}

func TestReadSecret_FatalOnUnsetEnvVar(t *testing.T) {
	out := runReadSecretFatalSubprocess(t, "[env:CALM_TEST_DEFINITELY_UNSET_xyz]")
	requireFatalContains(t, out, "not set")
}

func TestReadSecret_FatalOnMissingFile(t *testing.T) {
	out := runReadSecretFatalSubprocess(t, "[file:/nonexistent/path/key.txt]")
	requireFatalContains(t, out, "read file")
}

func TestReadSecret_FatalOnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runReadSecretFatalSubprocess(t, "[file:"+path+"]")
	requireFatalContains(t, out, "is empty")
}

// runReadSecretFatalSubprocess re-execs the test binary with the
// CALM_FATAL_SECRET env var set; the matching helper test (below) reads it
// and calls ReadSecret, which Fatals. Returns captured combined stderr+stdout.
func runReadSecretFatalSubprocess(t *testing.T, ref string) string {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(testBinary, "-test.run=^TestReadSecretFatalHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "CALM_FATAL_SECRET="+ref)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	// Fatal -> exit code 1 expected.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("subprocess: want non-zero exit, got err=%v\nstdout: %s\nstderr: %s",
			err, stdout.String(), stderr.String())
	}
	combined := stderr.String() + stdout.String()
	return combined
}

func requireFatalContains(t *testing.T, output, want string) {
	t.Helper()
	if !strings.Contains(output, `"level":"fatal"`) {
		t.Fatalf("output missing `\"level\":\"fatal\"` (Fatal did not fire?):\n%s", output)
	}
	if !strings.Contains(output, want) {
		t.Fatalf("output missing %q:\n%s", want, output)
	}
}

// TestReadSecretFatalHelper is a no-op unless invoked by the parent test
// (CALM_FATAL_SECRET set). In that case it constructs a real JSON logger,
// calls ReadSecret, and exits via logger.Fatal — parent captures the output.
func TestReadSecretFatalHelper(t *testing.T) {
	ref, ok := os.LookupEnv("CALM_FATAL_SECRET")
	if !ok {
		t.Skip("not a subprocess invocation")
	}
	logger, err := logging.New(logging.Config{Level: "info", Format: "json", Output: os.Stderr})
	if err != nil {
		t.Fatalf("init logger: %v", err)
	}
	reader := New(logger)
	_ = reader.ReadSecret(context.Background(), Secret(ref))
	// Unreachable: ReadSecret Fatals on error and the well-formed cases above
	// aren't tested via this helper.
	t.Fatal("ReadSecret returned without Fataling — bad test fixture?")
}
