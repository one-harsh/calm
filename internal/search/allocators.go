// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package search

import (
	"sort"
	"strings"
)

const mmrLambda = 0.7

// rankRound offers every query's rank-r before any rank-r+1. A non-fitting
// candidate is skipped, not a stop — a large rank-0 in one query must not
// starve a small rank-0 in another (DL15 multi-query coverage).
type rankRound struct{}

func (rankRound) Allocate(in allocInput) allocOutput {
	out := newOutput(in)
	maxRank := 0
	for _, cs := range in.candidates {
		if len(cs) > maxRank {
			maxRank = len(cs)
		}
	}
	used := 0
	for r := 0; r < maxRank; r++ {
		for qi, cs := range in.candidates {
			if r >= len(cs) {
				continue
			}
			c := cs[r]
			if used+c.size <= in.budget {
				out.included[qi] = append(out.included[qi], c)
				used += c.size
				continue
			}
			out.omitted[qi]++
			out.budgetExceeded = true
		}
	}
	out.byteBudgetUsed = used
	return out
}

// equalBudget deliberately does NOT redistribute an idle query's unused share —
// the simplest-baseline contract (DL15); score-proportional is the variant that
// spills idle share.
type equalBudget struct{}

func (equalBudget) Allocate(in allocInput) allocOutput {
	out := newOutput(in)
	q := len(in.candidates)
	if q == 0 {
		return out
	}
	share := in.budget / q
	remainder := in.budget % q
	used := 0
	for qi, cs := range in.candidates {
		capBytes := share
		if qi < remainder {
			capBytes++
		}
		qUsed := 0
		for _, c := range cs {
			if qUsed+c.size <= capBytes {
				out.included[qi] = append(out.included[qi], c)
				qUsed += c.size
				continue
			}
			out.omitted[qi]++
			out.budgetExceeded = true
		}
		used += qUsed
	}
	out.byteBudgetUsed = used
	return out
}

// scoreProportional apportions by rank-0 relevance (largest-remainder;
// all-zero → equal), then rank-rounds the leftover pool so idle share spills —
// redistribution is this variant's contract, unlike equal-budget (DL15).
type scoreProportional struct{}

func (scoreProportional) Allocate(in allocInput) allocOutput {
	out := newOutput(in)
	q := len(in.candidates)
	if q == 0 {
		return out
	}

	weights := make([]float64, q)
	total := 0.0
	for qi, cs := range in.candidates {
		if len(cs) > 0 {
			weights[qi] = cs[0].relevance
		}
		total += weights[qi]
	}
	if total <= 0 {
		for qi := range weights {
			weights[qi] = 1.0
		}
		total = float64(q)
	}

	shares := largestRemainder(weights, total, in.budget)

	used := 0
	next := make([]int, q)
	for qi, cs := range in.candidates {
		qUsed := 0
		for ci, c := range cs {
			if qUsed+c.size <= shares[qi] {
				out.included[qi] = append(out.included[qi], c)
				qUsed += c.size
				next[qi] = ci + 1
				continue
			}
			break
		}
		used += qUsed
	}

	leftover := in.budget - used
	maxRank := 0
	for qi, cs := range in.candidates {
		if len(cs)-next[qi] > maxRank {
			maxRank = len(cs) - next[qi]
		}
	}
	offered := make([]int, q)
	for r := 0; r < maxRank; r++ {
		for qi, cs := range in.candidates {
			idx := next[qi] + offered[qi]
			if idx >= len(cs) {
				continue
			}
			c := cs[idx]
			offered[qi]++
			if c.size <= leftover {
				out.included[qi] = append(out.included[qi], c)
				leftover -= c.size
				used += c.size
			} else {
				out.omitted[qi]++
				out.budgetExceeded = true
			}
		}
	}
	out.byteBudgetUsed = used
	return out
}

// Floors each fair share; leftover bytes go to the largest fractional parts,
// ties to the lower index (determinism).
func largestRemainder(weights []float64, total float64, budget int) []int {
	shares := make([]int, len(weights))
	type frac struct {
		idx  int
		part float64
	}
	fracs := make([]frac, len(weights))
	assigned := 0
	for i, w := range weights {
		exact := w / total * float64(budget)
		floor := int(exact)
		shares[i] = floor
		assigned += floor
		fracs[i] = frac{idx: i, part: exact - float64(floor)}
	}
	remaining := budget - assigned
	sort.SliceStable(fracs, func(a, b int) bool {
		if fracs[a].part != fracs[b].part {
			return fracs[a].part > fracs[b].part
		}
		return fracs[a].idx < fracs[b].idx
	})
	for i := 0; i < remaining && i < len(fracs); i++ {
		shares[fracs[i].idx]++
	}
	return shares
}

