// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
)

// uniqueClient yields a per-test client name so a namespace-wide management
// query can be scoped to this test's own rows, keeping the shared-namespace
// suite parallel-safe.
func uniqueClient(prefix string) string { return "mgmt-" + prefix + "-" + randHex(6) }

// registerClient registers a workload client through the live handler stack so
// session-create requests naming it satisfy the FK.
func registerClient(t *testing.T, namespace, name string) {
	t.Helper()
	resp, err := env.clientForNamespace(t, namespace).RegisterClientWithResponse(context.Background(), name)
	if err != nil {
		t.Fatalf("registerClient(%q/%q): %v", namespace, name, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("registerClient(%q/%q): status=%d body=%s", namespace, name, resp.StatusCode(), string(resp.Body))
	}
}

// createSessionAs is createSessionForTest bound to a named client and optional
// labels — the management surface filters on both.
func createSessionAs(t *testing.T, namespace, clientName string, labels map[string]string) seededSession {
	t.Helper()
	body := genapi.CreateSessionJSONRequestBody{Client: &clientName}
	if labels != nil {
		body.Labels = &labels
	}
	resp, err := env.clientForNamespace(t, namespace).CreateSessionWithResponse(
		context.Background(), &genapi.CreateSessionParams{}, body,
	)
	if err != nil {
		t.Fatalf("createSessionAs(%q/%q): %v", namespace, clientName, err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("createSessionAs(%q/%q): status=%d body=%s", namespace, clientName, resp.StatusCode(), string(resp.Body))
	}
	var id int64
	if err := env.sqlDB.QueryRowContext(
		context.Background(),
		`SELECT id FROM sessions WHERE namespace = $1 AND session_token_hash = $2`,
		namespace, auth.HashToken(namespace, resp.JSON201.SessionToken),
	).Scan(&id); err != nil {
		t.Fatalf("createSessionAs(%q/%q): resolve id: %v", namespace, clientName, err)
	}
	return seededSession{
		ID:           id,
		SessionToken: resp.JSON201.SessionToken,
		Namespace:    namespace,
		Client:       resp.JSON201.Client,
	}
}

// writeEventsForSession posts n events to a session through the live handler.
func writeEventsForSession(t *testing.T, token string, n int) {
	t.Helper()
	events := make([]genapi.EventInput, n)
	for i := range events {
		events[i] = genapi.EventInput{Type: "tool_invocation", Priority: 3, Data: map[string]any{"i": i}}
	}
	resp, err := env.client.WriteEventsWithResponse(context.Background(),
		&genapi.WriteEventsParams{XCALMSessionToken: token},
		genapi.WriteEventsJSONRequestBody{Events: events})
	if err != nil {
		t.Fatalf("writeEventsForSession: %v", err)
	}
	if resp.StatusCode() != http.StatusAccepted {
		t.Fatalf("writeEventsForSession: status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// searchStatus runs a search on a session token and reports the status code —
// a deleted session's token must stop resolving rather than return content.
func searchStatus(t *testing.T, apiClient *genapi.ClientWithResponses, token, query string) (int, int) {
	t.Helper()
	resp, err := apiClient.SearchWithResponse(context.Background(),
		&genapi.SearchParams{XCALMSessionToken: token},
		genapi.SearchJSONRequestBody{Queries: []string{query}})
	if err != nil {
		t.Fatalf("Search %q: %v", query, err)
	}
	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return resp.StatusCode(), 0
	}
	return resp.StatusCode(), len(resp.JSON200.Results[0].Hits)
}

func manageListSessions(t *testing.T, apiClient *genapi.ClientWithResponses, params *genapi.ManageListSessionsParams) []genapi.ManagedSession {
	t.Helper()
	resp, err := apiClient.ManageListSessionsWithResponse(context.Background(), params)
	if err != nil {
		t.Fatalf("ManageListSessions: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("ManageListSessions: status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON200.Sessions
}

func manageListClients(t *testing.T, apiClient *genapi.ClientWithResponses) []genapi.ClientSummary {
	t.Helper()
	resp, err := apiClient.ManageListClientsWithResponse(context.Background())
	if err != nil {
		t.Fatalf("ManageListClients: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("ManageListClients: status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON200.Clients
}

func clientListed(clients []genapi.ClientSummary, name string) bool {
	for _, c := range clients {
		if c.Name == name {
			return true
		}
	}
	return false
}

// ---------- ManageListSessions ----------

// An operator lists the sessions in the API key's namespace and sees what the
// workload committed: every session under its client, stamped with namespace,
// TTL, activity, and the labels it was created with.
func TestManageListSessions_ReflectsCreatedSessions(t *testing.T) {
	t.Parallel()
	client := uniqueClient("list-reflects")
	registerClient(t, testNamespace, client)
	createSessionAs(t, testNamespace, client, map[string]string{"env": "prod", "team": "ml"})
	createSessionAs(t, testNamespace, client, nil)

	got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &client})
	if len(got) != 2 {
		t.Fatalf("listed %d sessions; want the 2 created under %q", len(got), client)
	}
	var labelled *genapi.ManagedSession
	for i := range got {
		s := &got[i]
		if s.Namespace != testNamespace || s.Client != client {
			t.Errorf("listed session = %q/%q; want %q/%q", s.Namespace, s.Client, testNamespace, client)
		}
		if s.TtlMinutes <= 0 {
			t.Errorf("ttl_minutes = %d; want the session's positive TTL", s.TtlMinutes)
		}
		if s.CreatedAt.IsZero() || s.LastActivity.IsZero() {
			t.Errorf("listed session carries zero created_at/last_activity: %+v", s)
		}
		if s.Labels != nil {
			labelled = s
		}
	}
	if labelled == nil {
		t.Fatal("no listed session carried labels; want the labelled session surfaced")
	}
	if (*labelled.Labels)["env"] != "prod" || (*labelled.Labels)["team"] != "ml" {
		t.Errorf("labels = %+v; want env=prod team=ml", *labelled.Labels)
	}
}

// The listing reports each session's live event count, so an operator can spot
// busy sessions without opening them.
func TestManageListSessions_EventCountReflectsWrites(t *testing.T) {
	t.Parallel()
	client := uniqueClient("list-events")
	registerClient(t, testNamespace, client)
	sess := createSessionAs(t, testNamespace, client, nil)

	writeEventsForSession(t, sess.SessionToken, 3)

	got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &client})
	if len(got) != 1 {
		t.Fatalf("listed %d sessions; want 1", len(got))
	}
	if got[0].EventCount != 3 {
		t.Errorf("event_count = %d; want 3", got[0].EventCount)
	}
}

// The client filter scopes the listing to one workload: a sibling client's
// sessions in the same namespace stay out of the answer.
func TestManageListSessions_ClientFilterScopesToOneClient(t *testing.T) {
	t.Parallel()
	wanted := uniqueClient("filter-wanted")
	sibling := uniqueClient("filter-sibling")
	registerClient(t, testNamespace, wanted)
	registerClient(t, testNamespace, sibling)
	createSessionAs(t, testNamespace, wanted, nil)
	createSessionAs(t, testNamespace, wanted, nil)
	createSessionAs(t, testNamespace, sibling, nil)

	got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &wanted})
	if len(got) != 2 {
		t.Fatalf("client-filtered listing returned %d sessions; want the 2 under %q", len(got), wanted)
	}
	for _, s := range got {
		if s.Client != wanted {
			t.Errorf("filtered listing leaked client %q; want only %q", s.Client, wanted)
		}
	}
}

// Label filters AND together: a session is listed only if it carries every
// requested key/value pair, not merely one of them.
func TestManageListSessions_LabelFilterMatchesEveryRequestedPair(t *testing.T) {
	t.Parallel()
	client := uniqueClient("filter-labels")
	registerClient(t, testNamespace, client)
	createSessionAs(t, testNamespace, client, map[string]string{"env": "staging", "team": "search"})
	createSessionAs(t, testNamespace, client, map[string]string{"env": "staging", "team": "ingest"})

	got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{
		Client: &client,
		Labels: &map[string]string{"env": "staging", "team": "search"},
	})
	if len(got) != 1 {
		t.Fatalf("listed %d sessions; want the 1 matching env=staging AND team=search", len(got))
	}
	if (*got[0].Labels)["team"] != "search" {
		t.Errorf("labels = %+v; want team=search", *got[0].Labels)
	}
}

// Namespace isolation is a trust boundary: another namespace's API key sees
// none of this namespace's sessions, not even when it names the same client.
func TestManageListSessions_CrossNamespaceInvisible(t *testing.T) {
	t.Parallel()
	client := uniqueClient("list-xns")
	registerClient(t, testNamespace, client)
	createSessionAs(t, testNamespace, client, nil)

	tenantA := env.clientForNamespace(t, testTenantANamespace)
	if got := manageListSessions(t, tenantA, &genapi.ManageListSessionsParams{Client: &client}); len(got) != 0 {
		t.Errorf("namespace %q listed %d of namespace %q's sessions; want none",
			testTenantANamespace, len(got), testNamespace)
	}
}

// ---------- ManageDeleteSessions ----------

// dry_run reports how many sessions the delete would affect and removes
// nothing; the sessions are still listable afterward.
func TestManageDeleteSessions_DryRunPreviewsWithoutDeleting(t *testing.T) {
	t.Parallel()
	client := uniqueClient("del-dryrun")
	registerClient(t, testNamespace, client)
	createSessionAs(t, testNamespace, client, nil)
	createSessionAs(t, testNamespace, client, nil)

	dryRun := true
	resp, err := env.client.ManageDeleteSessionsWithResponse(context.Background(),
		&genapi.ManageDeleteSessionsParams{Client: &client, DryRun: &dryRun})
	if err != nil {
		t.Fatalf("ManageDeleteSessions dry_run: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.AffectedSessions == nil || *resp.JSON200.AffectedSessions != 2 {
		t.Errorf("affected_sessions = %v; want 2", resp.JSON200.AffectedSessions)
	}
	if resp.JSON200.DeletedSessions != nil {
		t.Errorf("dry_run reported deleted_sessions = %v; want nil", resp.JSON200.DeletedSessions)
	}
	if got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &client}); len(got) != 2 {
		t.Errorf("dry_run removed rows: listing shows %d sessions; want 2", len(got))
	}
}

