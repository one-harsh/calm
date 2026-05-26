// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/client"
	"github.com/one-harsh/calm/internal/session"
)

type Deps struct {
	Logger   *logging.Logger
	Clients  *client.Service
	Sessions *session.Service
}

type Handlers struct {
	deps Deps
}

func New(deps Deps) *Handlers {
	return &Handlers{deps: deps}
}
