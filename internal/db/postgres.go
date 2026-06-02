// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	logging "github.com/one-harsh/context-logging"
)

// Store's per-entity repo methods are each self-contained. Cross-repo
// atomicity goes through Store.WithTx — composing two Clients()/Sessions()
// calls does NOT share a transaction.
type Store struct {
	db       *sql.DB
	logger   *logging.Logger
	clients  *clientRepo
	sessions *sessionRepo
	events   *eventsRepo
	sources  *sourcesRepo
}

func (s *Store) Clients() ClientRepo   { return s.clients }
func (s *Store) Sessions() SessionRepo { return s.sessions }
func (s *Store) Events() EventsRepo    { return s.events }
func (s *Store) Sources() SourcesRepo  { return s.sources }

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

	s := &Store{db: conn, logger: logger}
	s.clients = &clientRepo{queryer: conn, logger: logger}
	s.sessions = &sessionRepo{queryer: conn, logger: logger}
	s.events = &eventsRepo{queryer: conn, logger: logger}
	s.sources = &sourcesRepo{queryer: conn, logger: logger}
	return s, nil
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