// knapsackGreedy is honestly a 0/1 DP knapsack maximizing total relevance; the
// name is the frozen wire string. Config validation caps the budget at 256KiB —
// the load-bearing bound on O(N·budget) (~131M ops at N=500). Strict > updates
// make earlier items win ties (deterministic reconstruction).
type knapsackGreedy struct{}

type knapItem struct {
	qi   int
	cand candidate
}

func (knapsackGreedy) Allocate(in allocInput) allocOutput {
	out := newOutput(in)

	items := []knapItem{}
	for qi, cs := range in.candidates {
		for _, c := range cs {
			items = append(items, knapItem{qi: qi, cand: c})
		}
	}
	n := len(items)
	budget := in.budget
	if n == 0 || budget <= 0 {
		return out
	}

	dp := make([]float64, budget+1)
	// bitset rows: n×(budget+1) bits ≈ 16MB transient at the 256KiB ceiling.
	words := (budget + 64) / 64
	take := make([][]uint64, n)
	for i := range take {
		take[i] = make([]uint64, words)
	}
	for i := 0; i < n; i++ {
		size := items[i].cand.size
		val := items[i].cand.relevance
		for w := budget; w >= size; w-- {
			if dp[w-size]+val > dp[w] {
				dp[w] = dp[w-size] + val
				take[i][w>>6] |= 1 << (uint(w) & 63)
			}
		}
	}

	chosen := make([]bool, n)
	w := budget
	for i := n - 1; i >= 0; i-- {
		if take[i][w>>6]&(1<<(uint(w)&63)) != 0 {
			chosen[i] = true
			w -= items[i].cand.size
		}
	}

	used := 0
	anyDropped := false
	for i, it := range items {
		if chosen[i] {
			out.included[it.qi] = append(out.included[it.qi], it.cand)
			used += it.cand.size
		} else {
			out.omitted[it.qi]++
			anyDropped = true
		}
	}
	out.byteBudgetUsed = used
	out.budgetExceeded = anyDropped
	return out
}

// mmr: greedy λ·relevance − (1−λ)·maxSim over Jaccard snippet-token sets.
// Per-query display order is selection order — the one intentional reorder
// (DL15); a near-duplicate cross-query hit is passed over even with budget
// to spare.
type mmr struct{}

type mmrItem struct {
	qi     int
	cand   candidate
	tokens map[string]struct{}
}

func (mmr) Allocate(in allocInput) allocOutput {
	out := newOutput(in)

	items := []mmrItem{}
	for qi, cs := range in.candidates {
		for _, c := range cs {
			items = append(items, mmrItem{qi: qi, cand: c, tokens: tokenSet(c.hit.Snippet)})
		}
	}
	n := len(items)
	if n == 0 {
		return out
	}

	selected := make([]bool, n)
	// maxSims is updated incrementally against only each newly included token
	// set — O(n²) total jaccard calls, not O(n²·|selected|).
	maxSims := make([]float64, n)
	used := 0
	for count := 0; count < n; count++ {
		best := -1
		var bestScore float64
		for i := range items {
			if selected[i] {
				continue
			}
			score := mmrLambda*items[i].cand.relevance - (1-mmrLambda)*maxSims[i]
			if best == -1 || score > bestScore {
				best = i
				bestScore = score
			}
		}
		if best == -1 {
			break
		}
		selected[best] = true
		it := items[best]
		if used+it.cand.size <= in.budget {
			out.included[it.qi] = append(out.included[it.qi], it.cand)
			used += it.cand.size
			for i := range items {
				if selected[i] {
					continue
				}
				if sim := jaccard(items[i].tokens, it.tokens); sim > maxSims[i] {
					maxSims[i] = sim
				}
			}
		} else {
			out.omitted[it.qi]++
			out.budgetExceeded = true
		}
	}
	out.byteBudgetUsed = used
	return out
}

func tokenSet(s string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, f := range strings.Fields(strings.ToLower(s)) {
		set[f] = struct{}{}
	}
	return set
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
