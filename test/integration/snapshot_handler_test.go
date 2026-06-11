// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// A freshly created session with no events returns 200 with an empty events list,
// budget_exceeded false, and byte_budget_used 0.
func TestGetSnapshotHandler_EmptySession(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	resp, err := client.GetSnapshotWithResponse(context.Background(), &genapi.GetSnapshotParams{
		XCALMSessionToken: sess.SessionToken,
	})
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Events) != 0 {
		t.Errorf("events = %d; want 0", len(resp.JSON200.Events))
	}
	if resp.JSON200.BudgetExceeded {
		t.Error("budget_exceeded = true; want false")
	}
	if resp.JSON200.ByteBudgetUsed != 0 {
		t.Errorf("byte_budget_used = %d; want 0", resp.JSON200.ByteBudgetUsed)
	}
}

// Events are returned ordered by ascending priority then descending recency; the handler
// preserves the DAL ordering through to the wire response.
func TestGetSnapshotHandler_OrdersByPriorityAndRecency(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	t0 := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	seedEventAt(t, env.sqlDB, sess.ID, "p2", 2, []byte(`{}`), t0)
	seedEventAt(t, env.sqlDB, sess.ID, "p1-old", 1, []byte(`{}`), t0)
	seedEventAt(t, env.sqlDB, sess.ID, "p1-new", 1, []byte(`{}`), t1)

	resp, err := client.GetSnapshotWithResponse(context.Background(), &genapi.GetSnapshotParams{
		XCALMSessionToken: sess.SessionToken,
	})
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	wantOrder := []string{"p1-new", "p1-old", "p2"}
	if len(resp.JSON200.Events) != len(wantOrder) {
		t.Fatalf("events = %d; want %d", len(resp.JSON200.Events), len(wantOrder))
	}
	for i, want := range wantOrder {
		if resp.JSON200.Events[i].Type != want {
			t.Errorf("event[%d].type = %q; want %q", i, resp.JSON200.Events[i].Type, want)
		}
	}
}

// When total event payload exceeds the requested byte budget, the response includes only
// a truncated subset of events with budget_exceeded true and byte_budget_used within the limit.
func TestGetSnapshotHandler_BudgetCapTruncates(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	t0 := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	for i := range 20 {
		seedEventAt(t, env.sqlDB, sess.ID,
			fmt.Sprintf("evt-%02d", i), 1, []byte(`{"k":"some padding value"}`),
			t0.Add(time.Duration(i)*time.Second))
	}

	budget := 256
	resp, err := client.GetSnapshotWithResponse(context.Background(), &genapi.GetSnapshotParams{
		XCALMSessionToken: sess.SessionToken,
		BudgetBytes:       &budget,
	})
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if !resp.JSON200.BudgetExceeded {
		t.Error("budget_exceeded = false; want true")
	}
	if n := len(resp.JSON200.Events); n == 0 || n >= 20 {
		t.Errorf("events = %d; want a truncated subset (0 < n < 20)", n)
	}
	if resp.JSON200.ByteBudgetUsed > budget {
		t.Errorf("byte_budget_used = %d; want <= %d", resp.JSON200.ByteBudgetUsed, budget)
	}
}

// A single priority-1 event larger than the byte budget is returned anyway rather than
// dropped; budget_exceeded is true and byte_budget_used overshoots the limit (never-worse:
// high-priority context is never silently omitted).
func TestGetSnapshotHandler_OversizedP1ReturnedNotDropped(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)

	budget := 256
	blob := strings.Repeat("x", budget*2)
	seedEventAt(t, env.sqlDB, sess.ID, "huge-p1", 1,
		[]byte(fmt.Sprintf(`{"blob":%q}`, blob)),
		time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC))

	resp, err := client.GetSnapshotWithResponse(context.Background(), &genapi.GetSnapshotParams{
		XCALMSessionToken: sess.SessionToken,
		BudgetBytes:       &budget,
	})
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if len(resp.JSON200.Events) != 1 || resp.JSON200.Events[0].Type != "huge-p1" {
		t.Fatalf("events = %+v; want the single oversized P1 returned", resp.JSON200.Events)
	}
	if !resp.JSON200.BudgetExceeded {
		t.Error("budget_exceeded = false; want true")
	}
	if resp.JSON200.ByteBudgetUsed <= budget {
		t.Errorf("byte_budget_used = %d; want overshoot above budget %d", resp.JSON200.ByteBudgetUsed, budget)
	}
}

// A default-namespace session token presented with a tenant-a API key returns 404;
// SessionResolve hashes under tenant-a and finds nothing (namespace-isolation).
func TestGetSnapshotHandler_CrossNamespaceInvisible404(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	// Present a default-namespace token while authenticated to tenant-a:
	// SessionResolve hashes under tenant-a and finds nothing.
	client := env.clientForNamespace(t, testTenantANamespace)

	resp, err := client.GetSnapshotWithResponse(context.Background(), &genapi.GetSnapshotParams{
		XCALMSessionToken: sess.SessionToken,
	})
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status = %d; want 404 body=%s", resp.StatusCode(), string(resp.Body))
	}
}
