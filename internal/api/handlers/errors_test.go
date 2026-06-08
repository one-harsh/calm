// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/feedback"
)

func TestMapSessionError_KnownSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want errorMapping
	}{
		{"ErrSessionExists", db.ErrSessionExists, errorMapping{http.StatusConflict, "session_exists", "session already exists in this namespace"}},
		{"ErrSessionNotFound", db.ErrSessionNotFound, errorMapping{http.StatusNotFound, "session_not_found", "session not found in this namespace"}},
		{"ErrSessionTokenHashRequired", db.ErrSessionTokenHashRequired, errorMapping{http.StatusBadRequest, "invalid_request", "session token is required"}},
		{"ErrInvalidTTL", db.ErrInvalidTTL, errorMapping{http.StatusBadRequest, "invalid_request", "ttl_minutes must be a positive integer"}},
		{"ErrNamespaceRequired", db.ErrNamespaceRequired, errorMapping{http.StatusBadRequest, "invalid_request", "namespace is required"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mapSessionError(tc.err)
			if !ok {
				t.Fatalf("mapSessionError(%v): ok=false; want true", tc.err)
			}
			if got != tc.want {
				t.Errorf("mapSessionError(%v) = %+v; want %+v", tc.err, got, tc.want)
			}
		})
	}
}

func TestMapSessionError_UnmappedReturnsFalse(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"unrelated error", errors.New("unexpected")},
		{"nil", nil},
		{"context.Canceled", context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mapSessionError(tc.err)
			if ok {
				t.Errorf("mapSessionError(%v): ok=true want false; got %+v", tc.err, got)
			}
			if (got != errorMapping{}) {
				t.Errorf("mapSessionError(%v) returned non-zero mapping on unmapped err: %+v", tc.err, got)
			}
		})
	}
}

func TestMapSessionError_WrappedSentinels(t *testing.T) {
	// Wrap-tolerance: errors.Is should see through %w wrapping.
	wrapped := fmt.Errorf("dal: %w", db.ErrSessionNotFound)
	got, ok := mapSessionError(wrapped)
	if !ok {
		t.Fatal("wrapped ErrSessionNotFound: ok=false; want true")
	}
	if got.Status != http.StatusNotFound || got.Code != "session_not_found" {
		t.Errorf("wrapped ErrSessionNotFound = %+v; want 404 + session_not_found", got)
	}
}

func TestMapSessionError_DetailNeverLeaksInternalString(t *testing.T) {
	// Regression: returned Detail must never include the DAL sentinel's raw
	// err.Error() (which carries the "db: " operator-facing prefix).
	for _, sentinel := range []error{
		db.ErrSessionExists,
		db.ErrSessionNotFound,
		db.ErrSessionTokenHashRequired,
		db.ErrInvalidTTL,
		db.ErrNamespaceRequired,
	} {
		m, _ := mapSessionError(sentinel)
		if strings.HasPrefix(m.Detail, "db:") || strings.Contains(m.Detail, sentinel.Error()) {
			t.Errorf("mapSessionError(%v) detail = %q; must not leak internal sentinel text", sentinel, m.Detail)
		}
	}
}

func TestMapFeedbackError_KnownSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want errorMapping
	}{
		{"ErrFeedbackWindowExpired", feedback.ErrFeedbackWindowExpired, errorMapping{http.StatusGone, "feedback_window_expired", "correlation_id is older than the feedback acceptance window"}},
		{"ErrInvalidCorrelationID", feedback.ErrInvalidCorrelationID, errorMapping{http.StatusBadRequest, "invalid_correlation_id", "correlation_id is not a valid UUIDv7"}},
		{"ErrCorrelationNotFound", db.ErrCorrelationNotFound, errorMapping{http.StatusNotFound, "correlation_not_found", "correlation not found within the resolved session"}},
		{"ErrFeedbackAlreadySubmitted", db.ErrFeedbackAlreadySubmitted, errorMapping{http.StatusConflict, "feedback_already_submitted", "feedback already submitted for this correlation"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mapFeedbackError(tc.err)
			if !ok {
				t.Fatalf("mapFeedbackError(%v): ok=false; want true", tc.err)
			}
			if got != tc.want {
				t.Errorf("mapFeedbackError(%v) = %+v; want %+v", tc.err, got, tc.want)
			}
		})
	}
}

func TestMapFeedbackError_WrappedSentinels(t *testing.T) {
	// Wrap-tolerance: errors.Is sees through %w wrapping (DAL impl wraps via
	// multi-%w with ErrStorageBackend; the sentinel still surfaces).
	wrapped := fmt.Errorf("dal: %w", db.ErrCorrelationNotFound)
	got, ok := mapFeedbackError(wrapped)
	if !ok {
		t.Fatal("wrapped ErrCorrelationNotFound: ok=false; want true")
	}
	if got.Status != http.StatusNotFound || got.Code != "correlation_not_found" {
		t.Errorf("wrapped ErrCorrelationNotFound = %+v; want 404 + correlation_not_found", got)
	}
}

func TestMapFeedbackError_UnmappedReturnsFalse(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"unrelated error", errors.New("unexpected")},
		{"nil", nil},
		{"context.Canceled", context.Canceled},
		{"session sentinel — wrong family", db.ErrSessionNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := mapFeedbackError(tc.err)
			if ok {
				t.Errorf("mapFeedbackError(%v): ok=true want false; got %+v", tc.err, got)
			}
			if (got != errorMapping{}) {
				t.Errorf("mapFeedbackError(%v) returned non-zero mapping on unmapped err: %+v", tc.err, got)
			}
		})
	}
}

func TestMapFeedbackError_DetailNeverLeaksInternalString(t *testing.T) {
	for _, sentinel := range []error{
		feedback.ErrFeedbackWindowExpired,
		feedback.ErrInvalidCorrelationID,
		db.ErrCorrelationNotFound,
		db.ErrFeedbackAlreadySubmitted,
	} {
		m, _ := mapFeedbackError(sentinel)
		if strings.HasPrefix(m.Detail, "db:") || strings.HasPrefix(m.Detail, "feedback:") || strings.Contains(m.Detail, sentinel.Error()) {
			t.Errorf("mapFeedbackError(%v) detail = %q; must not leak internal sentinel text", sentinel, m.Detail)
		}
	}
}

func TestIsContextError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"context.Canceled", context.Canceled, true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"wrapped Canceled", fmt.Errorf("op: %w", context.Canceled), true},
		{"unrelated error", errors.New("nope"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextError(tc.err); got != tc.want {
				t.Errorf("isContextError(%v) = %v; want %v", tc.err, got, tc.want)
			}
		})
	}
}
