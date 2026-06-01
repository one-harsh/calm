// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers pgx for the admin connection
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/api/handlers"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/clientreg"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/server"
	"github.com/one-harsh/calm/internal/session"
)

const (
	envPGDSN     = "CALM_TEST_PG_DSN"
	defaultPGDSN = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"

	// Test bearer credentials. Every in-process integration request
	// authenticates as the master key for the `default` namespace.
	testMasterKey = "test-master-key-0123456789abcdef"
	testNamespace = "default"

	testTenantAKey       = "test-tenant-a-key-0123456789abcdef"
	testTenantANamespace = "tenant-a"

	testDefaultTTLMinutes = 120
	testMaxTTLMinutes     = 240
)

type harness struct {
	client    *genapi.ClientWithResponses
	serverURL string
	store     *db.Store
	sqlDB     *sql.DB // raw connection to the harness's test DB for assertions
	teardown  func()
}

// env is the package-level handle every test reads from. Set in TestMain
// before m.Run().
var env *harness

func TestMain(m *testing.M) {
	h, err := bootstrap()
	if err != nil {
		log.Fatalf("integration test bootstrap failed: %v", err)
	}
	env = h

	code := m.Run()

	env.teardown()
	os.Exit(code)
}

func bootstrap() (*harness, error) {
	store, sqlDB, dbTeardown, err := openTestStore()
	if err != nil {
		return nil, err
	}

	registry := auth.NewMemoryRegistry(
		map[string]string{
			testMasterKey:  testNamespace,
			testTenantAKey: testTenantANamespace,
		},
		nil,
		nil,
	)
	clientSvc := clientreg.New(store)

	// Match production behavior: seed the `default` client for each
	// configured namespace at startup (cmd/calm/main.go does the same).
	// Tests that omit `client` from session-create payloads land on `default`
	// and require the FK target to exist.
	if err := clientSvc.SeedDefaults(context.Background(), []string{testNamespace, testTenantANamespace}); err != nil {
		_ = store.Close()
		dbTeardown()
		return nil, fmt.Errorf("seed default clients: %w", err)
	}

	sessionSvc := session.New(store, session.Config{
		CacheSize:          10_000,
		IdempotencyKeyTTL:  time.Hour,
		IdempotencyKeySize: 10_000,
	})
	handler, err := server.NewHandler(server.Config{
		MaxIngestPayloadKB:   1024,
		RateLimitPerSecond:   100,
		RequestTimeout:       2 * time.Second,
		GracefulShutdownWait: 0,
	}, server.Deps{
		Logger:         logging.Nop(),
		Registry:       registry,
		ClientResolver: clientSvc,
		Sessions:       sessionSvc,
		Handlers: handlers.New(handlers.Deps{
			Logger:   logging.Nop(),
			Registry: registry,
			Clients:  clientSvc,
			Sessions: sessionSvc,
			Events:   store.Events(),
			Cfg: handlers.HandlersConfig{
				DefaultTTLMinutes: testDefaultTTLMinutes,
				MaxTTLMinutes:     testMaxTTLMinutes,
			},
		}),
	})
	if err != nil {
		_ = store.Close()
		dbTeardown()
		return nil, fmt.Errorf("build handler: %w", err)
	}

	srv := httptest.NewServer(handler)

	client, err := genapi.NewClientWithResponses(srv.URL, genapi.WithRequestEditorFn(apiKeyHeader(testMasterKey)))
	if err != nil {
		srv.Close()
		_ = store.Close()
		dbTeardown()
		return nil, fmt.Errorf("build client: %w", err)
	}

	return &harness{
		client:    client,
		serverURL: srv.URL,
		store:     store,
		sqlDB:     sqlDB,
		teardown: func() {
			srv.Close()
			_ = sqlDB.Close()
			_ = store.Close()
			dbTeardown()
		},
	}, nil
}

