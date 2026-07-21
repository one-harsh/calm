// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const snippetBudgetRunes = 400

const contentTypeCode = "code"

type snippetResult struct {
	text     string
	fallback bool
}

// extractSnippet returns an exact excerpt of content bounded by snippetBudgetRunes
// and aligned to boundaries appropriate to contentType.
//
//   - Chunks that already fit the budget are returned verbatim (fallback=false),
//     short-circuiting before locus detection so the fallback metric counts only
//     the population where content exceeded budget and no term was located.
//   - When at least one query term is located, the budget-sized window covering
//     the densest cluster of term sites wins, snapped inward.
//   - When no term is located, a snapped leading window is returned with
//     fallback=true (the instrumented stemmed-miss signal).
func extractSnippet(content, contentType, query string) snippetResult {
	runes := []rune(content)
	if len(runes) <= snippetBudgetRunes {
		return snippetResult{text: content, fallback: false}
	}

	sites := locateSites(content, query)
	if len(sites) == 0 {
		return snippetResult{text: snapWindow(runes, 0, snippetBudgetRunes, contentType, nil), fallback: true}
	}

	anchors := evidenceSites(sites)
	from, to := densestWindow(anchors, len(runes))
	return snippetResult{text: snapWindow(runes, from, to, contentType, sitesIn(anchors, from, to)), fallback: false}
}

// evidenceSites returns the non-noise sites when any exist — stopword sites
// must not out-score evidence terms in window selection — and all sites
// otherwise, so a query of only stopwords still centers on a located
// occurrence instead of degrading to the leading window.
func evidenceSites(sites []site) []site {
	var out []site
	for _, s := range sites {
		if !s.noise {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return sites
	}
	return out
}

type queryTerm struct {
	text  string
	noise bool
}

// splitTerms tokenizes query on whitespace, trims leading/trailing punctuation
// from each token, and dedups case-insensitively preserving first-seen order.
// Punctuation is trimmed because no index tokenizer treats it as part of a
// token — a term carrying a trailing "?" could only under-match the content
// that ranked the hit; the trimmed core is a substring of the raw token, so
// trimming never loses a match. Stopword (trigramStopwords) and single-rune
// terms are kept but marked noise: still locatable — code chunks legitimately
// match "for", and locating them keeps fallback meaning "nothing located" —
// but never allowed to drive window selection over evidence terms.
func splitTerms(query string) []queryTerm {
	seen := make(map[string]bool)
	var out []queryTerm
	for _, raw := range strings.Fields(query) {
		core := strings.TrimFunc(raw, isTermPunct)
		if core == "" {
			continue
		}
		lower := strings.ToLower(core)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, queryTerm{
			text:  core,
			noise: trigramStopwords[lower] || utf8.RuneCountInString(core) == 1,
		})
	}
	return out
}

func isTermPunct(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
}

type site struct {
	start   int // rune index, inclusive
	end     int // rune index, exclusive
	termIdx int
	noise   bool
}

func locateSites(content, query string) []site {
	terms := splitTerms(query)
	if len(terms) == 0 {
		return nil
	}

	// Fold per rune (unicode.ToLower), not strings.ToLower over the whole
	// string: whole-string folding can change a rune's byte width (Kelvin sign
	// 3→1, İ 2→1) while leaving rune count unchanged, which drifts any
	// byte-derived offset off the term it located. Folding and searching in
	// rune space (indexRunes) keeps every site an exact rune index.
	folded := foldRunes(content)

	var sites []site
	for ti, term := range terms {
		needle := foldRunes(term.text)
		if len(needle) == 0 {
			continue
		}
		for cursor := 0; ; {
			at := indexRunes(folded, needle, cursor)
			if at < 0 {
				break
			}
			sites = append(sites, site{start: at, end: at + len(needle), termIdx: ti, noise: term.noise})
			cursor = at + len(needle)
		}
	}
	sortSitesByStart(sites)
	return sites
}

func foldRunes(s string) []rune {
	rs := []rune(s)
	for i, r := range rs {
		rs[i] = unicode.ToLower(r)
	}
	return rs
}

