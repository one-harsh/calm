// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// Oracle for the management API. Every promise below is driven through the
// generated client, so a response that does not decode into the committed
// spec's schema fails structurally rather than by hand-parsing: listing
// reflects created sessions, the client filter narrows, a namespace sees
// nothing of another namespace's sessions or clients, a manage delete cascades
// and leaves the deleted session's content unreachable through search, and the
// client surface lists, deletes-with-cascade, and refuses the bootstrap client.
//
// Self-contained helpers (t7-prefixed) so the oracle never depends on helper
// functions the graded solution might rename. Only stable harness symbols
// (env, clientForNamespace, testNamespace, testTenantANamespace, randHex) are
// reused.

const t7DefaultClient = "default"

func t7ptr[T any](v T) *T { return &v }

func t7RegisterClient(t *testing.T, api *genapi.ClientWithResponses, name string) {
	t.Helper()
	resp, err := api.RegisterClientWithResponse(context.Background(), name)
	if err != nil {
		t.Fatalf("register client %q: %v", name, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("register client %q: status=%d body=%s", name, resp.StatusCode(), string(resp.Body))
	}
}

func t7CreateSession(t *testing.T, api *genapi.ClientWithResponses, client string) genapi.Session {
	t.Helper()
	resp, err := api.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{Client: &client})
	if err != nil {
		t.Fatalf("create session under client %q: %v", client, err)
	}
	if resp.StatusCode() != http.StatusCreated || resp.JSON201 == nil {
		t.Fatalf("create session under client %q: status=%d body=%s", client, resp.StatusCode(), string(resp.Body))
	}
	return *resp.JSON201
}

