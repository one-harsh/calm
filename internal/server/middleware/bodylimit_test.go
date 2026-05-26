// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBodySizeLimit_UnderCapPassesThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		body, _ := io.ReadAll(r.Body)
		if string(body) != "small" {
			t.Errorf("downstream got body %q; want small", string(body))
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("small"))
	rec := httptest.NewRecorder()
	BodySizeLimit(1024)(next).ServeHTTP(rec, req)

	if !called {
		t.Fatal("downstream not called for under-cap body")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}

func TestBodySizeLimit_DeclaredContentLengthOverCapRejected_413(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream must not be called when ContentLength exceeds cap")
	})

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("doesntmatter"))
	req.ContentLength = 2048
	rec := httptest.NewRecorder()
	BodySizeLimit(1024)(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body not JSON: %v (body=%q)", err, rec.Body.String())
	}
	if body["error"] != "payload too large" {
		t.Errorf("error field = %v; want 'payload too large'", body["error"])
	}
	// max_bytes is encoded as JSON number → float64 after Unmarshal.
	if got, _ := body["max_bytes"].(float64); got != 1024 {
		t.Errorf("max_bytes = %v; want 1024", body["max_bytes"])
	}
}

func TestBodySizeLimit_StreamedOverCapTrippedByMaxBytesReader(t *testing.T) {
	// ContentLength=-1 simulates unknown-length streaming (chunked transfer).
	// The Content-Length short-circuit can't catch this; MaxBytesReader fires
	// when the downstream actually reads past the cap.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil {
			t.Error("downstream io.ReadAll returned nil err; want MaxBytesError on oversize stream")
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(strings.Repeat("x", 4096)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	BodySizeLimit(1024)(next).ServeHTTP(rec, req)
}
