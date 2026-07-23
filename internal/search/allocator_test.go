// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	"reflect"
	"testing"

	"github.com/one-harsh/calm/internal/db"
)

func cand(size int, relevance float64, snippet, layer string) candidate {
	return candidate{
		hit:       db.SearchHit{Snippet: snippet, MatchLayer: layer},
		size:      size,
		relevance: relevance,
	}
}

func totalIncluded(out allocOutput) int {
	n := 0
	for _, cs := range out.included {
		n += len(cs)
	}
	return n
}

func sumIncludedSizes(out allocOutput) int {
	s := 0
	for _, cs := range out.included {
		for _, c := range cs {
			s += c.size
		}
	}
	return s
}

func TestAllocator_AllFit(t *testing.T) {
	for _, v := range []Variant{VariantRankRound, VariantScoreProportional, VariantKnapsackGreedy, VariantEqualBudget, VariantMMR} {
		in := allocInput{
			queries:    []string{"a", "b"},
			candidates: [][]candidate{{cand(10, 1, "x", "primary")}, {cand(10, 1, "y", "primary")}},
			budget:     100,
		}
		out := NewAllocator(v).Allocate(in)
		if totalIncluded(out) != 2 {
			t.Errorf("%s: included %d; want 2 (all fit)", v, totalIncluded(out))
		}
		if out.budgetExceeded {
			t.Errorf("%s: budgetExceeded true though all fit", v)
		}
	}
}

func TestAllocator_NothingFitsReturnsEmptyExceeded(t *testing.T) {
	for _, v := range []Variant{VariantRankRound, VariantScoreProportional, VariantKnapsackGreedy, VariantEqualBudget, VariantMMR} {
		in := allocInput{
			queries:    []string{"a", "b"},
			candidates: [][]candidate{{cand(50, 1, "x", "primary")}, {cand(50, 1, "y", "primary")}},
			budget:     10,
		}
		out := NewAllocator(v).Allocate(in)
		if totalIncluded(out) != 0 {
			t.Errorf("%s: included %d; STRICT contract wants 0", v, totalIncluded(out))
		}
		if !out.budgetExceeded {
			t.Errorf("%s: budgetExceeded false though nothing fit", v)
		}
		if out.byteBudgetUsed != 0 {
			t.Errorf("%s: byteBudgetUsed %d; want 0", v, out.byteBudgetUsed)
		}
	}
}

func TestAllocator_ByteAccountingSums(t *testing.T) {
	for _, v := range []Variant{VariantRankRound, VariantScoreProportional, VariantKnapsackGreedy, VariantEqualBudget, VariantMMR} {
		in := allocInput{
			queries:    []string{"a", "b"},
			candidates: [][]candidate{{cand(10, 1, "x", "primary"), cand(15, 0.5, "y", "primary")}, {cand(20, 1, "z", "primary")}},
			budget:     40,
		}
		out := NewAllocator(v).Allocate(in)
		if out.byteBudgetUsed != sumIncludedSizes(out) {
			t.Errorf("%s: byteBudgetUsed %d != sum of included sizes %d", v, out.byteBudgetUsed, sumIncludedSizes(out))
		}
		if out.byteBudgetUsed > in.budget {
			t.Errorf("%s: byteBudgetUsed %d overshot budget %d", v, out.byteBudgetUsed, in.budget)
		}
	}
}

func TestAllocator_DeterministicTieBreak(t *testing.T) {
	for _, v := range []Variant{VariantRankRound, VariantScoreProportional, VariantKnapsackGreedy, VariantEqualBudget, VariantMMR} {
		in := allocInput{
			queries: []string{"a", "b", "c"},
			candidates: [][]candidate{
				{cand(10, 1, "x", "primary")},
				{cand(10, 1, "y", "primary")},
				{cand(10, 1, "z", "primary")},
			},
			budget: 25,
		}
		first := NewAllocator(v).Allocate(in)
		second := NewAllocator(v).Allocate(in)
		if !reflect.DeepEqual(first, second) {
			t.Errorf("%s: non-deterministic output %+v vs %+v", v, first, second)
		}
	}
}

func TestAllocator_DuplicateQueriesSeparateEntries(t *testing.T) {
	in := allocInput{
		queries:    []string{"dup", "dup"},
		candidates: [][]candidate{{cand(10, 1, "x", "primary")}, {cand(10, 1, "x", "primary")}},
		budget:     100,
	}
	out := NewAllocator(VariantRankRound).Allocate(in)
	if len(out.included) != 2 {
		t.Fatalf("included groups = %d; want 2 separate positional entries", len(out.included))
	}
	if len(out.included[0]) != 1 || len(out.included[1]) != 1 {
		t.Errorf("duplicate queries collapsed: %+v", out.included)
	}
}

