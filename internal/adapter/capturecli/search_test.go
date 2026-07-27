// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// expectLatch drives a rejected create so the auth latch persists on disk.
func expectLatch(c *calm.MockClient) {
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	c.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return("", &calm.StatusError{Op: "create", Code: 401, Status: "401"}).Once()
}

// Retrieval never establishes: with no session on disk, search fails with the
// unavailability phrasing and a nonzero exit, and the strict mock proves no
// create was attempted.
func TestSearch_NoSession_UnavailableNoCreate(t *testing.T) {
	c := calm.NewMockClient(t) // strict: any CALM call fails the test
	d, stdout, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "find me"})

	if code == 0 {
		t.Fatalf("exit = 0; want nonzero for unavailable retrieval")
	}
	if !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)) {
		t.Errorf("stderr must carry the unavailability phrasing; got:\n%s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("no results to stdout when unavailable; got:\n%s", stdout.String())
	}
}

// A source label whose fused token is not in the registry rejects locally as
// session_lost, without reaching CALM (AD02).
func TestSearch_StaleToken_SessionLostNoCall(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	d, stdout, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello")) // establishes the session
	stdout.Reset()
	stderr.Reset()

	code := Dispatch(context.Background(), d,
		[]string{"search", "--session", "conv", "source=calm:v1:file:read:ghost.go@zzzzzz", "q"})

	if code == 0 {
		t.Fatalf("exit = 0; want nonzero for a stale source")
	}
	if !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonSessionLost)) {
		t.Errorf("stderr must carry the session_lost phrasing; got:\n%s", stderr.String())
	}
}

func TestSearch_RankedResults(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(calm.SearchResults{
		Queries: []calm.QueryResult{{Query: "needle", Hits: []calm.Hit{
			{Title: "run#1", Snippet: "found the needle here", Source: "calm:v1:x", MatchLayer: "primary"},
			{Title: "run#2", Snippet: "another needle", Source: "calm:v1:y", MatchLayer: "trigram"},
		}}},
	}, nil).Once()
	d, stdout, _ := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	stdout.Reset()
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "needle"})

	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	if !strings.Contains(stdout.String(), "2 hits across 1 query") || !strings.Contains(stdout.String(), "found the needle here") {
		t.Errorf("stdout must render the ranked hits; got:\n%s", stdout.String())
	}
}

func TestSearch_DocumentOrder(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	next := 3
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(calm.SearchResults{
		Queries:    []calm.QueryResult{{Hits: []calm.Hit{{Title: "chunk-0", Snippet: "body", Truncated: true}}}},
		NextOffset: &next,
	}, nil).Once()
	d, stdout, _ := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	stdout.Reset()
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "source=calm:v1:x"})

	if code != 0 {
		t.Fatalf("exit = %d; want 0", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "in document order from offset 0") || !strings.Contains(out, "chunk-0") ||
		!strings.Contains(out, documentOrderTruncatedMarker) || !strings.Contains(out, "offset: 3") {
		t.Errorf("stdout must render document-order chunks with markers; got:\n%s", out)
	}
}

func TestSearch_EmptyResults(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(calm.SearchResults{}, nil).Once()
	d, stdout, _ := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	stdout.Reset()
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "nomatch"})

	if code != 0 || strings.TrimSpace(stdout.String()) != "no matches" {
		t.Errorf("empty search: exit=%d stdout=%q; want 0 and bare 'no matches'", code, stdout.String())
	}
}

func TestSearch_DocumentOrderEmpty(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(calm.SearchResults{}, nil).Once()
	d, stdout, _ := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	stdout.Reset()
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "source=calm:v1:x"})

	if code != 0 || !strings.Contains(stdout.String(), "no chunks at this offset under source=calm:v1:x") {
		t.Errorf("empty doc-order: exit=%d stdout=%q; want 0 and source-scoped 'no chunks'", code, stdout.String())
	}
}

// AD03 parity with the MCP shell: a credential rejection observed from search
// latches the conversation, not just this call.
func TestSearch_AuthRejected(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).
		Return(calm.SearchResults{}, &calm.StatusError{Op: "search", Code: 401, Status: "401"}).Once()
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "q"})

	if code == 0 || !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonAuthFailed)) {
		t.Errorf("401 search: exit=%d stderr=%q; want nonzero + auth_failed", code, stderr.String())
	}
	mgr, err := d.manager("conv")
	if err != nil {
		t.Fatal(err)
	}
	if v, verr := mgr.View(context.Background()); verr != nil || !v.AuthFailed {
		t.Errorf("view = %+v, %v; want the latch persisted by the search-observed 401", v, verr)
	}
}

