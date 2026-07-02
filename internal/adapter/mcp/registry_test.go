// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strconv"
	"sync"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/extract"
)

func TestRegistry_LatestCurrentValidates(t *testing.T) {
	r := newTokenRegistry()
	r.Record("calm:v1:file:read:foo.go", "a3f2k6")

	calmLabel, ok := r.ValidateAndStrip("calm:v1:file:read:foo.go@a3f2k6")
	if !ok {
		t.Fatalf("expected current token to validate; got ok=false")
	}
	if calmLabel != "calm:v1:file:read:foo.go" {
		t.Errorf("calmLabel = %q; want base without @token", calmLabel)
	}
}

func TestRegistry_LatestStaleAfterReplace(t *testing.T) {
	r := newTokenRegistry()
	r.Record("calm:v1:file:read:foo.go", "tokold") // invocation 1
	r.Record("calm:v1:file:read:foo.go", "toknew") // invocation 2 replaces

	// Old fused label is now stale.
	if _, ok := r.ValidateAndStrip("calm:v1:file:read:foo.go@tokold"); ok {
		t.Errorf("stale latest token unexpectedly validated")
	}
	// New fused label still works.
	if _, ok := r.ValidateAndStrip("calm:v1:file:read:foo.go@toknew"); !ok {
		t.Errorf("current latest token failed to validate")
	}
}

func TestRegistry_HistoryStableAcrossInvocations(t *testing.T) {
	r := newTokenRegistry()
	r.Record("calm:v1:vcs:git:diff:HEAD#3", "aaa222")
	r.Record("calm:v1:vcs:git:diff:HEAD#5", "bbb333")

	// Both history tokens remain valid because the seq suffix is part of the key.
	if _, ok := r.ValidateAndStrip("calm:v1:vcs:git:diff:HEAD#3@aaa222"); !ok {
		t.Errorf("history token for #3 failed to validate")
	}
	if _, ok := r.ValidateAndStrip("calm:v1:vcs:git:diff:HEAD#5@bbb333"); !ok {
		t.Errorf("history token for #5 failed to validate")
	}
	// Cross-invocation tokens don't validate — a #3 label with #5's token
	// would only align if invocation 3 had minted bbb333.
	if _, ok := r.ValidateAndStrip("calm:v1:vcs:git:diff:HEAD#3@bbb333"); ok {
		t.Errorf("cross-invocation token unexpectedly validated")
	}
}

func TestRegistry_ResetInvalidatesAll(t *testing.T) {
	r := newTokenRegistry()
	r.Record("calm:v1:file:read:foo.go", "a3f2k6")
	r.Record("calm:v1:vcs:git:diff:HEAD#3", "aaa222")

	r.Reset()

	if _, ok := r.ValidateAndStrip("calm:v1:file:read:foo.go@a3f2k6"); ok {
		t.Errorf("latest token survived reset")
	}
	if _, ok := r.ValidateAndStrip("calm:v1:vcs:git:diff:HEAD#3@aaa222"); ok {
		t.Errorf("history token survived reset")
	}
}

func TestRegistry_BaseOnlyForwardsUnchanged(t *testing.T) {
	r := newTokenRegistry()

	cases := []string{
		"calm:v1:file:read:foo.go",
		"calm:v1:vcs:git:diff:HEAD#7",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got, ok := r.ValidateAndStrip(in)
			if !ok {
				t.Errorf("base-only label unexpectedly rejected: %q", in)
			}
			if got != in {
				t.Errorf("calmLabel = %q; want %q (unchanged)", got, in)
			}
		})
	}
}

func TestRegistry_MalformedSuffixRejected(t *testing.T) {
	r := newTokenRegistry()

	// The `@` is present but the tail isn't a valid 6-char base32 token.
	// Forwarding this to CALM would send a `@`-bearing label CALM's grammar
	// doesn't understand — treating as staleness is safer.
	cases := []string{
		"calm:v1:file:read:foo.go@short",
		"calm:v1:file:read:foo.go@a3f2k9", // '9' not in [a-z2-7]
		"calm:v1:file:read:foo.go@toolongstring",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, ok := r.ValidateAndStrip(in); ok {
				t.Errorf("malformed suffix unexpectedly validated: %q", in)
			}
		})
	}
}

// Regression: an agent that takes the emitted fused label `<base>@<token>`
// and appends `#<n>` produces `<base>@<token>#<n>` — seq after token, which
// violates the LABELING.md grammar (`<base>[#<seq>][@<token>]`). It must
// reject even when that token is current for both the base and the history
// source; the canonical `<base>#<n>@<token>` ordering validates.
func TestRegistry_SeqAppendedAfterTokenRejected(t *testing.T) {
	r := newTokenRegistry()
	r.Record("calm:v1:vcs:git:diff:HEAD", "a3f2k6")
	r.Record("calm:v1:vcs:git:diff:HEAD#1", "a3f2k6")

	if _, ok := r.ValidateAndStrip("calm:v1:vcs:git:diff:HEAD@a3f2k6#1"); ok {
		t.Errorf("seq-after-token form unexpectedly validated")
	}
	calmLabel, ok := r.ValidateAndStrip("calm:v1:vcs:git:diff:HEAD#1@a3f2k6")
	if !ok {
		t.Fatalf("canonical seq-before-token form failed to validate")
	}
	if calmLabel != "calm:v1:vcs:git:diff:HEAD#1" {
		t.Errorf("calmLabel = %q; want history label with @token stripped", calmLabel)
	}
}

func TestRegistry_UnregisteredValidTokenRejected(t *testing.T) {
	r := newTokenRegistry()
	// A structurally valid token that was never recorded — the classic
	// stale-across-session case.
	if _, ok := r.ValidateAndStrip("calm:v1:file:read:foo.go@abcdef"); ok {
		t.Errorf("unregistered token unexpectedly validated")
	}
}

// Concurrent Record + Validate must not race (run with -race).
func TestRegistry_ConcurrentRecordValidate(t *testing.T) {
	r := newTokenRegistry()

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			base := "calm:v1:file:read:foo" + strconv.Itoa(n)
			r.Record(base, extract.MintToken())
		}(i)
		go func(n int) {
			defer wg.Done()
			base := "calm:v1:file:read:foo" + strconv.Itoa(n)
			// Result doesn't matter — this exercises the read lock path.
			_, _ = r.ValidateAndStrip(base + "@abcdef")
		}(i)
	}
	wg.Wait()
}
