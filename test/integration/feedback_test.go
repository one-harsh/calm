// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/db"
)

func TestFeedback_RequiresSessionToken(t *testing.T) {
	// The OpenAPI spec declares X-CALM-Session-Token as required on POST /v1/feedback.
	// Missing token is rejected by the validation middleware (400) — before any handler
	// or DAL touch.
	resp, err := env.client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: ""},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: mustNewV7(t),
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (missing required header); body=%s", resp.StatusCode(), string(resp.Body))
	}
}

func TestFeedback_UnknownOutcomeEnumRejected400(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	s := seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, 60)

	body := strings.NewReader(`{"correlation_id": "` + mustNewV7(t).String() + `", "outcome": "exploded"}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.serverURL+"/v1/feedback", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CALM-API-Key", testMasterKey)
	req.Header.Set("X-CALM-Session-Token", s.SessionToken)
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if httpResp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d; want 400 (outcome not in enum)", httpResp.StatusCode)
	}
}

func TestFeedback_OutsideWindowReturns410(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	s := seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, 60)

	// Mint a UUIDv7 with a backdated timestamp far outside the default 60-minute
	// window. The service short-circuits with 410 before touching the DAL.
	stale := uuidV7AtTime(time.Now().Add(-2 * time.Hour))
	resp, err := env.client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: s.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: stale,
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusGone {
		t.Errorf("status = %d; want 410 (window expired); body=%s", resp.StatusCode(), string(resp.Body))
	}
}

func TestFeedback_TornDownSessionReturns404SessionNotFound(t *testing.T) {
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	s := seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, 60)

	// Workload tears down its session.
	delResp, err := env.client.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: s.SessionToken})
	if err != nil || delResp.StatusCode() != http.StatusNoContent {
		t.Fatalf("teardown DeleteSession: err=%v status=%d", err, delResp.StatusCode())
	}

	// Same workload then attempts feedback with the (now-stale) session token.
	// SessionResolve fires before the feedback handler and rejects with 404,
	// pinning the documented deferred-evaluation gap.
	resp, err := env.client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: s.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: mustNewV7(t),
			Outcome:       genapi.FeedbackRequestOutcomeRetry,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (session torn down)", resp.StatusCode())
	}
}

func TestFeedback_InWindowReturns503PendingDAL(t *testing.T) {
	// Until the correlations DAL lands, the in-window path runs through the
	// stub UpdateOutcome which returns ErrCorrelationsNotImplemented; the
	// handler maps that to 503 + feedback_dal_unavailable so workloads see a
	// clean "feature not yet available" status instead of a 500. When the
	// real DAL ships, this test pivots to assert the actual 204/404/409 paths
	// it currently t.Skip's below.
	seedClient(t, env.sqlDB, testNamespace, db.DefaultClient)
	s := seedSession(t, env.sqlDB, testNamespace, db.DefaultClient, 60)

	resp, err := env.client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: s.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: mustNewV7(t),
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 (transitional state); body=%s", resp.StatusCode(), string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "feedback_dal_unavailable") {
		t.Errorf("body = %s; want error code feedback_dal_unavailable", string(resp.Body))
	}
}

func TestFeedback_HappyPathAcceptsAndEmitsMetric(t *testing.T) {
	t.Skip("requires WI-44b correlations DAL (CorrelationsRepo.UpdateOutcome)")
}

func TestFeedback_DoubleSubmit409(t *testing.T) {
	t.Skip("requires WI-44b correlations DAL")
}

func TestFeedback_UnknownCorrelation404(t *testing.T) {
	t.Skip("requires WI-44b correlations DAL")
}

func TestFeedback_CrossSessionWithinNamespace404(t *testing.T) {
	t.Skip("requires WI-44b correlations DAL — session-id filter on UpdateOutcome")
}

func TestFeedback_CrossNamespaceReturns404(t *testing.T) {
	t.Skip("requires WI-44b correlations DAL — namespace filter on UpdateOutcome")
}

// mustNewV7 mints a fresh UUIDv7 or fails the test.
func mustNewV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid.NewV7: %v", err)
	}
	return id
}

// uuidV7AtTime constructs a UUIDv7 whose embedded timestamp matches the given
// time. The first 48 bits of a UUIDv7 are the unix epoch milliseconds.
func uuidV7AtTime(at time.Time) uuid.UUID {
	id := uuid.UUID{}
	ms := at.UnixMilli()
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	// Version 7 in the high nibble of byte 6
	id[6] = 0x70
	// IETF variant in the high two bits of byte 8
	id[8] = 0x80
	return id
}
