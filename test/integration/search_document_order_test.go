// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/db"
)

func strPtr(s string) *string { return &s }

func metaString(t *testing.T, meta []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		t.Fatalf("unmarshal request_meta: %v", err)
	}
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("request_meta[%q] = %v; want a string", key, m[key])
	}
	return s
}

// storedChunks reads a source's chunks in document order straight from the store
// (the ground truth the document-order walk must reproduce).
func storedChunks(t *testing.T, sessionID int64, source string) []db.DocChunk {
	t.Helper()
	got, _, err := env.store.Sources().ChunksInOrder(context.Background(), testNamespace,
		db.DocOrderInput{SessionID: sessionID, Source: source, Limit: 1000, Offset: 0})
	if err != nil {
		t.Fatalf("storedChunks: %v", err)
	}
	return got
}

func wholeChunkSize(t *testing.T, c db.DocChunk) int {
	t.Helper()
	return wireHitSize(t, genapi.SearchHit{
		Title: c.Title, Snippet: c.Content, Source: c.Source, MatchLayer: genapi.Document,
	})
}

// walkDocumentOrder follows next_offset until it is absent, concatenating each
// page's snippets in order — the sequential-reread the adapter performs.
func walkDocumentOrder(t *testing.T, token, source string, limit, budget, maxPages int) (snippets []string, sawTruncated bool) {
	t.Helper()
	offset := 0
	for pages := 0; pages <= maxPages; pages++ {
		resp, err := env.client.SearchWithResponse(context.Background(),
			&genapi.SearchParams{XCALMSessionToken: token},
			genapi.SearchJSONRequestBody{
				Source: strPtr(source), Limit: ptrInt(limit), Offset: ptrInt(offset), BudgetBytes: ptrInt(budget),
			})
		if err != nil {
			t.Fatalf("walk search: %v", err)
		}
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("walk status = %d body=%s", resp.StatusCode(), string(resp.Body))
		}
		res := resp.JSON200
		if len(res.Results) != 1 || res.Results[0].Query != "" {
			t.Fatalf("document response = %+v; want one empty-query result", res.Results)
		}
		for _, h := range res.Results[0].Hits {
			if string(h.MatchLayer) != "document" {
				t.Errorf("hit match_layer = %q; want document", h.MatchLayer)
			}
			if h.Truncated != nil && *h.Truncated {
				sawTruncated = true
			}
			snippets = append(snippets, h.Snippet)
		}
		if res.NextOffset == nil {
			return snippets, sawTruncated
		}
		if len(res.Results[0].Hits) == 0 && *res.NextOffset == offset {
			t.Fatalf("empty page with non-advancing next_offset %d — would wedge", offset)
		}
		offset = *res.NextOffset
	}
	t.Fatalf("walk did not terminate within %d pages", maxPages)
	return nil, false
}

// The mode's promise: a small-budget paginated walk and a large-budget walk both
// reassemble to the byte-identical concatenation of the source's stored chunks —
// a workload rereads a long captured output in flow with no loss and no overlap.
func TestSearchDocumentOrder_WalkReassemblesCapturedOutput(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	var paras []string
	for i := 0; i < 6; i++ {
		paras = append(paras, strings.Repeat("line "+string(rune('a'+i))+" ", 40))
	}
	content := strings.Join(paras, "\n\n")
	ingestForSearch(t, client, sess.SessionToken, "capture.log", content)

	ground := storedChunks(t, sess.ID, "capture.log")
	if len(ground) < 3 {
		t.Fatalf("ground truth produced %d chunks; want >= 3 to exercise pagination", len(ground))
	}
	var truth []string
	maxSize := 0
	for _, c := range ground {
		truth = append(truth, c.Content)
		if s := wholeChunkSize(t, c); s > maxSize {
			maxSize = s
		}
	}

	// smallBudget >= the largest chunk (so no chunk truncates) but well below the
	// total (so the walk paginates). bigBudget admits every chunk per page.
	small, _ := walkDocumentOrder(t, sess.SessionToken, "capture.log", 5, maxSize+16, len(ground)+4)
	big, _ := walkDocumentOrder(t, sess.SessionToken, "capture.log", 50, testSearchMaxBudgetBytes, len(ground)+4)

	if strings.Join(small, "") != strings.Join(truth, "") {
		t.Errorf("small-budget walk did not reassemble the stored chunks byte-identically")
	}
	if strings.Join(big, "") != strings.Join(truth, "") {
		t.Errorf("big-budget walk did not reassemble the stored chunks byte-identically")
	}
}

