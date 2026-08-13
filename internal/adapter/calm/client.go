// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package calm is the adapter's port onto the CALM HTTP API. The Client
// interface and its DTOs are domain-typed and free of generated-client types;
// only genapi_client.go imports internal/api/genapi. This is the decoupling seam
// that keeps the rest of the adapter independent of the server's non-adapter
// internals, so extracting the adapter into its own module stays a lift.
package calm

import "context"

type Client interface {
	RegisterClient(ctx context.Context, name string) (created bool, err error)
	CreateSession(ctx context.Context, client string, ttlMinutes int, idempotencyKey string) (sessionToken string, err error)
	DeleteSession(ctx context.Context, sessionToken string) error
	Ingest(ctx context.Context, sessionToken string, in IngestInput) (IngestSummary, error)
	Search(ctx context.Context, sessionToken string, in SearchInput) (SearchResults, error)
	WriteEvents(ctx context.Context, sessionToken string, events []EventInput) error
	Feedback(ctx context.Context, sessionToken, correlationID, outcome string) error
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
	CorrelationID    string
}

type SectionPreview struct {
	Title   string
	Preview string
}

type SearchInput struct {
	Queries     []string
	Source      string
	Limit       int
	Offset      int
	BudgetBytes int
}

const DocumentOrderBudgetDefault = 32768

type SearchResults struct {
	Queries       []QueryResult
	NextOffset    *int
	CorrelationID string
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
	// Truncated marks the first chunk of a document-order page whose full
	// text alone exceeded the byte budget: Snippet is an exact-text prefix.
	Truncated bool
}

type EventInput struct {
	Type     string
	Priority int
	Data     map[string]any
}
