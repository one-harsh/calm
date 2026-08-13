// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"errors"
	"strconv"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// establishRetryInterval throttles lazy session establishment so a down CALM
// taxes at most one tool call per interval with the create timeout.
const establishRetryInterval = 60 * time.Second

// sessionFailureSignal classifies a CALM-call error. It returns nil when
// err is not a session-level failure — the caller falls through to its
// existing classification (capture_failed, calm_unreachable, ...).
func (s *Server) sessionFailureSignal(ctx context.Context, failedToken string, err error) *DegradedSignal {
	switch {
	case errors.Is(err, calm.ErrAuthRejected):
		// a direct 401/403 is unambiguous — CALM rejects credentials before
		// resolving the session, so a recreate would prove nothing.
		s.mu.Lock()
		s.authFailed = true
		s.session = ""
		s.mu.Unlock()
		s.log.WithContext(ctx).Warn("CALM rejected credentials; CALM disabled for this conversation",
			obs.DegradedReasonFieldAuthFailed, logging.ErrorField(err))
		return &DegradedSignal{Reason: obs.DegradedReasonAuthFailed}
	case errors.Is(err, calm.ErrSessionNotFound):
		return s.recoverSession(ctx, failedToken)
	default:
		return nil
	}
}

// recoverSession is the recovery loop: one replacement create per session
// loss, doubling as credential validation. It returns the degradation signal
// for the ORIGINAL failed call.
func (s *Server) recoverSession(ctx context.Context, failedToken string) *DegradedSignal {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.authFailed {
		return &DegradedSignal{Reason: obs.DegradedReasonAuthFailed}
	}
	if s.session != failedToken {
		// Another path already replaced (or shutdown cleared) the session;
		// this call still failed against the dead token.
		return &DegradedSignal{Reason: obs.DegradedReasonSessionLost}
	}

	s.recoverySeq++
	key := s.idemKey
	if key != "" {
		// Fresh per-attempt key: reusing the initialize key would let CALM's
		// dedup window replay the dead session token.
		key += "-r" + strconv.Itoa(s.recoverySeq)
	}
	cctx, cancel := context.WithTimeout(ctx, sessionOpTimeout)
	token, err := s.calm.CreateSession(cctx, s.sessionClient, s.ttlMinutes, key)
	cancel()

	switch {
	case err == nil:
		s.session = token
		s.registry.Reset()
		s.basis.Reset()
		s.log.WithContext(ctx).Warn("session lost; replacement created",
			obs.DegradedReasonFieldSessionLost)
		return &DegradedSignal{Reason: obs.DegradedReasonSessionLost}
	case isStatus4xx(err):
		// the recovery create is the credential validation — a 4xx rejection
		// means the credentials are the problem.
		s.authFailed = true
		s.session = ""
		s.log.WithContext(ctx).Warn("session recovery rejected; CALM disabled for this conversation",
			obs.DegradedReasonFieldAuthFailed, logging.ErrorField(err))
		return &DegradedSignal{Reason: obs.DegradedReasonAuthFailed}
	default:
		// transient create failure teaches nothing about credentials —
		// the session stays dead and the next 404 re-attempts recovery.
		s.log.WithContext(ctx).Warn("session recovery failed; will retry on next session loss",
			obs.DegradedReasonFieldCalmUnreachable, logging.ErrorField(err))
		return &DegradedSignal{Reason: obs.DegradedReasonCalmUnreachable, Detail: err.Error()}
	}
}

func (s *Server) ensureSession(ctx context.Context) (string, *DegradedSignal) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.authFailed {
		return "", &DegradedSignal{Reason: obs.DegradedReasonAuthFailed}
	}
	if s.session != "" {
		return s.session, nil
	}
	if time.Since(s.lastEstablishAttempt) < establishRetryInterval {
		return "", &DegradedSignal{Reason: obs.DegradedReasonCalmUnreachable}
	}
	s.lastEstablishAttempt = time.Now()

	// initialize stamps sessionClient even when its create fails; the default
	// covers a capture arriving before any initialize.
	client := s.sessionClient
	if client == "" {
		client = s.defaultClient
	}

	// Registration must land before the create: an unregistered client's
	// create is a guaranteed 400, and 4xx on create reads as a credential
	// verdict — latching auth_failed on good credentials would disable CALM
	// for the process over a missed registration.
	if !s.clientRegistered {
		rctx, rcancel := context.WithTimeout(ctx, sessionOpTimeout)
		_, rerr := s.calm.RegisterClient(rctx, client)
		rcancel()
		switch {
		case rerr == nil:
			s.clientRegistered = true
		case errors.Is(rerr, calm.ErrAuthRejected):
			s.authFailed = true
			s.log.WithContext(ctx).Warn("client registration rejected; CALM disabled for this conversation",
				obs.DegradedReasonFieldAuthFailed, logging.ErrorField(rerr))
			return "", &DegradedSignal{Reason: obs.DegradedReasonAuthFailed}
		default:
			s.log.WithContext(ctx).Warn("client registration failed; deferring session create",
				obs.DegradedReasonFieldCalmUnreachable, logging.ErrorField(rerr))
			return "", &DegradedSignal{Reason: obs.DegradedReasonCalmUnreachable, Detail: rerr.Error()}
		}
	}

	s.recoverySeq++
	key := s.idemKey
	if key != "" {
		key += "-r" + strconv.Itoa(s.recoverySeq)
	}
	cctx, cancel := context.WithTimeout(ctx, sessionOpTimeout)
	token, err := s.calm.CreateSession(cctx, client, s.ttlMinutes, key)
	cancel()

	switch {
	case err == nil:
		s.session = token
		s.sessionClient = client
		s.registry.Reset()
		s.basis.Reset()
		s.log.WithContext(ctx).Info("session created",
			logging.StringField("client", client))
		return token, nil
	case isStatus4xx(err):
		s.authFailed = true
		s.log.WithContext(ctx).Warn("session create rejected; CALM disabled for this conversation",
			obs.DegradedReasonFieldAuthFailed, logging.ErrorField(err))
		return "", &DegradedSignal{Reason: obs.DegradedReasonAuthFailed}
	default:
		s.log.WithContext(ctx).Warn("lazy session create failed; will retry after interval",
			obs.DegradedReasonFieldCalmUnreachable, logging.ErrorField(err))
		return "", &DegradedSignal{Reason: obs.DegradedReasonCalmUnreachable, Detail: err.Error()}
	}
}

func isStatus4xx(err error) bool {
	var se *calm.StatusError
	return errors.As(err, &se) && se.Code >= 400 && se.Code < 500
}