// A bulk delete cascades the matched sessions' whole content footprint: the
// counts come back on the response, the child rows leave the database, and the
// deleted session's token stops resolving, so nothing it held can re-enter a
// context window.
func TestManageDeleteSessions_PostDeleteContentInvisible(t *testing.T) {
	t.Parallel()
	client := uniqueClient("del-content")
	registerClient(t, testNamespace, client)
	sess := createSessionAs(t, testNamespace, client, nil)
	apiClient := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, apiClient, sess.SessionToken, "notes.log", "the build failed with a zebramarker linker error")

	if status, hits := searchStatus(t, apiClient, sess.SessionToken, "zebramarker"); status != http.StatusOK || hits == 0 {
		t.Fatalf("pre-delete search: status=%d hits=%d; want 200 with a hit", status, hits)
	}
	var sourceID int64
	if err := env.sqlDB.QueryRowContext(context.Background(),
		`SELECT id FROM sources WHERE session_id = $1`, sess.ID).Scan(&sourceID); err != nil {
		t.Fatalf("resolve seeded source id: %v", err)
	}

	del, err := env.client.ManageDeleteSessionsWithResponse(context.Background(),
		&genapi.ManageDeleteSessionsParams{Client: &client})
	if err != nil {
		t.Fatalf("ManageDeleteSessions: %v", err)
	}
	if del.StatusCode() != http.StatusOK {
		t.Fatalf("status=%d body=%s", del.StatusCode(), string(del.Body))
	}
	if del.JSON200.DeletedSessions == nil || *del.JSON200.DeletedSessions != 1 {
		t.Fatalf("deleted_sessions = %v; want 1", del.JSON200.DeletedSessions)
	}
	if del.JSON200.AffectedSessions != nil {
		t.Errorf("affected_sessions set on a real delete; want nil (dry-run-only field)")
	}
	if del.JSON200.Cascaded == nil || del.JSON200.Cascaded.Sources != 1 || del.JSON200.Cascaded.Chunks == 0 {
		t.Errorf("cascaded = %+v; want sources=1 chunks>0", del.JSON200.Cascaded)
	}

	// No orphans: the cascade takes the content rows with the session.
	if n := countRows(t, env.sqlDB, `SELECT COUNT(*) FROM sessions WHERE id = $1`, sess.ID); n != 0 {
		t.Errorf("session rows after delete = %d; want 0", n)
	}
	if n := countRows(t, env.sqlDB, `SELECT COUNT(*) FROM chunks WHERE source_id = $1`, sourceID); n != 0 {
		t.Errorf("chunk rows after delete = %d; want 0 (cascade through sources)", n)
	}

	if status, _ := searchStatus(t, apiClient, sess.SessionToken, "zebramarker"); status != http.StatusNotFound {
		t.Errorf("post-delete search status = %d; want 404 (session removed)", status)
	}
	if got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &client}); len(got) != 0 {
		t.Errorf("post-delete listing shows %d sessions; want 0", len(got))
	}
}

