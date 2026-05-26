// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/client"
	"github.com/one-harsh/calm/internal/db"
)

func TestSeedNamespaceClients_HappyAndIdempotent(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	svc := client.New(store.Clients())
	namespaces := []string{"default", "tenant-a", "tenant-b"}
	if err := svc.SeedDefaults(ctx, namespaces); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	assertClientRowCount(ctx, t, sqlDB, namespaces, 1)

	if err := svc.SeedDefaults(ctx, namespaces); err != nil {
		t.Fatalf("second seed (idempotency check): %v", err)
	}
	assertClientRowCount(ctx, t, sqlDB, namespaces, 1)
}

func openConcreteStore(t *testing.T) (*db.Store, *sql.DB, func()) {
	t.Helper()

	adminDSN := defaultPGDSN
	dbName := "calm_seed_test_" + randHex(8)
	if err := createTestDB(adminDSN, dbName); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	suiteDSN, err := withDBName(adminDSN, dbName)
	if err != nil {
		_ = dropTestDB(adminDSN, dbName)
		t.Fatalf("rewrite test dsn: %v", err)
	}

	store, err := db.Open(context.Background(), suiteDSN, true, logging.Nop())
	if err != nil {
		_ = dropTestDB(adminDSN, dbName)
		t.Fatalf("open store: %v", err)
	}

	sqlDB, err := sql.Open("pgx", suiteDSN)
	if err != nil {
		_ = store.Close()
		_ = dropTestDB(adminDSN, dbName)
		t.Fatalf("open sibling sql.DB: %v", err)
	}

	teardown := func() {
		_ = sqlDB.Close()
		_ = store.Close()
		_ = dropTestDB(adminDSN, dbName)
	}
	return store, sqlDB, teardown
}

func assertClientRowCount(ctx context.Context, t *testing.T, sqlDB *sql.DB, namespaces []string, want int) {
	t.Helper()
	for _, ns := range namespaces {
		var got int
		if err := sqlDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM clients WHERE namespace = $1 AND name = $2`,
			ns, db.DefaultClient,
		).Scan(&got); err != nil {
			t.Fatalf("count clients for %q: %v", ns, err)
		}
		if got != want {
			t.Errorf("clients(%q, default): want %d row, got %d", ns, want, got)
		}
	}
}
