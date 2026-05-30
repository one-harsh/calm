// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package clientreg

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

// RegisterResult is what Service.Register returns. Created reflects whether
// the row was inserted (false on collision; the DAL Register is idempotent).
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
	store db.DAL
}

func New(store db.DAL) *Service {
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

// RegisterWithCredential mints a fresh random token, hashes it
// (sha256(namespace || 0x00 || token)), and persists the hash. Returns the
// raw token for the handler to surface to the workload (one-time-display;
// the server never stores it).
//
// Errors with db.ErrClientExists on duplicate —
// no silent re-issue, since the older token can't be recovered from its hash.
func (s *Service) RegisterWithCredential(ctx context.Context, namespace, name string) (CredentialedRegisterResult, error) {
	if namespace == "" {
		return CredentialedRegisterResult{}, db.ErrNamespaceRequired
	}
	if name == "" {
		return CredentialedRegisterResult{}, db.ErrClientNameRequired
	}
	raw, err := newRandomToken()
	if err != nil {
		return CredentialedRegisterResult{}, fmt.Errorf("generate client token: %w", err)
	}
	hash := hashToken(namespace, raw)
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

// RotateToken mints a fresh token, hashes it, and atomically replaces the
// stored hash via RotateCredential. Old token is rejected immediately on
// subsequent calls (strict rotation; no overlap window). Returns the new
// raw token for the handler to surface.
func (s *Service) RotateToken(ctx context.Context, namespace, name string) (string, error) {
	if namespace == "" {
		return "", db.ErrNamespaceRequired
	}
	if name == "" {
		return "", db.ErrClientNameRequired
	}
	raw, err := newRandomToken()
	if err != nil {
		return "", fmt.Errorf("generate client token: %w", err)
	}
	hash := hashToken(namespace, raw)
	if err := s.store.Clients().RotateCredential(ctx, namespace, name, hash); err != nil {
		return "", err
	}
	return raw, nil
}

// ResolveByToken hashes the workload-presented raw token and looks up the
// (namespace, hash) pair. Returns the client name or ErrInvalidClientCredential.
// Used by the auth middleware in namespaces that require client credentials.
//
// TODO: caching layer if this becomes a bottleneck (hashes are stable, so
// cache entries don't need expiration; just invalidate on RotateCredential).
func (s *Service) ResolveByToken(ctx context.Context, namespace, rawToken string) (string, error) {
	if namespace == "" {
		return "", db.ErrNamespaceRequired
	}
	if rawToken == "" {
		return "", db.ErrInvalidClientCredential
	}
	return s.store.Clients().LookupByToken(ctx, namespace, hashToken(namespace, rawToken))
}

// SeedDefaults ensures every configured namespace has its DL01 default-client
// row, the FK precondition for sessions that omit `client` at creation
// (which insert with `client='default'`). Idempotent at the DAL layer.
// Credentialed namespaces skip the seed — workloads must explicitly register
// (the default client would have no token and would be unreachable from
// session operations there, undermining the within-namespace isolation
// the namespace opted into).
func (s *Service) SeedDefaults(ctx context.Context, namespaces []string) error {
	clients := s.store.Clients()
	for _, ns := range namespaces {
		nsCtx := logging.Bind(ctx, obs.Namespace(ns), obs.Client(db.DefaultClient))
		if _, err := clients.Register(nsCtx, ns, db.DefaultClient); err != nil {
			return fmt.Errorf("%w: seed default client for namespace %q", err, ns)
		}
	}
	return nil
}

// newRandomToken returns a 32-byte crypto-random string, base64url-encoded
// (43 chars, URL-safe). 256 bits of entropy — collision-resistant at any
// realistic scale.
func newRandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashToken returns sha256(namespace || 0x00 || rawToken). Namespace acts as
// a domain separator so the same raw token in two namespaces hashes to two
// distinct bytea values.
func hashToken(namespace, rawToken string) []byte {
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte{0})
	h.Write([]byte(rawToken))
	return h.Sum(nil)
}
