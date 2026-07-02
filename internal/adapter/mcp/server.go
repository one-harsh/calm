// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const (
	defaultProtocolVersion = "2025-06-18"
	sessionOpTimeout       = 5 * time.Second
)

type Config struct {
	Tools             []Tool
	Calm              calm.Client
	Logger            *logging.Logger
	ServerName        string
	ServerVersion     string
	DefaultClient     string
	SessionTTLMinutes int
	WorkspaceRoot     string
}

type Server struct {
	tools         map[string]Tool
	order         []string
	calm          calm.Client
	log           *logging.Logger
	name          string
	version       string
	defaultClient string
	ttlMinutes    int
	// DESIGN-DEVIATION: DESIGN.md §5 Workspace Binding — single-workspace only;
	// multi-workspace WorkspaceID exists in the extract grammar and is tested,
	// but no caller populates it from runtime, so cross-workspace
	// same-relative-path captures collide. Multi-workspace binding (explicit
	// roots or host-config discovery + collision-free WorkspaceID derivation +
	// startup conflict refusal) is needed to lift this.
	workspaceRoot string
	seq           atomic.Int64

	wmu sync.Mutex // serializes writes to the protocol channel
	out io.Writer

	mu sync.Mutex
	// DESIGN-DEVIATION: AD03 — session lost mid-conversation has no recovery
	// path. 404 on session-touching calls isn't detected; no POST /v1/sessions
	// retry; no session_lost degradation surfaced in visible text. The recovery
	// loop should wrap CALM calls, attempt POST /v1/sessions on 404 (which
	// doubles as credential validation), and distinguish session_lost from
	// auth_failed when the create-also-fails.
	session string // empty when CALM is unavailable (never-worse: degraded mode)

	// registry tracks per-invocation staleness tokens for source labels per
	// LABELING.md §2. Its own internal mutex covers reads/writes; there's no
	// coupling with s.mu (which protects the session token above). AI-03's
	// session replacement invokes registry.Reset to invalidate all prior tokens.
	registry *tokenRegistry
}

func NewServer(cfg Config) *Server {
	s := &Server{
		tools:         make(map[string]Tool, len(cfg.Tools)),
		calm:          cfg.Calm,
		log:           cfg.Logger,
		name:          cfg.ServerName,
		version:       cfg.ServerVersion,
		defaultClient: cfg.DefaultClient,
		ttlMinutes:    cfg.SessionTTLMinutes,
		workspaceRoot: cfg.WorkspaceRoot,
		registry:      newTokenRegistry(),
	}
	for _, t := range cfg.Tools {
		s.addTool(t)
	}
	s.registerBuiltins()
	return s
}

func (s *Server) addTool(t Tool) {
	s.tools[t.Name] = t
	s.order = append(s.order, t.Name)
}

func (s *Server) registerBuiltins() {
	// TODO: register the structured-inspection, structured-editing, and
	// context-health tools per DESIGN.md §3. Only calm_run_command and
	// calm_search ship today. Missing: calm_read_file, calm_list_dir,
	// calm_grep, calm_git_status, calm_git_diff (structured inspection,
	// read-only by contract); calm_edit_file, calm_write_file (structured
	// editing, dual-mode capture + file_touched event per AD04);
	// calm_report_outcome (context health, calls /v1/feedback).
	s.addToolIfAbsent(s.newRunCommandTool())
	s.addToolIfAbsent(s.newSearchTool())
}

func (s *Server) addToolIfAbsent(t Tool) {
	if _, ok := s.tools[t.Name]; ok {
		return
	}
	s.addTool(t)
}

func (s *Server) sessionToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.session
}

// stdin reads block and can't be interrupted by ctx, so reading runs in a
// goroutine; the loop exits on EOF or ctx cancel (SIGINT/SIGTERM).
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReader(in)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return ctx.Err()
		case err := <-readErr:
			s.shutdown()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case line := <-lines:
			s.handleLine(ctx, line)
		}
	}
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(ctx, nil, codeParseError, "parse error")
		return
	}
	result, rerr := s.dispatch(ctx, &req)
	if req.isNotification() {
		return
	}
	if rerr != nil {
		s.writeError(ctx, req.ID, rerr.Code, rerr.Message)
		return
	}
	s.writeResult(ctx, req.ID, result)
}

func (s *Server) dispatch(ctx context.Context, req *request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(ctx, req.Params)
	case "notifications/initialized":
		return nil, nil
	case "tools/list":
		return s.toolsList(), nil
	case "tools/call":
		return s.handleToolCall(ctx, req.Params)
	case "ping":
		return map[string]any{}, nil
	default:
		if req.isNotification() {
			return nil, nil // ignore unknown notifications
		}
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      clientInfo     `json:"serverInfo"`
}

func (s *Server) handleInitialize(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p initializeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid initialize params"}
		}
	}

	client := s.defaultClient
	if p.ClientInfo.Name != "" {
		client = p.ClientInfo.Name
	}
	s.mu.Lock()
	haveSession := s.session != ""
	s.mu.Unlock()

	if haveSession {
		s.log.WithContext(ctx).Warn("duplicate initialize; reusing existing session")
	} else {
		rctx, rcancel := context.WithTimeout(ctx, sessionOpTimeout)
		created, rerr := s.calm.RegisterClient(rctx, client)
		rcancel()
		if rerr != nil {
			s.log.WithContext(ctx).Warn("client registration failed; continuing",
				logging.StringField("client", client), logging.ErrorField(rerr))
		} else {
			s.log.WithContext(ctx).Debug("client registered",
				logging.StringField("client", client), logging.BoolField("created", created))
		}

		// never-worse: session-create failure must not break the handshake (degraded → raw fallback).
		sctx, cancel := context.WithTimeout(ctx, sessionOpTimeout)
		token, err := s.calm.CreateSession(sctx, client, s.ttlMinutes)
		cancel()
		if err != nil {
			s.log.WithContext(ctx).Warn("create session failed; continuing without CALM",
				logging.StringField("client", client), logging.ErrorField(err))
		} else {
			s.mu.Lock()
			s.session = token
			s.mu.Unlock()
			s.log.WithContext(ctx).Info("session created", logging.StringField("client", client))
		}
	}

	pv := p.ProtocolVersion
	if pv == "" {
		pv = defaultProtocolVersion
	}
	return initializeResult{
		ProtocolVersion: pv,
		Capabilities:    map[string]any{"tools": map[string]any{}},
		ServerInfo:      clientInfo{Name: s.name, Version: s.version},
	}, nil
}

