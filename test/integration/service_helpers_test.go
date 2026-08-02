// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/clientreg"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/session"
)

// Composed session/client deletes live in the service layer (transactions live
// in services, not the DAL). These helpers construct a throwaway service so
// tests that assert the cascade choreography exercise the same composition
// production does, rather than reaching for a removed DAL-composed method.

func sessionSvc(store *db.Store) *session.Service {
	return session.New(store, session.Config{CacheSize: 10_000}, logging.Nop())
}

func clientSvc(store *db.Store) *clientreg.Service {
	return clientreg.New(store, logging.Nop())
}