// A single over-budget lead chunk returns an exact-text prefix flagged truncated,
// and next_offset advances past it — reread never wedges on an oversized chunk.
func TestSearchDocumentOrder_TruncatedFlagObservable(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	// A large lead paragraph (single chunk under chunkMaxBytes) followed by a
	// second chunk: truncating the head leaves a remainder, so next_offset
	// advances past it rather than terminating the walk.
	content := strings.Repeat("lorem ipsum dolor sit amet ", 120) + "\n\ntail paragraph after the big one"
	ingestForSearch(t, client, sess.SessionToken, "big.log", content)

	ground := storedChunks(t, sess.ID, "big.log")
	if len(ground) < 2 {
		t.Fatalf("stored %d chunks; want >= 2 (large head + trailing chunk)", len(ground))
	}
	firstWhole := wholeChunkSize(t, ground[0])
	budget := firstWhole / 2 // fits a prefix, not the whole lead chunk.

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Source: strPtr("big.log"), Offset: ptrInt(0), BudgetBytes: ptrInt(budget)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	hits := resp.JSON200.Results[0].Hits
	if len(hits) != 1 {
		t.Fatalf("hits = %d; want 1 truncated lead chunk", len(hits))
	}
	h := hits[0]
	if h.Truncated == nil || !*h.Truncated {
		t.Fatalf("truncated = %v; want true", h.Truncated)
	}
	if !strings.HasPrefix(ground[0].Content, h.Snippet) || len(h.Snippet) >= len(ground[0].Content) {
		t.Error("snippet is not a strict exact-text prefix of the stored chunk (content-fidelity)")
	}
	if !resp.JSON200.BudgetExceeded {
		t.Error("budget_exceeded = false; want true")
	}
	if resp.JSON200.NextOffset == nil || *resp.JSON200.NextOffset != 1 {
		t.Errorf("next_offset = %v; want 1 (advances past the truncated chunk)", resp.JSON200.NextOffset)
	}
	if resp.JSON200.ByteBudgetUsed > budget {
		t.Errorf("byte_budget_used %d overshot budget %d", resp.JSON200.ByteBudgetUsed, budget)
	}
}

// Supplying both queries and source keeps ranked retrieval: match_layer is a
// ranked layer, offset is ignored, and no next_offset appears.
func TestSearchDocumentOrder_QueriesPlusSourceStaysRanked(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "build.log", "the build failed with a fatal linker error")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}, Source: strPtr("build.log"), Offset: ptrInt(9999)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.NextOffset != nil {
		t.Errorf("next_offset = %d; want absent on a ranked response (offset ignored)", *resp.JSON200.NextOffset)
	}
	hits := resp.JSON200.Results[0].Hits
	if len(hits) == 0 {
		t.Fatal("ranked search returned no hits; offset must be ignored, not applied")
	}
	for _, h := range hits {
		if string(h.MatchLayer) == "document" {
			t.Errorf("match_layer = document on a ranked (queries+source) call; want primary/trigram")
		}
	}
}

