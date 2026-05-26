// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"fmt"
)

// queryer is what both *sql.DB and *sql.Tx satisfy. Repo methods hold a
// queryer so the same SQL runs against either: directly against the DB
// (when reached via Store.Clients() / Store.Sessions()) or against a tx
// (when reached via Store.WithTx → Repos.Clients / Repos.Sessions).
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// `inTx` runs `fn` inside a transaction. Multi-statement repo methods use this
// so they remain atomic whether called from Store or from inside WithTx.
func inTx(ctx context.Context, q queryer, fn func(*sql.Tx) error) error {
	// If q is already a tx, we're already in a transaction — just run fn.
	if tx, ok := q.(*sql.Tx); ok {
		return fn(tx)
	}

	db, ok := q.(*sql.DB)
	if !ok {
		return fmt.Errorf("%w: inTx: unsupported queryer %T", ErrStorageBackend, q)
	}

	// Otherwise, q is a DB and we need to begin a new transaction for fn.
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

// Repos bundles the tx-bound repositories passed into a WithTx closure. Each
// repo is the same ClientRepo / SessionRepo interface callers know from
// Store.Clients() / Store.Sessions(), backed by an impl that runs against
// the enclosing transaction.
type Repos struct {
	Clients  ClientRepo
	Sessions SessionRepo
}

// `WithTx` executes `fn` inside a single transaction. Both repos handed to
// `fn` share that transaction — composition across entities (e.g. "register
// client then create session" atomically) goes through this primitive.
// Rolls back on any `fn` error or commit failure.
func (s *Store) WithTx(ctx context.Context, fn func(Repos) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin tx: %w", ErrStorageBackend, err)
	}
	defer func() { _ = tx.Rollback() }()
	repos := Repos{
		Clients:  &clientRepo{queryer: tx, logger: s.logger},
		Sessions: &sessionRepo{queryer: tx, logger: s.logger},
	}
	if err := fn(repos); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit tx: %w", ErrStorageBackend, err)
	}
	return nil
}
