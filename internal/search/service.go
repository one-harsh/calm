// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	logging "github.com/one-harsh/context-logging"
	"golang.org/x/sync/errgroup"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

const (
	requestTypeSearch = "search"

	matchLayerPrimary = "primary"
	matchLayerTrigram = "trigram"

	// Three disjoint bands (gaps deliberate) mirror the DAL's ordering tier in
	// allocator value space: an additive bonus on unbounded raw scores cannot
	// guarantee AND > OR > trigram; bands do. Positive floors keep every value
	// > 0 so a fitting item always improves the knapsack DP's strict-> update.
	// Cross-query relevance is usable, not truly commensurable (DL15).
	matchesAllRelevanceFloor = 0.85
	primaryRelevanceCeil     = 0.8
	primaryRelevanceFloor    = 0.6
	trigramRelevanceCeil     = 0.5
	trigramRelevanceFloor    = 0.1
)

type Service struct {
	store  db.DAL
	logger *logging.Logger
}

func New(store db.DAL, logger *logging.Logger) *Service {
	return &Service{store: store, logger: logger}
}

type Result struct {
	Queries        []QueryResult
	ByteBudgetUsed int
	BudgetExceeded bool
	BudgetBytes    int
	Allocator      Variant
}

type QueryResult struct {
	Query         string
	Hits          []db.SearchHit
	BudgetOmitted int
}

// budgetBytes is the committed budget (default/clamp applied at the handler),
// echoed verbatim on the Result. The correlation capture is best-effort.
func (s *Service) Search(
	ctx context.Context,
	namespace string,
	sessionID int64,
	correlationID uuid.UUID,
	in db.SearchInput,
	queries []string,
	budgetBytes int,
	variant Variant,
) (Result, error) {
	perQuery, err := s.fanOut(ctx, namespace, in, queries)
	if err != nil {
		return Result{}, err
	}

	normalizeRelevance(perQuery)

	cands := make([][]candidate, len(queries))
	for qi, hits := range perQuery {
		cs := make([]candidate, 0, len(hits))
		for _, h := range hits {
			size, err := wireSize(h)
			if err != nil {
				return Result{}, err
			}
			cs = append(cs, candidate{hit: h, size: size, relevance: h.Relevance})
		}
		cands[qi] = cs
	}

	alloc := NewAllocator(variant).Allocate(allocInput{queries: queries, candidates: cands, budget: budgetBytes})

	result := Result{
		Queries:        make([]QueryResult, len(queries)),
		ByteBudgetUsed: alloc.byteBudgetUsed,
		BudgetExceeded: alloc.budgetExceeded,
		BudgetBytes:    budgetBytes,
		Allocator:      variant,
	}
	totalOmitted := 0
	for qi, q := range queries {
		hits := make([]db.SearchHit, len(alloc.included[qi]))
		for i, c := range alloc.included[qi] {
			hits[i] = c.hit
		}
		result.Queries[qi] = QueryResult{Query: q, Hits: hits, BudgetOmitted: alloc.omitted[qi]}
		totalOmitted += alloc.omitted[qi]
	}

	// Breakdown counts included (delivered) hits, not the pre-budget candidate set.
	primary, trigram, total, snippetFallbacks := hitBreakdown(result.Queries)
	if s.logger.Enabled(logging.DebugLevel) {
		s.logger.WithContext(ctx).Debug(
			"search executed",
			obs.SearchQueries(len(queries)),
			obs.SearchHitsTotal(total),
			obs.SearchHitsPrimary(primary),
			obs.SearchHitsTrigram(trigram),
			obs.SearchSnippetFallbacks(snippetFallbacks),
			obs.SearchByteBudgetUsed(result.ByteBudgetUsed),
			obs.SearchBudgetExhausted(result.BudgetExceeded),
			obs.SearchResultsOmitted(totalOmitted),
			variant.LogField(),
		)
	}
	s.captureCorrelation(ctx, namespace, sessionID, correlationID, result, primary, trigram, total, snippetFallbacks, totalOmitted)
	return result, nil
}

