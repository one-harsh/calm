// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package chunk turns raw ingest content into format-aware db.Chunk slices:
// a detector classifies unhinted content (json/markdown/text), and one
// chunker per wire format maps content to titled, content_type-carrying
// chunks. Chunk content is exact source text (content-fidelity); the same
// input always yields byte-identical output.
package chunk

import (
	"fmt"
	"strings"

	"github.com/one-harsh/calm/internal/db"
)

const (
	formatLog        = "log"
	formatStacktrace = "stacktrace"
	formatCSV        = "csv"
	formatTSV        = "tsv"
	formatMetrics    = "metrics"
	formatJSON       = "json"
	formatMarkdown   = "markdown"
	formatText       = "text"
)

const (
	contentTypeProse = "prose"
	contentTypeCode  = "code"
)

const (
	// titleCap bounds every chunker's title, rune-safe.
	titleCap = 60
	// chunkTargetBytes sizes split units (text windows, log-run packing, CSV
	// row groups, metrics family splits): 1 MB payload cap ÷ 500-chunk cap —
	// a max-size payload chunked at target lands at the chunk cap instead of
	// silently truncating — and passage-sized for BM25.
	chunkTargetBytes = 2048
	// chunkMaxBytes tolerates up-to-2× units before splitting, so a slightly
	// long paragraph or run stays whole. It is also the format-agnostic hard cap
	// on stored chunk size that Split enforces (see boundChunks): a chunk over
	// the bound can only be read by inflating the byte budget to swallow it
	// whole, which is the outcome document-order reread exists to avoid.
	chunkMaxBytes = 2 * chunkTargetBytes
)

type chunkFunc func(source, content, contentType string) []db.Chunk

// chunkers is populated by each chunker file's register call at init time —
// a format's implementation and registration live in one file. A format with
// no registered chunker falls back to text in Split.
var chunkers = map[string]chunkFunc{}

func register(format string, fn chunkFunc) {
	chunkers[format] = fn
}

// Split resolves the effective format (hint wins; else detection) and
// dispatches to that format's chunker. The effective format is returned for
// observability. An unknown format falls back to the text chunker — the
// validation middleware enforces the wire enum, so this is defensive only.
func Split(source, content, format, contentType string) ([]db.Chunk, string) {
	if contentType == "" {
		contentType = contentTypeProse
	}
	if strings.TrimSpace(content) == "" {
		return nil, format
	}
	if format == "" {
		format = detectFormat(content)
	}
	fn, ok := chunkers[format]
	if !ok {
		return boundChunks(textChunks(source, content, contentType)), formatText
	}
	return boundChunks(fn(source, content, contentType)), format
}

// boundChunks enforces chunkMaxBytes on every stored chunk, whatever the format
// chunker produced, so document-order reread can always page a large capture. A
// chunk over the bound is split into byte-exact substrings whose concatenation
// reproduces the original chunk (content-fidelity); title and content_type carry
// to every piece.
func boundChunks(in []db.Chunk) []db.Chunk {
	out := make([]db.Chunk, 0, len(in))
	for _, c := range in {
		if len(c.Content) <= chunkMaxBytes {
			out = append(out, c)
			continue
		}
		for _, seg := range splitOnLineBoundaries(c.Content, chunkMaxBytes) {
			out = append(out, db.Chunk{Title: c.Title, Content: seg, ContentType: c.ContentType})
		}
	}
	return out
}

// splitOnLineBoundaries partitions s into consecutive byte-exact pieces, each at
// most maxBytes. Whole lines (trailing newline included) pack greedily until the
// next line would overflow the current piece. A single line longer than maxBytes
// is hard-split on rune boundaries — the one cut that is not a line boundary —
// so a newline-free run is never left tail-unreadable by reread. Concatenating
// the pieces reproduces s exactly.
func splitOnLineBoundaries(s string, maxBytes int) []string {
	var pieces []string
	segStart := 0
	for i := 0; i < len(s); {
		lineEnd := len(s)
		if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
			lineEnd = i + nl + 1
		}
		switch {
		case lineEnd-i > maxBytes:
			if i > segStart {
				pieces = append(pieces, s[segStart:i])
			}
			pieces = append(pieces, splitOnRuneBoundaries(s[i:lineEnd], maxBytes)...)
			segStart = lineEnd
		case i > segStart && lineEnd-segStart > maxBytes:
			pieces = append(pieces, s[segStart:i])
			segStart = i
		}
		i = lineEnd
	}
	if segStart < len(s) {
		pieces = append(pieces, s[segStart:])
	}
	return pieces
}

// splitOnRuneBoundaries cuts s into consecutive pieces of at most maxBytes, each
// ending on a rune boundary so no multi-byte rune is split (content-fidelity).
// Concatenating the pieces reproduces s. This is the sole cut for a newline-free
// run wider than the bound — the one span line-boundary splitting cannot bound.
func splitOnRuneBoundaries(s string, maxBytes int) []string {
	var pieces []string
	for len(s) > maxBytes {
		cut := runeBoundaryBefore(s, maxBytes)
		if cut <= 0 {
			cut = maxBytes
		}
		pieces = append(pieces, s[:cut])
		s = s[cut:]
	}
	return append(pieces, s)
}

// capTitle bounds a title to titleCap runes; empty input falls back to the
// numbered form.
func capTitle(title string, fallback string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return fallback
	}
	r := []rune(title)
	if len(r) > titleCap {
		return string(r[:titleCap])
	}
	return title
}

func numberedTitle(kind string, n int) string {
	return fmt.Sprintf("%s %d", kind, n)
}
