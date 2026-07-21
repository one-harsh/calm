// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// assertSnippetInvariants pins the two properties every extractSnippet return
// must satisfy on every path: the snippet is an exact substring of the source
// content (content-fidelity) and never exceeds the rune budget.
func assertSnippetInvariants(t *testing.T, content string, got snippetResult) {
	t.Helper()
	if !strings.Contains(content, got.text) {
		t.Errorf("snippet %q is not a substring of content", got.text)
	}
	if n := utf8.RuneCountInString(got.text); n > snippetBudgetRunes {
		t.Errorf("snippet rune-len = %d; want <= %d", n, snippetBudgetRunes)
	}
	if got.text == "" && content != "" {
		t.Errorf("snippet is empty for non-empty content")
	}
}

// filler produces prose padding of the requested rune length made of whole
// sentences, so snapping has terminators to work with away from the anchor.
func filler(runes int) string {
	unit := "padding words here. "
	var b strings.Builder
	for utf8.RuneCountInString(b.String()) < runes {
		b.WriteString(unit)
	}
	return string([]rune(b.String())[:runes])
}

func TestExtractSnippet_ShortChunkVerbatim(t *testing.T) {
	content := "the build failed with a fatal linker error"
	got := extractSnippet(content, "prose", "linker")
	if got.fallback {
		t.Errorf("fallback = true; want false for a short chunk")
	}
	if got.text != content {
		t.Errorf("text = %q; want the whole content verbatim", got.text)
	}
	assertSnippetInvariants(t, content, got)
}

func TestExtractSnippet_MultiSiteDensityPick(t *testing.T) {
	// First match is isolated; a later region packs the term densely. The
	// density winner must center on the dense region, not the first match.
	head := "alpha appears once. " + filler(300)
	dense := "alpha and alpha and alpha and alpha together. "
	tail := filler(300)
	content := head + dense + tail

	got := extractSnippet(content, "prose", "alpha")
	assertSnippetInvariants(t, content, got)
	if got.fallback {
		t.Fatalf("fallback = true; want a located window")
	}
	if strings.Count(got.text, "alpha") < 4 {
		t.Errorf("snippet %q; want the dense alpha cluster, got %d occurrences",
			got.text, strings.Count(got.text, "alpha"))
	}
}

func TestExtractSnippet_DensityTieOnOccurrences(t *testing.T) {
	// Two regions each cover the same single distinct term; the region with
	// more occurrences wins the tie.
	sparse := "beta here. " + filler(200)
	dense := "beta beta beta beta beta close. "
	content := filler(50) + sparse + dense + filler(300)

	got := extractSnippet(content, "prose", "beta")
	assertSnippetInvariants(t, content, got)
	if strings.Count(got.text, "beta") < 5 {
		t.Errorf("snippet %q; want the higher-occurrence region", got.text)
	}
}

func TestExtractSnippet_DensityTieOnEarliestStart(t *testing.T) {
	// Two identical single-occurrence regions equally dense; earliest start wins.
	content := "gamma early. " + filler(400) + "gamma late. " + filler(400)
	got := extractSnippet(content, "prose", "gamma")
	assertSnippetInvariants(t, content, got)
	if !strings.Contains(got.text, "gamma early") {
		t.Errorf("snippet %q; want the earliest gamma region on the tie", got.text)
	}
}

func TestExtractSnippet_DistinctBeatsRepetition(t *testing.T) {
	// A window with two distinct terms once each beats a window with one term
	// repeated five times.
	repeated := "delta delta delta delta delta. "
	both := "epsilon and zeta share this line. "
	content := filler(50) + repeated + filler(300) + both + filler(300)

	got := extractSnippet(content, "prose", "epsilon zeta delta")
	assertSnippetInvariants(t, content, got)
	if !strings.Contains(got.text, "epsilon") || !strings.Contains(got.text, "zeta") {
		t.Errorf("snippet %q; want the 2-distinct window over the 1-term repetition", got.text)
	}
}

