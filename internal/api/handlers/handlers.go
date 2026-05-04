// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
)

type Deps struct {
	Logger *logging.Logger
	Store  db.DAL
}

type Handlers struct {
	deps Deps
}

func New(deps Deps) *Handlers {
	return &Handlers{deps: deps}
}