// A document-order call records a correlation row whose request_meta carries
// mode=document and hits_document == hit_count, with no allocator dimension.
func TestSearchDocumentOrder_CorrelationCarriesModeDocument(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "cap.log", "alpha\n\nbeta\n\ngamma")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Source: strPtr("cap.log")})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}

	correlationID := mustParseCorrelationID(t, resp.HTTPResponse.Header.Get("X-CALM-Correlation-Id"))
	row := readCorrelationRow(t, correlationID)
	if row.requestType != "search" {
		t.Errorf("request_type = %q; want search", row.requestType)
	}
	if !hasKey(t, row.requestMeta, "mode") || metaString(t, row.requestMeta, "mode") != "document" {
		t.Errorf("request_meta = %s; want mode=document", row.requestMeta)
	}
	hitsDoc := metaInt(t, row.requestMeta, "hits_document")
	hitCount := metaInt(t, row.requestMeta, "hit_count")
	if hitsDoc == 0 || hitsDoc != hitCount {
		t.Errorf("hits_document = %d, hit_count = %d; want equal and non-zero", hitsDoc, hitCount)
	}
	if hasKey(t, row.requestMeta, "allocator") {
		t.Errorf("request_meta = %s; document mode must carry no allocator dimension", row.requestMeta)
	}
}

// The relaxed contract accepts a queries-less source-scoped call with 200 — the
// OpenAPI validator no longer rejects it as a missing required field.
func TestSearchDocumentOrder_QueriesOmittedAcceptedPostRelax(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "cap.log", "alpha\n\nbeta")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Source: strPtr("cap.log")})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200 (queries now optional) body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// A negative offset is rejected 400 by the OpenAPI validator (minimum: 0) before
// reaching the handler.
func TestSearchDocumentOrder_NegativeOffsetRejected400(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Source: strPtr("cap.log"), Offset: ptrInt(-1)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (offset minimum 0) body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// A large heading-free markdown section maps to a single span that the stored
// bound splits into several chunks. With no limit and a mid-size budget, the
// page count is driven by the budget, every page stays within it, and the pages
// reassemble byte-identically to the stored content.
func TestSearchDocumentOrder_LargeCaptureFillsToBudgetAndPages(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	var b strings.Builder
	b.WriteString("# Large Capture\n\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "line %04d: the quick brown fox jumps over the lazy dog repeatedly\n", i)
	}
	mdFormat := genapi.Markdown
	ir, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "big.md", Content: b.String(), Format: &mdFormat})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if ir.StatusCode() != http.StatusOK {
		t.Fatalf("ingest status = %d body=%s", ir.StatusCode(), string(ir.Body))
	}

	ground := storedChunks(t, sess.ID, "big.md")
	if len(ground) < 3 {
		t.Fatalf("stored %d chunks; want the bound to split the section into several", len(ground))
	}
	var truth strings.Builder
	maxSize := 0
	for _, c := range ground {
		truth.WriteString(c.Content)
		if s := wholeChunkSize(t, c); s > maxSize {
			maxSize = s
		}
	}

	// Several whole chunks per page (page fills toward the budget, not a fixed
	// count), none large enough to truncate, well below the total so it paginates.
	budget := 3 * maxSize
	offset := 0
	var got strings.Builder
	for pages := 0; ; pages++ {
		if pages > len(ground)+4 {
			t.Fatalf("walk did not terminate within %d pages", pages)
		}
		resp, err := client.SearchWithResponse(context.Background(),
			&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
			genapi.SearchJSONRequestBody{Source: strPtr("big.md"), Offset: ptrInt(offset), BudgetBytes: ptrInt(budget)})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("search status = %d body=%s", resp.StatusCode(), string(resp.Body))
		}
		res := resp.JSON200
		if res.ByteBudgetUsed > budget {
			t.Errorf("page byte_budget_used %d exceeds budget %d", res.ByteBudgetUsed, budget)
		}
		for _, h := range res.Results[0].Hits {
			if h.Truncated != nil && *h.Truncated {
				t.Errorf("unexpected truncation: budget %d fits every whole chunk", budget)
			}
			got.WriteString(h.Snippet)
		}
		if res.NextOffset == nil {
			break
		}
		offset = *res.NextOffset
	}
	if got.String() != truth.String() {
		t.Error("paged fill-mode walk did not reassemble the stored content byte-identically")
	}

	// A high budget with no limit fills the whole capture in one page.
	full, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Source: strPtr("big.md"), BudgetBytes: ptrInt(testSearchMaxBudgetBytes)})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if full.JSON200.NextOffset != nil {
		t.Errorf("next_offset = %d; want absent — a high budget fills the whole capture in one page", *full.JSON200.NextOffset)
	}
	if len(full.JSON200.Results[0].Hits) != len(ground) {
		t.Errorf("hits = %d; want all %d chunks on one budget-filled page", len(full.JSON200.Results[0].Hits), len(ground))
	}
}