func TestAllocator_EmptyQueryNoCandidates(t *testing.T) {
	in := allocInput{
		queries:    []string{"a", "b"},
		candidates: [][]candidate{{}, {cand(10, 1, "y", "primary")}},
		budget:     100,
	}
	out := NewAllocator(VariantRankRound).Allocate(in)
	if len(out.included[0]) != 0 {
		t.Errorf("empty query produced hits: %+v", out.included[0])
	}
	if len(out.included[1]) != 1 {
		t.Errorf("non-empty query lost its hit: %+v", out.included[1])
	}
}

func TestRankRound_OnePerQueryBeforeSeconds(t *testing.T) {
	in := allocInput{
		queries: []string{"a", "b"},
		candidates: [][]candidate{
			{cand(10, 1, "a0", "primary"), cand(10, 1, "a1", "primary")},
			{cand(10, 1, "b0", "primary")},
		},
		budget: 20,
	}
	out := rankRound{}.Allocate(in)
	if len(out.included[0]) != 1 || out.included[0][0].hit.Snippet != "a0" {
		t.Errorf("query a included = %+v; want [a0]", out.included[0])
	}
	if len(out.included[1]) != 1 || out.included[1][0].hit.Snippet != "b0" {
		t.Errorf("query b included = %+v; want [b0]", out.included[1])
	}
	if out.omitted[0] != 1 {
		t.Errorf("query a omitted = %d; want 1 (a1 dropped)", out.omitted[0])
	}
}

func TestRankRound_LargeHitDoesNotStarve(t *testing.T) {
	in := allocInput{
		queries: []string{"big", "small"},
		candidates: [][]candidate{
			{cand(100, 1, "big0", "primary")},
			{cand(10, 1, "small0", "primary")},
		},
		budget: 30,
	}
	out := rankRound{}.Allocate(in)
	if len(out.included[0]) != 0 {
		t.Errorf("big query included %+v; want none", out.included[0])
	}
	if len(out.included[1]) != 1 {
		t.Errorf("small query starved: %+v", out.included[1])
	}
	if !out.budgetExceeded {
		t.Errorf("budgetExceeded false though big0 was dropped")
	}
}

func TestRankRound_RoundInterleaving(t *testing.T) {
	in := allocInput{
		queries: []string{"a", "b"},
		candidates: [][]candidate{
			{cand(10, 1, "a0", "primary"), cand(10, 1, "a1", "primary")},
			{cand(10, 1, "b0", "primary"), cand(10, 1, "b1", "primary")},
		},
		budget: 30,
	}
	out := rankRound{}.Allocate(in)
	if len(out.included[0]) != 2 {
		t.Errorf("query a included = %+v; want a0,a1", out.included[0])
	}
	if len(out.included[1]) != 1 || out.included[1][0].hit.Snippet != "b0" {
		t.Errorf("query b included = %+v; want b0 only", out.included[1])
	}
}

func TestEqualBudget_PerQueryCapEnforcedNoRedistribution(t *testing.T) {
	in := allocInput{
		queries: []string{"a", "b"},
		candidates: [][]candidate{
			{cand(10, 1, "a0", "primary"), cand(10, 1, "a1", "primary")},
			{},
		},
		budget: 40,
	}
	out := equalBudget{}.Allocate(in)
	if len(out.included[0]) != 2 {
		t.Errorf("query a included = %+v; want both within its 20-byte cap", out.included[0])
	}
	in.candidates[0] = append(in.candidates[0], cand(10, 1, "a2", "primary"))
	out = equalBudget{}.Allocate(in)
	if len(out.included[0]) != 2 {
		t.Errorf("no-redistribution violated: query a included %d; want 2 (capped)", len(out.included[0]))
	}
	if out.omitted[0] != 1 {
		t.Errorf("query a omitted = %d; want 1 (a2 over cap)", out.omitted[0])
	}
}

func TestEqualBudget_ShareZeroReturnsEmpty(t *testing.T) {
	in := allocInput{
		queries: []string{"a", "b", "c"},
		candidates: [][]candidate{
			{cand(1, 1, "a", "primary")}, {cand(1, 1, "b", "primary")}, {cand(1, 1, "c", "primary")},
		},
		budget: 2,
	}
	out := equalBudget{}.Allocate(in)
	if len(out.included[0]) != 1 || len(out.included[1]) != 1 {
		t.Errorf("remainder-byte queries starved: %+v", out.included)
	}
	if len(out.included[2]) != 0 {
		t.Errorf("share-zero query got a hit: %+v", out.included[2])
	}
}

func TestScoreProportional_HighScoreQueryGetsLargerShare(t *testing.T) {
	in := allocInput{
		queries: []string{"hi", "lo"},
		candidates: [][]candidate{
			{cand(30, 1.0, "hi0", "primary"), cand(30, 1.0, "hi1", "primary")},
			{cand(30, 0.0, "lo0", "primary")},
		},
		budget: 60,
	}
	out := scoreProportional{}.Allocate(in)
	if len(out.included[0]) != 2 {
		t.Errorf("high-score query included = %+v; want both", out.included[0])
	}
	if len(out.included[1]) != 0 {
		t.Errorf("low-score query got budget it wasn't apportioned: %+v", out.included[1])
	}
}

