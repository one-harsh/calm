// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// Workload ingests a two-paragraph document; expects 200 with source echoed, sections_total 2,
// a two-entry summary, and summary_truncated false.
func TestIngestHandler_Happy(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "build.log", Content: "alpha\n\nbeta"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.Source != "build.log" {
		t.Errorf("source = %q; want build.log", resp.JSON200.Source)
	}
	if resp.JSON200.SectionsTotal != 2 || len(resp.JSON200.Summary) != 2 {
		t.Errorf("total=%d summary=%d; want 2/2", resp.JSON200.SectionsTotal, len(resp.JSON200.Summary))
	}
	if resp.JSON200.SummaryTruncated {
		t.Error("summary_truncated = true; want false")
	}
}

// Re-ingesting the same source with different content replaces prior chunks atomically;
// the session shows exactly one source with the updated chunk count (idempotent-indexing).
func TestIngestHandler_IdempotentReingest(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ctx := context.Background()

	do := func(content string) {
		r, err := client.IngestWithResponse(ctx,
			&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
			genapi.IngestJSONRequestBody{Source: "s", Content: content})
		if err != nil {
			t.Fatalf("Ingest: %v", err)
		}
		if r.StatusCode() != http.StatusOK {
			t.Fatalf("status = %d body=%s", r.StatusCode(), string(r.Body))
		}
	}
	do("one\n\ntwo\n\nthree")
	do("solo")

	sr, err := client.ListSourcesWithResponse(ctx, &genapi.ListSourcesParams{XCALMSessionToken: sess.SessionToken})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if sr.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", sr.StatusCode(), string(sr.Body))
	}
	if len(sr.JSON200.Sources) != 1 {
		t.Fatalf("sources = %d; want 1 (re-ingest reuses the source)", len(sr.JSON200.Sources))
	}
	if sr.JSON200.Sources[0].Chunks != 1 {
		t.Errorf("chunks = %d; want 1 after re-ingest replaced content", sr.JSON200.Sources[0].Chunks)
	}
}

// Re-indexing one source replaces only that source's chunks; sibling sources in the same
// session keep their chunks intact and searchable (idempotent-indexing is scoped to the
// source, not the session).
func TestIngestHandler_ReingestPreservesSiblingSources(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ctx := context.Background()

	ingestForSearch(t, client, sess.SessionToken, "a.log", "alphamarker one\n\nalphamarker two")
	ingestForSearch(t, client, sess.SessionToken, "b.log", "betamarker content")
	ingestForSearch(t, client, sess.SessionToken, "a.log", "gammamarker only")

	search := func(q string) []genapi.SearchHit {
		r, err := client.SearchWithResponse(ctx,
			&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
			genapi.SearchJSONRequestBody{Queries: []string{q}})
		if err != nil {
			t.Fatalf("Search %q: %v", q, err)
		}
		if r.StatusCode() != http.StatusOK {
			t.Fatalf("Search %q: status %d body=%s", q, r.StatusCode(), string(r.Body))
		}
		return r.JSON200.Results[0].Hits
	}

	if hits := search("betamarker"); len(hits) != 1 || hits[0].Source != "b.log" {
		t.Errorf("betamarker hits = %+v; want one hit from b.log (sibling preserved)", hits)
	}
	if hits := search("alphamarker"); len(hits) != 0 {
		t.Errorf("alphamarker hits = %+v; want none (old a.log content replaced)", hits)
	}
	if hits := search("gammamarker"); len(hits) != 1 || hits[0].Source != "a.log" {
		t.Errorf("gammamarker hits = %+v; want one hit from a.log (new content live)", hits)
	}

	sr, err := client.ListSourcesWithResponse(ctx, &genapi.ListSourcesParams{XCALMSessionToken: sess.SessionToken})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if sr.StatusCode() != http.StatusOK {
		t.Fatalf("ListSources: status %d body=%s", sr.StatusCode(), string(sr.Body))
	}
	if len(sr.JSON200.Sources) != 2 {
		t.Fatalf("sources = %+v; want 2 (a.log and b.log)", sr.JSON200.Sources)
	}
	for _, s := range sr.JSON200.Sources {
		if s.Chunks != 1 {
			t.Errorf("source %q chunks = %d; want 1 (a.log replaced to one section, b.log untouched)", s.Label, s.Chunks)
		}
	}
}