// A single newline-free oversized value (a minified-JSON member) is the reread
// pathology this hardening kills: the stored bound rune-splits it into several
// pageable chunks, and a no-limit walk pages it to completion, reassembling the
// stored bytes exactly with no page over budget.
func TestSearchDocumentOrder_NewlineFreeBlobPagesToCompletion(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	blob := strings.Repeat("a", 90000) // one minified value, no newlines
	content := `{"blob": "` + blob + `"}`
	jsonFormat := genapi.Json
	ir, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sess.SessionToken},
		genapi.IngestJSONRequestBody{Source: "blob.json", Content: content, Format: &jsonFormat})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if ir.StatusCode() != http.StatusOK {
		t.Fatalf("ingest status = %d body=%s", ir.StatusCode(), string(ir.Body))
	}

	ground := storedChunks(t, sess.ID, "blob.json")
	if len(ground) < 3 {
		t.Fatalf("stored %d chunks; want the newline-free value rune-split into several", len(ground))
	}
	var truth strings.Builder
	maxSize := 0
	for _, c := range ground {
		truth.WriteString(c.Content)
		if s := wholeChunkSize(t, c); s > maxSize {
			maxSize = s
		}
	}

	budget := 3 * maxSize
	offset := 0
	var got strings.Builder
	for pages := 0; ; pages++ {
		if pages > len(ground)+4 {
			t.Fatalf("walk did not terminate within %d pages", pages)
		}
		resp, err := client.SearchWithResponse(context.Background(),
			&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
			genapi.SearchJSONRequestBody{Source: strPtr("blob.json"), Offset: ptrInt(offset), BudgetBytes: ptrInt(budget)})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("search status = %d body=%s", resp.StatusCode(), string(resp.Body))
		}
		res := resp.JSON200
		if res.ByteBudgetUsed > budget {
			t.Errorf("page byte_budget_used %d exceeds budget %d", res.ByteBudgetUsed, budget)
		}
		for _, h := range res.Results[0].Hits {
			got.WriteString(h.Snippet)
		}
		if res.NextOffset == nil {
			break
		}
		offset = *res.NextOffset
	}
	if got.String() != truth.String() {
		t.Error("paged walk did not reassemble the stored newline-free blob byte-identically")
	}
}

// An explicit limit is a hard cap even when the budget could fit far more: with
// a high budget the page still stops at the limit and next_offset advances, so
// callers keep count control independent of the byte budget.
func TestSearchDocumentOrder_ExplicitLimitCapsBelowBudget(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "cap.log", "alpha\n\nbeta\n\ngamma\n\ndelta")

	ground := storedChunks(t, sess.ID, "cap.log")
	if len(ground) < 3 {
		t.Fatalf("stored %d chunks; want >= 3 so a limit of 2 caps below the total", len(ground))
	}

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Source: strPtr("cap.log"), Limit: ptrInt(2), BudgetBytes: ptrInt(testSearchMaxBudgetBytes)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	res := resp.JSON200
	if len(res.Results[0].Hits) != 2 {
		t.Fatalf("hits = %d; want exactly 2 (explicit limit caps below the budget)", len(res.Results[0].Hits))
	}
	if res.BudgetExceeded {
		t.Error("budget_exceeded = true; want false (the limit bound the page, not the budget)")
	}
	if res.NextOffset == nil || *res.NextOffset != 2 {
		t.Errorf("next_offset = %v; want 2 (limit stopped the page with chunks remaining)", res.NextOffset)
	}
}
