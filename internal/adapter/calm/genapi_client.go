// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm

import (
	"context"
	"fmt"
	"net/http"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/api/genapi"
)

type genapiClient struct {
	api *genapi.ClientWithResponses
}

func NewGenapiClient(baseURL, apiKey string, log *logging.Logger) (Client, error) {
	if log == nil {
		log = logging.Nop()
	}
	transport := chain(
		http.DefaultTransport,
		withWorkloadRequestID(),
		withTracePropagation(),
		withAuth(apiKey),
		withLogging(log),
	)
	api, err := genapi.NewClientWithResponses(
		baseURL,
		genapi.WithHTTPClient(&http.Client{Transport: transport}),
	)
	if err != nil {
		return nil, fmt.Errorf("init calm client: %w", err)
	}
	return &genapiClient{api: api}, nil
}

func (c *genapiClient) RegisterClient(ctx context.Context, name string) (bool, error) {
	resp, err := c.api.RegisterClientWithResponse(ctx, genapi.ClientName(name))
	if err != nil {
		return false, err
	}
	switch {
	case resp.JSON201 != nil:
		return true, nil
	case resp.JSON409 != nil:
		// Already registered — success.
		return false, nil
	default:
		return false, statusErr("register client", resp.StatusCode(), resp.Status())
	}
}

func (c *genapiClient) CreateSession(ctx context.Context, client string, ttlMinutes int, idempotencyKey string) (string, error) {
	params := &genapi.CreateSessionParams{}
	if idempotencyKey != "" {
		params.IdempotencyKey = &idempotencyKey
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
		return "", statusErr("create session", resp.StatusCode(), resp.Status())
	}
	return resp.JSON201.SessionToken, nil
}

func (c *genapiClient) DeleteSession(ctx context.Context, token string) error {
	resp, err := c.api.DeleteSessionWithResponse(ctx, &genapi.DeleteSessionParams{XCALMSessionToken: token})
	if err != nil {
		return err
	}
	if code := resp.StatusCode(); code < 200 || code >= 300 {
		return statusErr("delete session", code, resp.Status())
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
		return IngestSummary{}, statusErr("ingest", resp.StatusCode(), resp.Status())
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
	var body genapi.SearchJSONRequestBody
	if len(in.Queries) > 0 {
		body.Queries = in.Queries
	} else if in.Offset > 0 {
		// Document-order mode: forward offset only, never queries. Offset 0 is
		// the server default, so it rides as an omitted field.
		body.Offset = &in.Offset
	}
	if in.Source != "" {
		body.Source = &in.Source
	}
	if in.Limit > 0 {
		body.Limit = &in.Limit
	}
	if in.BudgetBytes > 0 {
		body.BudgetBytes = &in.BudgetBytes
	}
	resp, err := c.api.SearchWithResponse(ctx, &genapi.SearchParams{XCALMSessionToken: token}, body)
	if err != nil {
		return SearchResults{}, err
	}
	if resp.JSON200 == nil {
		return SearchResults{}, statusErr("search", resp.StatusCode(), resp.Status())
	}
	out := SearchResults{NextOffset: resp.JSON200.NextOffset}
	for _, q := range resp.JSON200.Results {
		qr := QueryResult{Query: q.Query}
		for _, h := range q.Hits {
			qr.Hits = append(qr.Hits, Hit{
				Title:      h.Title,
				Snippet:    h.Snippet,
				Source:     h.Source,
				MatchLayer: string(h.MatchLayer),
				Truncated:  h.Truncated != nil && *h.Truncated,
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
	resp, err := c.api.WriteEventsWithResponse(
		ctx,
		&genapi.WriteEventsParams{XCALMSessionToken: token},
		genapi.WriteEventsJSONRequestBody{Events: in},
	)
	if err != nil {
		return err
	}
	if resp.JSON202 == nil {
		return statusErr("write events", resp.StatusCode(), resp.Status())
	}
	return nil
}
