// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/secrets"
)

// reader returns a real SecretReader for tests. Resolution failures Fatal by
// design (see secrets.SecretReader), so test fixtures stick to references
// that resolve cleanly; the secrets-package tests cover resolution error
// modes separately.
func reader() secrets.SecretReader {
	return secrets.New(logging.Nop())
}

func TestParseKeysFile_Valid(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantKeys  map[string]string
		wantRates map[string]int
	}{
		{
			name:      "minimal_no_rate",
			input:     "prod:[text:abc]",
			wantKeys:  map[string]string{"abc": "prod"},
			wantRates: map[string]int{},
		},
		{
			name:      "with_rate",
			input:     "prod:[text:abc]:100",
			wantKeys:  map[string]string{"abc": "prod"},
			wantRates: map[string]int{"prod": 100},
		},
		{
			name:      "comments_and_blank_lines",
			input:     "# top comment\n\nprod:[text:abc]\n\n# trailing comment\n",
			wantKeys:  map[string]string{"abc": "prod"},
			wantRates: map[string]int{},
		},
		{
			name:      "crlf_line_endings",
			input:     "prod:[text:abc]\r\nstaging:[text:def]\r\n",
			wantKeys:  map[string]string{"abc": "prod", "def": "staging"},
			wantRates: map[string]int{},
		},
		{
			name:      "bom_stripped",
			input:     "\ufeffprod:[text:abc]",
			wantKeys:  map[string]string{"abc": "prod"},
			wantRates: map[string]int{},
		},
		{
			name:      "whitespace_trimmed",
			input:     "  prod  :  [text:abc]  :  100  ",
			wantKeys:  map[string]string{"abc": "prod"},
			wantRates: map[string]int{"prod": 100},
		},
		{
			name:      "dup_namespace_diff_keys",
			input:     "prod:[text:k1]\nprod:[text:k2]\n",
			wantKeys:  map[string]string{"k1": "prod", "k2": "prod"},
			wantRates: map[string]int{},
		},
		{
			name:      "consistent_rate_across_keys",
			input:     "prod:[text:k1]:100\nprod:[text:k2]:100\n",
			wantKeys:  map[string]string{"k1": "prod", "k2": "prod"},
			wantRates: map[string]int{"prod": 100},
		},
		{
			name:      "rate_on_one_key_blank_on_other",
			input:     "prod:[text:k1]:100\nprod:[text:k2]\n",
			wantKeys:  map[string]string{"k1": "prod", "k2": "prod"},
			wantRates: map[string]int{"prod": 100},
		},
		{
			name:      "empty_input",
			input:     "",
			wantKeys:  map[string]string{},
			wantRates: map[string]int{},
		},
		{
			name:      "only_comments",
			input:     "# foo\n# bar\n",
			wantKeys:  map[string]string{},
			wantRates: map[string]int{},
		},
		{
			name:      "no_trailing_newline",
			input:     "prod:[text:abc]",
			wantKeys:  map[string]string{"abc": "prod"},
			wantRates: map[string]int{},
		},
		{
			name:      "underscores_and_dashes_in_namespace",
			input:     "my_ns-1:[text:abc]\n",
			wantKeys:  map[string]string{"abc": "my_ns-1"},
			wantRates: map[string]int{},
		},
		{
			name:      "multiple_namespaces",
			input:     "default:[text:m1]:1000\nproduction:[text:k2]:500\nstaging:[text:k3]:100\n",
			wantKeys:  map[string]string{"m1": "default", "k2": "production", "k3": "staging"},
			wantRates: map[string]int{"default": 1000, "production": 500, "staging": 100},
		},
		{
			name:      "secret_ref_with_colons_in_payload",
			input:     "prod:[text:postgres://user:pass@host/db]",
			wantKeys:  map[string]string{"postgres://user:pass@host/db": "prod"},
			wantRates: map[string]int{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys, rates, err := parseKeysFile(context.Background(), strings.NewReader(tc.input), reader())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !mapsEqualString(keys, tc.wantKeys) {
				t.Errorf("keys mismatch:\n got: %v\nwant: %v", keys, tc.wantKeys)
			}
			if !mapsEqualInt(rates, tc.wantRates) {
				t.Errorf("rates mismatch:\n got: %v\nwant: %v", rates, tc.wantRates)
			}
		})
	}
}

func TestParseKeysFile_EnvAndFileRefs(t *testing.T) {
	// Resolve via env reference.
	t.Setenv("CALM_AUTH_LOADER_TEST_KEY", "key-from-env")

	// Resolve via file reference.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(filePath, []byte("key-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	input := "prod:[env:CALM_AUTH_LOADER_TEST_KEY]:500\nstaging:[file:" + filePath + "]\n"
	keys, rates, err := parseKeysFile(context.Background(), strings.NewReader(input), reader())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys["key-from-env"] != "prod" {
		t.Errorf("env-resolved key missing or wrong namespace; got keys=%v", keys)
	}
	if keys["key-from-file"] != "staging" {
		t.Errorf("file-resolved key missing or wrong namespace (note: file content was trimmed); got keys=%v", keys)
	}
	if rates["prod"] != 500 {
		t.Errorf("rate for prod = %d; want 500", rates["prod"])
	}
}

