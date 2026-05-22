// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/obs"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, dsn string, migrateOnStartup bool, logger *logging.Logger) (*Store, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres open: %w", err)
	}

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	if err := Preflight(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, err
	}

	if migrateOnStartup {
		if err := migrateUp(ctx, conn, logger); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("postgres migrate: %w", err)
		}
	} else {
		logger.WithContext(ctx).Info("storage ready (migrate_on_startup=false; assuming migrations applied out-of-band)")
	}

	return &Store{db: conn}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func migrateUp(ctx context.Context, conn *sql.DB, logger *logging.Logger) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("open migrations source: %w", err)
	}

	driver, err := pgxdriver.WithInstance(conn, &pgxdriver.Config{})
	if err != nil {
		return fmt.Errorf("init migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}

	noChange := false
	if err := m.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			noChange = true
		} else {
			return fmt.Errorf("apply migrations: %w", err)
		}
	}

	version, dirty, verr := m.Version()
	if verr != nil {
		return fmt.Errorf("read migration version: %w", verr)
	}

	logger.WithContext(ctx).Info(
		"storage ready",
		logging.IntField("migration_version", int(version)),
		logging.BoolField("migration_dirty", dirty),
		logging.BoolField("migration_no_change", noChange),
	)
	return nil
}

// ---------- Clients ----------

// SeedNamespaceClients inserts a (namespace, DefaultClient) row for each
// configured namespace. Idempotent via ON CONFLICT; tolerates concurrent
// startup on peer replicas. Sessions that omit `client` at creation
// attribute to DefaultClient (DL01), so this row is FK-required before
// any session insert succeeds.
func (s *Store) SeedNamespaceClients(ctx context.Context, namespaces []string) error {
	for _, ns := range namespaces {
		nsCtx := logging.Bind(ctx, obs.Namespace(ns))
		if _, err := s.db.ExecContext(nsCtx,
			`INSERT INTO clients (namespace, name) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			ns, DefaultClient,
		); err != nil {
			return fmt.Errorf("seed default client for namespace %q: %w", ns, err)
		}
	}
	return nil
}

func (s *Store) RegisterClient(ctx context.Context, namespace, name string) error {
	return ErrNotImplemented
}

func (s *Store) ListClients(ctx context.Context, namespace string) ([]ClientSummary, error) {
	return nil, ErrNotImplemented
}

func (s *Store) CountClientSessions(ctx context.Context, namespace, name string) (int, error) {
	return 0, ErrNotImplemented
}

func (s *Store) DeleteClient(ctx context.Context, namespace, name string) (DeleteClientResult, error) {
	return DeleteClientResult{}, ErrNotImplemented
}

// ---------- Sessions ----------

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	return ErrNotImplemented
}

func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	return Session{}, ErrNotImplemented
}

func (s *Store) TouchSession(ctx context.Context, id string, lastActivity time.Time) error {
	return ErrNotImplemented
}

func (s *Store) ListManagedSessions(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error) {
	return nil, ErrNotImplemented
}

func (s *Store) CountSessions(ctx context.Context, filter ListSessionsFilter) (int, error) {
	return 0, ErrNotImplemented
}

func (s *Store) DeleteSession(ctx context.Context, id string) (DeleteSessionResult, error) {
	return DeleteSessionResult{}, ErrNotImplemented
}

func (s *Store) DeleteSessions(ctx context.Context, filter ListSessionsFilter) (DeleteSessionsResult, error) {
	return DeleteSessionsResult{}, ErrNotImplemented
}

func (s *Store) ScanExpiredSessions(ctx context.Context, now time.Time) ([]string, error) {
	return nil, ErrNotImplemented
}

// ---------- Content ----------

func (s *Store) Index(ctx context.Context, in IndexInput) error {
	return ErrNotImplemented
}

func (s *Store) Search(ctx context.Context, in SearchInput) ([]SearchResult, error) {
	return nil, ErrNotImplemented
}

func (s *Store) ListSources(ctx context.Context, sessionID string) ([]SourceSummary, error) {
	return nil, ErrNotImplemented
}

// ---------- Events ----------

func (s *Store) WriteEvents(ctx context.Context, sessionID string, events []Event) (int, error) {
	return 0, ErrNotImplemented
}

func (s *Store) ReadEvents(ctx context.Context, sessionID string, filter EventFilter) ([]Event, error) {
	return nil, ErrNotImplemented
}
