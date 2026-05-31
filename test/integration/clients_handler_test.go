// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/api/handlers"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/clientreg"
	"github.com/one-harsh/calm/internal/server"
	"github.com/one-harsh/calm/internal/session"
)

// ----- Uncredentialed namespaces (default harness; require_client_credentials=false) -----

func TestRegisterClientHandler_NewClientReturns201(t *testing.T) {
	resp, err := env.client.RegisterClientWithResponse(context.Background(), "new-client-mode-a")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON201 == nil || resp.JSON201.Name != "new-client-mode-a" {
		t.Errorf("response body = %+v; want name=new-client-mode-a", resp.JSON201)
	}
	// Uncredentialed namespace: no client_token returned.
	if resp.JSON201.ClientToken != nil {
		t.Errorf("uncredentialed registration should not return a client_token; got %q", *resp.JSON201.ClientToken)
	}
}

func TestRegisterClientHandler_DuplicateReturns409(t *testing.T) {
	// First registration succeeds.
	if _, err := env.client.RegisterClientWithResponse(context.Background(), "dup-mode-a"); err != nil {
		t.Fatalf("first RegisterClient: %v", err)
	}
	// Second returns 409.
	resp, err := env.client.RegisterClientWithResponse(context.Background(), "dup-mode-a")
	if err != nil {
		t.Fatalf("second RegisterClient: %v", err)
	}
	if resp.StatusCode() != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON409 == nil || resp.JSON409.Error != "client_exists" {
		t.Errorf("body = %+v; want error=client_exists", resp.JSON409)
	}
}

func TestRegisterClientHandler_RegisteredClientEnablesSessionCreate(t *testing.T) {
	clientName := "session-flow-mode-a"
	if _, err := env.client.RegisterClientWithResponse(context.Background(), clientName); err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	sessResp, err := env.client.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{
			Client: &clientName,
		},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sessResp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201", sessResp.StatusCode())
	}
}

func TestRotateClientTokenHandler_UncredentialedNamespaceReturns401(t *testing.T) {
	// Rotation requires the caller to have authenticated AS the client being
	// rotated. In an uncredentialed namespace no client token was presented,
	// so the auth-context client is "" and the rotation 401s. Pins the
	// "you didn't authenticate as the client you're rotating" semantic.
	if _, err := env.client.RegisterClientWithResponse(context.Background(), "rotate-target"); err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	resp, err := env.client.RotateClientTokenWithResponse(context.Background(), "rotate-target")
	if err != nil {
		t.Fatalf("RotateClientToken: %v", err)
	}
	if resp.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("uncredentialed rotate: status = %d; want 401; body=%s", resp.StatusCode(), string(resp.Body))
	}
}

// ----- Credentialed namespaces (per-suite server with require_client_credentials=true) -----

const (
	testCredentialedKey       = "test-credentialed-key-0123456789abcdef"
	testCredentialedNamespace = "credentialed-ns"
)

// newCredentialedTestServer builds a fresh handler + httptest server with one
// namespace configured for require_client_credentials. Reuses the package's
// test Postgres but uses an isolated namespace to avoid collisions with the
// shared `env` harness.
func newCredentialedTestServer(t *testing.T) (apiClient *genapi.ClientWithResponses, serverURL string, teardown func()) {
	t.Helper()

	registry := auth.NewMemoryRegistry(
		map[string]string{testCredentialedKey: testCredentialedNamespace},
		nil,
		map[string]bool{testCredentialedNamespace: true},
	)
	clientSvc := clientreg.New(env.store)

	handler, err := server.NewHandler(server.Config{
		MaxIngestPayloadKB:   1024,
		RateLimitPerSecond:   1000, // out of the way
		RequestTimeout:       2 * time.Second,
		GracefulShutdownWait: 0,
	}, server.Deps{
		Logger:         logging.Nop(),
		Registry:       registry,
		ClientResolver: clientSvc,
		Handlers: handlers.New(handlers.Deps{
			Logger:   logging.Nop(),
			Registry: registry,
			Clients:  clientSvc,
			Sessions: session.New(env.store, session.Config{CacheSize: 10_000}),
			Cfg: handlers.HandlersConfig{
				DefaultTTLMinutes: testDefaultTTLMinutes,
				MaxTTLMinutes:     testMaxTTLMinutes,
			},
		}),
	})
	if err != nil {
		t.Fatalf("build handler: %v", err)
	}
	srv := httptest.NewServer(handler)

	c, err := genapi.NewClientWithResponses(srv.URL, genapi.WithRequestEditorFn(apiKeyHeader(testCredentialedKey)))
	if err != nil {
		srv.Close()
		t.Fatalf("build client: %v", err)
	}
	return c, srv.URL, srv.Close
}

