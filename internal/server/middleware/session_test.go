// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/session"
)

var testRoutes = SessionTokenRoutes{"POST /v1/events": true}

func requestWith(namespace, token string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
	if token != "" {
		req.Header.Set(auth.HeaderSessionToken, token)
	}
	if namespace != "" {
		req = req.WithContext(auth.WithNamespace(req.Context(), namespace))
	}
	return req
}

func TestSessionResolve_RouteNotInAllowlistPassesThrough(t *testing.T) {
	svc := NewMockSessionResolver(t)
	called := false

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	req.Header.Set(auth.HeaderSessionToken, "stray")
	req = req.WithContext(auth.WithNamespace(req.Context(), "ns-a"))
	rec := httptest.NewRecorder()
	SessionResolve(svc, testRoutes, logging.Nop())(okHandlerCalled(&called)).ServeHTTP(rec, req)

	if !called {
		t.Fatal("downstream not called")
	}
}

func TestSessionResolve_NamespaceMissingReturns404(t *testing.T) {
	svc := NewMockSessionResolver(t)
	rec := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
	req.Header.Set(auth.HeaderSessionToken, "tok")
	SessionResolve(svc, testRoutes, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", rec.Code)
	}
}

func TestSessionResolve_UnknownTokenReturns404(t *testing.T) {
	svc := NewMockSessionResolver(t)
	svc.EXPECT().Lookup(mock.Anything, "ns-a", "unknown").Return(session.SessionMetadata{}, db.ErrSessionNotFound).Once()
	rec := httptest.NewRecorder()

	SessionResolve(svc, testRoutes, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, requestWith("ns-a", "unknown"))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d; want 404", rec.Code)
	}
	var body map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["error"] != "session_not_found" {
		t.Errorf("error = %q; want session_not_found", body["error"])
	}
}

func TestSessionResolve_LookupStorageErrorReturns500(t *testing.T) {
	svc := NewMockSessionResolver(t)
	svc.EXPECT().Lookup(mock.Anything, "ns-a", "tok").Return(session.SessionMetadata{}, errors.New("connection refused")).Once()
	rec := httptest.NewRecorder()

	SessionResolve(svc, testRoutes, logging.Nop())(rejectHandler(t)).ServeHTTP(rec, requestWith("ns-a", "tok"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d; want 500", rec.Code)
	}
}

func TestSessionResolve_HappyPathStuffsMetadataAndTouches(t *testing.T) {
	svc := NewMockSessionResolver(t)
	md := session.SessionMetadata{ID: 42, Client: "alice", TTLMinutes: 60, CreatedAt: time.Now()}
	svc.EXPECT().Lookup(mock.Anything, "ns-a", "tok").Return(md, nil).Once()
	svc.EXPECT().Touch(mock.Anything, "ns-a", "tok", mock.Anything).Return(nil).Once()

	var seen session.SessionMetadata
	var seenOK bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenOK = session.MetadataFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	SessionResolve(svc, testRoutes, logging.Nop())(handler).ServeHTTP(rec, requestWith("ns-a", "tok"))

	if !seenOK || seen.ID != 42 {
		t.Errorf("handler saw md=%+v ok=%v; want id=42 ok=true", seen, seenOK)
	}
}

func TestSessionResolve_4xxStillTouches(t *testing.T) {
	svc := NewMockSessionResolver(t)
	svc.EXPECT().Lookup(mock.Anything, "ns-a", "tok").Return(session.SessionMetadata{ID: 1}, nil).Once()
	svc.EXPECT().Touch(mock.Anything, "ns-a", "tok", mock.Anything).Return(nil).Once()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	rec := httptest.NewRecorder()
	SessionResolve(svc, testRoutes, logging.Nop())(handler).ServeHTTP(rec, requestWith("ns-a", "tok"))
}

func TestSessionResolve_5xxSkipsTouch(t *testing.T) {
	svc := NewMockSessionResolver(t)
	svc.EXPECT().Lookup(mock.Anything, "ns-a", "tok").Return(session.SessionMetadata{ID: 1}, nil).Once()
	// No Touch EXPECT — mockery fails the test if Touch is called.

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	rec := httptest.NewRecorder()
	SessionResolve(svc, testRoutes, logging.Nop())(handler).ServeHTTP(rec, requestWith("ns-a", "tok"))
}

func TestSessionResolve_TouchErrorDoesNotChangeResponse(t *testing.T) {
	svc := NewMockSessionResolver(t)
	svc.EXPECT().Lookup(mock.Anything, "ns-a", "tok").Return(session.SessionMetadata{ID: 1}, nil).Once()
	svc.EXPECT().Touch(mock.Anything, "ns-a", "tok", mock.Anything).Return(errors.New("transient")).Once()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	})
	rec := httptest.NewRecorder()
	SessionResolve(svc, testRoutes, logging.Nop())(handler).ServeHTTP(rec, requestWith("ns-a", "tok"))

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d; want 202", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"accepted":1`) {
		t.Errorf("body lost: %q", rec.Body.String())
	}
}

func TestSessionResolve_TouchErrorSessionNotFoundIsExpected(t *testing.T) {
	svc := NewMockSessionResolver(t)
	svc.EXPECT().Lookup(mock.Anything, "ns-a", "tok").Return(session.SessionMetadata{ID: 1}, nil).Once()
	svc.EXPECT().Touch(mock.Anything, "ns-a", "tok", mock.Anything).Return(db.ErrSessionNotFound).Once()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	SessionResolve(svc, testRoutes, logging.Nop())(handler).ServeHTTP(rec, requestWith("ns-a", "tok"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}
}
