// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"strconv"
	"strings"
	"sync"

	"github.com/one-harsh/calm/internal/adapter/extract"
)

// Registry is the per-session map of source identities to their current
// staleness tokens per LABELING.md §2. Latest sources are keyed by their base
// label (`calm:v1:file:read:foo.go`) and their token is overwritten on each new
// invocation — the prior token becomes stale by construction. History sources
// are keyed by their full immutable form (`calm:v1:vcs:git:diff:HEAD#7`), so
// later invocations record distinct keys rather than invalidating earlier
// history references.
//
// Session replacement invokes Reset to invalidate all prior tokens
// wholesale — the implicit session-epoch.
type Registry struct {
	mu     sync.Mutex
	tokens map[string]string // CALM-facing label (no @token) → current token
}

func NewRegistry() *Registry {
	return &Registry{tokens: make(map[string]string)}
}

// Record records the token minted for a persisted source label. Latest labels
// overwrite by design; history labels include `#<seq>` in source, so each
// invocation records under a distinct key.
func (r *Registry) Record(source, token string) {
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
func (r *Registry) ValidateAndStrip(label string) (calmLabel string, ok bool) {
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

// Reset clears all recorded tokens. Session replacement invokes this after
// minting a new CALM session — every reference to a token from the prior
// session is henceforth stale by construction.
func (r *Registry) Reset() {
	r.mu.Lock()
	r.tokens = make(map[string]string)
	r.mu.Unlock()
}

// Snapshot returns a copy of the label→token map for a shell that persists
// registry state across process death (Part III's on-disk store). The copy is
// caller-owned; mutating it never touches live registry state.
func (r *Registry) Snapshot() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]string, len(r.tokens))
	for k, v := range r.tokens {
		out[k] = v
	}
	return out
}

// Load replaces the registry's contents with a previously snapshotted map,
// copying it so the caller retains no alias into registry state. It restores a
// persisted registry when a shell resumes a session; a nil map clears it.
func (r *Registry) Load(tokens map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = make(map[string]string, len(tokens))
	for k, v := range tokens {
		r.tokens[k] = v
	}
}