func TestSearch_CalmError(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).
		Return(calm.SearchResults{}, &calm.StatusError{Op: "search", Code: 503, Status: "503"}).Once()
	d, stdout, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	stdout.Reset()
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "q"})

	if code == 0 || !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)) {
		t.Errorf("search error: exit=%d stderr=%q; want nonzero + calm_unreachable", code, stderr.String())
	}
}

func TestSearch_Latched_AuthFailed(t *testing.T) {
	c := calm.NewMockClient(t)
	expectLatch(c)
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello")) // latches
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "q"})

	if code == 0 || !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonAuthFailed)) {
		t.Errorf("latched search: exit=%d stderr=%q; want nonzero + auth_failed", code, stderr.String())
	}
}

// AD03 parity with the MCP shell: a search-observed 404 runs the one CAS'd
// replacement create — this query reports session_lost, the conversation heals
// for the next capture.
func TestSearch_SessionNotFound_RecoversConversation(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).
		Return(calm.SearchResults{}, &calm.StatusError{Op: "search", Code: 404, Status: "404"}).Once()
	c.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("tok2", nil).Once()
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "q"})

	if code == 0 || !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonSessionLost)) {
		t.Errorf("404 search: exit=%d stderr=%q; want nonzero + session_lost", code, stderr.String())
	}
	mgr, err := d.manager("conv")
	if err != nil {
		t.Fatal(err)
	}
	if v, verr := mgr.View(context.Background()); verr != nil || v.Token != "tok2" {
		t.Errorf("view = %+v, %v; want the replacement session persisted", v, verr)
	}
}

// A positional is consumed only as a well-formed recall-hint option; malformed
// or hint-shaped-but-literal text stays searchable, and "--" ends option
// parsing entirely.
func TestSplitSearchArgs(t *testing.T) {
	var source string
	var offset, limit, budget int
	q := splitSearchArgs(
		[]string{"source=calm:v1:shell:task", "offset=3", "limit=abc", "source=code", "budget-bytes=9000", "find", "me", "--", "offset=9"},
		&source, &offset, &limit, &budget,
	)
	if source != "calm:v1:shell:task" || offset != 3 || budget != 9000 {
		t.Errorf("source=%q offset=%d budget=%d; want hint options consumed", source, offset, budget)
	}
	if limit != 0 {
		t.Errorf("limit=%d; want malformed option untouched", limit)
	}
	want := []string{"limit=abc", "source=code", "find", "me", "offset=9"}
	if len(q) != len(want) {
		t.Fatalf("queries = %v; want %v", q, want)
	}
	for i := range want {
		if q[i] != want[i] {
			t.Errorf("queries[%d] = %q; want %q", i, q[i], want[i])
		}
	}
}

func TestUsageValidations(t *testing.T) {
	ctx := context.Background()
	run := func(args ...string) (int, string) {
		c := calm.NewMockClient(t) // strict: validations must precede any CALM call
		d, _, stderr := newDeps(t, c)
		return Dispatch(ctx, d, args), stderr.String()
	}

	if code, err := run("init", "--reset"); code != 2 || !strings.Contains(err, "requires --session") {
		t.Errorf("init --reset without --session: exit=%d err=%q", code, err)
	}
	if code, err := run("search", "--session", "c"); code != 2 || !strings.Contains(err, "queries or source") {
		t.Errorf("search with neither queries nor source: exit=%d err=%q", code, err)
	}
	if code, _ := run("search", "--session", "c", "--offset=-1", "q"); code != 2 {
		t.Errorf("search with negative offset: exit=%d; want 2", code)
	}
	if code, err := run("feedback", "--session", "c", "only-ref"); code != 2 || !strings.Contains(err, "usage") {
		t.Errorf("feedback with a missing outcome: exit=%d err=%q", code, err)
	}
	// A malformed flag on any agent-facing command is a usage error (exit 2).
	for _, cmd := range []string{"search", "feedback", "init"} {
		if code, _ := run(cmd, "--nope"); code != 2 {
			t.Errorf("%s with a bad flag: exit=%d; want 2", cmd, code)
		}
	}
}
