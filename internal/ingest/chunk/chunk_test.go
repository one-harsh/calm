// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/one-harsh/calm/internal/db"
)

func TestSplit_TextSplitsOnBlankLines(t *testing.T) {
	content := "para one\nstill one\n\npara two\n\n\npara three"
	got, _ := Split("src", content, formatText, "")
	if len(got) != 3 {
		t.Fatalf("got %d chunks; want 3: %+v", len(got), got)
	}
	if got[0].Content != "para one\nstill one" {
		t.Errorf("chunk[0].Content = %q", got[0].Content)
	}
}

func TestSplit_EmptyContentNoChunks(t *testing.T) {
	if got, _ := Split("src", "   \n  ", "", ""); len(got) != 0 {
		t.Fatalf("got %+v; want zero chunks for whitespace-only content", got)
	}
}

func TestSplit_HonorsContentTypeHint(t *testing.T) {
	got, _ := Split("src", "hello world", formatText, contentTypeCode)
	if got[0].ContentType != contentTypeCode {
		t.Errorf("content_type = %q; want code", got[0].ContentType)
	}
}

func TestSplit_HintWinsOverDetection(t *testing.T) {
	// JSON-shaped content with a text hint stays on the text path.
	got, eff := Split("src", "{\"a\":1}\n{\"b\":2}", formatText, "")
	if eff != formatText || len(got) != 1 {
		t.Fatalf("hinted text = %q/%d chunks; want text path", eff, len(got))
	}
}

// Byte-stability property: the same input yields byte-identical chunks on
// every run, across every fixture and every format (hinted and detected).
// This is what lets the fixtures double as retrieval-level baselines — and
// what catches any map-iteration order leaking into output.
func TestSplit_ByteStable(t *testing.T) {
	fixtures := []string{
		"train-multirank.log", "eval-results.jsonl", "readme-excerpt.md",
		"api-response.json", "api-response-array.json", "scrape.prom",
		"results.csv", "python-traceback.txt", "go-panic.txt",
	}
	formats := []string{
		"", formatLog, formatStacktrace, formatCSV, formatTSV,
		formatMetrics, formatJSON, formatMarkdown, formatText,
	}
	for _, name := range fixtures {
		content := fixture(t, name)
		for _, format := range formats {
			first, effA := Split("src", content, format, "")
			second, effB := Split("src", content, format, "")
			if effA != effB {
				t.Errorf("%s/%q: effective format unstable: %q vs %q", name, format, effA, effB)
			}
			if len(first) != len(second) {
				t.Errorf("%s/%q: chunk count unstable: %d vs %d", name, format, len(first), len(second))
				continue
			}
			for i := range first {
				if first[i] != second[i] {
					t.Errorf("%s/%q: chunk[%d] differs between runs:\n%q\nvs\n%q",
						name, format, i, first[i], second[i])
					break
				}
			}
		}
	}
}

// An oversized multi-line chunk splits into several pieces, each within the
// bound, whose concatenation reproduces the original chunk byte for byte
// (content-fidelity) and whose title/content_type carry unchanged — the
// property document-order reread depends on to page a large capture.
func TestBoundChunks_OversizedSplitsByteExact(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() <= chunkMaxBytes*3; i++ {
		fmt.Fprintf(&b, "line %05d: the quick brown fox jumps over the lazy dog\n", i)
	}
	content := b.String()

	got := boundChunks([]db.Chunk{{Title: "big", Content: content, ContentType: contentTypeCode}})
	if len(got) < 2 {
		t.Fatalf("got %d chunks; want the oversized chunk split into several", len(got))
	}
	var re strings.Builder
	for i, c := range got {
		if len(c.Content) > chunkMaxBytes {
			t.Errorf("chunk[%d] len %d exceeds bound %d", i, len(c.Content), chunkMaxBytes)
		}
		if c.Title != "big" || c.ContentType != contentTypeCode {
			t.Errorf("chunk[%d] lost title/content_type: %+v", i, c)
		}
		re.WriteString(c.Content)
	}
	if re.String() != content {
		t.Error("reassembled split chunks do not reproduce the original bytes")
	}
}

// A chunk exactly at the bound stays whole; one byte over splits into byte-exact
// pieces. The bound is an at-or-below ceiling, not a strict-below one.
func TestBoundChunks_BoundaryExactFitAndOneOver(t *testing.T) {
	exact := strings.Repeat("a", chunkMaxBytes-1) + "\n"
	if len(exact) != chunkMaxBytes {
		t.Fatalf("setup: exact len %d; want %d", len(exact), chunkMaxBytes)
	}
	if got := boundChunks([]db.Chunk{{Title: "x", Content: exact, ContentType: contentTypeProse}}); len(got) != 1 || got[0].Content != exact {
		t.Fatalf("exact-fit: got %d chunks; want 1 unchanged at the boundary", len(got))
	}

	over := exact + "b" // chunkMaxBytes + 1, splits on the line boundary
	got := boundChunks([]db.Chunk{{Title: "x", Content: over, ContentType: contentTypeProse}})
	if len(got) != 2 {
		t.Fatalf("one-over: got %d chunks; want 2", len(got))
	}
	for i, c := range got {
		if len(c.Content) > chunkMaxBytes {
			t.Errorf("one-over chunk[%d] len %d exceeds bound", i, len(c.Content))
		}
	}
	if got[0].Content+got[1].Content != over {
		t.Error("one-over pieces do not reassemble the original bytes")
	}
}

