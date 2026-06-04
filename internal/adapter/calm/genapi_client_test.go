// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func fakeCALM(t *testing.T, h http.HandlerFunc) calm.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := calm.NewGenapiClient(srv.URL, "test-key", "idem-key")
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	return c
}

func jsonResp(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func TestGenapiCreateSession_SendsKeysAndReturnsToken(t *testing.T) {
	var gotIdem, gotAPIKey string
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotIdem = r.Header.Get("Idempotency-Key")
		gotAPIKey = r.Header.Get("X-CALM-API-Key")
		jsonResp(w, http.StatusCreated, `{"session_token":"tok-1","client":"calm-adapter","namespace":"ns","ttl_minutes":60,"created_at":"2026-01-01T00:00:00Z"}`)
	})

	tok, err := c.CreateSession(context.Background(), "calm-adapter", 60)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if tok != "tok-1" {
		t.Errorf("token = %q; want tok-1", tok)
	}
	if gotIdem != "idem-key" {
		t.Errorf("Idempotency-Key = %q; want idem-key", gotIdem)
	}
	if gotAPIKey != "test-key" {
		t.Errorf("X-CALM-API-Key = %q; want test-key", gotAPIKey)
	}
}

func TestGenapiCreateSession_ErrorStatus(t *testing.T) {
	c := fakeCALM(t, func(w http.ResponseWriter, _ *http.Request) {
		jsonResp(w, http.StatusInternalServerError, `{"error":"boom"}`)
	})
	if _, err := c.CreateSession(context.Background(), "", 60); err == nil {
		t.Fatal("CreateSession: want error on 500, got nil")
	}
}

func TestGenapiDeleteSession(t *testing.T) {
	var gotToken string
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/sessions" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotToken = r.Header.Get("X-CALM-Session-Token")
		jsonResp(w, http.StatusOK, `{"cascaded":{"sources":0,"chunks":0,"events":0,"labels":0}}`)
	})
	if err := c.DeleteSession(context.Background(), "sess-tok"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if gotToken != "sess-tok" {
		t.Errorf("X-CALM-Session-Token = %q; want sess-tok", gotToken)
	}
}

func TestGenapiIngest_MapsSummary(t *testing.T) {
	var gotToken string
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/ingest" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotToken = r.Header.Get("X-CALM-Session-Token")
		jsonResp(w, http.StatusOK, `{"source":"s","sections_indexed":1,"sections_total":2,"summary":[{"title":"sec","preview":"prev"}],"summary_truncated":true,"distinctive_terms":["alpha"]}`)
	})
	got, err := c.Ingest(context.Background(), "sess-tok", calm.IngestInput{
		Source: "s", Content: "c", ContentType: "code", Format: "log",
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if gotToken != "sess-tok" {
		t.Errorf("session token = %q; want sess-tok", gotToken)
	}
	if got.Source != "s" || got.SectionsIndexed != 1 || got.SectionsTotal != 2 || !got.SummaryTruncated {
		t.Errorf("summary scalars = %+v", got)
	}
	if len(got.Sections) != 1 || got.Sections[0].Title != "sec" || got.Sections[0].Preview != "prev" {
		t.Errorf("sections = %+v; want [{sec prev}]", got.Sections)
	}
	if len(got.DistinctiveTerms) != 1 || got.DistinctiveTerms[0] != "alpha" {
		t.Errorf("distinctive terms = %+v", got.DistinctiveTerms)
	}
}

func TestGenapiSearch_MapsHits(t *testing.T) {
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/search" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		jsonResp(w, http.StatusOK, `{"results":[{"query":"q","hits":[{"title":"t","snippet":"sn","source":"src","match_layer":"primary"}]}]}`)
	})
	got, err := c.Search(context.Background(), "sess-tok", calm.SearchInput{Queries: []string{"q"}, Source: "src", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got.Queries) != 1 || got.Queries[0].Query != "q" || len(got.Queries[0].Hits) != 1 {
		t.Fatalf("results = %+v", got)
	}
	h := got.Queries[0].Hits[0]
	if h.Title != "t" || h.Snippet != "sn" || h.Source != "src" || h.MatchLayer != "primary" {
		t.Errorf("hit = %+v", h)
	}
}

func TestGenapiWriteEvents(t *testing.T) {
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/events" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		jsonResp(w, http.StatusAccepted, `{"accepted":1}`)
	})
	err := c.WriteEvents(context.Background(), "sess-tok", []calm.EventInput{
		{Type: "tool_invocation", Priority: 3, Data: map[string]any{"k": "v"}},
	})
	if err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
}
