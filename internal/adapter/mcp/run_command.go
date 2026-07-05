// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const toolNameRunCommand = "calm_run_command"

const (
	execTimeout   = 60 * time.Second
	ingestTimeout = 10 * time.Second
	eventTimeout  = 5 * time.Second
)

const runCommandDescription = "Run a shell command locally, capturing its output into CALM. Prefer this " +
	"over the native shell/Bash tool for every shell command: output is indexed for on-demand retrieval " +
	"and large output stays out of the context window. Small outputs come back verbatim (still captured " +
	"and searchable via calm_search). Larger outputs come back as a compact summary plus a source label " +
	"ending in @<token>; fetch the full captured output later with calm_search " +
	"source=<label exactly as returned> rather than re-running. The label refers to the latest output " +
	"for that identity; for one specific past run, drop the @<token> suffix and use <base>#<n>. " +
	"Never append #<n> after the @<token>."

const runCommandSchema = `{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "Shell command to run, executed via sh -c on the local machine."},
    "cwd": {"type": "string", "description": "Working directory; defaults to the adapter's workspace root."}
  },
  "required": ["command"],
  "additionalProperties": false
}`

type runCommandArgs struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

func (s *Server) newRunCommandTool() Tool {
	// TODO: tool description should affirmatively state "Tool runs from the
	// workspace root by default; do not `cd` in your command. Use the `cwd`
	// parameter to run from a different directory." The cwd default is buried
	// in the parameter doc; the main description is silent on it. Observed
	// agents prefix commands with `cd <root> &&` despite the default, leaking
	// host paths into captured output and wasting tokens.
	return Tool{
		Name:        toolNameRunCommand,
		Description: runCommandDescription,
		InputSchema: json.RawMessage(runCommandSchema),
		Handler:     s.runCommand,
	}
}

func (s *Server) runCommand(ctx context.Context, args json.RawMessage) (res ToolResult, err error) {
	var a runCommandArgs
	if uerr := json.Unmarshal(args, &a); uerr != nil {
		return ToolResult{}, &ArgError{Detail: uerr.Error()}
	}
	if strings.TrimSpace(a.Command) == "" {
		return ToolResult{}, &ArgError{Detail: "command is required"}
	}

	dir := a.Cwd
	if dir == "" {
		dir = s.workspaceRoot
	}

	ectx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	r, runErr := exec.Run(ectx, a.Command, dir)
	if runErr != nil {
		return TextResult("failed to run command: "+runErr.Error(), true), nil
	}

	raw := commandPayload(r)
	return s.capturePipeline(ctx, captureSpec{
		ingest:  raw,
		visible: raw,
		res:     r,
		plan: func() (extract.Plan, error) {
			inv := extract.Invocation{Seq: s.seq.Add(1), Command: a.Command, Cwd: dir, WorkspaceRoot: s.workspaceRoot}
			return extract.DerivePlan(inv, extract.ExecResult{
				Stdout:   r.Stdout,
				Stderr:   r.Stderr,
				ExitCode: r.ExitCode,
				TimedOut: r.TimedOut,
			})
		},
	})
}

// dualWriteIngest runs the preservation-first dual-write per LABELING.md
// (history first, then latest), recording per-source persisted outcomes and
// returning the preferred summary (latest wins when both succeed). The error
// is non-nil only for session-level failures; the first one short-circuits
// the remaining write — it would fail identically against the same dead token.
func (s *Server) dualWriteIngest(ctx context.Context, token string, plan extract.Plan, raw string) ([]extract.WriteOutcome, *calm.IngestSummary, error) {
	var outcomes []extract.WriteOutcome
	var rep *calm.IngestSummary
	if plan.HistorySource != "" {
		sum, e := s.ingest(ctx, token, plan.HistorySource, raw, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.HistorySource, Persisted: e == nil})
		switch {
		case e == nil:
			rep = &sum
		case isSessionLevel(e):
			return outcomes, nil, e
		default:
			s.log.WithContext(ctx).Warn("history ingest failed",
				logging.StringField("source", plan.HistorySource), logging.ErrorField(e))
		}
	}
	if plan.LatestSource != "" {
		sum, e := s.ingest(ctx, token, plan.LatestSource, raw, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.LatestSource, Persisted: e == nil})
		switch {
		case e == nil:
			rep = &sum
		case isSessionLevel(e):
			return outcomes, nil, e
		default:
			s.log.WithContext(ctx).Warn("latest ingest failed",
				logging.StringField("source", plan.LatestSource), logging.ErrorField(e))
		}
	}
	return outcomes, rep, nil
}

func isSessionLevel(err error) bool {
	return errors.Is(err, calm.ErrSessionNotFound) || errors.Is(err, calm.ErrAuthRejected)
}

// formatCaptureOutcome classifies the dual-write outcome into one of three
// states (capture_failed / capture_partial / happy). It binds captured/source
// fields onto the summary for the partial+happy paths, returns the
// visible-text content, and signals degradation via DegradedSignal for the
// partial+failed paths — invokeTool layers the canonical phrasing prefix +
// degraded summary fields on top. `token` is the per-call staleness suffix
// fused into the recall-hint label.
func (s *Server) formatCaptureOutcome(ctx context.Context, outcomes []extract.WriteOutcome, rep *calm.IngestSummary, raw string, r exec.Result, token string) (ToolResult, error) {
	anyFailed := false
	for _, o := range outcomes {
		if !o.Persisted {
			anyFailed = true
			break
		}
	}
	switch {
	case rep == nil:
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		s.log.WithContext(ctx).Warn("all ingests failed; returning raw output",
			obs.DegradedReasonFieldCaptureFailed)
		return TextResult(raw, false), &DegradedSignal{Reason: obs.DegradedReasonCaptureFailed}
	case anyFailed:
		logging.BindSummary(
			ctx,
			logging.BoolField(obs.KeyCaptured, true),
			obs.SourceLabel(rep.Source),
		)
		return TextResult(presentCapture(ctx, *rep, raw, r, token), false), &DegradedSignal{Reason: obs.DegradedReasonCapturePartial}
	default:
		logging.BindSummary(
			ctx,
			logging.BoolField(obs.KeyCaptured, true),
			obs.SourceLabel(rep.Source),
		)
		return TextResult(presentCapture(ctx, *rep, raw, r, token), false), nil
	}
}

