// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// The CAS branch: when the failed token is no longer the current session
// (another path already recovered, or shutdown cleared it), recoverSession
// returns session_lost without a create. Unreachable through the serialized
// dispatch harness, so exercised white-box.
func TestRecoverSession_AlreadyReplaced_NoSecondCreate(t *testing.T) {
	m := calm.NewMockClient(t) // strict: any CreateSession call fails the test
	s := NewServer(Config{Calm: m, Logger: logging.Nop(), SessionTTLMinutes: 60})
	s.mu.Lock()
	s.session = "tok-2"
	s.mu.Unlock()

	sig := s.recoverSession(context.Background(), "stale-tok")
	if sig.Reason != obs.DegradedReasonSessionLost {
		t.Errorf("reason = %q; want session_lost", sig.Reason)
	}
}

// Each recovery attempt derives a distinct idempotency key from the base —
// never the base itself, never a repeat — so CALM's create-dedup window can
// neither replay the dead initialize session nor a failed prior attempt.
func TestRecoverSession_IdempotencyKeyVariesPerAttempt(t *testing.T) {
	m := calm.NewMockClient(t)
	var keys []string
	m.EXPECT().CreateSession(mock.Anything, "claude-code", 60, mock.Anything).RunAndReturn(
		func(_ context.Context, _ string, _ int, key string) (string, error) {
			keys = append(keys, key)
			return "", errors.New("dial tcp: connection refused")
		},
	).Times(2)

	s := NewServer(Config{Calm: m, Logger: logging.Nop(), SessionTTLMinutes: 60, SessionIdempotencyKey: "base"})
	s.mu.Lock()
	s.session = "tok-1"
	s.sessionClient = "claude-code"
	s.mu.Unlock()

	// Transient failures leave the session dead, so each call re-attempts.
	_ = s.recoverSession(context.Background(), "tok-1")
	_ = s.recoverSession(context.Background(), "tok-1")

	if len(keys) != 2 || keys[0] != "base-r1" || keys[1] != "base-r2" {
		t.Errorf("recovery keys = %v; want [base-r1 base-r2]", keys)
	}
}