// withClientToken returns a clone of c that also stamps the given client token
// into Authorization: Bearer on every request.
func withClientToken(t *testing.T, serverURL, token string) *genapi.ClientWithResponses {
	t.Helper()
	c, err := genapi.NewClientWithResponses(serverURL,
		genapi.WithRequestEditorFn(apiKeyHeader(testCredentialedKey)),
		genapi.WithRequestEditorFn(clientTokenBearer(token)),
	)
	if err != nil {
		t.Fatalf("build credentialed client: %v", err)
	}
	return c
}

func TestRegisterClientHandler_CredentialedMintsToken(t *testing.T) {
	apiClient, _, teardown := newCredentialedTestServer(t)
	defer teardown()

	resp, err := apiClient.RegisterClientWithResponse(context.Background(), "alice-mode-b")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON201.ClientToken == nil {
		t.Fatal("credentialed registration must return a client_token")
	}
	if *resp.JSON201.ClientToken == "" {
		t.Error("client_token should be non-empty")
	}
}

func TestRegisterClientHandler_CredentialedDuplicateReturns409(t *testing.T) {
	apiClient, _, teardown := newCredentialedTestServer(t)
	defer teardown()

	if _, err := apiClient.RegisterClientWithResponse(context.Background(), "dup-mode-b"); err != nil {
		t.Fatalf("first RegisterClient: %v", err)
	}
	resp, err := apiClient.RegisterClientWithResponse(context.Background(), "dup-mode-b")
	if err != nil {
		t.Fatalf("second RegisterClient: %v", err)
	}
	if resp.StatusCode() != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body=%s", resp.StatusCode(), string(resp.Body))
	}
}

func TestCreateSessionHandler_CredentialedWithoutClientTokenReturns401(t *testing.T) {
	apiClient, _, teardown := newCredentialedTestServer(t)
	defer teardown()

	// Register first (registration is exempt from the client-token requirement).
	if _, err := apiClient.RegisterClientWithResponse(context.Background(), "alice-401-test"); err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	// CreateSession without Authorization: Bearer → 401.
	resp, err := apiClient.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401; body=%s", resp.StatusCode(), string(resp.Body))
	}
}

func TestCreateSessionHandler_CredentialedWithValidTokenSucceeds(t *testing.T) {
	apiClient, serverURL, teardown := newCredentialedTestServer(t)
	defer teardown()

	regResp, err := apiClient.RegisterClientWithResponse(context.Background(), "alice-token-test")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	token := *regResp.JSON201.ClientToken

	credentialed := withClientToken(t, serverURL, token)
	resp, err := credentialed.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d; want 201; body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON201.Client != "alice-token-test" {
		t.Errorf("session.client = %q; want alice-token-test (derived from token)", resp.JSON201.Client)
	}
}

