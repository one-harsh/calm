// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func ingestForSearch(t *testing.T, client *genapi.ClientWithResponses, token, source, content string) {
	t.Helper()
	r, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: token},
		genapi.IngestJSONRequestBody{Source: source, Content: content})
	if err != nil {
		t.Fatalf("ingest %q: %v", source, err)
	}
	if r.StatusCode() != http.StatusOK {
		t.Fatalf("ingest %q: status %d body=%s", source, r.StatusCode(), string(r.Body))
	}
}

// Workload ingests a document then searches it via the HTTP API; expects a 200 response with
// a hit whose source, match_layer, and snippet (containing the query term) are all correct.
func TestSearchHandler_Happy(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "build.log", "the build failed with a fatal linker error")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Results) != 1 || resp.JSON200.Results[0].Query != "linker" {
		t.Fatalf("results = %+v; want one element echoing query \"linker\"", resp.JSON200.Results)
	}
	hits := resp.JSON200.Results[0].Hits
	if len(hits) == 0 {
		t.Fatal("want at least one hit")
	}
	h := hits[0]
	if h.Source != "build.log" || h.MatchLayer != genapi.Primary {
		t.Errorf("hit = %+v; want source build.log, match_layer primary", h)
	}
	if !strings.Contains(strings.ToLower(h.Snippet), "linker") {
		t.Errorf("snippet %q does not contain the query", h.Snippet)
	}
}

// Source filter restricts results to the named source and limit caps the hit count; both
// controls compose without error when applied together.
func TestSearchHandler_SourceFilterAndLimit(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "a.log", "needle one\n\nneedle two\n\nneedle three")
	ingestForSearch(t, client, sess.SessionToken, "b.log", "needle elsewhere")

	source := "a.log"
	limit := 2
	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"needle"}, Source: &source, Limit: &limit})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	hits := resp.JSON200.Results[0].Hits
	if len(hits) != 2 {
		t.Fatalf("hits = %d; want 2 (limit)", len(hits))
	}
	for _, h := range hits {
		if h.Source != "a.log" {
			t.Errorf("hit source %q; want only a.log (source filter)", h.Source)
		}
	}
}

// Multi-query response preserves input order; a query with no matches returns an empty hits
// list rather than being omitted.
func TestSearchHandler_MultiQueryOrderedWithEmpty(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "s", "the linker failed")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"linker", "zzz-no-match"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	res := resp.JSON200.Results
	if len(res) != 2 || res[0].Query != "linker" || res[1].Query != "zzz-no-match" {
		t.Fatalf("results = %+v; want [linker, zzz-no-match] in order", res)
	}
	if len(res[0].Hits) == 0 {
		t.Error("first query should have hits")
	}
	if len(res[1].Hits) != 0 {
		t.Errorf("no-match query hits = %d; want empty", len(res[1].Hits))
	}

	// A duplicate query is a separate positional entry, not deduplicated.
	dupResp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"linker", "linker"}})
	if err != nil {
		t.Fatalf("Search (duplicate): %v", err)
	}
	dup := dupResp.JSON200.Results
	if len(dup) != 2 || dup[0].Query != "linker" || dup[1].Query != "linker" {
		t.Fatalf("duplicate results = %+v; want two positional \"linker\" entries", dup)
	}
}

// A session token presented to a different namespace's API key returns 404; the session is
// invisible to the foreign namespace (namespace-isolation).
func TestSearchHandler_CrossNamespaceInvisible404(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testTenantANamespace)

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// An empty queries array is rejected with 400 by the OpenAPI validator before reaching the handler.
func TestSearchHandler_MissingQueriesRejected400(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (validator rejects empty queries) body=%s", resp.StatusCode(), string(resp.Body))
	}
}
