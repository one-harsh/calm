// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const toolNameRunCommand = "calm_run_command"

const (
	execTimeout   = 60 * time.Second
	ingestTimeout = 10 * time.Second
	eventTimeout  = 5 * time.Second
)

const runCommandDescription = "Run a shell command locally, capturing its output into CALM instead of " +
	"returning it raw. Prefer this over the native shell/Bash tool for every shell command: it keeps " +
	"large/raw output out of the context window and indexes it for on-demand retrieval. Returns a compact " +
	"summary plus a source label; fetch the full captured output later with calm_search source=<label> " +
	"rather than re-running. The base label is the latest output for that identity; <label>#<n> is one " +
	"specific past run."

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
		return TextResult("invalid arguments: "+uerr.Error(), true), nil
	}
	if strings.TrimSpace(a.Command) == "" {
		return TextResult("command is required", true), nil
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

	raw := r.Stdout
	res = TextResult(raw, false) // default; the recover keeps whatever res holds at panic time
	defer func() {
		if p := recover(); p != nil {
			s.log.WithContext(ctx).Warn("run_command panicked; returning best-available output",
				logging.AnyField("panic", p))
			err = nil
		}
	}()

	token := s.sessionToken()
	if token == "" {
		s.log.WithContext(ctx).Warn("CALM unavailable; returning raw output")
		return
	}

	inv := extract.Invocation{Seq: s.seq.Add(1), Command: a.Command, Cwd: dir, WorkspaceRoot: s.workspaceRoot}
	plan, derr := extract.DerivePlan(inv, extract.ExecResult{
		Stdout:   r.Stdout,
		Stderr:   r.Stderr,
		ExitCode: r.ExitCode,
		TimedOut: r.TimedOut,
	})
	if derr != nil {
		s.log.WithContext(ctx).Warn("derive plan failed; returning raw output", logging.ErrorField(derr))
		return
	}

	// Preservation-first: history before latest, so a partial failure still leaves the
	// output recoverable. latest's summary is the preferred rep when it persists.
	var outcomes []extract.WriteOutcome
	var rep *calm.IngestSummary
	if plan.HistorySource != "" {
		sum, e := s.ingest(ctx, token, plan.HistorySource, r.Stdout, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.HistorySource, Persisted: e == nil})
		if e == nil {
			rep = &sum
		} else {
			s.log.WithContext(ctx).Warn("history ingest failed",
				logging.StringField("source", plan.HistorySource), logging.ErrorField(e))
		}
	}
	if plan.LatestSource != "" {
		sum, e := s.ingest(ctx, token, plan.LatestSource, r.Stdout, plan)
		outcomes = append(outcomes, extract.WriteOutcome{Source: plan.LatestSource, Persisted: e == nil})
		if e == nil {
			rep = &sum
		} else {
			s.log.WithContext(ctx).Warn("latest ingest failed",
				logging.StringField("source", plan.LatestSource), logging.ErrorField(e))
		}
	}

	// Finalize the response before emitting events
	if rep != nil {
		res = TextResult(formatCompact(*rep, r), false)
	} else {
		s.log.WithContext(ctx).Warn("all ingests failed; returning raw output")
	}

	// Fire-and-forget: events are pure observability and must never delay the response
	// (never-worse) — a stalled /v1/events can't hold the tool call hostage.
	if ev := extract.FinalizeEvents(plan, outcomes); len(ev) > 0 {
		s.emitEvents(ctx, token, ev)
	}

	return
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

const (
	maxCompactSections = 5
	maxCompactLen      = 4096
)

func formatCompact(sum calm.IngestSummary, r exec.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Captured %d/%d sections under %q.\n", sum.SectionsIndexed, sum.SectionsTotal, sum.Source)
	fmt.Fprintf(&b, "Retrieve full output: calm_search source=%s\n", sum.Source)

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
