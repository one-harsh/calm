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

func TestIngestHandler_Happy(t *testing.T) {
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

func TestIngestHandler_IdempotentReingest(t *testing.T) {
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

func TestIngestHandler_SummaryTruncatedAt50(t *testing.T) {
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

func TestIngestHandler_EmptyContentZeroSections(t *testing.T) {
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

func TestIngestHandler_IntentsAcceptedDocumentOrder(t *testing.T) {
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

func TestIngestHandler_CrossNamespaceInvisible404(t *testing.T) {
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

func TestIngestHandler_PayloadTooLarge413(t *testing.T) {
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

func TestListSourcesHandler_Empty(t *testing.T) {
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

func TestListSourcesHandler_CrossNamespaceInvisible404(t *testing.T) {
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
