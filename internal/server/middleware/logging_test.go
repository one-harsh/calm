// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	logging "github.com/one-harsh/context-logging"
)

func TestResponseWriter_DefaultStatusIs200(t *testing.T) {
	rec := httptest.NewRecorder()
	ww := wrapResponseWriter(rec)
	if ww.status != http.StatusOK {
		t.Errorf("default status = %d; want 200 (matches stdlib http.ResponseWriter default)", ww.status)
	}
}

func TestResponseWriter_WriteHeaderCapturesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	ww := wrapResponseWriter(rec)
	ww.WriteHeader(http.StatusTeapot)
	if ww.status != http.StatusTeapot {
		t.Errorf("captured status = %d; want 418", ww.status)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("underlying recorder status = %d; want 418 (must propagate)", rec.Code)
	}
}

func TestResponseWriter_WriteAccumulatesBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	ww := wrapResponseWriter(rec)
	n1, _ := ww.Write([]byte("hello "))
	n2, _ := ww.Write([]byte("world"))
	if ww.bytesWritten != n1+n2 {
		t.Errorf("bytesWritten = %d; want %d (sum of individual writes)", ww.bytesWritten, n1+n2)
	}
	if rec.Body.String() != "hello world" {
		t.Errorf("body = %q; want 'hello world'", rec.Body.String())
	}
}

func TestLogging_PassesThroughAndPreservesStatusAndBody(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created-body"))
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	rec := httptest.NewRecorder()
	Logging(logging.Nop())(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d; want 201", rec.Code)
	}
	if rec.Body.String() != "created-body" {
		t.Errorf("body = %q; want created-body", rec.Body.String())
	}
}

func TestLogging_DownstreamWithoutExplicitStatusGets200(t *testing.T) {
	// Handler doesn't call WriteHeader → wrapper keeps default 200, which
	// matches the stdlib behavior. Logging must not crash and must not
	// over-write the status.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("implicit-200"))
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	Logging(logging.Nop())(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200 (default)", rec.Code)
	}
}