func TestCreateSessionHandler_CredentialedBodyClientMustMatchTokenClient(t *testing.T) {
	apiClient, serverURL, teardown := newCredentialedTestServer(t)
	defer teardown()

	regResp, err := apiClient.RegisterClientWithResponse(context.Background(), "alice-mismatch")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	token := *regResp.JSON201.ClientToken

	// Register a second client so the spoofing target exists.
	if _, err := apiClient.RegisterClientWithResponse(context.Background(), "bob-mismatch"); err != nil {
		t.Fatalf("RegisterClient bob: %v", err)
	}

	credentialed := withClientToken(t, serverURL, token)
	wrongClient := "bob-mismatch"
	resp, err := credentialed.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{Client: &wrongClient},
	)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 (client mismatch); body=%s", resp.StatusCode(), string(resp.Body))
	}
	if resp.JSON400 == nil || resp.JSON400.Error != "client_mismatch" {
		t.Errorf("body = %+v; want error=client_mismatch", resp.JSON400)
	}
}

func TestCreateSessionHandler_CredentialedCrossClientIsolation(t *testing.T) {
	// Two clients in the same credentialed namespace each hold their own token.
	// Neither can read or delete the other's sessions.
	apiClient, serverURL, teardown := newCredentialedTestServer(t)
	defer teardown()

	aliceReg, err := apiClient.RegisterClientWithResponse(context.Background(), "alice-iso")
	if err != nil {
		t.Fatalf("RegisterClient alice: %v", err)
	}
	bobReg, err := apiClient.RegisterClientWithResponse(context.Background(), "bob-iso")
	if err != nil {
		t.Fatalf("RegisterClient bob: %v", err)
	}
	aliceClient := withClientToken(t, serverURL, *aliceReg.JSON201.ClientToken)
	bobClient := withClientToken(t, serverURL, *bobReg.JSON201.ClientToken)

	// Alice creates a session.
	if _, err := aliceClient.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	); err != nil {
		t.Fatalf("alice CreateSession: %v", err)
	}

	// Bob tries to delete alice's session. With session_id deletion bound
	// to (namespace, session_id), bob's request would normally succeed at
	// the DAL — the isolation isn't on the delete operation itself today,
	// it's on session-creation (each session is bound to its creating
	// client). For now, we pin the create-side contract: bob cannot create
	// a session that claims to be alice's by spoofing the client field.
	wrongClient := "alice-iso"
	resp, err := bobClient.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{Client: &wrongClient},
	)
	if err != nil {
		t.Fatalf("bob CreateSession spoof attempt: %v", err)
	}
	if resp.StatusCode() != http.StatusBadRequest {
		t.Errorf("bob spoof attempt: status = %d; want 400 (client_mismatch)", resp.StatusCode())
	}
}

func TestRotateClientTokenHandler_CredentialedHappyPath(t *testing.T) {
	apiClient, serverURL, teardown := newCredentialedTestServer(t)
	defer teardown()

	regResp, err := apiClient.RegisterClientWithResponse(context.Background(), "rotate-test")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	originalToken := *regResp.JSON201.ClientToken

	// Rotate using the current token.
	credentialed := withClientToken(t, serverURL, originalToken)
	rotResp, err := credentialed.RotateClientTokenWithResponse(context.Background(), "rotate-test")
	if err != nil {
		t.Fatalf("RotateClientToken: %v", err)
	}
	if rotResp.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rotResp.StatusCode(), string(rotResp.Body))
	}
	newToken := rotResp.JSON200.ClientToken
	if newToken == "" || newToken == originalToken {
		t.Errorf("new token = %q; must be non-empty and different from original %q", newToken, originalToken)
	}

	// Old token rejected.
	oldCredentialed := withClientToken(t, serverURL, originalToken)
	respOld, err := oldCredentialed.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("CreateSession with old token: %v", err)
	}
	if respOld.StatusCode() != http.StatusUnauthorized {
		t.Errorf("old token should be rejected: status = %d; want 401", respOld.StatusCode())
	}

	// New token works.
	newCredentialed := withClientToken(t, serverURL, newToken)
	respNew, err := newCredentialed.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("CreateSession with new token: %v", err)
	}
	if respNew.StatusCode() != http.StatusCreated {
		t.Errorf("new token should work: status = %d; want 201", respNew.StatusCode())
	}
}

