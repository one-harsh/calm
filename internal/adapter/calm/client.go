// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package calm is the adapter's port onto the CALM HTTP API. The Client
// interface and its DTOs are domain-typed and free of generated-client types;
// only genapi_client.go imports internal/api/genapi. This is the decoupling seam
// that keeps the rest of the adapter independent of the server's non-adapter
// internals, so extracting the adapter into its own module stays a lift.
package calm

import "context"

// Client is the adapter's port onto CALM. Methods are stateless — the session
// token is supplied per call by the caller (the MCP server owns session state).
// Non-2xx responses surface as *StatusError; match status classes via
// errors.Is against ErrSessionNotFound / ErrAuthRejected.
//
// CreateSession's idempotencyKey is per call: a retried initialize create may
// reuse its key to get the same session back, while a recovery create must
// present a fresh key so CALM's dedup window can't replay the dead session.
// Empty key sends no Idempotency-Key header.
//
// TODO: add Feedback method (POST /v1/feedback) — required by
// calm_report_outcome tool.
type Client interface {
	RegisterClient(ctx context.Context, name string) (created bool, err error)
	CreateSession(ctx context.Context, client string, ttlMinutes int, idempotencyKey string) (sessionToken string, err error)
	DeleteSession(ctx context.Context, sessionToken string) error
	Ingest(ctx context.Context, sessionToken string, in IngestInput) (IngestSummary, error)
	Search(ctx context.Context, sessionToken string, in SearchInput) (SearchResults, error)
	WriteEvents(ctx context.Context, sessionToken string, events []EventInput) error
}

type Format string

const (
	FormatLog        Format = "log"
	FormatStacktrace Format = "stacktrace"
	FormatCSV        Format = "csv"
	FormatTSV        Format = "tsv"
	FormatMetrics    Format = "metrics"
	FormatJSON       Format = "json"
	FormatMarkdown   Format = "markdown"
	FormatText       Format = "text"
)

type IngestInput struct {
	Source      string
	Content     string
	ContentType string
	Format      Format
}

type IngestSummary struct {
	Source           string
	SectionsIndexed  int
	SectionsTotal    int
	SummaryTruncated bool
	Sections         []SectionPreview
	DistinctiveTerms []string
}

type SectionPreview struct {
	Title   string
	Preview string
}

type SearchInput struct {
	Queries []string
	Source  string
	Limit   int
}

type SearchResults struct {
	Queries []QueryResult
}

type QueryResult struct {
	Query string
	Hits  []Hit
}

type Hit struct {
	Title      string
	Snippet    string
	Source     string
	MatchLayer string
}

type EventInput struct {
	Type     string
	Priority int
	Data     map[string]any
}
