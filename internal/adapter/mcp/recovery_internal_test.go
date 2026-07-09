// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

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

// Establishment attempts are throttled: inside the window ensureSession
// degrades without a create; past it, the next capture re-attempts.
// Registration is memoized on first success (the mock's Once enforces it
// across all attempts). White-box because the throttle window is backdated
// directly.
func TestEnsureSession_ThrottledThenRetries(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "calm-adapter").Return(true, nil).Once()
	calls := 0
	m.EXPECT().CreateSession(mock.Anything, "calm-adapter", 60, mock.Anything).RunAndReturn(
		func(context.Context, string, int, string) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("dial tcp: connection refused")
			}
			return "tok-lazy", nil
		},
	).Times(2)
	s := NewServer(Config{Calm: m, Logger: logging.Nop(), SessionTTLMinutes: 60, DefaultClient: "calm-adapter"})

	if _, sig := s.ensureSession(context.Background()); sig == nil || sig.Reason != obs.DegradedReasonCalmUnreachable {
		t.Fatalf("failed attempt signal = %+v; want calm_unreachable", sig)
	}
	if _, sig := s.ensureSession(context.Background()); sig == nil || sig.Reason != obs.DegradedReasonCalmUnreachable {
		t.Fatalf("throttled signal = %+v; want calm_unreachable without a create", sig)
	}
	if calls != 1 {
		t.Fatalf("creates inside the window = %d; want 1", calls)
	}

	s.mu.Lock()
	s.lastEstablishAttempt = time.Now().Add(-establishRetryInterval - time.Second)
	s.mu.Unlock()
	tok, sig := s.ensureSession(context.Background())
	if sig != nil || tok != "tok-lazy" {
		t.Fatalf("post-interval attempt = %q, %+v; want established token", tok, sig)
	}
	if _, sig := s.ensureSession(context.Background()); sig != nil {
		t.Errorf("established session must return without a create; got %+v", sig)
	}
}

// A 401 on the lazy registration is a credential verdict: auth_failed
// latches and no create fires (strict mock proves it).
func TestEnsureSession_RegisterAuthRejected_Latches(t *testing.T) {
	m := calm.NewMockClient(t)
	m.EXPECT().RegisterClient(mock.Anything, "calm-adapter").
		Return(false, &calm.StatusError{Op: "register client", Code: 401, Status: "401 Unauthorized"}).Once()
	s := NewServer(Config{Calm: m, Logger: logging.Nop(), SessionTTLMinutes: 60, DefaultClient: "calm-adapter"})

	if _, sig := s.ensureSession(context.Background()); sig == nil || sig.Reason != obs.DegradedReasonAuthFailed {
		t.Fatalf("signal = %+v; want auth_failed", sig)
	}
	if _, sig := s.ensureSession(context.Background()); sig == nil || sig.Reason != obs.DegradedReasonAuthFailed {
		t.Errorf("latched signal = %+v; want auth_failed with zero CALM traffic", sig)
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