func TestRotateClientTokenHandler_CredentialedSessionsPersistAcrossRotation(t *testing.T) {
	// HLD-pinned contract: rotation invalidates the credential, not the data.
	// Sessions created before rotation remain accessible with the new token.
	apiClient, serverURL, teardown := newCredentialedTestServer(t)
	defer teardown()

	regResp, err := apiClient.RegisterClientWithResponse(context.Background(), "rotate-persist")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	originalToken := *regResp.JSON201.ClientToken

	credentialed := withClientToken(t, serverURL, originalToken)
	createResp, err := credentialed.CreateSessionWithResponse(context.Background(),
		&genapi.CreateSessionParams{},
		genapi.CreateSessionJSONRequestBody{},
	)
	if err != nil {
		t.Fatalf("create pre-rotation session: %v", err)
	}
	if createResp.JSON201 == nil {
		t.Fatalf("create pre-rotation session: status=%d body=%s", createResp.StatusCode(), string(createResp.Body))
	}
	preRotationToken := createResp.JSON201.SessionToken

	rotResp, err := credentialed.RotateClientTokenWithResponse(context.Background(), "rotate-persist")
	if err != nil {
		t.Fatalf("RotateClientToken: %v", err)
	}
	newToken := rotResp.JSON200.ClientToken

	// The pre-rotation session should still exist; delete it with the new token.
	newCredentialed := withClientToken(t, serverURL, newToken)
	delResp, err := newCredentialed.DeleteSessionWithResponse(context.Background(),
		&genapi.DeleteSessionParams{XCALMSessionToken: preRotationToken})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if delResp.StatusCode() != http.StatusOK {
		t.Errorf("pre-rotation session should still exist after rotation: status = %d; body=%s",
			delResp.StatusCode(), string(delResp.Body))
	}
}

func TestRegisterClientHandler_CredentialedExemptFromClientTokenRequirement(t *testing.T) {
	// The chicken-and-egg: registration itself can't require a token (the
	// client doesn't have one yet). Pin that the registration path is
	// reachable in a credentialed namespace without Authorization: Bearer.
	apiClient, _, teardown := newCredentialedTestServer(t)
	defer teardown()

	// apiClient has only X-CALM-API-Key set, no Authorization header.
	resp, err := apiClient.RegisterClientWithResponse(context.Background(), "first-ever-client")
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if resp.StatusCode() != http.StatusCreated {
		t.Fatalf("registration without token: status = %d; want 201; body=%s",
			resp.StatusCode(), string(resp.Body))
	}
}

func TestAuth_HealthAndVersionBypassAuth(t *testing.T) {
	// Operational endpoints (health, version) are reachable without any
	// authentication header. Pin that contract.
	for _, path := range []string{"/v1/health", "/v1/version"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, env.serverURL+path, nil)
		if err != nil {
			t.Fatalf("%s: build req: %v", path, err)
		}
		// No headers.
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = resp.Body.Close()
		// Endpoints are stubs (ErrNotImplemented → 501). The proof is that
		// auth doesn't intercept with 401.
		if resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("%s: got 401; operational endpoints must bypass auth", path)
		}
	}
}

func TestAuth_StaleBearerHeader_Ignored(t *testing.T) {
	// Workloads upgrading from the previous bearer-in-Authorization shape
	// might leave the old header in place. Uncredentialed namespace should ignore it
	// (the contract is now X-CALM-API-Key). Regression net against silent
	// re-adoption.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		env.serverURL+"/v1/clients/legacy-header-test", strings.NewReader(""))
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	req.Header.Set(auth.HeaderAuthorization, auth.BearerPrefix+testMasterKey)
	// No X-CALM-API-Key.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401 (Authorization: Bearer no longer authenticates the namespace API key)", resp.StatusCode)
	}
}
