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

// Service owns client-entity orchestration. Handlers call Service, never the
// db.ClientRepo directly — this is where cache invalidation, metrics, and
// audit hooks land as they come online.
type Service struct {
	repo db.ClientRepo
}

func New(repo db.ClientRepo) *Service {
	return &Service{repo: repo}
}

func (s *Service) List(ctx context.Context, namespace string) ([]db.ClientSummary, error) {
	return s.repo.List(ctx, namespace)
}

func (s *Service) CountSessions(ctx context.Context, namespace, name string) (int, error) {
	return s.repo.CountSessions(ctx, namespace, name)
}

func (s *Service) Delete(ctx context.Context, namespace, name string) (db.DeleteClientResult, error) {
	return s.repo.Delete(ctx, namespace, name)
}

// SeedDefaults ensures every configured namespace has its DL01 default-client
// row, the FK precondition for sessions that omit `client` at creation
// (which insert with `client='default'`). Idempotent.
func (s *Service) SeedDefaults(ctx context.Context, namespaces []string) error {
	for _, ns := range namespaces {
		nsCtx := logging.Bind(ctx, obs.Namespace(ns), obs.Client(db.DefaultClient))
		if err := s.repo.Register(nsCtx, ns, db.DefaultClient); err != nil {
			return fmt.Errorf("%w: seed default client for namespace %q", err, ns)
		}
	}
	return nil
}
