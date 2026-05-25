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
	db     *sql.DB
	logger *logging.Logger
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

	return &Store{db: conn, logger: logger}, nil
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
		nsCtx := logging.Bind(ctx, obs.Namespace(ns), obs.Client(DefaultClient))
		if err := s.RegisterClient(nsCtx, ns, DefaultClient); err != nil {
			return fmt.Errorf("seed default client for namespace %q: %w", ns, err)
		}
	}
	return nil
}

func (s *Store) RegisterClient(ctx context.Context, namespace, name string) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if name == "" {
		return ErrClientNameRequired
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO clients (namespace, name) VALUES ($1, $2) ON CONFLICT (namespace, name) DO NOTHING`,
		namespace, name,
	); err != nil {
		return fmt.Errorf("register client %q/%q: %w: %w", namespace, name, ErrStorageBackend, err)
	}
	return nil
}

func (s *Store) ListClients(ctx context.Context, namespace string) ([]ClientSummary, error) {
	if namespace == "" {
		return nil, ErrNamespaceRequired
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.name, COUNT(s.session_id), MAX(s.last_activity)
		   FROM clients c
		   LEFT JOIN sessions s
		     ON s.namespace = c.namespace AND s.client = c.name
		  WHERE c.namespace = $1
		  GROUP BY c.name
		  ORDER BY c.name ASC`,
		namespace,
	)
	if err != nil {
		return nil, fmt.Errorf("list clients in %q: %w: %w", namespace, ErrStorageBackend, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ClientSummary, 0)
	for rows.Next() {
		var sum ClientSummary
		var last sql.NullTime
		if err := rows.Scan(&sum.Name, &sum.SessionCount, &last); err != nil {
			return nil, fmt.Errorf("scan client row in %q: %w: %w", namespace, ErrStorageBackend, err)
		}
		if last.Valid {
			t := last.Time
			sum.LastActivity = &t
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate client rows in %q: %w: %w", namespace, ErrStorageBackend, err)
	}
	return out, nil
}

func (s *Store) CountClientSessions(ctx context.Context, namespace, name string) (int, error) {
	if namespace == "" {
		return 0, ErrNamespaceRequired
	}
	if name == "" {
		return 0, ErrClientNameRequired
	}
	var exists bool
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM clients WHERE namespace = $1 AND name = $2),
		        (SELECT COUNT(*) FROM sessions WHERE namespace = $1 AND client = $2)`,
		namespace, name,
	).Scan(&exists, &count)
	if err != nil {
		return 0, fmt.Errorf("count sessions for %q/%q: %w: %w", namespace, name, ErrStorageBackend, err)
	}
	if !exists {
		return 0, ErrClientNotFound
	}
	return count, nil
}

func (s *Store) DeleteClient(ctx context.Context, namespace, name string) (DeleteClientResult, error) {
	if namespace == "" {
		return DeleteClientResult{}, ErrNamespaceRequired
	}
	if name == "" {
		return DeleteClientResult{}, ErrClientNameRequired
	}
	if name == DefaultClient {
		return DeleteClientResult{}, ErrClientProtected
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteClientResult{}, fmt.Errorf("begin delete client %q/%q: %w: %w", namespace, name, ErrStorageBackend, err)
	}
	defer func() { _ = tx.Rollback() }()

	// FOR UPDATE on clients row: a concurrent INSERT INTO sessions referencing
	// this client takes FOR KEY SHARE on the same row for FK enforcement, which
	// conflicts with our FOR UPDATE. The insert blocks until our COMMIT — at
	// which point the client row is gone and the FK check fails. Count and
	// cascade therefore match exactly; no LOCK TABLE escalation needed.
	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM clients WHERE namespace = $1 AND name = $2 FOR UPDATE`,
		namespace, name,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteClientResult{}, ErrClientNotFound
	}
	if err != nil {
		return DeleteClientResult{}, fmt.Errorf("lock client %q/%q: %w: %w", namespace, name, ErrStorageBackend, err)
	}
	s.logger.WithContext(ctx).Debug("delete client: lock acquired")

	result := DeleteClientResult{Client: name}
	err = tx.QueryRowContext(ctx,
		`WITH target_sessions AS (
		   SELECT session_id FROM sessions WHERE namespace = $1 AND client = $2
		 )
		 SELECT
		   (SELECT COUNT(*) FROM target_sessions),
		   (SELECT COUNT(*) FROM sources         WHERE session_id IN (SELECT session_id FROM target_sessions)),
		   (SELECT COUNT(*) FROM chunks          WHERE source_id IN
		       (SELECT id FROM sources WHERE session_id IN (SELECT session_id FROM target_sessions))),
		   (SELECT COUNT(*) FROM session_events  WHERE session_id IN (SELECT session_id FROM target_sessions)),
		   (SELECT COUNT(*) FROM session_labels  WHERE session_id IN (SELECT session_id FROM target_sessions))`,
		namespace, name,
	).Scan(
		&result.DeletedSessions,
		&result.Cascaded.Sources,
		&result.Cascaded.Chunks,
		&result.Cascaded.Events,
		&result.Cascaded.Labels,
	)
	if err != nil {
		return DeleteClientResult{}, fmt.Errorf("count cascade for %q/%q: %w: %w", namespace, name, ErrStorageBackend, err)
	}
	s.logger.WithContext(ctx).Debug("delete client: cascade computed",
		logging.IntField("sessions", result.DeletedSessions),
		logging.IntField("sources", result.Cascaded.Sources),
		logging.IntField("chunks", result.Cascaded.Chunks),
		logging.IntField("events", result.Cascaded.Events),
		logging.IntField("labels", result.Cascaded.Labels),
	)

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM clients WHERE namespace = $1 AND name = $2`,
		namespace, name,
	); err != nil {
		return DeleteClientResult{}, fmt.Errorf("delete client %q/%q: %w: %w", namespace, name, ErrStorageBackend, err)
	}

	if err := tx.Commit(); err != nil {
		return DeleteClientResult{}, fmt.Errorf("commit delete client %q/%q: %w: %w", namespace, name, ErrStorageBackend, err)
	}
	s.logger.WithContext(ctx).Debug("delete client: committed")
	return result, nil
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
