// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func fakeCALM(t *testing.T, h http.HandlerFunc) calm.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := calm.NewGenapiClient(srv.URL, "test-key", nil)
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

	tok, err := c.CreateSession(context.Background(), "calm-adapter", 60, "idem-key")
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
	if _, err := c.CreateSession(context.Background(), "", 60, ""); err == nil {
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
		Source: "s", Content: "c", ContentType: "code", Format: calm.FormatLog,
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

// Pins the request-body shape per mode: document-order omits the queries key
// entirely (CALM selects the mode by its absence) and carries offset and
// budget_bytes; ranked omits offset and, when unset, budget_bytes.
func TestGenapiSearch_BodyShapePerMode(t *testing.T) {
	var bodies []map[string]any
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		bodies = append(bodies, m)
		jsonResp(w, http.StatusOK, `{"results":[],"budget_bytes":4096,"byte_budget_used":0,"budget_exceeded":false}`)
	})

	if _, err := c.Search(context.Background(), "tok", calm.SearchInput{Source: "src", Offset: 3, BudgetBytes: 512}); err != nil {
		t.Fatalf("document-order Search: %v", err)
	}
	if _, err := c.Search(context.Background(), "tok", calm.SearchInput{Queries: []string{"q"}, Offset: 7}); err != nil {
		t.Fatalf("ranked Search: %v", err)
	}

	docOrder, ranked := bodies[0], bodies[1]
	if _, present := docOrder["queries"]; present {
		t.Errorf("document-order body must omit the queries key; got %v", docOrder)
	}
	if docOrder["offset"] != float64(3) || docOrder["budget_bytes"] != float64(512) {
		t.Errorf("document-order body = %v; want offset 3 and budget_bytes 512", docOrder)
	}
	if _, present := ranked["offset"]; present {
		t.Errorf("ranked body must omit offset; got %v", ranked)
	}
	if _, present := ranked["budget_bytes"]; present {
		t.Errorf("ranked body must omit budget_bytes when unset; got %v", ranked)
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

// Non-2xx statuses classify through the sentinel scheme: 404 is
// ErrSessionNotFound (the AD03 recovery trigger), 401/403 is ErrAuthRejected,
// and anything else is a bare *StatusError matching neither sentinel.
func TestGenapiStatusErrors_SentinelClassification(t *testing.T) {
	ops := map[string]func(calm.Client) error{
		"ingest": func(c calm.Client) error {
			_, err := c.Ingest(context.Background(), "tok", calm.IngestInput{Source: "s", Content: "c"})
			return err
		},
		"search": func(c calm.Client) error {
			_, err := c.Search(context.Background(), "tok", calm.SearchInput{Queries: []string{"q"}})
			return err
		},
		"write events": func(c calm.Client) error {
			return c.WriteEvents(context.Background(), "tok", []calm.EventInput{{Type: "t", Priority: 1}})
		},
		"create session": func(c calm.Client) error {
			_, err := c.CreateSession(context.Background(), "", 60, "")
			return err
		},
	}
	statuses := []struct {
		code            int
		wantSessionLost bool
		wantAuth        bool
	}{
		{404, true, false},
		{401, false, true},
		{403, false, true},
		{500, false, false},
	}
	for name, op := range ops {
		for _, st := range statuses {
			t.Run(fmt.Sprintf("%s_%d", name, st.code), func(t *testing.T) {
				c := fakeCALM(t, func(w http.ResponseWriter, _ *http.Request) {
					jsonResp(w, st.code, `{"error":"x"}`)
				})
				err := op(c)
				if err == nil {
					t.Fatalf("want error on %d, got nil", st.code)
				}
				if got := errors.Is(err, calm.ErrSessionNotFound); got != st.wantSessionLost {
					t.Errorf("errors.Is(ErrSessionNotFound) = %v; want %v (err=%v)", got, st.wantSessionLost, err)
				}
				if got := errors.Is(err, calm.ErrAuthRejected); got != st.wantAuth {
					t.Errorf("errors.Is(ErrAuthRejected) = %v; want %v (err=%v)", got, st.wantAuth, err)
				}
				var se *calm.StatusError
				if !errors.As(err, &se) || se.Code != st.code {
					t.Errorf("errors.As(*StatusError) code = %+v; want Code %d", se, st.code)
				}
			})
		}
	}
}

// An empty per-call idempotency key sends no Idempotency-Key header — the
// caller controls retry-dedup per create, not the client construction.
func TestGenapiCreateSession_EmptyKeyOmitsHeader(t *testing.T) {
	var sawHeader bool
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header[http.CanonicalHeaderKey("Idempotency-Key")]
		jsonResp(w, http.StatusCreated, `{"session_token":"tok-1","client":"calm-adapter","namespace":"ns","ttl_minutes":60,"created_at":"2026-01-01T00:00:00Z"}`)
	})
	if _, err := c.CreateSession(context.Background(), "", 60, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sawHeader {
		t.Error("Idempotency-Key header sent despite empty key")
	}
}

const testCorrelationID = "018f0000-0000-7000-8000-000000000000"

// Feedback posts the parsed correlation id and outcome under the session token
// and treats 204 as acceptance.
func TestGenapiFeedback_PostsAndAccepts(t *testing.T) {
	var gotToken, gotBody string
	c := fakeCALM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/feedback" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotToken = r.Header.Get("X-CALM-Session-Token")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Feedback(context.Background(), "sess-tok", testCorrelationID, "success"); err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if gotToken != "sess-tok" {
		t.Errorf("X-CALM-Session-Token = %q; want sess-tok", gotToken)
	}
	if !strings.Contains(gotBody, testCorrelationID) || !strings.Contains(gotBody, "success") {
		t.Errorf("request body = %q; want correlation id + outcome", gotBody)
	}
}

// A malformed ref is rejected as a 400 StatusError without a round-trip.
func TestGenapiFeedback_MalformedRefNoRoundTrip(t *testing.T) {
	c := fakeCALM(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("CALM must not be called for a malformed feedback ref")
	})
	err := c.Feedback(context.Background(), "sess-tok", "not-a-uuid", "success")
	var se *calm.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusBadRequest {
		t.Fatalf("err = %v; want *StatusError code 400", err)
	}
}

// Rejection statuses surface as *StatusError carrying the code so the shell can
// phrase 409/410 distinctly.
func TestGenapiFeedback_StatusErrors(t *testing.T) {
	for _, code := range []int{http.StatusConflict, http.StatusGone} {
		c := fakeCALM(t, func(w http.ResponseWriter, _ *http.Request) {
			jsonResp(w, code, `{"error":"nope"}`)
		})
		err := c.Feedback(context.Background(), "sess-tok", testCorrelationID, "retry")
		var se *calm.StatusError
		if !errors.As(err, &se) || se.Code != code {
			t.Errorf("code %d: err = %v; want *StatusError code %d", code, err, code)
		}
	}
}
