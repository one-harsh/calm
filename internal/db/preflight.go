// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const sharedPreloadRequired = "pg_textsearch"

// Preflight verifies the Postgres CALM is talking to has the extensions and
// configuration the schema relies on:
//   - shared_preload_libraries contains pg_textsearch (required for the bm25
//     access method to load).
//   - pg_textsearch and pg_trgm are installed (in pg_available_extensions).
func Preflight(ctx context.Context, conn *sql.DB) error {
	if err := checkSharedPreloadLibraries(ctx, conn); err != nil {
		return err
	}
	return checkExtensionsAvailable(ctx, conn)
}

func checkSharedPreloadLibraries(ctx context.Context, conn *sql.DB) error {
	var raw string
	if err := conn.QueryRowContext(ctx, `SHOW shared_preload_libraries`).Scan(&raw); err != nil {
		return fmt.Errorf("postgres preflight: read shared_preload_libraries: %w", err)
	}
	for _, lib := range strings.Split(raw, ",") {
		if strings.TrimSpace(lib) == sharedPreloadRequired {
			return nil
		}
	}
	return fmt.Errorf(
		"postgres preflight: shared_preload_libraries=%q does not contain %q; "+
			"add it to postgresql.conf and restart the server",
		raw, sharedPreloadRequired,
	)
}

func checkExtensionsAvailable(ctx context.Context, conn *sql.DB) error {
	required := []string{"pg_textsearch", "pg_trgm"}
	rows, err := conn.QueryContext(ctx, `
		SELECT name FROM pg_available_extensions WHERE name = ANY($1)
	`, required)
	if err != nil {
		return fmt.Errorf("postgres preflight: probe pg_available_extensions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	found := make(map[string]bool, len(required))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("postgres preflight: scan extension name: %w", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres preflight: iterate extensions: %w", err)
	}

	var missing []string
	for _, name := range required {
		if !found[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"postgres preflight: required extensions not available: %s — install them on the Postgres server",
			strings.Join(missing, ", "),
		)
	}
	return nil
}