func TestExtractSnippet_DistantPairSharesOneWindow(t *testing.T) {
	// Two distinct terms ~350 runes apart both fit in one legal window, but no
	// window CENTERED on either site contains the other (each centered probe
	// scores one distinct term). Left-anchored probing must find the window
	// covering both.
	content := filler(80) + "alpha " + filler(340) + "omega " + filler(400)
	got := extractSnippet(content, "prose", "alpha omega")
	assertSnippetInvariants(t, content, got)
	if got.fallback {
		t.Fatalf("fallback = true; want located terms")
	}
	if !strings.Contains(got.text, "alpha") || !strings.Contains(got.text, "omega") {
		t.Errorf("snippet %q; want the off-center window covering both terms", got.text)
	}
}

func TestExtractSnippet_NaturalQueryStopwordsDontDominate(t *testing.T) {
	// LLM-style queries carry stopwords and punctuation. A head cluster dense
	// in "the"/"where" must not out-score the evidence term, and the trailing
	// "?" must not break location of the term it rides on.
	head := strings.Repeat("the whole team wondered where it all went. ", 14)
	content := head + "ProcessRequest failed after the retry budget was exhausted. " + filler(200)
	got := extractSnippet(content, "prose", "where did the ProcessRequest fail?")
	assertSnippetInvariants(t, content, got)
	if got.fallback {
		t.Fatalf("fallback = true; want evidence terms located")
	}
	if !strings.Contains(got.text, "ProcessRequest failed") {
		t.Errorf("snippet %q; want the evidence region, not a stopword cluster", got.text)
	}
}

func TestExtractSnippet_PunctuationOnlyDifference(t *testing.T) {
	// Query "migration?" must locate content that says "migration" — the index
	// tokenizers never see the punctuation, so the locator must not either.
	content := filler(420) + "the schema migration finished cleanly. " + filler(100)
	got := extractSnippet(content, "prose", "migration?")
	assertSnippetInvariants(t, content, got)
	if got.fallback {
		t.Fatalf("fallback = true; want the trimmed term located")
	}
	if !strings.Contains(got.text, "migration") {
		t.Errorf("snippet %q; want the migration sentence", got.text)
	}
}

func TestExtractSnippet_StopwordOnlyQueryStillLocates(t *testing.T) {
	// Noise terms are demoted, not dropped: a query of only stopwords still
	// centers on a located occurrence rather than reporting fallback.
	content := filler(390) + "nobody knew where the config lived. " + filler(200)
	got := extractSnippet(content, "prose", "where the")
	assertSnippetInvariants(t, content, got)
	if got.fallback {
		t.Fatalf("fallback = true; want noise terms still located")
	}
	if !strings.Contains(got.text, "where the config") {
		t.Errorf("snippet %q; want the window centered on the located stopwords", got.text)
	}
}

func TestExtractSnippet_ProseSentenceSnap(t *testing.T) {
	decisive := "The decisive fact is that the linker ran out of memory."
	content := filler(300) + " " + decisive + " " + filler(300)
	got := extractSnippet(content, "prose", "linker")
	assertSnippetInvariants(t, content, got)
	if !strings.Contains(got.text, decisive) {
		t.Errorf("snippet %q; want the decisive sentence whole", got.text)
	}
	// Snapped to a sentence terminator, not a mid-word cut.
	trimmed := strings.TrimSpace(got.text)
	if len(trimmed) > 0 && !strings.HasSuffix(trimmed, ".") && !strings.HasSuffix(trimmed, "!") && !strings.HasSuffix(trimmed, "?") {
		t.Errorf("snippet %q; want a sentence-terminated tail", got.text)
	}
}

func TestExtractSnippet_ProseWordBoundaryFallback(t *testing.T) {
	// No sentence terminator anywhere near the match, so snapping falls to word
	// boundaries: the snippet must not start or end mid-word.
	noTerm := strings.Repeat("word ", 120) + "needle " + strings.Repeat("token ", 120)
	got := extractSnippet(noTerm, "prose", "needle")
	assertSnippetInvariants(t, noTerm, got)
	if strings.HasPrefix(got.text, " ") {
		t.Errorf("snippet %q leads with whitespace; want a word boundary", got.text)
	}
	// A word-boundary snap: neither end splits a token.
	if !strings.Contains(got.text, "needle") {
		t.Errorf("snippet %q; want the needle retained", got.text)
	}
	first := strings.Fields(got.text)
	if len(first) > 0 && !strings.Contains(noTerm, first[0]) {
		t.Errorf("leading token %q not a whole word", first[0])
	}
}

