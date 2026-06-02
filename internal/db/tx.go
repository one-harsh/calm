// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"fmt"
)

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn inside a tx, or passes through if q is already a tx.
func inTx(ctx context.Context, q queryer, fn func(*sql.Tx) error) error {
	if tx, ok := q.(*sql.Tx); ok {
		return fn(tx)
	}

	db, ok := q.(*sql.DB)
	if !ok {
		return fmt.Errorf("%w: inTx: unsupported queryer %T", ErrStorageBackend, q)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin tx: %w", ErrStorageBackend, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit tx: %w", ErrStorageBackend, err)
	}
	return nil
}

type Repos struct {
	Clients  ClientRepo
	Sessions SessionRepo
	Events   EventsRepo
	Sources  SourcesRepo
}

// WithTx executes fn inside a single transaction shared across both repos.
func (s *Store) WithTx(ctx context.Context, fn func(Repos) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin tx: %w", ErrStorageBackend, err)
	}
	defer func() { _ = tx.Rollback() }()
	repos := Repos{
		Clients:  &clientRepo{queryer: tx, logger: s.logger},
		Sessions: &sessionRepo{queryer: tx, logger: s.logger},
		Events:   &eventsRepo{queryer: tx, logger: s.logger},
		Sources:  &sourcesRepo{queryer: tx, logger: s.logger},
	}
	if err := fn(repos); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit tx: %w", ErrStorageBackend, err)
	}
	return nil
}