// Whole newline-terminated lines pack greedily up to the bound; every piece is a
// whole-line run and the pieces reassemble exactly.
func TestSplitOnLineBoundaries_GreedyWholeLines(t *testing.T) {
	// Ten 11-byte lines, bound 33 => three whole lines per piece.
	content := strings.Repeat("0123456789\n", 10)
	pieces := splitOnLineBoundaries(content, 33)
	if strings.Join(pieces, "") != content {
		t.Fatal("pieces do not reassemble")
	}
	for i, p := range pieces {
		if len(p) > 33 {
			t.Errorf("piece[%d] = %d bytes; exceeds bound 33", i, len(p))
		}
		if p == "" || !strings.HasSuffix(p, "\n") {
			t.Errorf("piece[%d] = %q; want whole newline-terminated lines", i, p)
		}
	}
}

// A bound-filling first line followed by a short unterminated tail: the tail
// straddles the bound and must lead its own piece, not overflow the first. A
// blind spot in count-only split tests.
func TestSplitOnLineBoundaries_TrailingUnterminatedStraddle(t *testing.T) {
	const maxBytes = 32
	head := strings.Repeat("h", maxBytes-1) + "\n" // exactly maxBytes with newline
	content := head + "tail"                       // unterminated short tail
	pieces := splitOnLineBoundaries(content, maxBytes)
	if strings.Join(pieces, "") != content {
		t.Fatalf("pieces do not reassemble: %q", pieces)
	}
	if len(pieces) != 2 || pieces[0] != head || pieces[1] != "tail" {
		t.Fatalf("pieces = %q; want [head, tail] split on the line boundary", pieces)
	}
	for i, p := range pieces {
		if len(p) > maxBytes {
			t.Errorf("piece[%d] = %d bytes; exceeds bound %d", i, len(p), maxBytes)
		}
	}
}

// A newline-free run wider than the bound is the one span line splitting cannot
// bound: it hard-splits on rune boundaries. No piece exceeds the bound, no
// multi-byte rune is split, and the pieces reassemble exactly.
func TestSplitOnLineBoundaries_NewlineFreeRuneHardSplit(t *testing.T) {
	const maxBytes = 100
	run := strings.Repeat("☃", 500) // 3 bytes each, 1500 bytes, no newline
	pieces := splitOnLineBoundaries(run, maxBytes)
	if strings.Join(pieces, "") != run {
		t.Fatal("pieces do not reassemble the newline-free run")
	}
	if len(pieces) < 2 {
		t.Fatalf("got %d pieces; want the run hard-split into several", len(pieces))
	}
	for i, p := range pieces {
		if len(p) > maxBytes {
			t.Errorf("piece[%d] = %d bytes; exceeds bound %d", i, len(p), maxBytes)
		}
		if !utf8.ValidString(p) {
			t.Errorf("piece[%d] split a multi-byte rune", i)
		}
	}
}

// The bound is wired into the public entry point across formats: a markdown
// chunker that maps a heading-free section to one over-bound span still yields
// several pageable chunks whose bytes reassemble to the chunker's section.
func TestSplit_MarkdownMonolithSectionIsPaged(t *testing.T) {
	var b strings.Builder
	b.WriteString("# One Big Section\n")
	for b.Len() <= chunkMaxBytes*2 {
		b.WriteString("prose line with enough words to matter here\n")
	}
	content := b.String()

	raw := markdownChunks("doc.md", content, contentTypeProse)
	if len(raw) != 1 || len(raw[0].Content) <= chunkMaxBytes {
		t.Fatalf("setup: want one over-bound section from the chunker; got %d chunks", len(raw))
	}

	got, eff := Split("doc.md", content, formatMarkdown, contentTypeProse)
	if eff != formatMarkdown {
		t.Fatalf("effective format = %q; want markdown", eff)
	}
	if len(got) < 2 {
		t.Fatalf("got %d chunks; want the monolithic section paged for reread", len(got))
	}
	var re strings.Builder
	for i, c := range got {
		if len(c.Content) > chunkMaxBytes {
			t.Errorf("chunk[%d] = %d bytes; exceeds the stored bound %d", i, len(c.Content), chunkMaxBytes)
		}
		re.WriteString(c.Content)
	}
	if re.String() != raw[0].Content {
		t.Error("paged markdown pieces do not reassemble to the chunker's section bytes")
	}
}
