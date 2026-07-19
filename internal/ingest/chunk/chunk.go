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
	// long paragraph or run stays whole.
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
		return textChunks(source, content, contentType), formatText
	}
	return fn(source, content, contentType), format
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