func t7Ingest(t *testing.T, api *genapi.ClientWithResponses, token, source, content string) {
	t.Helper()
	resp, err := api.IngestWithResponse(context.Background(),
		&genapi.IngestParams{XCALMSessionToken: token},
		genapi.IngestJSONRequestBody{Source: source, Content: content})
	if err != nil {
		t.Fatalf("ingest into %q: %v", source, err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("ingest into %q: status=%d body=%s", source, resp.StatusCode(), string(resp.Body))
	}
}

// t7ListSessions drives manageListSessions and requires a 200 whose body
// decodes into the committed spec's ListManagedSessionsResult.
func t7ListSessions(t *testing.T, api *genapi.ClientWithResponses, client *string) []genapi.ManagedSession {
	t.Helper()
	resp, err := api.ManageListSessionsWithResponse(context.Background(), &genapi.ManageListSessionsParams{Client: client})
	if err != nil {
		t.Fatalf("manage list sessions: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("manage list sessions: status=%d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200 == nil {
		t.Fatalf("manage list sessions: body does not decode into the spec's ListManagedSessionsResult: %s", string(resp.Body))
	}
	return resp.JSON200.Sessions
}

// t7ListClients drives manageListClients and requires a 200 whose body decodes
// into the committed spec's ListClientsResult.
func t7ListClients(t *testing.T, api *genapi.ClientWithResponses) []genapi.ClientSummary {
	t.Helper()
	resp, err := api.ManageListClientsWithResponse(context.Background())
	if err != nil {
		t.Fatalf("manage list clients: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("manage list clients: status=%d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200 == nil {
		t.Fatalf("manage list clients: body does not decode into the spec's ListClientsResult: %s", string(resp.Body))
	}
	return resp.JSON200.Clients
}

func t7SessionsForClient(sessions []genapi.ManagedSession, client string) []genapi.ManagedSession {
	out := make([]genapi.ManagedSession, 0, len(sessions))
	for _, s := range sessions {
		if s.Client == client {
			out = append(out, s)
		}
	}
	return out
}

func t7HasClient(clients []genapi.ClientSummary, name string) bool {
	for _, c := range clients {
		if c.Name == name {
			return true
		}
	}
	return false
}

// t7SearchFinds reports whether the needle is reachable through search on this
// session token. A session whose credential no longer resolves is "not
// reachable", not a test error — the manage delete is allowed to invalidate it.
func t7SearchFinds(t *testing.T, api *genapi.ClientWithResponses, token, needle string) bool {
	t.Helper()
	resp, err := api.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: token},
		genapi.SearchJSONRequestBody{Queries: []string{needle}})
	if err != nil {
		t.Fatalf("search for %q: %v", needle, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return false
	}
	for _, q := range resp.JSON200.Results {
		for _, h := range q.Hits {
			if strings.Contains(h.Snippet, needle) {
				return true
			}
		}
	}
	return false
}

// A workload creates sessions under two clients; the management listing shows
// them all, and the client filter narrows the listing to one client's sessions.
func TestT7Oracle_ListingReflectsCreatedSessionsAndClientFilterNarrows(t *testing.T) {
	t.Parallel()
	api := env.clientForNamespace(t, testNamespace)
	clientA := "t7-listing-a-" + randHex(4)
	clientB := "t7-listing-b-" + randHex(4)
	t7RegisterClient(t, api, clientA)
	t7RegisterClient(t, api, clientB)
	first := t7CreateSession(t, api, clientA)
	t7CreateSession(t, api, clientA)
	t7CreateSession(t, api, clientB)
	t7Ingest(t, api, first.SessionToken, "t7-listing-"+randHex(4), "content seeded for the management listing scenario")

	all := t7ListSessions(t, api, nil)
	if got := len(t7SessionsForClient(all, clientA)); got != 2 {
		t.Errorf("listing shows %d sessions for client %q; want the 2 that were created", got, clientA)
	}
	if got := len(t7SessionsForClient(all, clientB)); got != 1 {
		t.Errorf("listing shows %d sessions for client %q; want the 1 that was created", got, clientB)
	}
	for _, s := range t7SessionsForClient(all, clientA) {
		if s.Namespace != testNamespace {
			t.Errorf("listed session namespace = %q; want %q", s.Namespace, testNamespace)
		}
		if s.TtlMinutes <= 0 {
			t.Errorf("listed session ttl_minutes = %d; want the session's positive TTL", s.TtlMinutes)
		}
		if s.CreatedAt.IsZero() || s.LastActivity.IsZero() {
			t.Errorf("listed session carries zero created_at/last_activity: %+v", s)
		}
	}

	onlyA := t7ListSessions(t, api, &clientA)
	if len(onlyA) != 2 {
		t.Fatalf("client filter %q returned %d sessions; want exactly the 2 created under it", clientA, len(onlyA))
	}
	for _, s := range onlyA {
		if s.Client != clientA {
			t.Errorf("client filter %q returned a session for client %q", clientA, s.Client)
		}
	}
}

// Two namespaces each hold sessions; neither namespace's management listing
// surfaces the other's sessions or clients.
func TestT7Oracle_CrossNamespaceSessionsAndClientsAreInvisible(t *testing.T) {
	t.Parallel()
	nsA := env.clientForNamespace(t, testNamespace)
	nsB := env.clientForNamespace(t, testTenantANamespace)
	clientA := "t7-xns-a-" + randHex(4)
	clientB := "t7-xns-b-" + randHex(4)
	t7RegisterClient(t, nsA, clientA)
	t7RegisterClient(t, nsB, clientB)
	t7CreateSession(t, nsA, clientA)
	t7CreateSession(t, nsB, clientB)

	if got := len(t7SessionsForClient(t7ListSessions(t, nsA, nil), clientA)); got != 1 {
		t.Fatalf("precondition: namespace %q lists %d sessions for its own client %q; want 1", testNamespace, got, clientA)
	}
	bAll := t7ListSessions(t, nsB, nil)
	if got := len(t7SessionsForClient(bAll, clientB)); got != 1 {
		t.Fatalf("precondition: namespace %q lists %d sessions for its own client %q; want 1", testTenantANamespace, got, clientB)
	}

	if got := len(t7SessionsForClient(bAll, clientA)); got != 0 {
		t.Errorf("namespace %q lists %d of namespace %q's sessions; a namespace must see none", testTenantANamespace, got, testNamespace)
	}
	for _, s := range bAll {
		if s.Namespace != testTenantANamespace {
			t.Errorf("namespace %q listing carries a session stamped namespace %q", testTenantANamespace, s.Namespace)
		}
	}
	if got := len(t7ListSessions(t, nsB, &clientA)); got != 0 {
		t.Errorf("filtering namespace %q by namespace %q's client returned %d sessions; want none", testTenantANamespace, testNamespace, got)
	}

	aClients := t7ListClients(t, nsA)
	if !t7HasClient(aClients, clientA) {
		t.Fatalf("precondition: namespace %q does not list its own client %q", testNamespace, clientA)
	}
	if t7HasClient(aClients, clientB) {
		t.Errorf("namespace %q lists namespace %q's client %q", testNamespace, testTenantANamespace, clientB)
	}
	if t7HasClient(t7ListClients(t, nsB), clientA) {
		t.Errorf("namespace %q lists namespace %q's client %q", testTenantANamespace, testNamespace, clientA)
	}
}

// An operator deletes a session through the manage surface; the delete
// cascades and the session's content is no longer reachable through search.
func TestT7Oracle_DeletedSessionContentIsUnreachable(t *testing.T) {
	t.Parallel()
	api := env.clientForNamespace(t, testNamespace)
	client := "t7-del-" + randHex(4)
	t7RegisterClient(t, api, client)
	sess := t7CreateSession(t, api, client)
	needle := "t7needle" + randHex(4)
	t7Ingest(t, api, sess.SessionToken, "t7-delete-"+randHex(4),
		"the manage delete scenario stores "+needle+" inside this session")

	if !t7SearchFinds(t, api, sess.SessionToken, needle) {
		t.Fatalf("precondition: %q is not reachable through search before the delete", needle)
	}

	resp, err := api.ManageDeleteSessionsWithResponse(context.Background(),
		&genapi.ManageDeleteSessionsParams{Client: &client})
	if err != nil {
		t.Fatalf("manage delete sessions: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("manage delete sessions: status=%d; want 200; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200 == nil {
		t.Fatalf("manage delete sessions: body does not decode into the spec's DeleteManagedSessionsResult: %s", string(resp.Body))
	}
	if resp.JSON200.DeletedSessions == nil {
		t.Fatalf("manage delete sessions: deleted_sessions absent on a non-dry-run delete: %s", string(resp.Body))
	}
	if got := *resp.JSON200.DeletedSessions; got != 1 {
		t.Errorf("deleted_sessions = %d; want the 1 session held by client %q", got, client)
	}
	if resp.JSON200.Cascaded == nil {
		t.Fatalf("manage delete sessions: cascaded absent on a non-dry-run delete: %s", string(resp.Body))
	}
	if resp.JSON200.Cascaded.Sources < 1 || resp.JSON200.Cascaded.Chunks < 1 {
		t.Errorf("cascaded = %+v; the deleted session held one ingested source, so the delete must cascade through its sources and chunks", *resp.JSON200.Cascaded)
	}

	if got := len(t7ListSessions(t, api, &client)); got != 0 {
		t.Errorf("listing still shows %d sessions for client %q after the delete", got, client)
	}
	if t7SearchFinds(t, api, sess.SessionToken, needle) {
		t.Errorf("%q is still reachable through search after the session was deleted", needle)
	}
}

// An operator lists clients, then deletes one; the delete cascades its
// sessions away and the client leaves the listing.
func TestT7Oracle_ClientListingAndClientDeleteCascade(t *testing.T) {
	t.Parallel()
	api := env.clientForNamespace(t, testNamespace)
	client := "t7-cli-" + randHex(4)
	t7RegisterClient(t, api, client)
	sess := t7CreateSession(t, api, client)
	t7Ingest(t, api, sess.SessionToken, "t7-clients-"+randHex(4), "content seeded for the client cascade scenario")

	if !t7HasClient(t7ListClients(t, api), client) {
		t.Fatalf("client %q was registered and holds a session but is not listed", client)
	}

	resp, err := api.ManageDeleteClientWithResponse(context.Background(), client, &genapi.ManageDeleteClientParams{})
	if err != nil {
		t.Fatalf("manage delete client %q: %v", client, err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("manage delete client %q: status=%d; want 200; body=%s", client, resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200 == nil {
		t.Fatalf("manage delete client %q: body does not decode into the spec's DeleteClientResult: %s", client, string(resp.Body))
	}
	if resp.JSON200.DeletedClient == nil || *resp.JSON200.DeletedClient != client {
		t.Errorf("deleted_client = %v; want %q", resp.JSON200.DeletedClient, client)
	}

	if t7HasClient(t7ListClients(t, api), client) {
		t.Errorf("client %q is still listed after being deleted", client)
	}
	if got := len(t7ListSessions(t, api, &client)); got != 0 {
		t.Errorf("client %q still holds %d sessions after the delete cascaded", client, got)
	}
}

// The bootstrap fallback client is not deletable — it is the target sessions
// without a client field land on.
func TestT7Oracle_DefaultClientIsNotDeletable(t *testing.T) {
	t.Parallel()
	api := env.clientForNamespace(t, testNamespace)
	resp, err := api.ManageDeleteClientWithResponse(context.Background(), t7DefaultClient,
		&genapi.ManageDeleteClientParams{DryRun: t7ptr(false)})
	if err != nil {
		t.Fatalf("manage delete client %q: %v", t7DefaultClient, err)
	}
	if resp.StatusCode() != http.StatusConflict {
		t.Fatalf("deleting the %q client returned status=%d; want 409; body=%s",
			t7DefaultClient, resp.StatusCode(), string(resp.Body))
	}
}
