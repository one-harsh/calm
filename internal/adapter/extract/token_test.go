// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"regexp"
	"testing"
)

var tokenPattern = regexp.MustCompile(`^[a-z2-7]{6}$`)

// Every minted token matches the canonical grammar (6 chars, lowercase
// base32 alphabet). Uniqueness at 1000 samples: fewer than 10 collisions
// expected given a 32^6 ≈ 1B keyspace (birthday bound ~5e-7 per pair).
func TestMintToken_FormatAndUniqueness(t *testing.T) {
	seen := make(map[string]int, 1000)
	for range 1000 {
		tok := MintToken()
		if !tokenPattern.MatchString(tok) {
			t.Fatalf("token %q does not match ^[a-z2-7]{6}$", tok)
		}
		seen[tok]++
	}
	collisions := 0
	for _, n := range seen {
		if n > 1 {
			collisions += n - 1
		}
	}
	if collisions > 10 {
		t.Errorf("too many collisions in 1000 samples: %d (keyspace is ~1B; expected <10)", collisions)
	}
}

func TestParseSource_AllForms(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantBase  string
		wantSeq   int64
		wantToken string
	}{
		{
			name:      "base only",
			in:        "calm:v1:file:read:foo.go",
			wantBase:  "calm:v1:file:read:foo.go",
			wantSeq:   -1,
			wantToken: "",
		},
		{
			name:      "base with seq",
			in:        "calm:v1:vcs:git:diff:HEAD#7",
			wantBase:  "calm:v1:vcs:git:diff:HEAD",
			wantSeq:   7,
			wantToken: "",
		},
		{
			name:      "base with token",
			in:        "calm:v1:file:read:foo.go@a3f2k6",
			wantBase:  "calm:v1:file:read:foo.go",
			wantSeq:   -1,
			wantToken: "a3f2k6",
		},
		{
			name:      "fully fused history",
			in:        "calm:v1:vcs:git:diff:HEAD#7@a3f2k6",
			wantBase:  "calm:v1:vcs:git:diff:HEAD",
			wantSeq:   7,
			wantToken: "a3f2k6",
		},
		{
			name:      "malformed token (wrong length) — token ignored, kept in base",
			in:        "calm:v1:file:read:foo.go@abc",
			wantBase:  "calm:v1:file:read:foo.go@abc",
			wantSeq:   -1,
			wantToken: "",
		},
		{
			name:      "malformed token (illegal char '8') — token ignored, kept in base",
			in:        "calm:v1:file:read:foo.go@a3f28k",
			wantBase:  "calm:v1:file:read:foo.go@a3f28k",
			wantSeq:   -1,
			wantToken: "",
		},
		{
			name:      "seq appended after token — no valid token, '@' stays in base",
			in:        "calm:v1:vcs:git:diff:HEAD@a3f2k6#1",
			wantBase:  "calm:v1:vcs:git:diff:HEAD@a3f2k6",
			wantSeq:   1,
			wantToken: "",
		},
		{
			name:      "malformed seq — seq ignored, kept in base",
			in:        "calm:v1:vcs:git:diff:HEAD#abc@a3f2k6",
			wantBase:  "calm:v1:vcs:git:diff:HEAD#abc",
			wantSeq:   -1,
			wantToken: "a3f2k6",
		},
		{
			name:      "empty input",
			in:        "",
			wantBase:  "",
			wantSeq:   -1,
			wantToken: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, seq, tok := ParseSource(c.in)
			if base != c.wantBase {
				t.Errorf("base = %q; want %q", base, c.wantBase)
			}
			if seq != c.wantSeq {
				t.Errorf("seq = %d; want %d", seq, c.wantSeq)
			}
			if tok != c.wantToken {
				t.Errorf("token = %q; want %q", tok, c.wantToken)
			}
		})
	}
}

func TestIsValidToken(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"a3f2k6", true},
		{"zzzzzz", true},
		{"222222", true},
		{"777777", true},
		{"abcde", false},   // too short
		{"abcdefg", false}, // too long
		{"a3f2k9", false},  // '9' not in alphabet
		{"a3f2k0", false},  // '0' not in alphabet
		{"a3f2k1", false},  // '1' not in alphabet
		{"a3f2k8", false},  // '8' not in alphabet
		{"A3F2K6", false},  // uppercase rejected
		{"", false},        // empty
		{"abc.de", false},  // non-alphanumeric
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := isValidToken(c.in); got != c.want {
				t.Errorf("isValidToken(%q) = %v; want %v", c.in, got, c.want)
			}
		})
	}
}
