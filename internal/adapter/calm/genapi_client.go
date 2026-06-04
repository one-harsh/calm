// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm

import (
	"context"
	"fmt"
	"net/http"

	"github.com/one-harsh/calm/internal/api/genapi"
)

// Inlined (not imported from internal/auth) to keep this the adapter's only
// server-internal dependency — see boundary_test.go.
const headerAPIKey = "X-CALM-API-Key" //nolint:gosec // header name, not a credential

type genapiClient struct {
	api *genapi.ClientWithResponses
	// Stable per process: makes CreateSession idempotent, so a retried create (e.g. a
	// lost response after the server committed) returns the same session, not an orphan.
	idempotencyKey string
}

func NewGenapiClient(baseURL, apiKey, idempotencyKey string) (Client, error) {
	api, err := genapi.NewClientWithResponses(baseURL, genapi.WithRequestEditorFn(
		func(_ context.Context, req *http.Request) error {
			if apiKey != "" {
				req.Header.Set(headerAPIKey, apiKey)
			}
			return nil
		},
	))
	if err != nil {
		return nil, fmt.Errorf("init calm client: %w", err)
	}
	return &genapiClient{api: api, idempotencyKey: idempotencyKey}, nil
}

func (c *genapiClient) CreateSession(ctx context.Context, client string, ttlMinutes int) (string, error) {
	params := &genapi.CreateSessionParams{}
	if c.idempotencyKey != "" {
		params.IdempotencyKey = &c.idempotencyKey
	}
	body := genapi.CreateSessionJSONRequestBody{TtlMinutes: &ttlMinutes}
	if client != "" {
		body.Client = &client
	}
	resp, err := c.api.CreateSessionWithResponse(ctx, params, body)
	if err != nil {
		return "", err
	}
	if resp.JSON201 == nil {
		return "", fmt.Errorf("create session: %s", resp.Status())
	}
	return resp.JSON201.SessionToken, nil
}

func (c *genapiClient) DeleteSession(ctx context.Context, token string) error {
	resp, err := c.api.DeleteSessionWithResponse(ctx, &genapi.DeleteSessionParams{XCALMSessionToken: token})
	if err != nil {
		return err
	}
	if code := resp.StatusCode(); code < 200 || code >= 300 {
		return fmt.Errorf("delete session: %s", resp.Status())
	}
	return nil
}

func (c *genapiClient) Ingest(ctx context.Context, token string, in IngestInput) (IngestSummary, error) {
	body := genapi.IngestJSONRequestBody{Source: in.Source, Content: in.Content}
	if in.ContentType != "" {
		body.ContentType = &in.ContentType
	}
	if in.Format != "" {
		f := genapi.IngestRequestFormat(in.Format)
		body.Format = &f
	}
	resp, err := c.api.IngestWithResponse(ctx, &genapi.IngestParams{XCALMSessionToken: token}, body)
	if err != nil {
		return IngestSummary{}, err
	}
	if resp.JSON200 == nil {
		return IngestSummary{}, fmt.Errorf("ingest: %s", resp.Status())
	}
	r := resp.JSON200
	out := IngestSummary{
		Source:           r.Source,
		SectionsIndexed:  r.SectionsIndexed,
		SectionsTotal:    r.SectionsTotal,
		SummaryTruncated: r.SummaryTruncated,
		DistinctiveTerms: r.DistinctiveTerms,
	}
	for _, s := range r.Summary {
		preview := ""
		if s.Preview != nil {
			preview = *s.Preview
		}
		out.Sections = append(out.Sections, SectionPreview{Title: s.Title, Preview: preview})
	}
	return out, nil
}

func (c *genapiClient) Search(ctx context.Context, token string, in SearchInput) (SearchResults, error) {
	body := genapi.SearchJSONRequestBody{Queries: in.Queries}
	if in.Source != "" {
		body.Source = &in.Source
	}
	if in.Limit > 0 {
		body.Limit = &in.Limit
	}
	resp, err := c.api.SearchWithResponse(ctx, &genapi.SearchParams{XCALMSessionToken: token}, body)
	if err != nil {
		return SearchResults{}, err
	}
	if resp.JSON200 == nil {
		return SearchResults{}, fmt.Errorf("search: %s", resp.Status())
	}
	var out SearchResults
	for _, q := range resp.JSON200.Results {
		qr := QueryResult{Query: q.Query}
		for _, h := range q.Hits {
			qr.Hits = append(qr.Hits, Hit{
				Title:      h.Title,
				Snippet:    h.Snippet,
				Source:     h.Source,
				MatchLayer: string(h.MatchLayer),
			})
		}
		out.Queries = append(out.Queries, qr)
	}
	return out, nil
}

func (c *genapiClient) WriteEvents(ctx context.Context, token string, events []EventInput) error {
	in := make([]genapi.EventInput, 0, len(events))
	for _, e := range events {
		in = append(in, genapi.EventInput{Type: e.Type, Priority: e.Priority, Data: e.Data})
	}
	resp, err := c.api.WriteEventsWithResponse(ctx,
		&genapi.WriteEventsParams{XCALMSessionToken: token},
		genapi.WriteEventsJSONRequestBody{Events: in},
	)
	if err != nil {
		return err
	}
	if resp.JSON202 == nil {
		return fmt.Errorf("write events: %s", resp.Status())
	}
	return nil
}
