// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"fmt"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

type CaptureMode int

const (
	Replace CaptureMode = iota
	// Dual keeps the latest source AND a per-invocation history source, so a re-run
	// cannot clobber a semantically distinct earlier output.
	Dual
	Coexist
)

func (m CaptureMode) String() string {
	switch m {
	case Replace:
		return "replace"
	case Dual:
		return "dual"
	case Coexist:
		return "coexist"
	default:
		return "unknown"
	}
}

type Invocation struct {
	// Seq is a session-local sequence, not the event ordinal — a failed or deduped
	// event write would make that link wrong.
	Seq     int64
	Command string
	Cwd     string

	// WorkspaceRoot is explicit because Cwd alone can't tell a subdir from another
	// repo or a ".." escape.
	WorkspaceRoot string
	WorkspaceID   string
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// Plan's unexported event facts are shared with FinalizeEvents so a command's
// labels and events come from one parse and cannot diverge.
type Plan struct {
	Mode          CaptureMode
	LatestSource  string // "" in coexist mode
	HistorySource string // "" in replace mode
	ContentType   string
	Format        calm.Format
	base          eventFacts
}

// WriteOutcome gates FinalizeEvents cross-links to sources that actually persisted.
type WriteOutcome struct {
	Source    string
	Persisted bool
}

type eventFacts struct {
	commandSummary string
	subcommand     string
	exitCode       int
	timedOut       bool
	isGit          bool
	invocationID   int64
	errMessage     string
	traceSnippet   string
}

// DerivePlan errors only on a blank (untranslatable) command — the handler's cue to
// fall back to raw output; every other input yields at least a coexist plan, and it
// never panics.
func DerivePlan(inv Invocation, r ExecResult) (Plan, error) {
	c, ok := parse(inv.Command)
	if !ok {
		return Plan{}, fmt.Errorf("extract: untranslatable command %q", inv.Command)
	}

	facts := eventFacts{
		commandSummary: commandSummary(c),
		subcommand:     c.subcommand,
		exitCode:       r.ExitCode,
		timedOut:       r.TimedOut,
		isGit:          c.program == "git",
		invocationID:   inv.Seq,
	}
	if r.ExitCode != 0 || r.TimedOut {
		facts.errMessage = errorMessage(r)
		facts.traceSnippet = traceSnippet(r.Stderr)
	}

	suffix := seqSuffix(inv.Seq)

	cl, matched := classify(c, inv)
	if !matched {
		base := buildBase(coexistID(c), inv, maxSeqSuffix)
		return Plan{
			Mode:          Coexist,
			HistorySource: base + suffix,
			ContentType:   contentTypeProse,
			base:          facts,
		}, nil
	}

	plan := Plan{Mode: cl.mode, ContentType: cl.content, Format: cl.format, base: facts}
	switch cl.mode {
	case Replace:
		plan.LatestSource = buildBase(cl.id, inv, 0)
	case Dual:
		base := buildBase(cl.id, inv, maxSeqSuffix)
		plan.LatestSource = base
		plan.HistorySource = base + suffix
	case Coexist:
		base := buildBase(cl.id, inv, maxSeqSuffix)
		plan.HistorySource = base + suffix
	}
	return plan, nil
}

// Cross-links are set only for outcomes that persisted, so an event never points at a
// source that was never written.
func FinalizeEvents(p Plan, outcomes []WriteOutcome) []calm.EventInput {
	persisted := make(map[string]bool, len(outcomes))
	for _, o := range outcomes {
		if o.Persisted {
			persisted[o.Source] = true
		}
	}
	f := p.base
	events := make([]calm.EventInput, 0, 3)

	inv := map[string]any{
		keyToolName:     toolName,
		keyCommand:      f.commandSummary,
		keyExitCode:     f.exitCode,
		keyInvocationID: f.invocationID,
	}
	if p.LatestSource != "" && persisted[p.LatestSource] {
		inv[keyLatestSource] = p.LatestSource
	}
	if p.HistorySource != "" && persisted[p.HistorySource] {
		inv[keyHistorySource] = p.HistorySource
	}
	events = append(events, calm.EventInput{
		Type:     EventToolInvocation,
		Priority: priorityToolInvocation,
		Data:     inv,
	})

	if f.exitCode != 0 || f.timedOut {
		ed := map[string]any{
			keyMessage:      f.errMessage,
			keySource:       toolName,
			keyExitCode:     f.exitCode,
			keyInvocationID: f.invocationID,
		}
		if f.traceSnippet != "" {
			ed[keyTraceSnippet] = f.traceSnippet
		}
		events = append(events, calm.EventInput{
			Type:     EventErrorObserved,
			Priority: priorityErrorObserved,
			Data:     ed,
		})
	}

	if f.isGit {
		gd := map[string]any{
			keyCommand:      f.commandSummary,
			keyInvocationID: f.invocationID,
		}
		if f.subcommand != "" {
			gd[keySubcommand] = f.subcommand
		}
		events = append(events, calm.EventInput{
			Type:     EventGitOperation,
			Priority: priorityGitOperation,
			Data:     gd,
		})
	}

	return events
}
