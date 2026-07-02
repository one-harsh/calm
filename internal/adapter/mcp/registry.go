// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strconv"
	"strings"
	"sync"

	"github.com/one-harsh/calm/internal/adapter/extract"
)

// tokenRegistry is the per-Server session-scoped map of source identities to
// their current staleness tokens per LABELING.md §2. Latest sources are keyed
// by their base label (`calm:v1:file:read:foo.go`) and their token is
// overwritten on each new invocation — the prior token becomes stale by
// construction. History sources are keyed by their full immutable form
// (`calm:v1:vcs:git:diff:HEAD#7`), so later invocations record distinct keys
// rather than invalidating earlier history references.
//
// AI-03's session replacement invokes Reset to invalidate all prior tokens
// wholesale (implicit session-epoch); today only Reset's presence matters,
// not a call site.
type tokenRegistry struct {
	mu     sync.Mutex
	tokens map[string]string // CALM-facing label (no @token) → current token
}

func newTokenRegistry() *tokenRegistry {
	return &tokenRegistry{tokens: make(map[string]string)}
}

// Record records the token minted for a persisted source label. Latest labels
// overwrite by design; history labels include `#<seq>` in source, so each
// invocation records under a distinct key.
func (r *tokenRegistry) Record(source, token string) {
	if source == "" || token == "" {
		return
	}
	r.mu.Lock()
	r.tokens[source] = token
	r.mu.Unlock()
}

// ValidateAndStrip parses a caller-supplied source label and returns the
// CALM-facing form (with `#<seq>` preserved, `@<token>` stripped) alongside
// an ok flag. Cases:
//
//   - Base-only input (no `@` at all): forwarded verbatim, ok=true. This is
//     the LABELING.md-sanctioned bypass for shell-substrate references and
//     programmatic clients that don't track per-call tokens.
//   - Fused input with a valid, registered token: token stripped, ok=true.
//   - Fused input with a token that isn't in the registry (stale, cross-
//     session, or forged): ok=false — caller returns session_lost.
//   - Any label containing a raw `@` that ParseSource couldn't turn into a
//     valid token (malformed suffix): ok=false. Treating malformed as
//     session_lost is safer than forwarding a `@`-bearing label CALM's
//     grammar doesn't understand.
func (r *tokenRegistry) ValidateAndStrip(label string) (calmLabel string, ok bool) {
	base, seq, token := extract.ParseSource(label)
	if token == "" {
		if strings.Contains(label, "@") {
			return "", false
		}
		return label, true
	}
	key := base
	if seq >= 0 {
		key = base + "#" + strconv.FormatInt(seq, 10)
	}
	r.mu.Lock()
	stored, has := r.tokens[key]
	r.mu.Unlock()
	if !has || stored != token {
		return "", false
	}
	return key, true
}

// Reset clears all recorded tokens. AI-03's session replacement invokes this
// after minting a new CALM session — every reference to a token from the
// prior session is henceforth stale by construction.
func (r *tokenRegistry) Reset() {
	r.mu.Lock()
	r.tokens = make(map[string]string)
	r.mu.Unlock()
}