// A bulk delete cannot reach across the namespace boundary: another namespace's
// key matching the same client deletes nothing and leaves the sessions intact.
func TestManageDeleteSessions_CrossNamespaceCannotDelete(t *testing.T) {
	t.Parallel()
	client := uniqueClient("del-xns")
	registerClient(t, testNamespace, client)
	createSessionAs(t, testNamespace, client, nil)

	tenantA := env.clientForNamespace(t, testTenantANamespace)
	del, err := tenantA.ManageDeleteSessionsWithResponse(context.Background(),
		&genapi.ManageDeleteSessionsParams{Client: &client})
	if err != nil {
		t.Fatalf("ManageDeleteSessions (tenant-a): %v", err)
	}
	if del.StatusCode() != http.StatusOK {
		t.Fatalf("status=%d body=%s", del.StatusCode(), string(del.Body))
	}
	if del.JSON200.DeletedSessions == nil || *del.JSON200.DeletedSessions != 0 {
		t.Errorf("namespace %q deleted %v of namespace %q's sessions; want 0",
			testTenantANamespace, del.JSON200.DeletedSessions, testNamespace)
	}
	if got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &client}); len(got) != 1 {
		t.Errorf("cross-namespace delete removed the session: listing shows %d; want 1", len(got))
	}
}

