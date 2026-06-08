// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"sync"
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

func TestFeedback_HappyPathRecordsOutcome(t *testing.T) {
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	correlationID := ingestForCorrelation(t, client, sess.SessionToken)

	resp, err := client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: sess.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: correlationID,
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		t.Fatalf("status = %d; want 204; body=%s", resp.StatusCode(), string(resp.Body))
	}

	var outcome string
	var feedbackAt sql.NullTime
	if err := env.sqlDB.QueryRowContext(
		context.Background(),
		`SELECT outcome, feedback_received_at FROM correlations WHERE correlation_id = $1`,
		correlationID[:],
	).Scan(&outcome, &feedbackAt); err != nil {
		t.Fatalf("read back correlation: %v", err)
	}
	if outcome != "success" {
		t.Errorf("outcome = %q; want success", outcome)
	}
	if !feedbackAt.Valid {
		t.Error("feedback_received_at = NULL; want set")
	}
}

func TestFeedback_DoubleSubmit409(t *testing.T) {
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	correlationID := ingestForCorrelation(t, client, sess.SessionToken)

	first, err := client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: sess.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: correlationID,
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil || first.StatusCode() != http.StatusNoContent {
		t.Fatalf("first submission: err=%v status=%d body=%s", err, first.StatusCode(), string(first.Body))
	}

	second, err := client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: sess.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: correlationID,
			Outcome:       genapi.FeedbackRequestOutcomeRetry,
		})
	if err != nil {
		t.Fatalf("Feedback (second): %v", err)
	}
	if second.StatusCode() != http.StatusConflict {
		t.Errorf("status = %d; want 409 (already submitted); body=%s", second.StatusCode(), string(second.Body))
	}
	if !strings.Contains(string(second.Body), "feedback_already_submitted") {
		t.Errorf("body = %s; want error code feedback_already_submitted", string(second.Body))
	}
}

func TestFeedback_UnknownCorrelation404(t *testing.T) {
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	resp, err := client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: sess.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: mustNewV7(t),
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (unknown correlation); body=%s", resp.StatusCode(), string(resp.Body))
	}
	if !strings.Contains(string(resp.Body), "correlation_not_found") {
		t.Errorf("body = %s; want error code correlation_not_found", string(resp.Body))
	}
}

func TestFeedback_CrossSessionWithinNamespace404(t *testing.T) {
	sessA := createSessionForTest(t, testNamespace)
	sessB := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	correlationFromA := ingestForCorrelation(t, client, sessA.SessionToken)

	// Workload presents session B's token while referencing session A's correlation.
	// DAL filter on (session_id) collapses this to 404.
	resp, err := client.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: sessB.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: correlationFromA,
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (cross-session within namespace); body=%s", resp.StatusCode(), string(resp.Body))
	}

	// Sanity-check the row was not mutated under session B's request.
	var outcome string
	if err := env.sqlDB.QueryRowContext(
		context.Background(),
		`SELECT outcome FROM correlations WHERE correlation_id = $1`,
		correlationFromA[:],
	).Scan(&outcome); err != nil {
		t.Fatalf("read back correlation: %v", err)
	}
	if outcome != "unset" {
		t.Errorf("outcome = %q; want unset (session B's feedback should not have landed)", outcome)
	}
}

func TestFeedback_ConcurrentSubmitOnlyOneWinsRestAre409(t *testing.T) {
	// Two-callers race: GetWithLockedRow's row lock serializes the transactions
	// so exactly one POST returns 204; every other concurrent POST blocks on
	// the lock, then observes feedback_received_at SET, and exits via the
	// public 409 path. A 404 here would mean the loser slipped past Get with
	// feedback_received_at IS NULL and the UpdateOutcome guard converted the
	// race into a misleading "correlation not found".
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	correlationID := ingestForCorrelation(t, client, sess.SessionToken)

	const callers = 10
	var (
		startBarrier sync.WaitGroup
		readyBarrier sync.WaitGroup
		done         sync.WaitGroup
	)
	startBarrier.Add(1)
	readyBarrier.Add(callers)
	done.Add(callers)
	statuses := make([]int, callers)
	bodies := make([]string, callers)
	for i := 0; i < callers; i++ {
		go func(idx int) {
			defer done.Done()
			readyBarrier.Done()
			startBarrier.Wait()
			resp, err := client.FeedbackWithResponse(context.Background(),
				&genapi.FeedbackParams{XCALMSessionToken: sess.SessionToken},
				genapi.FeedbackJSONRequestBody{
					CorrelationId: correlationID,
					Outcome:       genapi.FeedbackRequestOutcomeSuccess,
				})
			if err != nil {
				statuses[idx] = -1
				bodies[idx] = err.Error()
				return
			}
			statuses[idx] = resp.StatusCode()
			bodies[idx] = string(resp.Body)
		}(i)
	}
	readyBarrier.Wait()
	startBarrier.Done()
	done.Wait()

	var success, conflict, other int
	var otherStatuses []int
	for i, s := range statuses {
		switch s {
		case http.StatusNoContent:
			success++
		case http.StatusConflict:
			conflict++
		default:
			other++
			otherStatuses = append(otherStatuses, s)
			t.Logf("caller %d: status=%d body=%s", i, s, bodies[i])
		}
	}
	if success != 1 {
		t.Errorf("204 count = %d; want exactly 1", success)
	}
	if conflict != callers-1 {
		t.Errorf("409 count = %d; want %d", conflict, callers-1)
	}
	if other != 0 {
		t.Errorf("unexpected statuses %v (404 here is the race-loser-as-not-found bug)", otherStatuses)
	}
}

func TestFeedback_CrossNamespaceReturns404(t *testing.T) {
	sessA := createSessionForTest(t, testNamespace)
	sessB := createSessionForTest(t, testTenantANamespace)
	clientA := env.clientForNamespace(t, testNamespace)
	clientB := env.clientForNamespace(t, testTenantANamespace)
	correlationFromA := ingestForCorrelation(t, clientA, sessA.SessionToken)

	// Tenant B's API key + tenant B's session, referencing tenant A's correlation.
	// EXISTS-on-sessions in the UPDATE collapses cross-namespace to 404.
	resp, err := clientB.FeedbackWithResponse(context.Background(),
		&genapi.FeedbackParams{XCALMSessionToken: sessB.SessionToken},
		genapi.FeedbackJSONRequestBody{
			CorrelationId: correlationFromA,
			Outcome:       genapi.FeedbackRequestOutcomeSuccess,
		})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Errorf("status = %d; want 404 (cross-namespace); body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// ingestForCorrelation issues a real ingest against the given session and
// returns the server-minted correlation_id from the response header. This is
// the path a workload follows to obtain the value it posts back to /v1/feedback.
func ingestForCorrelation(t *testing.T, client *genapi.ClientWithResponses, sessionToken string) uuid.UUID {
	t.Helper()
	resp, err := client.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: sessionToken},
		genapi.IngestJSONRequestBody{Source: "feedback-test", Content: "alpha\n\nbeta"})
	if err != nil {
		t.Fatalf("ingestForCorrelation: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("ingestForCorrelation: status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
	raw := resp.HTTPResponse.Header.Get("X-CALM-Correlation-Id")
	id, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("ingestForCorrelation: parse %q: %v", raw, err)
	}
	return id
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
