// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package clientreg

import (
	"context"
	"fmt"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

// RegisterResult.Created is false on collision (Register is idempotent at the DAL).
type RegisterResult struct {
	Name      string
	Namespace string
	Created   bool
	CreatedAt time.Time
}

type CredentialedRegisterResult struct {
	Name      string
	Namespace string
	RawToken  string
	CreatedAt time.Time
}

type Service struct {
	store  db.DAL
	logger *logging.Logger
}

func New(store db.DAL, logger *logging.Logger) *Service {
	return &Service{store: store, logger: logger}
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

func (s *Service) Register(ctx context.Context, namespace, name string) (RegisterResult, error) {
	if namespace == "" {
		return RegisterResult{}, db.ErrNamespaceRequired
	}
	if name == "" {
		return RegisterResult{}, db.ErrClientNameRequired
	}
	created, err := s.store.Clients().Register(ctx, namespace, name)
	if err != nil {
		return RegisterResult{}, err
	}
	return RegisterResult{
		Name:      name,
		Namespace: namespace,
		Created:   created,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// RegisterWithCredential errors with ErrClientExists on duplicate (no re-issue; token unrecoverable post-hash).
func (s *Service) RegisterWithCredential(ctx context.Context, namespace, name string) (CredentialedRegisterResult, error) {
	if namespace == "" {
		return CredentialedRegisterResult{}, db.ErrNamespaceRequired
	}
	if name == "" {
		return CredentialedRegisterResult{}, db.ErrClientNameRequired
	}
	raw, err := auth.NewRandomToken()
	if err != nil {
		return CredentialedRegisterResult{}, fmt.Errorf("generate client token: %w", err)
	}
	hash := auth.HashToken(namespace, raw)
	if err := s.store.Clients().RegisterWithCredential(ctx, namespace, name, hash); err != nil {
		return CredentialedRegisterResult{}, err
	}
	return CredentialedRegisterResult{
		Name:      name,
		Namespace: namespace,
		RawToken:  raw,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// RotateToken is strict — no overlap window; old token rejects immediately.
func (s *Service) RotateToken(ctx context.Context, namespace, name string) (string, error) {
	if namespace == "" {
		return "", db.ErrNamespaceRequired
	}
	if name == "" {
		return "", db.ErrClientNameRequired
	}
	raw, err := auth.NewRandomToken()
	if err != nil {
		return "", fmt.Errorf("generate client token: %w", err)
	}
	hash := auth.HashToken(namespace, raw)
	if err := s.store.Clients().RotateCredential(ctx, namespace, name, hash); err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Service) ResolveByToken(ctx context.Context, namespace, rawToken string) (string, error) {
	if namespace == "" {
		return "", db.ErrNamespaceRequired
	}
	if rawToken == "" {
		return "", db.ErrInvalidClientCredential
	}
	return s.store.Clients().LookupByToken(ctx, namespace, auth.HashToken(namespace, rawToken))
}

// SeedDefaults inserts the default client for each uncredentialed namespace (idempotent).
func (s *Service) SeedDefaults(ctx context.Context, namespaces []string) error {
	clients := s.store.Clients()
	for _, ns := range namespaces {
		nsCtx := logging.Bind(ctx, obs.Namespace(ns), obs.Client(db.DefaultClient))
		created, err := clients.Register(nsCtx, ns, db.DefaultClient)
		if err != nil {
			return fmt.Errorf("%w: seed default client for namespace %q", err, ns)
		}
		if created {
			s.logger.WithContext(nsCtx).WithAuditEvent(logging.ResourceCreate).Info("default client seeded",
				obs.AuditInitiatorSystem,
			)
		}
	}
	return nil
}