func indexRunes(haystack, needle []rune, from int) int {
	for i := from; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func sortSitesByStart(sites []site) {
	// Insertion sort: site slices are small (bounded by term occurrences in one
	// chunk) and this keeps ordering deterministic with no map/allocation churn.
	for i := 1; i < len(sites); i++ {
		cur := sites[i]
		j := i - 1
		for j >= 0 && (sites[j].start > cur.start || (sites[j].start == cur.start && sites[j].termIdx > cur.termIdx)) {
			sites[j+1] = sites[j]
			j--
		}
		sites[j+1] = cur
	}
}

// densestWindow returns the budget-sized rune window [from,to) covering the
// most distinct terms, breaking ties by most total occurrences then earliest
// start. Probes are left-anchored at each site start: any maximum-coverage
// window can be slid right until its left edge meets a site start without
// losing containment, so these probes are complete — probes centered on a
// site can miss a window that legally covers two distant terms. The winning
// coverage is then re-centered, splitting the leftover budget evenly around
// the covered span, which cannot drop a covered site because the span fits by
// construction.
func densestWindow(sites []site, n int) (from, to int) {
	budget := min(snippetBudgetRunes, n)

	// Candidates ascend with the start-sorted sites, so keeping the first
	// strictly-better score resolves the earliest-start tie-break.
	bestFrom := 0
	bestDistinct := -1
	bestTotal := -1
	for i := range sites {
		cand := clamp(sites[i].start, 0, n-budget)
		distinct, total := countInWindow(sites, cand, cand+budget)
		if distinct > bestDistinct || (distinct == bestDistinct && total > bestTotal) {
			bestDistinct, bestTotal, bestFrom = distinct, total, cand
		}
	}

	spanLo, spanHi := anchorBounds(sitesIn(sites, bestFrom, bestFrom+budget), bestFrom, bestFrom+budget)
	if spanLo < 0 {
		return bestFrom, bestFrom + budget
	}
	slack := budget - (spanHi - spanLo)
	from = clamp(spanLo-slack/2, 0, n-budget)
	return from, from + budget
}

// countInWindow returns the distinct-term count and total-occurrence count for
// sites fully contained in [from,to). Sites are start-sorted, so the scan can
// stop once a site starts at/after to.
func countInWindow(sites []site, from, to int) (distinct, total int) {
	seen := make(map[int]bool)
	for i := range sites {
		s := sites[i]
		if s.start >= to {
			break
		}
		if s.start < from || s.end > to {
			continue
		}
		total++
		if !seen[s.termIdx] {
			seen[s.termIdx] = true
			distinct++
		}
	}
	return distinct, total
}

// sitesIn returns the sites fully contained in [from,to), used by snapping to
// guarantee it never trims away the winning window's anchor occurrences.
func sitesIn(sites []site, from, to int) []site {
	var out []site
	for i := range sites {
		s := sites[i]
		if s.start >= to {
			break
		}
		if s.start >= from && s.end <= to {
			out = append(out, s)
		}
	}
	return out
}

// snapWindow trims [from,to) inward to natural boundaries appropriate to
// contentType and returns the exact rune substring. It never drops a contained
// anchor site (if a snap would, the hard window edge is kept on that side) and
// never returns empty. The result is a contiguous substring of the input, so
// strings.Contains(content, result) holds and the length stays ≤ budget.
func snapWindow(runes []rune, from, to int, contentType string, anchors []site) string {
	if to > len(runes) {
		to = len(runes)
	}
	if from < 0 {
		from = 0
	}
	if from >= to {
		return string(runes[from:to])
	}

	protectFrom, protectTo := anchorBounds(anchors, from, to)

	var newFrom, newTo int
	if contentType == contentTypeCode {
		newFrom, newTo = snapLines(runes, from, to)
	} else {
		newFrom, newTo = snapProse(runes, from, to)
	}

	// Never trim past a contained anchor site: keep the hard edge on that side.
	if protectFrom >= 0 && newFrom > protectFrom {
		newFrom = from
	}
	if protectTo >= 0 && newTo < protectTo {
		newTo = to
	}
	if newFrom >= newTo {
		newFrom, newTo = from, to
	}
	return string(runes[newFrom:newTo])
}

func anchorBounds(anchors []site, from, to int) (lo, hi int) {
	lo, hi = -1, -1
	for _, a := range anchors {
		if a.start < from || a.end > to {
			continue
		}
		if lo == -1 || a.start < lo {
			lo = a.start
		}
		if hi == -1 || a.end > hi {
			hi = a.end
		}
	}
	return lo, hi
}

// snapLines trims [from,to) inward to whole lines: from advances to the start
// of the next line unless it already sits at a line start (dropping a partial
// leading line); to retreats to just after the last newline in range (dropping
// a partial trailing line). Inward-only, so the result stays within budget.
func snapLines(runes []rune, from, to int) (int, int) {
	newFrom := from
	if newFrom > 0 && runes[newFrom-1] != '\n' {
		for newFrom < to && runes[newFrom-1] != '\n' {
			newFrom++
		}
	}

	newTo := to
	for newTo > newFrom && runes[newTo-1] != '\n' {
		newTo--
	}

	if newFrom >= newTo {
		return from, to
	}
	return newFrom, newTo
}

// snapProse trims [from,to) inward to sentence boundaries: from advances to just
// after the first terminator at-or-after it (a clean sentence start), to
// retreats to just after the last terminator at-or-before it. With no
// terminator on a side it falls to a word boundary, then to the hard cut.
// Inward-only, so the result stays within budget.
func snapProse(runes []rune, from, to int) (int, int) {
	newFrom := from
	if from > 0 && !isTerminator(runes[from-1]) {
		i := from
		for i < to && !isTerminator(runes[i]) {
			i++
		}
		if i < to {
			for i < to && (isTerminator(runes[i]) || runes[i] == ' ') {
				i++
			}
			newFrom = i
		} else {
			newFrom = wordStartForward(runes, from, to)
		}
	}

	newTo := to
	if to < len(runes) {
		i := to
		for i > newFrom && !isTerminator(runes[i-1]) {
			i--
		}
		if i > newFrom {
			newTo = i
		} else {
			newTo = wordEndBackward(runes, newFrom, to)
		}
	}

	if newFrom >= newTo {
		return from, to
	}
	return newFrom, newTo
}

// wordStartForward advances from to the next word boundary within [from,to),
// keeping the hard from if none is found. Inward-only.
func wordStartForward(runes []rune, from, to int) int {
	i := from
	for i < to && runes[i] != ' ' {
		i++
	}
	for i < to && runes[i] == ' ' {
		i++
	}
	if i >= to {
		return from
	}
	return i
}

// wordEndBackward retreats to back to the previous word boundary within
// (from,to], keeping the hard to if none is found. Inward-only.
func wordEndBackward(runes []rune, from, to int) int {
	i := to
	for i > from && runes[i-1] != ' ' {
		i--
	}
	for i > from && runes[i-1] == ' ' {
		i--
	}
	if i <= from {
		return to
	}
	return i
}

func isTerminator(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '\n'
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