func TestParseKeysFile_Error(t *testing.T) {
	// Empty env var resolves to "" — the loader rejects empty resolved values.
	t.Setenv("CALM_AUTH_LOADER_TEST_EMPTY", "")

	cases := []struct {
		name          string
		input         string
		wantErrSubstr string
	}{
		{"empty_namespace", ":[text:abc]", "empty namespace"},
		{"missing_secret_ref", "prod:", "bracketed secret reference"},
		{"bare_literal_not_bracketed", "prod:abc", "bracketed secret reference"},
		{"unterminated_bracket", "prod:[text:abc", "unterminated secret reference"},
		{"namespace_with_space", "pr od:[text:abc]", "invalid namespace"},
		{"namespace_with_dot", "prod.ns:[text:abc]", "invalid namespace"},
		{"namespace_too_long", strings.Repeat("a", 65) + ":[text:abc]", "invalid namespace"},
		{"rate_zero", "prod:[text:abc]:0", "rate"},
		{"rate_negative", "prod:[text:abc]:-5", "rate"},
		{"rate_non_numeric", "prod:[text:abc]:hi", "rate"},
		{"rate_too_large", "prod:[text:abc]:200001", "rate"},
		{"unexpected_content_after_bracket", "prod:[text:abc]extra", "unexpected content"},
		{"duplicate_key_resolved", "prod:[text:k1]\nstaging:[text:k1]\n", "duplicate api_key"},
		{"conflicting_rate", "prod:[text:k1]:100\nprod:[text:k2]:200\n", "conflicting rate"},
		{"empty_resolved_value", "prod:[env:CALM_AUTH_LOADER_TEST_EMPTY]", "empty value"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseKeysFile(context.Background(), strings.NewReader(tc.input), reader())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Errorf("error %q missing substring %q", err.Error(), tc.wantErrSubstr)
			}
			if !strings.Contains(err.Error(), "line") {
				t.Errorf("error %q missing 'line' context", err.Error())
			}
		})
	}
}

func TestLoadRegistry_UnsetPath(t *testing.T) {
	_, err := loadRegistry(context.Background(), "", logging.Nop())
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error %q should mention 'required'", err.Error())
	}
}

func TestLoadRegistry_MissingFile(t *testing.T) {
	_, err := loadRegistry(context.Background(), "/nonexistent/path/keys.txt", logging.Nop())
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q should mention 'not found'", err.Error())
	}
}

func TestLoadRegistry_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.txt")
	content := "production:[text:abc]:500\nstaging:[text:def]\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	reg, err := loadRegistry(context.Background(), path, logging.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if ns, ok := reg.Resolve("abc"); !ok || ns != "production" {
		t.Errorf("Resolve(abc) = (%q, %v); want (production, true)", ns, ok)
	}
	if ns, ok := reg.Resolve("def"); !ok || ns != "staging" {
		t.Errorf("Resolve(def) = (%q, %v); want (staging, true)", ns, ok)
	}
	if ns, ok := reg.Resolve("unknown"); ok {
		t.Errorf("Resolve(unknown) = (%q, true); want false", ns)
	}

	if rate, has := reg.RateFor("production"); !has || rate != 500 {
		t.Errorf("RateFor(production) = (%d, %v); want (500, true)", rate, has)
	}
	if rate, has := reg.RateFor("staging"); has || rate != 0 {
		t.Errorf("RateFor(staging) = (%d, %v); want (0, false)", rate, has)
	}
	if rate, has := reg.RateFor("unknown"); has || rate != 0 {
		t.Errorf("RateFor(unknown) = (%d, %v); want (0, false)", rate, has)
	}
}

func TestLoadRegistry_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(path, []byte(":[text:bad]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadRegistry(context.Background(), path, logging.Nop())
	if err == nil {
		t.Fatal("expected error for malformed file")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error %q should reference line number", err.Error())
	}
}

func TestIsPermissiveMode(t *testing.T) {
	cases := []struct {
		mode os.FileMode
		want bool
	}{
		{0o600, false},
		{0o400, false},
		{0o640, true},
		{0o644, true},
		{0o604, true},
		{0o666, true},
		{0o000, false},
	}
	for _, tc := range cases {
		if got := isPermissiveMode(tc.mode); got != tc.want {
			t.Errorf("isPermissiveMode(%v) = %v; want %v", tc.mode, got, tc.want)
		}
	}
}

func mapsEqualString(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func mapsEqualInt(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}
