// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/obs"
)

// Variant is a closed set: a new variant is a code change, never a config
// string — match_layer/outcome-enum discipline (DL15).
type Variant string

const (
	VariantRankRound         Variant = "rank-round"
	VariantScoreProportional Variant = "score-proportional"
	VariantKnapsackGreedy    Variant = "knapsack-greedy"
	VariantEqualBudget       Variant = "equal-budget"
	VariantMMR               Variant = "mmr"
)

// Closed-enum log fields live here, not in obs: obs must not import search
// (import direction is search → obs).
var allocatorLogFields = map[Variant]logging.LoggingField{
	VariantRankRound:         logging.StringField(obs.KeyAllocator, string(VariantRankRound)),
	VariantScoreProportional: logging.StringField(obs.KeyAllocator, string(VariantScoreProportional)),
	VariantKnapsackGreedy:    logging.StringField(obs.KeyAllocator, string(VariantKnapsackGreedy)),
	VariantEqualBudget:       logging.StringField(obs.KeyAllocator, string(VariantEqualBudget)),
	VariantMMR:               logging.StringField(obs.KeyAllocator, string(VariantMMR)),
}

func (v Variant) LogField() logging.LoggingField {
	if f, ok := allocatorLogFields[v]; ok {
		return f
	}
	return allocatorLogFields[VariantRankRound]
}

// ParseVariant is the single membership gate: config validation and the
// override header both resolve through it.
func ParseVariant(s string) (Variant, bool) {
	switch Variant(s) {
	case VariantRankRound, VariantScoreProportional, VariantKnapsackGreedy, VariantEqualBudget, VariantMMR:
		return Variant(s), true
	default:
		return "", false
	}
}

// NewAllocator is the only place that branches on variant identity (DL15).
func NewAllocator(v Variant) Allocator {
	switch v {
	case VariantScoreProportional:
		return scoreProportional{}
	case VariantKnapsackGreedy:
		return knapsackGreedy{}
	case VariantEqualBudget:
		return equalBudget{}
	case VariantMMR:
		return mmr{}
	case VariantRankRound:
		return rankRound{}
	default:
		return rankRound{}
	}
}

type candidate struct {
	hit       db.SearchHit
	size      int
	relevance float64
}

// candidates is in submitted order; duplicate queries stay separate entries.
type allocInput struct {
	queries    []string
	candidates [][]candidate
	budget     int
}

// omitted[qi] counts only qi's own budget-dropped candidates; budgetExceeded
// means at least one otherwise-returnable candidate was dropped for budget.
type allocOutput struct {
	included       [][]candidate
	omitted        []int
	byteBudgetUsed int
	budgetExceeded bool
}

// Implementations are pure and deterministic, and the budget is STRICT:
// include only when used+size <= budget — no overshoot, ever (DL15).
type Allocator interface {
	Allocate(in allocInput) allocOutput
}

func newOutput(in allocInput) allocOutput {
	out := allocOutput{
		included: make([][]candidate, len(in.candidates)),
		omitted:  make([]int, len(in.candidates)),
	}
	for qi := range out.included {
		out.included[qi] = []candidate{}
	}
	return out
}
