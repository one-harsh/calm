// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// faultClient injects failures real CALM can't produce on demand (a rejected
// recovery create, a 5xx, a direct 401) while forwarding everything else to
// the real client chain. It also counts create attempts — including ones the
// injection swallows — so tests can assert the adapter did NOT try to create.
type faultClient struct {
	*recordingClient
	mu             sync.Mutex
	createAttempts int
	failCreates    error
	failNextIngest error
	failNextSearch error
}

func (f *faultClient) CreateSession(ctx context.Context, client string, ttlMinutes int, idempotencyKey string) (string, error) {
	f.mu.Lock()
	f.createAttempts++
	failWith := f.failCreates
	f.mu.Unlock()
	if failWith != nil {
		return "", failWith
	}
	return f.recordingClient.CreateSession(ctx, client, ttlMinutes, idempotencyKey)
}

func (f *faultClient) Ingest(ctx context.Context, token string, in calm.IngestInput) (calm.IngestSummary, error) {
	f.mu.Lock()
	failWith := f.failNextIngest
	f.failNextIngest = nil
	f.mu.Unlock()
	if failWith != nil {
		return calm.IngestSummary{}, failWith
	}
	return f.recordingClient.Ingest(ctx, token, in)
}

func (f *faultClient) Search(ctx context.Context, token string, in calm.SearchInput) (calm.SearchResults, error) {
	f.mu.Lock()
	failWith := f.failNextSearch
	f.failNextSearch = nil
	f.mu.Unlock()
	if failWith != nil {
		return calm.SearchResults{}, failWith
	}
	return f.recordingClient.Search(ctx, token, in)
}

func (f *faultClient) set(mutate func(*faultClient)) {
	f.mu.Lock()
	mutate(f)
	f.mu.Unlock()
}

func (f *faultClient) attempts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createAttempts
}

// newFaultLoop is newAdapterLoop with the fault-injecting wrapper in the chain.
func newFaultLoop(t *testing.T, workspace string) (*faultClient, string, *mcpDriver) {
	t.Helper()
	inner, err := calm.NewGenapiClient(env.serverURL, testMasterKey, nil)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	fc := &faultClient{recordingClient: &recordingClient{Client: inner}}
	d := newMCPDriver(t, fc, workspace)
	if r := d.call("initialize", map[string]any{}); r.Error != nil {
		t.Fatalf("initialize: %+v", r.Error)
	}
	token := fc.sessionToken()
	if token == "" {
		t.Fatal("adapter did not create a session on initialize")
	}
	return fc, token, d
}

// A session deleted server-side mid-conversation is recovered per AD03: the
// first call after the loss surfaces session_lost over raw output, the adapter
// holds a replacement session, prior fused labels reject as stale, and the
// next capture round-trips cleanly — all against real CALM (real 404, real
// recovery create; a replayed idempotency key would hand the dead session back
// and the post-recovery capture would fail).
func TestAdapterSessionRecovery_DeletedSession_RecoversAndInvalidatesLabels(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxrecover"
	writeWorkspaceFile(t, workspace, "note.txt", marker+" pre-loss content\n")

	rc, t1, d := newAdapterLoop(t, workspace)

	pre := d.runCommand("cat note.txt")
	if pre.IsError {
		t.Fatalf("pre-loss capture errored: %+v", pre)
	}
	staleFused := parseSearchSourceFused(t, pre.Content[0].Text)

	if err := rc.DeleteSession(context.Background(), t1); err != nil {
		t.Fatalf("server-side DeleteSession: %v", err)
	}

	lost := d.runCommand("cat note.txt")
	if lost.IsError {
		t.Fatalf("session loss on an action tool must not be an error result: %+v", lost)
	}
	text := lost.Content[0].Text
	if !strings.HasPrefix(text, obs.DegradedPhrase(obs.DegradedReasonSessionLost)) {
		t.Fatalf("text = %q; want session_lost phrasing prefix", text)
	}
	if !strings.Contains(text, marker) {
		t.Fatalf("raw output missing from session-lost response:\n%s", text)
	}
	t2 := rc.sessionToken()
	if t2 == t1 {
		t.Fatal("adapter did not replace the session (dedup replay of the dead token?)")
	}

	post := d.runCommand("cat note.txt")
	if post.IsError {
		t.Fatalf("post-recovery capture errored: %+v", post)
	}
	base := parseSearchSource(t, post.Content[0].Text)
	if hits := hitCount(t, rc, t2, base, marker); hits == 0 {
		t.Fatal("post-recovery capture not retrievable under the replacement session")
	}

	// The pre-loss fused label is a different-epoch reference now: local reject.
	res := d.search([]string{marker}, staleFused)
	if !res.IsError {
		t.Fatalf("pre-loss fused label must reject after recovery: %+v", res)
	}
	if !strings.Contains(res.Content[0].Text, "session_lost") {
		t.Fatalf("expected session_lost phrasing; got:\n%s", res.Content[0].Text)
	}
}