type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func (s *Server) toolsList() any {
	out := make([]toolDescriptor, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		out = append(out, toolDescriptor{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return map[string]any{"tools": out}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p toolCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params"}
	}
	tool, ok := s.tools[p.Name]
	if !ok {
		// Unknown tool is a tool-level error (isError result), not a protocol error.
		return TextResult("unknown tool: "+p.Name, true), nil
	}
	return s.invokeTool(ctx, tool, p.Arguments), nil
}

// invokeTool runs a tool handler with panic isolation (R7 / never-worse): a
// translation-layer fault is downgraded to an isError result, never a crash.
// Also emits the per-call summary log per DESIGN.md §7. Handler errors are
// translated by type: *DegradedSignal triggers canonical degradation
// signaling (summary fields + phrasing prefix + optional [stderr] block);
// *ArgError surfaces as a tool-level "invalid arguments" result; any other
// error is treated as capture_failed degradation.
func (s *Server) invokeTool(ctx context.Context, tool Tool, args json.RawMessage) (res ToolResult) {
	start := time.Now()
	ctx, reqID := obs.WithCallContext(ctx)
	ctx = logging.Bind(
		ctx,
		logging.StringField("workload_request_id", reqID),
		logging.StringField("tool", tool.Name),
		logging.BoolField(obs.KeyCaptured, false),
		logging.BoolField(obs.KeyDegraded, false),
	)
	ctx = logging.BindSummary(ctx, obs.PresentationModeFieldSummary)
	defer func() {
		if r := recover(); r != nil {
			ctx = logging.Bind(
				ctx,
				logging.BoolField(obs.KeyDegraded, true),
				obs.DegradedReasonFieldCaptureFailed,
			)
			s.log.WithContext(ctx).Warn("tool handler panicked",
				logging.StringField("tool", tool.Name), logging.AnyField("panic", r))
			res = TextResult("tool failed", true)
		}
		visibleBytes := 0
		if len(res.Content) > 0 {
			visibleBytes = len(res.Content[0].Text)
		}
		s.log.SummaryWithContext(ctx).Info(
			"tool call completed",
			obs.ResponseVisibleBytes(visibleBytes),
			obs.CallDurationMs(time.Since(start).Milliseconds()),
		)
	}()
	out, err := tool.Handler(ctx, args)
	if err == nil {
		return out
	}
	var deg *DegradedSignal
	var arg *ArgError
	switch {
	case errors.As(err, &deg):
		return s.translateDegraded(ctx, out, deg)
	case errors.As(err, &arg):
		return TextResult(arg.Error(), true)
	default:
		ctx = logging.Bind(
			ctx,
			logging.BoolField(obs.KeyDegraded, true),
			obs.DegradedReasonFieldCaptureFailed,
		)
		s.log.WithContext(ctx).Warn("tool handler error",
			logging.StringField("tool", tool.Name), logging.ErrorField(err))
		return TextResult(err.Error(), true)
	}
}

// translateDegraded binds degraded summary fields, prepends the canonical
// phrasing to handler-supplied content, appends an optional [stderr] block,
// and preserves the handler's IsError flag (action tools: false; retrieval
// tools: true).
func (s *Server) translateDegraded(ctx context.Context, out ToolResult, deg *DegradedSignal) ToolResult {
	logging.BindSummary(
		ctx,
		logging.BoolField(obs.KeyDegraded, true),
		degradedReasonField(deg.Reason),
	)
	text := obs.DegradedPhrase(deg.Reason)
	if len(out.Content) > 0 && out.Content[0].Text != "" {
		text += "\n\n" + out.Content[0].Text
	}
	if deg.Detail != "" {
		text += "\n\n[stderr]\n" + deg.Detail
	}
	return ToolResult{Content: []Content{{Type: "text", Text: text}}, IsError: out.IsError}
}

func (s *Server) shutdown() {
	s.mu.Lock()
	token := s.session
	s.session = ""
	s.mu.Unlock()
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionOpTimeout)
	defer cancel()
	if err := s.calm.DeleteSession(ctx, token); err != nil {
		s.log.WithContext(ctx).Warn("delete session failed", logging.ErrorField(err))
		return
	}
	s.log.WithContext(ctx).Info("session deleted")
}

func (s *Server) writeResult(ctx context.Context, id json.RawMessage, result any) {
	s.write(ctx, response{JSONRPC: jsonRPCVersion, ID: id, Result: result})
}

func (s *Server) writeError(ctx context.Context, id json.RawMessage, code int, msg string) {
	s.write(ctx, response{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *Server) write(ctx context.Context, resp response) {
	b, err := json.Marshal(resp)
	if err != nil {
		s.log.WithContext(ctx).Error("marshal response", logging.ErrorField(err))
		return
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.out.Write(b); err != nil {
		s.log.WithContext(ctx).Error("write response", logging.ErrorField(err))
	}
}