// presentCapture picks the presentation mode by raw size: at or below inlineMaxBytes
// the raw payload wins (summary chrome would cost more context than the content);
// above it, the compact rep + fused recall label. The source label stays on the
// summary log in both modes.
func presentCapture(ctx context.Context, sum calm.IngestSummary, raw string, r exec.Result, token string) string {
	if len(raw) <= inlineMaxBytes {
		logging.BindSummary(ctx, obs.PresentationModeFieldInline)
		return formatInline(raw, r)
	}
	return formatCompact(sum, r, token)
}

// recordPersistedTokens registers `plan.Token` against each source that
// actually persisted, so later `calm_search source=<fused>` calls validate.
// A source that failed to persist isn't recorded — its fused label would
// resolve to nothing on the CALM side anyway, so admitting the token would
// only surface a misleading empty result instead of the honest
// session_lost signal (or plain failure).
func (s *Server) recordPersistedTokens(plan extract.Plan, outcomes []extract.WriteOutcome) {
	for _, o := range outcomes {
		if !o.Persisted {
			continue
		}
		switch o.Source {
		case plan.LatestSource, plan.HistorySource:
			s.registry.Record(o.Source, plan.Token)
		}
	}
}

func (s *Server) emitEvents(ctx context.Context, token string, ev []calm.EventInput) {
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

func (s *Server) ingest(ctx context.Context, token, source, content string, plan extract.Plan) (calm.IngestSummary, error) {
	ictx, cancel := context.WithTimeout(ctx, ingestTimeout)
	defer cancel()
	return s.calm.Ingest(ictx, token, calm.IngestInput{
		Source:      source,
		Content:     content,
		ContentType: plan.ContentType,
		Format:      plan.Format,
	})
}

// commandPayload merges captured stdout and stderr into a single visible /
// indexed payload. If a process wrote stderr (more than whitespace), it's
// part of the local result — no command-specific allowlist, no dropping
// diagnostics. Stream markers distinguish the sources when both are present
// so the LLM and source-scoped search can tell them apart.
func commandPayload(r exec.Result) string {
	hasStdout := r.Stdout != ""
	hasStderr := strings.TrimSpace(r.Stderr) != ""
	switch {
	case hasStdout && hasStderr:
		sep := "\n"
		if !strings.HasSuffix(r.Stdout, "\n") {
			sep = "\n\n"
		}
		return "[stdout]\n" + r.Stdout + sep + "[stderr]\n" + r.Stderr
	case hasStderr:
		return "[stderr]\n" + r.Stderr
	default:
		return r.Stdout
	}
}

const (
	maxCompactSections = 5
	maxCompactLen      = 4096
	inlineMaxBytes     = 512
)

// formatInline is inline mode: the raw payload verbatim with minimal framing — no
// source label, no recall hint. The trailer appears only when it carries signal
// (nonzero exit, timeout, truncation) or when raw is empty (so the response is
// never blank text).
func formatInline(raw string, r exec.Result) string {
	if r.ExitCode == 0 && !r.TimedOut && !r.Truncated && raw != "" {
		return raw
	}
	var b strings.Builder
	b.WriteString(raw)
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "exit=%d", r.ExitCode)
	if r.TimedOut {
		b.WriteString(" (timed out)")
	}
	if r.Truncated {
		b.WriteString(" (output truncated)")
	}
	return b.String()
}

func formatCompact(sum calm.IngestSummary, r exec.Result, token string) string {
	var b strings.Builder
	fusedSource := sum.Source
	if token != "" {
		fusedSource = sum.Source + "@" + token
	}
	fmt.Fprintf(&b, "Captured %d/%d sections under %q.\n", sum.SectionsIndexed, sum.SectionsTotal, fusedSource)
	fmt.Fprintf(&b, "Retrieve full output: calm_search source=%s\n", fusedSource)

	shown := sum.Sections
	if len(shown) > maxCompactSections {
		shown = shown[:maxCompactSections]
	}
	for _, sec := range shown {
		if sec.Preview != "" {
			fmt.Fprintf(&b, "- %s: %s\n", sec.Title, sec.Preview)
		} else {
			fmt.Fprintf(&b, "- %s\n", sec.Title)
		}
	}
	if more := len(sum.Sections) - len(shown); more > 0 {
		fmt.Fprintf(&b, "… +%d more sections\n", more)
	}
	if len(sum.DistinctiveTerms) > 0 {
		fmt.Fprintf(&b, "Terms: %s\n", strings.Join(sum.DistinctiveTerms, ", "))
	}

	fmt.Fprintf(&b, "exit=%d", r.ExitCode)
	if r.TimedOut {
		b.WriteString(" (timed out)")
	}
	if r.Truncated {
		b.WriteString(" (output truncated)")
	}

	out := b.String()
	if len(out) > maxCompactLen {
		out = strings.ToValidUTF8(out[:maxCompactLen], "") + "…"
	}
	return out
}
