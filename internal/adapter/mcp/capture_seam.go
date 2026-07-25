// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"

	"github.com/one-harsh/calm/internal/adapter/capture"
)

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
// registry.
func (s *Server) Record(_ context.Context, delta []capture.SourceToken) {
	for _, st := range delta {
		s.registry.Record(st.Source, st.Token)
	}
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
