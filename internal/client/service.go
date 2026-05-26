// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

// Service owns client-entity orchestration. Handlers call Service, never
// *db.Store's ClientRepo directly — this is where cache invalidation,
// metrics, and audit hooks land as they come online.
type Service struct {
	store *db.Store
}

func New(store *db.Store) *Service {
	return &Service{store: store}
}

func (s *Service) List(ctx context.Context, namespace string) ([]db.ClientSummary, error) {
	return s.store.Clients().List(ctx, namespace)
}

func (s *Service) CountSessions(ctx context.Context, namespace, name string) (int, error) {
	return s.store.Clients().CountSessions(ctx, namespace, name)
}

func (s *Service) Delete(ctx context.Context, namespace, name string) (db.DeleteClientResult, error) {
	return s.store.Clients().Delete(ctx, namespace, name)
}

// SeedDefaults ensures every configured namespace has its DL01 default-client
// row, the FK precondition for sessions that omit `client` at creation
// (which insert with `client='default'`). Idempotent.
func (s *Service) SeedDefaults(ctx context.Context, namespaces []string) error {
	clients := s.store.Clients()
	for _, ns := range namespaces {
		nsCtx := logging.Bind(ctx, obs.Namespace(ns), obs.Client(db.DefaultClient))
		if err := clients.Register(nsCtx, ns, db.DefaultClient); err != nil {
			return fmt.Errorf("%w: seed default client for namespace %q", err, ns)
		}
	}
	return nil
}
