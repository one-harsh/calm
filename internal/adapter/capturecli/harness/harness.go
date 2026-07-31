// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package harness is the mode-shaped seam between a harness's wire format and the
// capture flow: DESIGN.md §12's two modes (observation, wrap) plus session-start,
// as neutral events in and neutral responses out. Per-harness parse/render lives
// in this package (claude.go); the flow consumes only these types.
package harness

type EventKind int

const (
	KindPassThrough EventKind = iota
	KindRewrite
	KindObserve
	KindSessionStart
)

// Event is a parsed payload; Kind selects the populated member.
type Event struct {
	Kind         EventKind
	Rewrite      RewriteEvent
	Observe      ObserveEvent
	SessionStart SessionStartEvent
}

type RewriteEvent struct {
	SessionID string
	Command   string
}

type ObserveEvent struct {
	SessionID   string
	Command     string
	Cwd         string
	Stdout      string
	Stderr      string
	ExitCode    int
	Interrupted bool
	IsImage     bool
	Truncated   bool
	// CanReplace is false for an event whose replacement the harness ignores
	// (Claude's failure event): the flow captures but emits no replacement.
	CanReplace bool
}

type Disposition int

const (
	DispositionNone Disposition = iota
	DispositionFreshCard
	DispositionRefresherCard
)

type SessionStartEvent struct {
	SessionID   string
	Disposition Disposition
}

type ObserveResponse struct {
	Stdout      string
	Interrupted bool
}

type RewriteResponse struct {
	Command     string
	Description string
}
