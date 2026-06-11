// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// ---------- WriteEvents handler ----------

// A workload posts a single event to its session; CALM accepts it (202 accepted=1)
// and the row lands in the DB.
func TestWriteEventsHandler_HappySingleEvent(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)

	resp, err := env.client.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: s.SessionToken},
		genapi.WriteEventsJSONRequestBody{
			Events: []genapi.EventInput{
				{Type: "tool_invocation", Priority: 2, Data: map[string]any{"cmd": "ls"}},
			},
		},
	)
	if err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusAccepted {
		t.Fatalf("status = %d; want 202; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON202 == nil || resp.JSON202.Accepted != 1 {
		t.Errorf("accepted = %+v; want 1", resp.JSON202)
	}
	n := countRows(t, env.sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, s.ID)
	if n != 1 {
		t.Errorf("DB rows = %d; want 1", n)
	}
}

// A workload posts a batch of events; all are accepted with zero rejections.
func TestWriteEventsHandler_HappyBatch(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)

	resp, err := env.client.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: s.SessionToken},
		genapi.WriteEventsJSONRequestBody{
			Events: []genapi.EventInput{
				{Type: "a", Priority: 1, Data: map[string]any{"i": float64(1)}},
				{Type: "b", Priority: 2, Data: map[string]any{"i": float64(2)}},
				{Type: "c", Priority: 3, Data: map[string]any{"i": float64(3)}},
			},
		},
	)
	if err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusAccepted {
		t.Fatalf("status = %d; want 202; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON202.Accepted != 3 {
		t.Errorf("accepted = %d; want 3", resp.JSON202.Accepted)
	}
	if resp.JSON202.Rejected != nil && len(*resp.JSON202.Rejected) != 0 {
		t.Errorf("rejected = %+v; want nil/empty", resp.JSON202.Rejected)
	}
}

// A token that resolves to no session returns 404 before reaching the handler.
func TestWriteEventsHandler_UnknownSessionReturns404(t *testing.T) {
	t.Parallel()
	resp, err := env.client.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: "unknown-session-token-xxx"},
		genapi.WriteEventsJSONRequestBody{
			Events: []genapi.EventInput{
				{Type: "a", Priority: 1, Data: map[string]any{}},
			},
		},
	)
	if err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// A tenant B client presenting a session token from tenant A is rejected with
// 404 (namespace-isolation: invisibility-not-denial) and no events are written.
func TestWriteEventsHandler_CrossNamespaceSessionReturns404(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)
	tenantClient := env.clientForNamespace(t, testTenantANamespace)

	resp, err := tenantClient.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: s.SessionToken},
		genapi.WriteEventsJSONRequestBody{
			Events: []genapi.EventInput{
				{Type: "a", Priority: 1, Data: map[string]any{}},
			},
		},
	)
	if err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body=%s", resp.StatusCode(), string(resp.Body))
	}
	n := countRows(t, env.sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, s.ID)
	if n != 0 {
		t.Errorf("DB rows = %d; want 0", n)
	}
}

// An event with priority outside 1–4 is rejected with 400.
func TestWriteEventsHandler_PriorityOutOfRangeReturns400(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)

	resp, err := env.client.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: s.SessionToken},
		genapi.WriteEventsJSONRequestBody{
			Events: []genapi.EventInput{
				{Type: "a", Priority: 5, Data: map[string]any{}},
			},
		},
	)
	if err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// An empty events array fails OpenAPI validation and is rejected with 400.
func TestWriteEventsHandler_EmptyEventsArrayReturns400(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)

	resp, err := env.client.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: s.SessionToken},
		genapi.WriteEventsJSONRequestBody{Events: []genapi.EventInput{}},
	)
	if err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// The SessionResolve middleware advances last_activity after both write and read
// calls — confirming the Touch fires on every 2xx, not only on session-creating
// endpoints.
func TestEventsHandler_MiddlewareTouchesOnWriteAndRead(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)
	var beforeWrite time.Time
	if err := env.sqlDB.QueryRowContext(context.Background(),
		`SELECT last_activity FROM sessions WHERE id = $1`, s.ID).Scan(&beforeWrite); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	wresp, err := env.client.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: s.SessionToken},
		genapi.WriteEventsJSONRequestBody{
			Events: []genapi.EventInput{{Type: "a", Priority: 1, Data: map[string]any{}}},
		},
	)
	if err != nil || wresp.StatusCode() != http.StatusAccepted {
		t.Fatalf("Write: err=%v status=%d", err, wresp.StatusCode())
	}
	var afterWrite time.Time
	if err := env.sqlDB.QueryRowContext(context.Background(),
		`SELECT last_activity FROM sessions WHERE id = $1`, s.ID).Scan(&afterWrite); err != nil {
		t.Fatalf("read after-write: %v", err)
	}
	if !afterWrite.After(beforeWrite) {
		t.Errorf("after Write: last_activity not advanced by middleware")
	}

	time.Sleep(20 * time.Millisecond)
	rresp, err := env.client.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: s.SessionToken},
	)
	if err != nil || rresp.StatusCode() != http.StatusOK {
		t.Fatalf("Read: err=%v status=%d", err, rresp.StatusCode())
	}
	var afterRead time.Time
	if err := env.sqlDB.QueryRowContext(context.Background(),
		`SELECT last_activity FROM sessions WHERE id = $1`, s.ID).Scan(&afterRead); err != nil {
		t.Fatalf("read after-read: %v", err)
	}
	if !afterRead.After(afterWrite) {
		t.Errorf("after Read: last_activity not advanced by middleware")
	}
}