func TestScoreProportional_UnusedShareRedistributes(t *testing.T) {
	in := allocInput{
		queries: []string{"hi", "lo"},
		candidates: [][]candidate{
			{cand(10, 1.0, "hi0", "primary")},
			{cand(10, 0.0, "lo0", "primary")},
		},
		budget: 100,
	}
	out := scoreProportional{}.Allocate(in)
	if len(out.included[0]) != 1 || len(out.included[1]) != 1 {
		t.Errorf("idle share not redistributed: %+v", out.included)
	}
	if out.budgetExceeded {
		t.Errorf("budgetExceeded true though both fit after redistribution")
	}
}

func TestKnapsack_BeatsGreedy(t *testing.T) {
	in := allocInput{
		queries: []string{"a"},
		candidates: [][]candidate{{
			cand(6, 7, "ratio", "primary"),
			cand(5, 5, "x", "primary"),
			cand(5, 5, "y", "primary"),
		}},
		budget: 10,
	}
	out := knapsackGreedy{}.Allocate(in)
	if totalIncluded(out) != 2 {
		t.Fatalf("DP did not find the optimal pair; included %+v", out.included)
	}
	var sum float64
	for _, c := range out.included[0] {
		sum += c.relevance
	}
	if sum != 10 {
		t.Errorf("chosen relevance sum = %v; want 10 (optimal)", sum)
	}
}

func TestKnapsack_DeterministicReconstruction(t *testing.T) {
	in := allocInput{
		queries:    []string{"a"},
		candidates: [][]candidate{{cand(5, 5, "x", "primary"), cand(5, 5, "y", "primary")}},
		budget:     5,
	}
	out := knapsackGreedy{}.Allocate(in)
	if len(out.included[0]) != 1 || out.included[0][0].hit.Snippet != "x" {
		t.Errorf("reconstruction did not favor the earlier item on tie: %+v", out.included[0])
	}
}

func TestMMR_DedupNearDuplicate(t *testing.T) {
	in := allocInput{
		queries: []string{"a", "b"},
		candidates: [][]candidate{
			{cand(10, 1.0, "the quick brown fox", "primary")},
			{cand(10, 0.95, "the quick brown fox", "primary")},
		},
		budget: 100,
	}
	out := mmr{}.Allocate(in)
	if totalIncluded(out) != 2 {
		t.Errorf("with spare budget MMR should still include both; got %+v", out.included)
	}
}

func TestMMR_LambdaFavorsRelevance(t *testing.T) {
	in := allocInput{
		queries: []string{"a", "b"},
		candidates: [][]candidate{
			{cand(10, 0.2, "alpha", "primary")},
			{cand(10, 0.9, "beta", "primary")},
		},
		budget: 10,
	}
	out := mmr{}.Allocate(in)
	if len(out.included[1]) != 1 {
		t.Errorf("MMR did not select the higher-relevance distinct hit first: %+v", out.included)
	}
	if len(out.included[0]) != 0 {
		t.Errorf("lower-relevance hit was included over the higher one: %+v", out.included[0])
	}
}

func TestKnapsack_AllFitIncludesFloorRelevance(t *testing.T) {
	in := allocInput{
		queries: []string{"a", "b"},
		candidates: [][]candidate{
			{cand(10, 1.0, "top", "primary"), cand(10, 0.6, "floor-primary", "primary")},
			{cand(10, 0.1, "floor-trigram", "trigram")},
		},
		budget: 100,
	}
	out := knapsackGreedy{}.Allocate(in)
	if totalIncluded(out) != 3 {
		t.Fatalf("included %d of 3 under ample budget; floor-relevance item dropped: %+v", totalIncluded(out), out.included)
	}
	if out.budgetExceeded {
		t.Errorf("budgetExceeded = true though everything fit")
	}
}

func TestScoreProportional_DifferentiatesQueries(t *testing.T) {
	tenEach := func(rel float64, tag string) []candidate {
		cs := make([]candidate, 10)
		for i := range cs {
			cs[i] = cand(10, rel, tag, "primary")
		}
		return cs
	}

	in := allocInput{
		queries:    []string{"strong", "weak"},
		candidates: [][]candidate{tenEach(1.0, "s"), tenEach(0.62, "w")},
		budget:     100,
	}
	out := scoreProportional{}.Allocate(in)
	if len(out.included[0]) <= len(out.included[1]) {
		t.Errorf("strong query included %d, weak %d; want strictly larger share for the stronger rank-0 relevance",
			len(out.included[0]), len(out.included[1]))
	}

	equal := allocInput{
		queries:    []string{"a", "b"},
		candidates: [][]candidate{tenEach(1.0, "a"), tenEach(1.0, "b")},
		budget:     100,
	}
	outEq := scoreProportional{}.Allocate(equal)
	if len(outEq.included[0]) != len(outEq.included[1]) {
		t.Errorf("equal rank-0 relevances split %d/%d; want equal shares",
			len(outEq.included[0]), len(outEq.included[1]))
	}
}
