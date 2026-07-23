// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
)

func ptrInt(v int) *int { return &v }

type sizedWireHit struct {
	Title      string `json:"title"`
	Snippet    string `json:"snippet"`
	Source     string `json:"source"`
	MatchLayer string `json:"match_layer"`
	Truncated  *bool  `json:"truncated,omitempty"`
}

func wireHitSize(t *testing.T, h genapi.SearchHit) int {
	t.Helper()
	b, err := json.Marshal(sizedWireHit{
		Title:      h.Title,
		Snippet:    h.Snippet,
		Source:     h.Source,
		MatchLayer: string(h.MatchLayer),
		Truncated:  h.Truncated,
	}) // Truncated mirrors the service's sizing so truncated-hit byte accounting matches.
	if err != nil {
		t.Fatalf("marshal hit: %v", err)
	}
	return len(b)
}

// A budget large enough for the first hit but not both drops the second, flagging
// budget_exceeded and per-query budget_omitted while returning the first verbatim.
func TestSearch_BudgetOmitsSecondResults(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "s",
		"needle one here\n\nneedle two also here\n\nneedle three as well")

	full, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"needle"}})
	if err != nil {
		t.Fatalf("Search (full): %v", err)
	}
	fullHits := full.JSON200.Results[0].Hits
	if len(fullHits) < 2 {
		t.Skipf("corpus produced %d hits; need >=2 to test omission", len(fullHits))
	}
	budget := wireHitSize(t, fullHits[0])

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"needle"}, BudgetBytes: ptrInt(budget)})
	if err != nil {
		t.Fatalf("Search (budgeted): %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	got := resp.JSON200
	if len(got.Results[0].Hits) != 1 {
		t.Errorf("hits = %d; want 1 (budget admits only the first)", len(got.Results[0].Hits))
	}
	if got.Results[0].BudgetOmitted < 1 {
		t.Errorf("budget_omitted = %d; want >=1", got.Results[0].BudgetOmitted)
	}
	if !got.BudgetExceeded {
		t.Error("budget_exceeded = false; want true when a hit was dropped")
	}
	if got.ByteBudgetUsed > budget {
		t.Errorf("byte_budget_used %d overshot budget %d — strict contract violated", got.ByteBudgetUsed, budget)
	}
}

// STRICT no-overshoot: a budget too small for any hit returns empty results with
// budget_exceeded true and byte_budget_used zero — no first-candidate overshoot.
func TestSearch_NoHitFitsReturnsEmptyBudgetExceeded(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "s", "the linker failed with a fatal error")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}, BudgetBytes: ptrInt(1)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	got := resp.JSON200
	for _, r := range got.Results {
		if len(r.Hits) != 0 {
			t.Errorf("query %q returned hits under a 1-byte budget: %+v", r.Query, r.Hits)
		}
	}
	if !got.BudgetExceeded {
		t.Error("budget_exceeded = false; want true (nothing fit)")
	}
	if got.ByteBudgetUsed != 0 {
		t.Errorf("byte_budget_used = %d; want 0 (strict, no overshoot)", got.ByteBudgetUsed)
	}
}

// A budget above the operator ceiling is clamped, not rejected; the response
// echoes the committed (clamped) budget_bytes.
func TestSearch_BudgetClampsToCeilingAndEchoes(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "s", "the linker failed")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}, BudgetBytes: ptrInt(testSearchMaxBudgetBytes * 4)})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.BudgetBytes != testSearchMaxBudgetBytes {
		t.Errorf("budget_bytes = %d; want committed ceiling %d", resp.JSON200.BudgetBytes, testSearchMaxBudgetBytes)
	}
}

// Re-marshaling the returned hits and summing their standalone compact sizes
// equals byte_budget_used — pins the service's sizing mirror to the wire type.
func TestSearch_SnippetsByteIdenticalAcrossAllocation(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "s",
		"needle one here\n\nneedle two also here\n\nneedle three as well")

	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken},
		genapi.SearchJSONRequestBody{Queries: []string{"needle"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode(), string(resp.Body))
	}
	sum := 0
	for _, r := range resp.JSON200.Results {
		for _, h := range r.Hits {
			sum += wireHitSize(t, h)
		}
	}
	if sum != resp.JSON200.ByteBudgetUsed {
		t.Errorf("re-marshaled hit sizes sum to %d; byte_budget_used = %d", sum, resp.JSON200.ByteBudgetUsed)
	}
}

// The override header is honored where the namespace enables it; an unrecognized
// value is ignored (never 400).
func TestSearch_AllocatorOverrideHonoredWhenAllowed(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace) // override-enabled in the harness.
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "s", "the linker failed with a fatal error")

	variant := "mmr"
	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken, XCALMAllocatorVariant: &variant},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; override must not error body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// A namespace that does not allow override silently ignores the header — no 400.
func TestSearch_AllocatorOverrideIgnoredWhenDisallowed(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testTenantANamespace) // override NOT enabled.
	client := env.clientForNamespace(t, testTenantANamespace)
	ingestForSearch(t, client, sess.SessionToken, "s", "the linker failed")

	variant := "mmr"
	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken, XCALMAllocatorVariant: &variant},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; disallowed override must be ignored, not 400 body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// An unrecognized allocator header value is silently ignored — no 400 (the header
// has no OpenAPI enum precisely so out-of-set values fall through).
func TestSearch_InvalidAllocatorHeaderIgnored(t *testing.T) {
	t.Parallel()
	sess := createSessionForTest(t, testNamespace)
	client := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, client, sess.SessionToken, "s", "the linker failed")

	variant := "not-a-real-allocator"
	resp, err := client.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: sess.SessionToken, XCALMAllocatorVariant: &variant},
		genapi.SearchJSONRequestBody{Queries: []string{"linker"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; unrecognized allocator must be ignored, not 400 body=%s", resp.StatusCode(), string(resp.Body))
	}
}
