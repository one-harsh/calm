// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"errors"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

var errUntranslatable = errors.New("extract: untranslatable command")

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

// Plan's unexported event facts are shared with DeriveEvents so a command's
// labels and events come from one parse and cannot diverge.
type Plan struct {
	Mode          CaptureMode
	LatestSource  string // "" in coexist mode; base label without @<token>
	HistorySource string // "" in replace mode; base#seq without @<token>
	// Token is the per-call staleness suffix (6-char base32) fused with
	// Latest/HistorySource for LLM-facing emission per LABELING.md §2. Set by
	// the handler via extract.MintToken() after DerivePlan; unset by
	// DerivePlan itself so extract stays grammar-pure.
	Token       string
	ContentType string
	Format      calm.Format
	base        eventFacts
}

// WriteOutcome gates ApplyOutcomes cross-links to sources that actually persisted.
type WriteOutcome struct {
	Source    string
	Persisted bool
}

// EventDraft carries a derived event split by who computes each field
// (LABELING.md's event-derivation contract): Data holds the intent-derived
// fields, while latestLink/historyLink are the outcome-derived source cross-links
// a delivery resolves once it knows which sources persisted. Leaving the drafts
// free of resolved links is what lets one derivation serve both a client-side
// fan-out and a server-side compound ingest.
type EventDraft struct {
	Type        string
	Priority    int
	Data        map[string]any
	latestLink  string
	historyLink string
}

type eventFacts struct {
	toolName       string
	commandSummary string
	subcommand     string
	exitCode       int
	timedOut       bool
	isGit          bool
	invocationID   int64
	errMessage     string
	traceSnippet   string
	fileTouched    *fileTouchedFacts
}

type fileTouchedFacts struct {
	path          string // workspace-relative; agent-supplied form on escape fallback
	operation     FileOperation
	diff          string // sanitized unified diff; "" when the change is empty
	diffTruncated bool
}

// DerivePlan errors only on a blank (untranslatable) command — the handler's cue to
// fall back to raw output; every other input yields at least a coexist plan, and it
// never panics.
func DerivePlan(inv Invocation, r ExecResult) (Plan, error) {
	c, ok := parse(inv.Command)
	if !ok {
		// not adding the command to the error message because it can be a secret
		return Plan{}, errUntranslatable
	}

	facts := eventFacts{
		toolName:       toolName,
		commandSummary: commandSummary(c),
		subcommand:     c.subcommand,
		exitCode:       r.ExitCode,
		timedOut:       r.TimedOut,
		isGit:          c.program == "git",
		invocationID:   inv.Seq,
	}
	fillErrorFacts(&facts, r)

	cl, matched := classify(c, inv)
	if !matched {
		return coexistPlan(coexistID(c), inv, facts), nil
	}
	return assemble(cl, inv, facts), nil
}

func fillErrorFacts(f *eventFacts, r ExecResult) {
	if r.ExitCode != 0 || r.TimedOut {
		f.errMessage = errorMessage(r)
		f.traceSnippet = traceSnippet(r.Stderr)
	}
}

// assemble turns a classification into a Plan — the one place the capture
// mode's source-field shape is decided, shared by the shell and typed routes.
func assemble(cl classification, inv Invocation, facts eventFacts) Plan {
	plan := Plan{Mode: cl.mode, ContentType: cl.content, Format: cl.format, base: facts}
	switch cl.mode {
	case Replace:
		plan.LatestSource = buildBase(cl.id, inv, 0)
	case Dual:
		base := buildBase(cl.id, inv, maxSeqSuffix)
		plan.LatestSource = base
		plan.HistorySource = base + seqSuffix(inv.Seq)
	case Coexist:
		base := buildBase(cl.id, inv, maxSeqSuffix)
		plan.HistorySource = base + seqSuffix(inv.Seq)
	}
	return plan
}

func coexistPlan(id labelID, inv Invocation, facts eventFacts) Plan {
	base := buildBase(id, inv, maxSeqSuffix)
	return Plan{
		Mode:          Coexist,
		HistorySource: base + seqSuffix(inv.Seq),
		ContentType:   contentTypeProse,
		base:          facts,
	}
}

func DeriveEvents(p Plan) []EventDraft {
	f := p.base
	drafts := make([]EventDraft, 0, 4)

	drafts = append(drafts, EventDraft{
		Type:     EventToolInvocation,
		Priority: priorityToolInvocation,
		Data: map[string]any{
			keyToolName:     f.toolName,
			keyCommand:      f.commandSummary,
			keyExitCode:     f.exitCode,
			keyInvocationID: f.invocationID,
		},
		latestLink:  p.LatestSource,
		historyLink: p.HistorySource,
	})

	if f.exitCode != 0 || f.timedOut {
		ed := map[string]any{
			keyMessage:      f.errMessage,
			keySource:       f.toolName,
			keyExitCode:     f.exitCode,
			keyInvocationID: f.invocationID,
		}
		if f.traceSnippet != "" {
			ed[keyTraceSnippet] = f.traceSnippet
		}
		drafts = append(drafts, EventDraft{Type: EventErrorObserved, Priority: priorityErrorObserved, Data: ed})
	}

	if f.isGit {
		gd := map[string]any{
			keyCommand:      f.commandSummary,
			keyInvocationID: f.invocationID,
		}
		if f.subcommand != "" {
			gd[keySubcommand] = f.subcommand
		}
		drafts = append(drafts, EventDraft{Type: EventGitOperation, Priority: priorityGitOperation, Data: gd})
	}

	if f.fileTouched != nil {
		fd := map[string]any{
			keyPath:         f.fileTouched.path,
			keyOperation:    string(f.fileTouched.operation),
			keyInvocationID: f.invocationID,
		}
		if f.fileTouched.diff != "" {
			fd[keyDiff] = f.fileTouched.diff
		}
		if f.fileTouched.diffTruncated {
			fd[keyDiffTruncated] = true
		}
		drafts = append(drafts, EventDraft{
			Type:        EventFileTouched,
			Priority:    priorityFileTouched,
			Data:        fd,
			latestLink:  p.LatestSource,
			historyLink: p.HistorySource,
		})
	}

	return drafts
}

// ApplyOutcomes resolves each draft's outcome-derived cross-links against the
// per-source write outcomes (LABELING.md's event-derivation contract), building a
// fresh Data map per event so the drafts stay immutable — a cross-link is set
// only for a source that persisted, and reapplying the drafts under different
// outcomes never inherits a prior application's links.
func ApplyOutcomes(drafts []EventDraft, outcomes []WriteOutcome) []calm.EventInput {
	persisted := make(map[string]bool, len(outcomes))
	for _, o := range outcomes {
		if o.Persisted {
			persisted[o.Source] = true
		}
	}
	events := make([]calm.EventInput, 0, len(drafts))
	for _, d := range drafts {
		data := make(map[string]any, len(d.Data)+2)
		for k, val := range d.Data {
			data[k] = val
		}
		if d.latestLink != "" && persisted[d.latestLink] {
			data[keyLatestSource] = d.latestLink
		}
		if d.historyLink != "" && persisted[d.historyLink] {
			data[keyHistorySource] = d.historyLink
		}
		events = append(events, calm.EventInput{Type: d.Type, Priority: d.Priority, Data: data})
	}
	return events
}

func FinalizeEvents(p Plan, outcomes []WriteOutcome) []calm.EventInput {
	return ApplyOutcomes(DeriveEvents(p), outcomes)
}
