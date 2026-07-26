// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
)

// eventTimeout bounds the fire-and-forget event write. The MCP shell delivers
// events off the response path, so a slow /v1/events can't hold a tool call
// hostage (never-worse).
const eventTimeout = 5 * time.Second

// The Server is the MCP shell's capture.Session: it holds the per-session
// credential, sequence, and registry the engine reaches through this seam.
var _ capture.Session = (*Server)(nil)

// Ensure adapts lazy session establishment to the engine seam, allocating the
// capture sequence only on success so a degraded call never burns a number
// (DESIGN.md §4).
func (s *Server) Ensure(ctx context.Context) (capture.EnsureResult, *capture.Signal) {
	token, sig := s.ensureSession(ctx)
	if cs := sig.toCapture(); cs != nil {
		return capture.EnsureResult{}, cs
	}
	return capture.EnsureResult{Token: token, Seq: s.seq.Add(1)}, nil
}

// OnCallError adapts session-level failure classification (auth rejection,
// session loss with its one recovery create) to the engine seam.
func (s *Server) OnCallError(ctx context.Context, failedToken string, err error) *capture.Signal {
	return s.sessionFailureSignal(ctx, failedToken, err).toCapture()
}

// Record registers the capture's persisted delta in the shell's in-memory
// registry, discarding a delta whose session was replaced mid-call: the
// registry was reset for the new generation, and dead-generation labels must
// fail with staleness later, never validate against the new session (honest
// capture continuity).
func (s *Server) Record(_ context.Context, token string, delta []capture.SourceToken) {
	if cur, _ := s.sessionState(); cur != token {
		return
	}
	for _, st := range delta {
		s.registry.Record(st.Source, st.Token)
	}
}

// Emit is the MCP shell's event-delivery strategy: fire-and-forget in a detached
// goroutine bounded by eventTimeout, off the response path so events never delay
// the tool result (never-worse). Event derivation is engine-owned; this delivery
// mechanism is the shell's (DESIGN.md §5).
func (s *Server) Emit(ctx context.Context, token string, ev []calm.EventInput) {
	if cur, _ := s.sessionState(); cur != token {
		return
	}
	ectx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				s.log.WithContext(ectx).Warn("event emission panicked", logging.AnyField("panic", p))
			}
		}()
		wctx, cancel := context.WithTimeout(ectx, eventTimeout)
		defer cancel()
		// AD03: no recovery trigger here — every event write follows an ingest
		// on the same token, so either that ingest already recovered or the
		// next tool call will; recovering from this goroutine would add
		// concurrency surface for no visible benefit.
		if err := s.calm.WriteEvents(wctx, token, ev); err != nil {
			s.log.WithContext(wctx).Warn("write events failed", logging.ErrorField(err))
		}
	}()
}

// outcomeToResult maps a capture.Outcome onto the MCP tool-result contract: the
// visible text as a non-error result (capture tools stay never-worse), plus a
// *DegradedSignal when the engine classified the call as degraded so invokeTool
// layers the canonical phrasing prefix and degraded summary fields.
func (s *Server) outcomeToResult(out capture.Outcome) (ToolResult, error) {
	res := TextResult(out.Visible, false)
	if out.Reason == "" {
		return res, nil
	}
	return res, &DegradedSignal{Reason: out.Reason, Detail: out.Detail}
}