// ---------- ManageListClients ----------

// The client listing surfaces a registered client with the number of sessions
// it owns and when it was last active, so operators can spot dead or
// typo workloads.
func TestManageListClients_ReflectsRegisteredClientAndSessionCount(t *testing.T) {
	t.Parallel()
	client := uniqueClient("clients-list")
	registerClient(t, testNamespace, client)
	createSessionAs(t, testNamespace, client, nil)
	createSessionAs(t, testNamespace, client, nil)

	var found *genapi.ClientSummary
	clients := manageListClients(t, env.client)
	for i := range clients {
		if clients[i].Name == client {
			found = &clients[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("registered client %q is not in the listing", client)
	}
	if found.SessionCount != 2 {
		t.Errorf("session_count = %d; want 2", found.SessionCount)
	}
	if found.LastActivity == nil {
		t.Error("last_activity absent; want the owned sessions' activity")
	}
}

// A client registered in one namespace is invisible to another namespace's key.
func TestManageListClients_CrossNamespaceInvisible(t *testing.T) {
	t.Parallel()
	client := uniqueClient("clients-xns")
	registerClient(t, testNamespace, client)

	tenantA := env.clientForNamespace(t, testTenantANamespace)
	if clientListed(manageListClients(t, tenantA), client) {
		t.Errorf("namespace %q lists namespace %q's client %q", testTenantANamespace, testNamespace, client)
	}
}

// ---------- ManageDeleteClient ----------

// Deleting a client cascades every session it owns: the counts come back, the
// client leaves the listing, and the cascaded session's token stops resolving.
func TestManageDeleteClient_CascadesSessionsAndContent(t *testing.T) {
	t.Parallel()
	client := uniqueClient("delclient")
	registerClient(t, testNamespace, client)
	sess := createSessionAs(t, testNamespace, client, nil)
	apiClient := env.clientForNamespace(t, testNamespace)
	ingestForSearch(t, apiClient, sess.SessionToken, "c.log", "a quokkamarker turned up in the trace")

	resp, err := env.client.ManageDeleteClientWithResponse(context.Background(), client,
		&genapi.ManageDeleteClientParams{})
	if err != nil {
		t.Fatalf("ManageDeleteClient: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.DeletedClient == nil || *resp.JSON200.DeletedClient != client {
		t.Errorf("deleted_client = %v; want %q", resp.JSON200.DeletedClient, client)
	}
	if resp.JSON200.DeletedSessions == nil || *resp.JSON200.DeletedSessions != 1 {
		t.Errorf("deleted_sessions = %v; want 1", resp.JSON200.DeletedSessions)
	}
	if resp.JSON200.Cascaded == nil || resp.JSON200.Cascaded.Sources != 1 {
		t.Errorf("cascaded = %+v; want sources=1", resp.JSON200.Cascaded)
	}

	if clientListed(manageListClients(t, env.client), client) {
		t.Errorf("client %q is still listed after being deleted", client)
	}
	if got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &client}); len(got) != 0 {
		t.Errorf("client %q still holds %d sessions after the cascade", client, len(got))
	}
	if status, _ := searchStatus(t, apiClient, sess.SessionToken, "quokkamarker"); status != http.StatusNotFound {
		t.Errorf("post-delete search status = %d; want 404 (session cascaded away)", status)
	}
}

// dry_run on a client delete previews the sessions that would cascade and
// removes neither the client nor its sessions.
func TestManageDeleteClient_DryRunPreviewsWithoutDeleting(t *testing.T) {
	t.Parallel()
	client := uniqueClient("delclient-dryrun")
	registerClient(t, testNamespace, client)
	createSessionAs(t, testNamespace, client, nil)

	dryRun := true
	resp, err := env.client.ManageDeleteClientWithResponse(context.Background(), client,
		&genapi.ManageDeleteClientParams{DryRun: &dryRun})
	if err != nil {
		t.Fatalf("ManageDeleteClient dry_run: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON200.AffectedSessions == nil || *resp.JSON200.AffectedSessions != 1 {
		t.Errorf("affected_sessions = %v; want 1", resp.JSON200.AffectedSessions)
	}
	if resp.JSON200.DeletedClient != nil {
		t.Errorf("dry_run reported deleted_client = %v; want nil", resp.JSON200.DeletedClient)
	}
	if !clientListed(manageListClients(t, env.client), client) {
		t.Errorf("dry_run removed client %q; want it still registered", client)
	}
	if got := manageListSessions(t, env.client, &genapi.ManageListSessionsParams{Client: &client}); len(got) != 1 {
		t.Errorf("dry_run removed sessions: listing shows %d; want 1", len(got))
	}
}

// The default client is the bootstrap fallback for sessions that name no
// client: deleting it is refused with 409 and the row survives — and its
// preview is the same refusal, not a count.
func TestManageDeleteClient_DefaultClientProtected(t *testing.T) {
	t.Parallel()
	resp, err := env.client.ManageDeleteClientWithResponse(context.Background(), db.DefaultClient,
		&genapi.ManageDeleteClientParams{})
	if err != nil {
		t.Fatalf("ManageDeleteClient(default): %v", err)
	}
	if resp.StatusCode() != http.StatusConflict {
		t.Fatalf("status=%d body=%s; want 409", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON409 == nil || resp.JSON409.Error != "client_protected" {
		t.Errorf("body = %+v; want error=client_protected", resp.JSON409)
	}

	dryRun := true
	preview, err := env.client.ManageDeleteClientWithResponse(context.Background(), db.DefaultClient,
		&genapi.ManageDeleteClientParams{DryRun: &dryRun})
	if err != nil {
		t.Fatalf("ManageDeleteClient(default) dry_run: %v", err)
	}
	if preview.StatusCode() != http.StatusConflict {
		t.Errorf("dry_run status=%d; want the same 409 the delete returns", preview.StatusCode())
	}
	if n := countRows(t, env.sqlDB,
		`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`, testNamespace, db.DefaultClient); n != 1 {
		t.Errorf("default client rows = %d; want 1 (the delete must be refused)", n)
	}
}

// Deleting a client that was never registered returns 404 — invisibility, the
// same answer a client in another namespace gets.
func TestManageDeleteClient_UnknownClientReturns404(t *testing.T) {
	t.Parallel()
	resp, err := env.client.ManageDeleteClientWithResponse(context.Background(), uniqueClient("ghost"),
		&genapi.ManageDeleteClientParams{})
	if err != nil {
		t.Fatalf("ManageDeleteClient(unknown): %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Fatalf("status=%d body=%s; want 404", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON404 == nil || resp.JSON404.Error != "client_not_found" {
		t.Errorf("body = %+v; want error=client_not_found", resp.JSON404)
	}
}

// A client registered in one namespace cannot be deleted from another: the
// cross-namespace delete is a 404 (invisibility, not denial) and the client
// survives in its own namespace.
func TestManageDeleteClient_CrossNamespaceInvisible404(t *testing.T) {
	t.Parallel()
	client := uniqueClient("delclient-xns")
	registerClient(t, testNamespace, client)

	tenantA := env.clientForNamespace(t, testTenantANamespace)
	resp, err := tenantA.ManageDeleteClientWithResponse(context.Background(), client,
		&genapi.ManageDeleteClientParams{})
	if err != nil {
		t.Fatalf("ManageDeleteClient (tenant-a): %v", err)
	}
	if resp.StatusCode() != http.StatusNotFound {
		t.Errorf("cross-namespace delete status=%d; want 404 (invisibility)", resp.StatusCode())
	}
	if !clientListed(manageListClients(t, env.client), client) {
		t.Errorf("cross-namespace delete removed client %q from its own namespace", client)
	}
}