// ---------- ReadEvents handler ----------

// A workload reads from a session with no events; CALM returns 200 with an empty list.
func TestReadEventsHandler_EmptySession(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)

	resp, err := env.client.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: s.SessionToken},
	)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200 == nil || len(resp.JSON200.Events) != 0 {
		t.Errorf("events = %+v; want empty", resp.JSON200)
	}
}

// A workload writes two events then reads them back; CALM returns both ordered
// by ascending priority and the data payload is preserved exactly.
func TestReadEventsHandler_AfterWrite(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)

	if _, err := env.client.WriteEventsWithResponse(
		context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: s.SessionToken},
		genapi.WriteEventsJSONRequestBody{
			Events: []genapi.EventInput{
				{Type: "a", Priority: 2, Data: map[string]any{"x": float64(1)}},
				{Type: "b", Priority: 1, Data: map[string]any{"x": float64(2)}},
			},
		},
	); err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}

	resp, err := env.client.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: s.SessionToken},
	)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Events) != 2 {
		t.Fatalf("got %d events; want 2", len(resp.JSON200.Events))
	}
	if resp.JSON200.Events[0].Priority != 1 || resp.JSON200.Events[1].Priority != 2 {
		t.Errorf("ordering = [P%d, P%d]; want [P1, P2]",
			resp.JSON200.Events[0].Priority, resp.JSON200.Events[1].Priority)
	}
	if v, ok := resp.JSON200.Events[0].Data["x"]; !ok || v.(float64) != 2 {
		t.Errorf("event[0].data = %+v; want {x:2}", resp.JSON200.Events[0].Data)
	}
}

// The types query parameter filters events to the requested types only.
func TestReadEventsHandler_TypesFilter(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)
	seedEvent(t, env.sqlDB, s.ID, "a", 2, []byte(`{}`))
	seedEvent(t, env.sqlDB, s.ID, "b", 2, []byte(`{}`))
	seedEvent(t, env.sqlDB, s.ID, "c", 2, []byte(`{}`))

	types := []string{"a", "c"}
	resp, err := env.client.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: s.SessionToken, Types: &types},
	)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Events) != 2 {
		t.Fatalf("got %d events; want 2", len(resp.JSON200.Events))
	}
}

// The min_priority parameter filters events to those with priority ≤ the value.
func TestReadEventsHandler_MinPriorityFilter(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)
	seedEvent(t, env.sqlDB, s.ID, "p1", 1, []byte(`{}`))
	seedEvent(t, env.sqlDB, s.ID, "p2", 2, []byte(`{}`))
	seedEvent(t, env.sqlDB, s.ID, "p3", 3, []byte(`{}`))

	mp := 2
	resp, err := env.client.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: s.SessionToken, MinPriority: &mp},
	)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Events) != 2 {
		t.Fatalf("got %d events; want 2", len(resp.JSON200.Events))
	}
	for _, ev := range resp.JSON200.Events {
		if ev.Priority > 2 {
			t.Errorf("got priority %d; want <= 2", ev.Priority)
		}
	}
}

// The limit query parameter caps the number of events returned.
func TestReadEventsHandler_LimitHonored(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)
	for i := 0; i < 5; i++ {
		seedEvent(t, env.sqlDB, s.ID, "t", 2, []byte(`{}`))
	}

	limit := 2
	resp, err := env.client.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: s.SessionToken, Limit: &limit},
	)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Events) != 2 {
		t.Errorf("got %d events; want 2", len(resp.JSON200.Events))
	}
}

// A token that resolves to no session returns 404 before reaching the handler.
func TestReadEventsHandler_UnknownSessionReturns404(t *testing.T) {
	t.Parallel()
	resp, err := env.client.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: "unknown-session-token-xxx"},
	)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// A tenant B client presenting a session token from tenant A is rejected with
// 404 on reads (namespace-isolation: invisibility-not-denial).
func TestReadEventsHandler_CrossNamespaceSessionReturns404(t *testing.T) {
	t.Parallel()
	s := createSessionForTest(t, testNamespace)
	tenantClient := env.clientForNamespace(t, testTenantANamespace)

	resp, err := tenantClient.ReadEventsWithResponse(
		context.Background(),
		&genapi.ReadEventsParams{XCALMSessionToken: s.SessionToken},
	)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404; body=%s", resp.StatusCode(), string(resp.Body))
	}
}