// fanOut lands each query's hits in a position-indexed slice (submitted order,
// duplicates separate); first error cancels the rest. Read path only — search
// never runs inside a transaction and *sql.DB is pool-backed concurrency-safe.
func (s *Service) fanOut(ctx context.Context, namespace string, in db.SearchInput, queries []string) ([][]db.SearchHit, error) {
	out := make([][]db.SearchHit, len(queries))
	g, gctx := errgroup.WithContext(ctx)
	for qi, q := range queries {
		g.Go(func() error {
			qin := db.SearchInput{SessionID: in.SessionID, Query: q, Source: in.Source, Limit: in.Limit}
			hits, err := s.store.Sources().Search(gctx, namespace, qin)
			if err != nil {
				return err
			}
			out[qi] = hits
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// Min-max per group across the WHOLE response (all-equal → band hi): per-query
// scaling would pin every query's top to the same value and degenerate
// score-weighted allocators into equal shares (DL15).
func normalizeRelevance(perQuery [][]db.SearchHit) {
	inGroup := func(layer string, matchesAll bool) func(db.SearchHit) bool {
		return func(h db.SearchHit) bool { return h.MatchLayer == layer && h.MatchesAll == matchesAll }
	}
	normalizeGroup(perQuery, inGroup(matchLayerPrimary, true), matchesAllRelevanceFloor, 1.0)
	normalizeGroup(perQuery, inGroup(matchLayerPrimary, false), primaryRelevanceFloor, primaryRelevanceCeil)
	normalizeGroup(perQuery, inGroup(matchLayerTrigram, false), trigramRelevanceFloor, trigramRelevanceCeil)
}

func normalizeGroup(perQuery [][]db.SearchHit, in func(db.SearchHit) bool, lo, hi float64) {
	minV, maxV := 0.0, 0.0
	first := true
	for _, hits := range perQuery {
		for _, h := range hits {
			if !in(h) {
				continue
			}
			if first || h.Relevance < minV {
				minV = h.Relevance
			}
			if first || h.Relevance > maxV {
				maxV = h.Relevance
			}
			first = false
		}
	}
	if first {
		return
	}
	span := maxV - minV
	for _, hits := range perQuery {
		for i := range hits {
			if !in(hits[i]) {
				continue
			}
			if span == 0 {
				hits[i].Relevance = hi
				continue
			}
			hits[i].Relevance = lo + (hits[i].Relevance-minV)/span*(hi-lo)
		}
	}
}

func hitBreakdown(queries []QueryResult) (primary, trigram, total, snippetFallbacks int) {
	for _, r := range queries {
		for _, h := range r.Hits {
			total++
			if h.SnippetFallback {
				snippetFallbacks++
			}
			switch h.MatchLayer {
			case matchLayerPrimary:
				primary++
			case matchLayerTrigram:
				trigram++
			}
		}
	}
	return primary, trigram, total, snippetFallbacks
}

func (s *Service) captureCorrelation(
	ctx context.Context,
	namespace string,
	sessionID int64,
	correlationID uuid.UUID,
	result Result,
	primary, trigram, total, snippetFallbacks, resultsOmitted int,
) {
	meta, err := json.Marshal(map[string]any{
		"hits_primary":      primary,
		"hits_trigram":      trigram,
		"hit_count":         total,
		"snippet_fallbacks": snippetFallbacks,
		"allocator":         string(result.Allocator),
		"budget_exceeded":   result.BudgetExceeded,
		"byte_budget_used":  result.ByteBudgetUsed,
		"results_omitted":   resultsOmitted,
	})
	if err != nil {
		s.logger.WithContext(ctx).Warn("correlation marshal failed",
			obs.RequestType(requestTypeSearch), logging.ErrorField(err))
		return
	}
	if err := s.store.Correlations().Insert(ctx, namespace, sessionID, correlationID[:], requestTypeSearch, meta); err != nil {
		s.logger.WithContext(ctx).Warn("correlation insert failed",
			obs.RequestType(requestTypeSearch), logging.ErrorField(err))
	}
}

// wireSize is the byte-budget unit: compact JSON of the standalone wire hit,
// mirrored locally (no genapi import). Envelope/separators aren't counted, so
// the HTTP body runs slightly larger than byte_budget_used.
func wireSize(h db.SearchHit) (int, error) {
	b, err := json.Marshal(sizedHit{
		Title:      h.Title,
		Snippet:    h.Snippet,
		Source:     h.Source,
		MatchLayer: h.MatchLayer,
	})
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

type sizedHit struct {
	Title      string `json:"title"`
	Snippet    string `json:"snippet"`
	Source     string `json:"source"`
	MatchLayer string `json:"match_layer"`
}
