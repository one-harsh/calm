// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import "embed"

//go:embed migrations/*.sql
var migrationsFS embed.FS
