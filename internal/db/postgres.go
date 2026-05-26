// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"
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

// SeedNamespaceClients is the FK precondition for the DL01 default-client
// auto-attribution: sessions that omit `client` insert with `client='default'`,
// which requires the (namespace, 'default') row to exist.
func (s *Store) SeedNamespaceClients(ctx context.Context, namespaces []string) error {
	for _, ns := range namespaces {
		nsCtx := logging.Bind(ctx, obs.Namespace(ns), obs.Client(DefaultClient))
		if err := s.RegisterClient(nsCtx, ns, DefaultClient); err != nil {
			return fmt.Errorf("%w: seed default client for namespace %q", err, ns)
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
		return fmt.Errorf("%w: register client %q/%q: %w", ErrStorageBackend, namespace, name, err)
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
		return nil, fmt.Errorf("%w: list clients in %q: %w", ErrStorageBackend, namespace, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ClientSummary, 0)
	for rows.Next() {
		var sum ClientSummary
		var last sql.NullTime
		if err := rows.Scan(&sum.Name, &sum.SessionCount, &last); err != nil {
			return nil, fmt.Errorf("%w: scan client row in %q: %w", ErrStorageBackend, namespace, err)
		}
		if last.Valid {
			t := last.Time
			sum.LastActivity = &t
		}
		out = append(out, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate client rows in %q: %w", ErrStorageBackend, namespace, err)
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
		return 0, fmt.Errorf("%w: count sessions for %q/%q: %w", ErrStorageBackend, namespace, name, err)
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
		return DeleteClientResult{}, fmt.Errorf("%w: begin delete client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	defer func() { _ = tx.Rollback() }()

	// FOR UPDATE conflicts with FOR KEY SHARE that FK enforcement takes on
	// concurrent INSERT INTO sessions referencing this client — those inserts
	// block until our COMMIT and then fail FK. Count and cascade see the same
	// row set.
	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM clients WHERE namespace = $1 AND name = $2 FOR UPDATE`,
		namespace, name,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteClientResult{}, ErrClientNotFound
	}
	if err != nil {
		return DeleteClientResult{}, fmt.Errorf("%w: lock client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	s.logger.WithContext(ctx).Debug("delete client: lock acquired")

	// Child subqueries must filter by namespace = $1; without it, a session_id
	// that collides across namespaces (composite PK allows this) pulls
	// foreign-namespace children into the count.
	result := DeleteClientResult{Client: name}
	err = tx.QueryRowContext(ctx,
		`WITH target_sessions AS (
		   SELECT session_id FROM sessions WHERE namespace = $1 AND client = $2
		 )
		 SELECT
		   (SELECT COUNT(*) FROM target_sessions),
		   (SELECT COUNT(*) FROM sources WHERE namespace = $1
		      AND session_id IN (SELECT session_id FROM target_sessions)),
		   (SELECT COUNT(*) FROM chunks WHERE source_id IN
		      (SELECT id FROM sources WHERE namespace = $1
		         AND session_id IN (SELECT session_id FROM target_sessions))),
		   (SELECT COUNT(*) FROM session_events WHERE namespace = $1
		      AND session_id IN (SELECT session_id FROM target_sessions)),
		   (SELECT COUNT(*) FROM session_labels WHERE namespace = $1
		      AND session_id IN (SELECT session_id FROM target_sessions))`,
		namespace, name,
	).Scan(
		&result.DeletedSessions,
		&result.Cascaded.Sources,
		&result.Cascaded.Chunks,
		&result.Cascaded.Events,
		&result.Cascaded.Labels,
	)
	if err != nil {
		return DeleteClientResult{}, fmt.Errorf("%w: count cascade for %q/%q: %w", ErrStorageBackend, namespace, name, err)
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
		return DeleteClientResult{}, fmt.Errorf("%w: delete client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}

	if err := tx.Commit(); err != nil {
		return DeleteClientResult{}, fmt.Errorf("%w: commit delete client %q/%q: %w", ErrStorageBackend, namespace, name, err)
	}
	s.logger.WithContext(ctx).Debug("delete client: committed")
	return result, nil
}

// ---------- Sessions ----------

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	if sess.Namespace == "" {
		return ErrNamespaceRequired
	}
	if sess.ID == "" {
		return ErrSessionIDRequired
	}
	if sess.TTLMinutes <= 0 {
		return ErrInvalidTTL
	}
	if sess.Client == "" {
		sess.Client = DefaultClient
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin create session %q/%q: %w", ErrStorageBackend, sess.Namespace, sess.ID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO clients (namespace, name) VALUES ($1, $2) ON CONFLICT (namespace, name) DO NOTHING`,
		sess.Namespace, sess.Client,
	); err != nil {
		return fmt.Errorf("%w: auto-register client %q/%q: %w", ErrStorageBackend, sess.Namespace, sess.Client, err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (session_id, namespace, client, ttl_minutes) VALUES ($1, $2, $3, $4)`,
		sess.ID, sess.Namespace, sess.Client, sess.TTLMinutes,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrSessionExists
		}
		return fmt.Errorf("%w: insert session %q/%q: %w", ErrStorageBackend, sess.Namespace, sess.ID, err)
	}

	if len(sess.Labels) > 0 {
		placeholders := make([]string, 0, len(sess.Labels))
		args := make([]any, 0, len(sess.Labels)*4)
		i := 1
		for k, v := range sess.Labels {
			placeholders = append(placeholders, fmt.Sprintf("($%d, $%d, $%d, $%d)", i, i+1, i+2, i+3))
			args = append(args, sess.Namespace, sess.ID, k, v)
			i += 4
		}
		query := `INSERT INTO session_labels (namespace, session_id, key, value) VALUES ` + strings.Join(placeholders, ", ") //nolint:gosec
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("%w: insert labels for session %q/%q: %w", ErrStorageBackend, sess.Namespace, sess.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit create session %q/%q: %w", ErrStorageBackend, sess.Namespace, sess.ID, err)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, namespace, id string) (Session, error) {
	if namespace == "" {
		return Session{}, ErrNamespaceRequired
	}
	if id == "" {
		return Session{}, ErrSessionIDRequired
	}

	var sess Session
	var labelsJSON []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT
			s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.ttl_minutes,
			COALESCE(
				jsonb_object_agg(sl.key, sl.value) FILTER (WHERE sl.key IS NOT NULL),
				'{}'::jsonb
			) AS labels
		FROM sessions s
		LEFT JOIN session_labels sl
			ON sl.namespace = s.namespace AND sl.session_id = s.session_id
		WHERE s.namespace = $1 AND s.session_id = $2
		GROUP BY s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.ttl_minutes`,
		namespace, id,
	).Scan(
		&sess.ID, &sess.Namespace, &sess.Client,
		&sess.CreatedAt, &sess.LastActivity, &sess.TTLMinutes,
		&labelsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("%w: get session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	if len(labelsJSON) > 0 {
		var labels map[string]string
		if err := json.Unmarshal(labelsJSON, &labels); err != nil {
			return Session{}, fmt.Errorf("%w: decode labels for %q/%q: %w", ErrStorageBackend, namespace, id, err)
		}
		if len(labels) > 0 {
			sess.Labels = labels
		}
	}
	return sess, nil
}

func (s *Store) TouchSession(ctx context.Context, namespace, id string, lastActivity time.Time) error {
	if namespace == "" {
		return ErrNamespaceRequired
	}
	if id == "" {
		return ErrSessionIDRequired
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_activity = GREATEST(last_activity, $3) WHERE namespace = $1 AND session_id = $2`,
		namespace, id, lastActivity,
	)
	if err != nil {
		return fmt.Errorf("%w: touch session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: rows-affected for touch %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

func (s *Store) ListManagedSessions(ctx context.Context, filter ListSessionsFilter) ([]ManagedSession, error) {
	if filter.Namespace == "" {
		return nil, ErrNamespaceRequired
	}

	whereClause, args := buildSessionFilterWhere(filter, "s")
	queryHead := `
		SELECT
			s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.ttl_minutes,
			COALESCE(
				jsonb_object_agg(sl.key, sl.value) FILTER (WHERE sl.key IS NOT NULL),
				'{}'::jsonb
			) AS labels,
			(SELECT COUNT(*) FROM session_events
				WHERE namespace = s.namespace AND session_id = s.session_id) AS event_count
		FROM sessions s
		LEFT JOIN session_labels sl
			ON sl.namespace = s.namespace AND sl.session_id = s.session_id
		WHERE `
	queryTail := `
		GROUP BY s.session_id, s.namespace, s.client, s.created_at, s.last_activity, s.ttl_minutes
		ORDER BY s.last_activity DESC`
	query := queryHead + whereClause + queryTail //nolint:gosec

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ManagedSession, 0)
	for rows.Next() {
		var ms ManagedSession
		var labelsJSON []byte
		if err := rows.Scan(
			&ms.ID, &ms.Namespace, &ms.Client,
			&ms.CreatedAt, &ms.LastActivity, &ms.TTLMinutes,
			&labelsJSON, &ms.EventCount,
		); err != nil {
			return nil, fmt.Errorf("%w: scan session row in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		if len(labelsJSON) > 0 {
			var labels map[string]string
			if err := json.Unmarshal(labelsJSON, &labels); err != nil {
				return nil, fmt.Errorf("%w: decode labels in %q: %w", ErrStorageBackend, filter.Namespace, err)
			}
			if len(labels) > 0 {
				ms.Labels = labels
			}
		}
		out = append(out, ms)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate session rows in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	return out, nil
}

func (s *Store) CountSessions(ctx context.Context, filter ListSessionsFilter) (int, error) {
	if filter.Namespace == "" {
		return 0, ErrNamespaceRequired
	}
	whereClause, args := buildSessionFilterWhere(filter, "s")
	query := `SELECT COUNT(*) FROM sessions s WHERE ` + whereClause //nolint:gosec
	var count int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("%w: count sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	return count, nil
}

func (s *Store) DeleteSession(ctx context.Context, namespace, id string) (DeleteSessionResult, error) {
	if namespace == "" {
		return DeleteSessionResult{}, ErrNamespaceRequired
	}
	if id == "" {
		return DeleteSessionResult{}, ErrSessionIDRequired
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteSessionResult{}, fmt.Errorf("%w: begin delete session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	defer func() { _ = tx.Rollback() }()

	// FOR UPDATE conflicts with FOR KEY SHARE that FK enforcement takes on
	// concurrent INSERT INTO children — those inserts block until our COMMIT
	// and then fail FK. Count and cascade see the same row set.
	var one int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM sessions WHERE namespace = $1 AND session_id = $2 FOR UPDATE`,
		namespace, id,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteSessionResult{}, ErrSessionNotFound
	}
	if err != nil {
		return DeleteSessionResult{}, fmt.Errorf("%w: lock session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	s.logger.WithContext(ctx).Debug("delete session: lock acquired")

	result := DeleteSessionResult{SessionID: id}
	err = tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM sources        WHERE namespace = $1 AND session_id = $2),
			(SELECT COUNT(*) FROM chunks         WHERE source_id IN
				(SELECT id FROM sources WHERE namespace = $1 AND session_id = $2)),
			(SELECT COUNT(*) FROM session_events WHERE namespace = $1 AND session_id = $2),
			(SELECT COUNT(*) FROM session_labels WHERE namespace = $1 AND session_id = $2)`,
		namespace, id,
	).Scan(
		&result.Cascaded.Sources,
		&result.Cascaded.Chunks,
		&result.Cascaded.Events,
		&result.Cascaded.Labels,
	)
	if err != nil {
		return DeleteSessionResult{}, fmt.Errorf("%w: count cascade for %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	s.logger.WithContext(ctx).Debug("delete session: cascade computed",
		logging.IntField("sources", result.Cascaded.Sources),
		logging.IntField("chunks", result.Cascaded.Chunks),
		logging.IntField("events", result.Cascaded.Events),
		logging.IntField("labels", result.Cascaded.Labels),
	)

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE namespace = $1 AND session_id = $2`,
		namespace, id,
	); err != nil {
		return DeleteSessionResult{}, fmt.Errorf("%w: delete session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}

	if err := tx.Commit(); err != nil {
		return DeleteSessionResult{}, fmt.Errorf("%w: commit delete session %q/%q: %w", ErrStorageBackend, namespace, id, err)
	}
	s.logger.WithContext(ctx).Debug("delete session: committed")
	return result, nil
}

func (s *Store) DeleteSessions(ctx context.Context, filter ListSessionsFilter) (DeleteSessionsResult, error) {
	if filter.Namespace == "" {
		return DeleteSessionsResult{}, ErrNamespaceRequired
	}

	whereClause, args := buildSessionFilterWhere(filter, "")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeleteSessionsResult{}, fmt.Errorf("%w: begin delete sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	defer func() { _ = tx.Rollback() }()

	lockQuery := `SELECT session_id FROM sessions WHERE ` + whereClause + ` FOR UPDATE` //nolint:gosec
	rows, err := tx.QueryContext(ctx, lockQuery, args...)
	if err != nil {
		return DeleteSessionsResult{}, fmt.Errorf("%w: lock sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			_ = rows.Close()
			return DeleteSessionsResult{}, fmt.Errorf("%w: scan locked session row in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		ids = append(ids, sid)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return DeleteSessionsResult{}, fmt.Errorf("%w: iterate locked session rows in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	_ = rows.Close()
	s.logger.WithContext(ctx).Debug("delete sessions: locked rows", logging.IntField("count", len(ids)))

	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return DeleteSessionsResult{}, fmt.Errorf("%w: commit empty delete sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
		}
		return DeleteSessionsResult{}, nil
	}

	result := DeleteSessionsResult{DeletedSessions: len(ids)}
	err = tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM sources        WHERE namespace = $1 AND session_id = ANY($2)),
			(SELECT COUNT(*) FROM chunks         WHERE source_id IN
				(SELECT id FROM sources WHERE namespace = $1 AND session_id = ANY($2))),
			(SELECT COUNT(*) FROM session_events WHERE namespace = $1 AND session_id = ANY($2)),
			(SELECT COUNT(*) FROM session_labels WHERE namespace = $1 AND session_id = ANY($2))`,
		filter.Namespace, ids,
	).Scan(
		&result.Cascaded.Sources,
		&result.Cascaded.Chunks,
		&result.Cascaded.Events,
		&result.Cascaded.Labels,
	)
	if err != nil {
		return DeleteSessionsResult{}, fmt.Errorf("%w: count cascade for sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	s.logger.WithContext(ctx).Debug("delete sessions: cascade computed",
		logging.IntField("sources", result.Cascaded.Sources),
		logging.IntField("chunks", result.Cascaded.Chunks),
		logging.IntField("events", result.Cascaded.Events),
		logging.IntField("labels", result.Cascaded.Labels),
	)

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE namespace = $1 AND session_id = ANY($2)`,
		filter.Namespace, ids,
	); err != nil {
		return DeleteSessionsResult{}, fmt.Errorf("%w: delete sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}

	if err := tx.Commit(); err != nil {
		return DeleteSessionsResult{}, fmt.Errorf("%w: commit delete sessions in %q: %w", ErrStorageBackend, filter.Namespace, err)
	}
	s.logger.WithContext(ctx).Debug("delete sessions: committed")
	return result, nil
}

func (s *Store) ScanExpiredSessions(ctx context.Context, now time.Time) ([]SessionRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, namespace
		FROM sessions
		WHERE last_activity + (ttl_minutes || ' minutes')::interval < $1
		ORDER BY last_activity ASC`,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: scan expired sessions: %w", ErrStorageBackend, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]SessionRef, 0)
	for rows.Next() {
		var ref SessionRef
		if err := rows.Scan(&ref.SessionID, &ref.Namespace); err != nil {
			return nil, fmt.Errorf("%w: scan expired session row: %w", ErrStorageBackend, err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate expired session rows: %w", ErrStorageBackend, err)
	}
	return out, nil
}

// buildSessionFilterWhere returns the WHERE-clause body (no leading "WHERE")
// and arg slice for ListManagedSessions / CountSessions / DeleteSessions. The
// `alias` qualifies session columns (e.g. "s"); "" means bare. Caller validates
// filter.Namespace.
func buildSessionFilterWhere(filter ListSessionsFilter, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}

	clauses := make([]string, 0, 3)
	args := make([]any, 0, 2+2*len(filter.Labels))

	args = append(args, filter.Namespace)
	clauses = append(clauses, fmt.Sprintf("%snamespace = $%d", prefix, len(args)))

	if filter.Client != "" {
		args = append(args, filter.Client)
		clauses = append(clauses, fmt.Sprintf("%sclient = $%d", prefix, len(args)))
	}

	if len(filter.Labels) > 0 {
		pairs := make([]string, 0, len(filter.Labels))
		for k, v := range filter.Labels {
			args = append(args, k)
			kIdx := len(args)
			args = append(args, v)
			vIdx := len(args)
			pairs = append(pairs, fmt.Sprintf("(key = $%d AND value = $%d)", kIdx, vIdx))
		}
		clauses = append(clauses, fmt.Sprintf(
			"(%snamespace, %ssession_id) IN ("+
				"SELECT namespace, session_id FROM session_labels "+
				"WHERE namespace = $1 AND (%s) "+
				"GROUP BY namespace, session_id "+
				"HAVING COUNT(DISTINCT key) = %d)",
			prefix, prefix, strings.Join(pairs, " OR "), len(filter.Labels),
		))
	}

	return strings.Join(clauses, " AND "), args
}

// ---------- Content (stubbed; targets of Phase B WIs) ----------

func (s *Store) Index(ctx context.Context, in IndexInput) error {
	return ErrNotImplemented
}

func (s *Store) Search(ctx context.Context, in SearchInput) ([]SearchResult, error) {
	return nil, ErrNotImplemented
}

func (s *Store) ListSources(ctx context.Context, sessionID string) ([]SourceSummary, error) {
	return nil, ErrNotImplemented
}

// ---------- Events (stubbed; targets of Phase C WIs) ----------

func (s *Store) WriteEvents(ctx context.Context, sessionID string, events []Event) (int, error) {
	return 0, ErrNotImplemented
}

func (s *Store) ReadEvents(ctx context.Context, sessionID string, filter EventFilter) ([]Event, error) {
	return nil, ErrNotImplemented
}
