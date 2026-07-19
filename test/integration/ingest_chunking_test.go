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

// Workload ingests a JSONL eval-results body; format-aware chunking packs the
// small records into a range-titled section and gives a large record its own
// discriminating-field title — neither is blank-line paragraph shape.
func TestIngestFormatAwareChunking(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	body := `{"id": "eval-001", "task": "refund-flow", "score": 0.91}` + "\n" +
		`{"id": "eval-002", "task": "sku-lookup", "score": 0.42}` + "\n" +
		`{"id": "eval-003", "task": "schema-following", "score": 1.0}` + "\n" +
		fmt.Sprintf(`{"id": "eval-004", "task": "long-completion", "completion": %q}`,
			strings.Repeat("the model wrote quite a lot here ", 80))
	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "eval.jsonl", Content: body})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.SectionsTotal != 2 {
		t.Fatalf("sections_total = %d; want packed-small + standalone-large (2)", resp.JSON200.SectionsTotal)
	}
	if resp.JSON200.Summary[0].Title != "records 1-3" {
		t.Errorf("summary[0].title = %q; want records 1-3", resp.JSON200.Summary[0].Title)
	}
	if resp.JSON200.Summary[1].Title != "id=eval-004" {
		t.Errorf("summary[1].title = %q; want the large record's discriminating field", resp.JSON200.Summary[1].Title)
	}
}

// Chunking beyond the 500-section cap indexes exactly the first 500:
// sections_total reports everything produced, sections_indexed the stored
// count, and the stored chunk count agrees.
func TestIngestTruncatesAtChunkCap(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	var sb strings.Builder
	for i := range 520 {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		fmt.Fprintf(&sb, "paragraph number %d with a little padding text", i)
	}
	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "huge.txt", Content: sb.String()})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.SectionsTotal != 520 || resp.JSON200.SectionsIndexed != 500 {
		t.Fatalf("total/indexed = %d/%d; want 520/500",
			resp.JSON200.SectionsTotal, resp.JSON200.SectionsIndexed)
	}
	if !resp.JSON200.SummaryTruncated {
		t.Error("summary_truncated = false; want true")
	}

	list, err := client.ListSourcesWithResponse(context.Background(),
		&genapi.ListSourcesParams{XCALMSessionToken: sess.SessionToken})
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if list.StatusCode() != http.StatusOK || len(list.JSON200.Sources) != 1 {
		t.Fatalf("sources = %+v", list.JSON200)
	}
	if list.JSON200.Sources[0].Chunks != 500 {
		t.Errorf("stored chunks = %d; want exactly 500", list.JSON200.Sources[0].Chunks)
	}
}

// A fenced code block inside a markdown ingest is indexed as a code chunk:
// searching the exact identifier from inside the fence must hit — the
// identifier-preserving tokenizer proof that per-chunk content_type overrides
// reach both the FTS index and the vocabulary.
func TestIngestCodeFence_IdentifierSearchable(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	body := "# Handlers\n\nThe user lookup path.\n\n```go\nfunc getUserByIdZq(id int64) {}\n```\n"
	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "handlers.md", Content: body})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}

	search, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"getUserByIdZq"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if search.StatusCode() != http.StatusOK {
		t.Fatalf("search status = %d body=%s", search.StatusCode(), string(search.Body))
	}
	hits := search.JSON200.Results[0].Hits
	if len(hits) == 0 {
		t.Fatalf("identifier search returned no hits; code chunk not routed to the identifier-preserving index")
	}
	if !strings.Contains(hits[0].Snippet, "getUserByIdZq") {
		t.Errorf("hit snippet = %q; want the exact identifier", hits[0].Snippet)
	}
}