func (h *harness) clientForNamespace(t *testing.T, ns string) *genapi.ClientWithResponses {
	t.Helper()
	var key string
	switch ns {
	case testNamespace:
		key = testMasterKey
	case testTenantANamespace:
		key = testTenantAKey
	default:
		t.Fatalf("clientForNamespace: unknown test namespace %q", ns)
	}
	c, err := genapi.NewClientWithResponses(h.serverURL, genapi.WithRequestEditorFn(apiKeyHeader(key)))
	if err != nil {
		t.Fatalf("clientForNamespace(%q): %v", ns, err)
	}
	return c
}

// openTestStore creates a per-suite test database against the dev Postgres
// (started by `task dev:up`), opens a Store + raw *sql.DB against it, and
// returns a teardown that drops the database. Each suite run gets its own
// isolated DB under the long-lived Postgres sidecar.
func openTestStore() (*db.Store, *sql.DB, func(), error) {
	adminDSN := os.Getenv(envPGDSN)
	if adminDSN == "" {
		adminDSN = defaultPGDSN
	}

	dbName := "calm_test_" + randHex(8)

	if err := createTestDB(adminDSN, dbName); err != nil {
		return nil, nil, nil, err
	}

	suiteDSN, err := withDBName(adminDSN, dbName)
	if err != nil {
		_ = dropTestDB(adminDSN, dbName)
		return nil, nil, nil, fmt.Errorf("rewrite test DSN: %w", err)
	}

	cleanup := func() {
		if err := dropTestDB(adminDSN, dbName); err != nil {
			log.Printf("integration teardown: drop test db %q: %v", dbName, err)
		}
	}

	store, err := db.Open(context.Background(), suiteDSN, true, logging.Nop())
	if err != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("open store: %w", err)
	}
	sqlDB, err := sql.Open("pgx", suiteDSN)
	if err != nil {
		_ = store.Close()
		cleanup()
		return nil, nil, nil, fmt.Errorf("open raw sql connection: %w", err)
	}
	return store, sqlDB, cleanup, nil
}

func createTestDB(adminDSN, dbName string) error {
	conn, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return postgresHint(adminDSN, fmt.Errorf("connect to postgres admin: %w", err))
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.PingContext(ctx); err != nil {
		return postgresHint(adminDSN, fmt.Errorf("ping postgres: %w", err))
	}

	// dbName is constructed from a random hex string; safe to interpolate.
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		return fmt.Errorf("create test db %q: %w", dbName, err)
	}
	return nil
}

func dropTestDB(adminDSN, dbName string) error {
	conn, err := sql.Open("pgx", adminDSN)
	if err != nil {
		return fmt.Errorf("connect to postgres admin: %w", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, dbName)); err != nil {
		return fmt.Errorf("drop test db %q: %w", dbName, err)
	}
	return nil
}

func withDBName(adminDSN, dbName string) (string, error) {
	u, err := url.Parse(adminDSN)
	if err != nil {
		return "", err
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

func randHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// apiKeyHeader returns a genapi request editor that stamps the namespace API
// key into the X-CALM-API-Key header. Replaces the prior bearerAuth helper
// (the namespace key moved out of Authorization: Bearer into its own
// header when client tokens claimed the bearer slot).
func apiKeyHeader(key string) genapi.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set(auth.HeaderAPIKey, key)
		return nil
	}
}

// clientTokenBearer returns a genapi request editor that stamps the per-client
// token into Authorization: Bearer. Used by credentialed-namespace tests after
// they call POST /v1/clients/{name} and capture the token.
func clientTokenBearer(token string) genapi.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set(auth.HeaderAuthorization, auth.BearerPrefix+token)
		return nil
	}
}

// postgresHint annotates connection failures with the most likely operator
// fix, so a missing dev DB surfaces as a one-line diagnosis instead of a
// raw "connection refused" trace.
func postgresHint(dsn string, err error) error {
	var opErr interface{ Timeout() bool }
	if errors.As(err, &opErr) || strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "no such host") {
		return fmt.Errorf("could not reach postgres at %s; is `task dev:up` running? underlying: %w", dsn, err)
	}
	return err
}
