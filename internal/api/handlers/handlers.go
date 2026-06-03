// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"errors"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/clientreg"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/ingest"
	"github.com/one-harsh/calm/internal/session"
	"github.com/one-harsh/calm/internal/snapshot"
)

// ErrNotImplemented is the sentinel stubs return; StrictErrorHandler maps it to 501.
var ErrNotImplemented = errors.New("endpoint not implemented")

type HandlersConfig struct {
	DefaultTTLMinutes int
	MaxTTLMinutes     int
}

type Deps struct {
	Logger   *logging.Logger
	Registry auth.Registry
	Clients  *clientreg.Service
	Sessions *session.Service
	Events   db.EventsRepo
	Snapshot *snapshot.Service
	Ingest   *ingest.Service
	Sources  db.SourcesRepo
	Cfg      HandlersConfig
}

type Handlers struct {
	deps Deps
}

func New(deps Deps) *Handlers {
	return &Handlers{deps: deps}
}

var _ genapi.StrictServerInterface = (*Handlers)(nil)
