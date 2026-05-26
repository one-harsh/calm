// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	logging "github.com/one-harsh/context-logging"
)

func TestRecovery_NoPanicPassesThrough(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	Recovery(logging.Nop())(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d; want 202", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q; want ok", rec.Body.String())
	}
}

func TestRecovery_StringPanicReturns500JSON(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	Recovery(logging.Nop())(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", rec.Code)
	}
	if got := rec.Body.String(); got != `{"error":"internal server error"}` {
		t.Errorf("body = %q; want generic 500 JSON", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
}

func TestRecovery_ErrorPanicReturns500JSON(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(errors.New("err panic"))
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	Recovery(logging.Nop())(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", rec.Code)
	}
}

func TestRecovery_NonStringNonErrorPanicStillReturns500(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(42)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()

	Recovery(logging.Nop())(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", rec.Code)
	}
}

func TestToString_KnownTypes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"error", errors.New("boom"), "boom"},
		{"int (unsupported)", 42, ""},
		{"nil (unsupported)", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toString(tc.in); got != tc.want {
				t.Errorf("toString(%v) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
