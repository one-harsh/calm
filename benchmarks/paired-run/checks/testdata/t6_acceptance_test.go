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
	"github.com/one-harsh/calm/internal/db"
)

// Self-contained helpers (t6-prefixed) so the oracle never depends on helper
// functions the graded solution might rename. Only stable harness symbols
// (env, createSessionForTest, testNamespace, randHex) are reused.

func t6ptr[T any](v T) *T { return &v }

func t6Ingest(t *testing.T, token, source, content string) {
	t.Helper()
	resp, err := env.client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: token},
		genapi.IngestJSONRequestBody{Source: source, Content: content})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("ingest status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
}

func t6StoredChunks(t *testing.T, sessionID int64, source string) []db.DocChunk {
	t.Helper()
	got, _, err := env.store.Sources().ChunksInOrder(context.Background(), testNamespace,
		db.DocOrderInput{SessionID: sessionID, Source: source, Limit: 100000, Offset: 0})
	if err != nil {
		t.Fatalf("stored chunks: %v", err)
	}
	return got
}

// t6Doc issues a document-order request (source, no queries) with an explicit
// budget and offset and NO limit — the wire shape whose fill the fix must make
// budget-governed.
func t6Doc(t *testing.T, token, source string, budget, offset int) *genapi.SearchResult {
	t.Helper()
	resp, err := env.client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: token},
		genapi.SearchJSONRequestBody{Source: t6ptr(source), BudgetBytes: t6ptr(budget), Offset: t6ptr(offset)})
	if err != nil {
		t.Fatalf("document-order search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("document-order status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Results) != 1 {
		t.Fatalf("document response has %d result blocks; want exactly 1", len(resp.JSON200.Results))
	}
	return resp.JSON200
}

// t6Blob builds a Markdown single-section capture: one heading followed by at
// least minBytes of newline-separated prose with no sub-headings or code fences,
// so the format chunker maps it to one span today. No trailing newline, so the
// stored form equals the input byte-for-byte.
func t6Blob(minBytes int) string {
	var sb strings.Builder
	sb.WriteString("# Capture\n\n")
	for i := 0; sb.Len() < minBytes; i++ {
		fmt.Fprintf(&sb, "line %05d: the quick brown fox jumps over the lazy dog again and again\n", i)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// DEFECT 1 — the ingest chunker must bound stored chunk size so a large
// single-element capture is stored as multiple, byte-exact, pageable chunks.
func TestT6Oracle_OversizedCaptureIsBoundedAndByteExact(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	content := t6Blob(90000)
	source := "t6-oversized-" + randHex(4)
	t6Ingest(t, sess.SessionToken, source, content)

	chunks := t6StoredChunks(t, sess.ID, source)
	if len(chunks) <= 1 {
		t.Fatalf("a %d-byte capture stored as %d chunk(s); a one-chunk capture cannot be paged", len(content), len(chunks))
	}
	const ceiling = 16384 // generous: any passage-sized bound is well under this.
	var joined strings.Builder
	for i, c := range chunks {
		if n := len(c.Content); n > ceiling {
			t.Fatalf("chunk %d is %d bytes, over the %d-byte ceiling — chunk size is not bounded", i, n, ceiling)
		}
		if c.Content == "" || !strings.Contains(content, c.Content) {
			t.Fatalf("chunk %d is not a non-empty substring of the capture", i)
		}
		joined.WriteString(c.Content)
	}
	if joined.String() != content {
		t.Fatalf("concatenation of %d chunks (%d bytes) is not byte-identical to the stored capture (%d bytes)",
			len(chunks), joined.Len(), len(content))
	}

	// A small capture stays a single chunk — the bound never over-splits.
	small := "a short note that easily fits under any reasonable chunk bound"
	ssrc := "t6-small-" + randHex(4)
	t6Ingest(t, sess.SessionToken, ssrc, small)
	if sc := t6StoredChunks(t, sess.ID, ssrc); len(sc) != 1 {
		t.Fatalf("small capture stored as %d chunks; want 1", len(sc))
	}
}

// DEFECT 2 — budget_bytes must govern page fill: a high budget with no limit
// returns as many in-order chunks as fit, not the small default count.
func TestT6Oracle_BudgetGovernsPageFillWithoutLimit(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	var sb strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&sb, "## Section %d\n\nbody text for section %d with a little padding.\n\n", i, i)
	}
	source := "t6-manysections-" + randHex(4)
	t6Ingest(t, sess.SessionToken, source, sb.String())

	stored := t6StoredChunks(t, sess.ID, source)
	if len(stored) <= 5 {
		t.Fatalf("precondition: need >5 stored chunks to exercise fill, got %d", len(stored))
	}

	// High budget (fits every chunk; clamped to the namespace ceiling), no limit.
	doc := t6Doc(t, sess.SessionToken, source, 60000, 0)
	if got := len(doc.Results[0].Hits); got <= 5 {
		t.Fatalf("document-order with a high budget and no limit returned %d hits; budget did not govern fill past the default", got)
	}
}

// INTEGRATION — a large capture pages to completion under a small budget:
// no page exceeds the budget, offsets are monotonic, a budget smaller than a
// chunk yields a truncated verbatim prefix with a continuation cursor, and the
// concatenated pages equal the stored content (no single oversized refetch).
func TestT6Oracle_LargeCapturePagesToCompletion(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	content := t6Blob(90000)
	source := "t6-pageable-" + randHex(4)
	t6Ingest(t, sess.SessionToken, source, content)

	stored := t6StoredChunks(t, sess.ID, source)
	if len(stored) <= 1 {
		t.Fatalf("capture is one chunk (%d bytes); cannot page to completion", len(content))
	}

	// A budget smaller than any single chunk: first page is a truncated verbatim
	// prefix of chunk 0 carrying a continuation cursor.
	tiny := t6Doc(t, sess.SessionToken, source, 200, 0)
	th := tiny.Results[0].Hits
	if len(th) != 1 || th[0].Truncated == nil || !*th[0].Truncated {
		t.Fatalf("tiny budget: want exactly one truncated hit, got %+v", th)
	}
	if th[0].Snippet == "" || !strings.HasPrefix(stored[0].Content, th[0].Snippet) {
		t.Fatalf("truncated snippet is not a verbatim prefix of chunk 0")
	}
	if tiny.NextOffset == nil {
		t.Fatalf("truncated page has no continuation cursor — the tail is unreachable")
	}

	// Page to completion with a budget that fits whole chunks; reassemble.
	const budget = 32000
	offset, lastNext := 0, -1
	var joined strings.Builder
	for page := 0; page < len(stored)+5; page++ {
		doc := t6Doc(t, sess.SessionToken, source, budget, offset)
		if doc.ByteBudgetUsed > budget {
			t.Fatalf("page used %d bytes, over budget %d", doc.ByteBudgetUsed, budget)
		}
		for _, h := range doc.Results[0].Hits {
			if h.Truncated != nil && *h.Truncated {
				t.Fatalf("a whole-chunk-fitting budget still truncated a chunk")
			}
			joined.WriteString(h.Snippet)
		}
		if doc.NextOffset == nil {
			break
		}
		if *doc.NextOffset <= lastNext {
			t.Fatalf("offsets not monotonic: %d after %d", *doc.NextOffset, lastNext)
		}
		lastNext, offset = *doc.NextOffset, *doc.NextOffset
	}
	if joined.String() != content {
		t.Fatalf("paged content (%d bytes) != stored capture (%d bytes)", joined.Len(), len(content))
	}
}