// A recovery create rejected with 4xx latches auth_failed for the process.
// Injection hybrid: real CALM cannot produce create-rejected-after-404 — a
// revoked key 401s the original call before session resolution, and a key
// valid for another namespace recreates successfully there — so the create
// rejection is injected while the 404 trigger stays real (server-side delete).
func TestAdapterSessionRecovery_CreateRejected_AuthFailedSticky(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "note.txt", "sticky auth content\n")

	fc, t1, d := newFaultLoop(t, workspace)
	fc.set(func(f *faultClient) {
		f.failCreates = &calm.StatusError{Op: "create session", Code: 401, Status: "401 Unauthorized"}
	})
	if err := fc.DeleteSession(context.Background(), t1); err != nil {
		t.Fatalf("server-side DeleteSession: %v", err)
	}

	res := d.runCommand("cat note.txt")
	if res.IsError {
		t.Fatalf("auth failure on an action tool must not be an error result: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.HasPrefix(text, obs.DegradedPhrase(obs.DegradedReasonAuthFailed)) {
		t.Fatalf("text = %q; want auth_failed phrasing prefix", text)
	}
	if !strings.Contains(text, "sticky auth content") {
		t.Fatalf("raw output missing from auth-failed response:\n%s", text)
	}
	attemptsAfterLatch := fc.attempts()

	// Sticky: subsequent calls short-circuit — no further create attempts, no
	// CALM traffic.
	res = d.runCommand("cat note.txt")
	if !strings.HasPrefix(res.Content[0].Text, obs.DegradedPhrase(obs.DegradedReasonAuthFailed)) {
		t.Fatalf("second call = %q; want short-circuited auth_failed", res.Content[0].Text)
	}
	sr := d.search([]string{"sticky"}, "")
	if !sr.IsError || sr.Content[0].Text != obs.DegradedPhrase(obs.DegradedReasonAuthFailed) {
		t.Fatalf("search after latch = %+v; want bare auth_failed error", sr)
	}
	if got := fc.attempts(); got != attemptsAfterLatch {
		t.Fatalf("create attempts grew after latch: %d → %d (no-loop violated)", attemptsAfterLatch, got)
	}
}

// A 5xx on ingest is transient, not session loss: no replacement create, the
// original session survives, and the next capture succeeds on it. The 5xx is
// injected — real CALM can't emit one on demand.
func TestAdapterSessionRecovery_5xxDoesNotReplace(t *testing.T) {
	workspace := t.TempDir()
	const marker = "zphloxfivehundred"
	writeWorkspaceFile(t, workspace, "note.txt", marker+" content\n")

	fc, t1, d := newFaultLoop(t, workspace)
	fc.set(func(f *faultClient) {
		f.failNextIngest = &calm.StatusError{Op: "ingest", Code: 500, Status: "500 Internal Server Error"}
	})

	res := d.runCommand("echo transient blip")
	if res.IsError {
		t.Fatalf("5xx capture failure must not be an error result: %+v", res)
	}
	text := res.Content[0].Text
	if !strings.HasPrefix(text, obs.DegradedPhrase(obs.DegradedReasonCaptureFailed)) {
		t.Fatalf("text = %q; want capture_failed phrasing (not session_lost)", text)
	}
	if fc.sessionToken() != t1 {
		t.Fatal("5xx must not replace the session")
	}
	if got := fc.attempts(); got != 1 {
		t.Fatalf("create attempts = %d; want 1 (initialize only)", got)
	}

	post := d.runCommand("cat note.txt")
	if post.IsError {
		t.Fatalf("capture after transient 5xx errored: %+v", post)
	}
	base := parseSearchSource(t, post.Content[0].Text)
	if hits := hitCount(t, fc, t1, base, marker); hits == 0 {
		t.Fatal("capture after transient 5xx not retrievable on the original session")
	}
}

// A direct 401 on a session-touching call maps to auth_failed with no recovery
// attempt — credentials are rejected before session resolution, so a recreate
// would prove nothing. The 401 is injected (the suite's key is valid).
func TestAdapterSessionRecovery_Direct401_AuthFailed(t *testing.T) {
	fc, _, d := newFaultLoop(t, t.TempDir())
	fc.set(func(f *faultClient) {
		f.failNextSearch = &calm.StatusError{Op: "search", Code: 401, Status: "401 Unauthorized"}
	})

	res := d.search([]string{"zphlox"}, "")
	if !res.IsError {
		t.Fatalf("direct-401 search must be an error result: %+v", res)
	}
	if got := res.Content[0].Text; got != obs.DegradedPhrase(obs.DegradedReasonAuthFailed) {
		t.Fatalf("text = %q; want auth_failed phrasing", got)
	}
	if got := fc.attempts(); got != 1 {
		t.Fatalf("create attempts = %d; want 1 (initialize only — no recovery on 401)", got)
	}
}