func TestExtractSnippet_CodeLineSnap(t *testing.T) {
	head := strings.Repeat("// filler line of code here\n", 20)
	anchor := "func handleLinkerError() { return recover() }\n"
	tail := strings.Repeat("// trailing filler line\n", 20)
	content := head + anchor + tail

	got := extractSnippet(content, "code", "handleLinkerError")
	assertSnippetInvariants(t, content, got)
	if !strings.Contains(got.text, anchor[:len(anchor)-1]) {
		t.Errorf("snippet %q; want the anchor line whole", got.text)
	}
	// Whole lines: the snippet does not begin or end mid-line.
	if strings.HasPrefix(got.text, "iller") || strings.HasSuffix(got.text, "railing") {
		t.Errorf("snippet %q; want whole-line boundaries", got.text)
	}
}

func TestExtractSnippet_NoLocusLeadingWindowProse(t *testing.T) {
	// Query "cached" does not literally appear (content says "caching"); no
	// site → snapped leading window, fallback=true.
	content := "The caching subsystem warms on boot. " + filler(400)
	got := extractSnippet(content, "prose", "cached")
	assertSnippetInvariants(t, content, got)
	if !got.fallback {
		t.Errorf("fallback = false; want true for a no-locus prose match")
	}
	if !strings.HasPrefix(content, strings.TrimSpace(got.text[:min(10, len(got.text))])) {
		// Leading window: snippet starts at or near content head.
		if !strings.Contains(content[:200], got.text[:min(20, len(got.text))]) {
			t.Errorf("snippet %q; want a leading window", got.text)
		}
	}
}

func TestExtractSnippet_NoLocusLeadingWindowCode(t *testing.T) {
	head := "package main\n" + strings.Repeat("// a comment line\n", 60)
	content := head + strings.Repeat("var x = 1\n", 30)
	got := extractSnippet(content, "code", "nonexistentSymbol")
	assertSnippetInvariants(t, content, got)
	if !got.fallback {
		t.Errorf("fallback = false; want true for a no-locus code match")
	}
	if !strings.HasPrefix(got.text, "package main") {
		t.Errorf("snippet %q; want a whole-line leading window from the head", got.text)
	}
	if strings.HasSuffix(got.text, "var x =") {
		t.Errorf("snippet %q; want whole-line trailing boundary", got.text)
	}
}

func TestExtractSnippet_TitleOnlyMatch(t *testing.T) {
	// extractSnippet scans content only; a term present only in a title is a
	// no-locus fallback here (the title text never reaches this function).
	content := "provision the cluster and wait for readiness. " + filler(400)
	got := extractSnippet(content, "prose", "deployment")
	assertSnippetInvariants(t, content, got)
	if !got.fallback {
		t.Errorf("fallback = false; want true when the term is content-absent")
	}
	if !strings.Contains(got.text, "provision") {
		t.Errorf("snippet %q; want a content leading window", got.text)
	}
}

func TestExtractSnippet_BudgetAdherenceBoundaryless(t *testing.T) {
	// No terminators, no spaces, no newlines: snapping has nothing to grab, so
	// the hard rune cut at exactly the budget is the last resort.
	content := strings.Repeat("x", 1000)
	got := extractSnippet(content, "prose", "nomatch")
	assertSnippetInvariants(t, content, got)
	if utf8.RuneCountInString(got.text) != snippetBudgetRunes {
		t.Errorf("rune-len = %d; want exactly the budget on a boundaryless cut",
			utf8.RuneCountInString(got.text))
	}
}