// Ingesting more than 50 sections returns sections_total equal to the full count, summary
// capped at 50 entries, and summary_truncated true.
func TestIngestHandler_SummaryTruncatedAt50(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	var sb strings.Builder
	for i := range 60 {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "p%d", i)
	}
	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "big", Content: sb.String()})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.SectionsTotal != 60 {
		t.Errorf("sections_total = %d; want 60", resp.JSON200.SectionsTotal)
	}
	if !resp.JSON200.SummaryTruncated || len(resp.JSON200.Summary) != 50 {
		t.Errorf("truncated=%v summary=%d; want true/50", resp.JSON200.SummaryTruncated, len(resp.JSON200.Summary))
	}
}

// Whitespace-only content ingests without error; the source is recorded with zero chunks
// and the response shows 0 for sections_total, sections_indexed, and summary length.
func TestIngestHandler_EmptyContentZeroSections(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ctx := context.Background()

	resp, err := client.IngestWithResponse(ctx,
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "empty", Content: "   "})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.SectionsTotal != 0 || resp.JSON200.SectionsIndexed != 0 || len(resp.JSON200.Summary) != 0 {
		t.Errorf("empty ingest = total %d / indexed %d / summary %d; want 0/0/0",
			resp.JSON200.SectionsTotal, resp.JSON200.SectionsIndexed, len(resp.JSON200.Summary))
	}

	// The source is recorded with zero chunks — the honest empty index.
	sr, err := client.ListSourcesWithResponse(ctx, &genapi.ListSourcesParams{XCALMSessionToken: sess.SessionToken})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sr.JSON200.Sources) != 1 || sr.JSON200.Sources[0].Chunks != 0 {
		t.Errorf("sources = %+v; want one source with 0 chunks", sr.JSON200.Sources)
	}
}

// Ingest with intents returns a summary in document order; no per-section match signal is
// emitted because intent filtering is not yet applied.
func TestIngestHandler_IntentsAcceptedDocumentOrder(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	intents := []string{"errors", "config"}
	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "s", Content: "alpha\n\nbeta", Intents: &intents})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Summary) != 2 {
		t.Fatalf("summary = %d; want 2 (document order)", len(resp.JSON200.Summary))
	}
	// Smoke ignores intents: no per-section matches signal.
	for i, s := range resp.JSON200.Summary {
		if s.Matches != nil {
			t.Errorf("summary[%d].matches = %v; want absent (intents not processed)", i, *s.Matches)
		}
	}
}

// Presenting a session token to a namespace different from the one that minted it returns 404
// (namespace-isolation: cross-namespace sessions are invisible).
func TestIngestHandler_CrossNamespaceInvisible404(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testTenantANamespace)

	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "s", Content: "c"})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// A request body exceeding 1 MB is rejected with 413 before reaching the handler.
func TestIngestHandler_PayloadTooLarge413(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	big := strings.Repeat("x", 1024*1024+1024) // > 1MB body cap
	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "s", Content: big})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", resp.StatusCode())
	}
}

// A freshly created session with no ingested content returns an empty sources list.
func TestListSourcesHandler_Empty(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	resp, err := client.ListSourcesWithResponse(context.Background(),
		&genapi.ListSourcesParams{XCALMSessionToken: sess.SessionToken})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Sources) != 0 {
		t.Errorf("sources = %d; want 0", len(resp.JSON200.Sources))
	}
}

// Listing sources with a session token from a different namespace returns 404 (namespace-isolation).
func TestListSourcesHandler_CrossNamespaceInvisible404(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testTenantANamespace)

	resp, err := client.ListSourcesWithResponse(context.Background(),
		&genapi.ListSourcesParams{XCALMSessionToken: sess.SessionToken})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 body=%s", resp.StatusCode(), string(resp.Body))
	}
}