func TestExtractSnippet_MultiByteRuneSafety(t *testing.T) {
	// CJK + accented text around the match; every return must be valid UTF-8 and
	// a clean substring (no broken runes from byte-space slicing).
	head := strings.Repeat("日本語のテキストです。", 40) // multibyte, > budget
	anchor := "café serves matcha. "
	tail := strings.Repeat("más texto acentuado aquí. ", 40)
	content := head + anchor + tail

	got := extractSnippet(content, "prose", "matcha")
	assertSnippetInvariants(t, content, got)
	if !utf8.ValidString(got.text) {
		t.Errorf("snippet is not valid UTF-8: %q", got.text)
	}
	if !strings.Contains(got.text, "matcha") {
		t.Errorf("snippet %q; want the matcha match", got.text)
	}
}

func TestExtractSnippet_DedupRepeatedQueryTerm(t *testing.T) {
	// "linker linker" dedups to one distinct term; density must not double-count
	// it as two distinct terms.
	content := "the linker failed here. " + filler(400)
	got := extractSnippet(content, "prose", "linker linker")
	assertSnippetInvariants(t, content, got)
	if got.fallback {
		t.Errorf("fallback = true; want a located window")
	}
	terms := splitTerms("linker linker")
	if len(terms) != 1 {
		t.Errorf("splitTerms deduped to %v; want one distinct term", terms)
	}
}

func TestExtractSnippet_EmptyQueryOrNoTerms(t *testing.T) {
	content := "some readable content here. " + filler(400)
	for _, q := range []string{"", "   ", "\t\n"} {
		got := extractSnippet(content, "prose", q)
		assertSnippetInvariants(t, content, got)
		if !got.fallback {
			t.Errorf("query %q: fallback = false; want a deterministic leading-window fallback", q)
		}
		if !strings.Contains(content[:200], got.text[:min(20, len(got.text))]) {
			t.Errorf("query %q: snippet %q; want a leading window", q, got.text)
		}
	}
}

func TestExtractSnippet_CaseFoldWidthChangeKeepsLocus(t *testing.T) {
	// Whole-string lowercasing widens İ (2 bytes) into i̇ (3 bytes), so offsets
	// computed in the lowered string drift one byte per preceding İ and would
	// center the window off the term while still reporting fallback=false.
	content := strings.Repeat("İİİİ pad. ", 60) + "the decisive marker sentence sits here. " + filler(200)
	got := extractSnippet(content, "prose", "marker")
	assertSnippetInvariants(t, content, got)
	if got.fallback {
		t.Fatalf("fallback = true; want a located window")
	}
	if !strings.Contains(got.text, "marker") {
		t.Errorf("snippet %q drifted off the located term", got.text)
	}
}

func TestExtractSnippet_GoFuncSourceWholeLines(t *testing.T) {
	// A real multi-line Go function, sliced verbatim from this package's source
	// via parser offsets, exercises code-mode extraction on representative
	// content: the located identifier surfaces inside a whole-line window.
	src, err := os.ReadFile("snippet.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	var fn string
	for _, d := range parsed.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "snapProse" {
			continue
		}
		fn = string(src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset])
	}
	if utf8.RuneCountInString(fn) <= snippetBudgetRunes {
		t.Fatalf("snapProse source is %d runes; the fixture must exceed the budget",
			utf8.RuneCountInString(fn))
	}

	got := extractSnippet(fn, "code", "wordStartForward")
	assertSnippetInvariants(t, fn, got)
	if got.fallback {
		t.Fatalf("fallback = true; want the identifier located")
	}
	if !strings.Contains(got.text, "wordStartForward") {
		t.Fatalf("snippet %q; want the located identifier inside", got.text)
	}

	at := strings.Index(fn, got.text)
	fnLines := strings.Split(fn, "\n")
	snipLines := strings.Split(strings.TrimSuffix(got.text, "\n"), "\n")
	firstLine := strings.Count(fn[:at], "\n")
	lastLine := firstLine + len(snipLines) - 1
	if snipLines[0] != fnLines[firstLine] {
		t.Errorf("first snippet line %q is not the full source line %q", snipLines[0], fnLines[firstLine])
	}
	if snipLines[len(snipLines)-1] != fnLines[lastLine] {
		t.Errorf("last snippet line %q is not the full source line %q", snipLines[len(snipLines)-1], fnLines[lastLine])
	}
}
