// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"regexp"
	"strings"

	"github.com/one-harsh/calm/internal/db"
)

// timestampRe recognizes the common wild timestamp shapes: ISO-8601-ish,
// bare wall-clock, syslog, glog, and bracketed epoch. A line anchors a run
// when a match STARTS within its first anchorPrefixBytes — tolerating
// `[rank 3] `-style prefixes, which stay inside runs rather than splitting
// them.
var timestampRe = regexp.MustCompile(
	`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}([.,]\d+)?(Z|[+-]\d{2}:?\d{2})?` +
		`|\b\d{2}:\d{2}:\d{2}([.,]\d+)?\b` +
		`|[A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2}` +
		`|[IWEF]\d{4} \d{2}:\d{2}:\d{2}\.\d{6}` +
		`|\[\d{10}(\.\d+)?\]`,
)

const anchorPrefixBytes = 48

// logChunks groups lines into timestamp-anchored runs (an anchor line
// through the line before the next anchor; a leading unanchored block is its
// own run), then greedily packs runs into chunks up to chunkTargetBytes — a
// dense per-line-timestamped log must not become one chunk per line. A
// single run beyond chunkMaxBytes windows. No anchors at all delegates to
// the text chunker.
func logChunks(source, content, contentType string) []db.Chunk {
	lines := strings.Split(content, "\n")
	var runs []string
	var cur []string
	anchored := false
	flush := func() {
		if len(cur) == 0 {
			return
		}
		run := strings.TrimRight(strings.Join(cur, "\n"), "\n")
		cur = nil
		if strings.TrimSpace(run) != "" {
			runs = append(runs, run)
		}
	}
	for _, line := range lines {
		if timestampAnchor(line) {
			anchored = true
			flush()
		}
		cur = append(cur, line)
	}
	flush()

	if !anchored {
		return textChunks(source, content, contentType)
	}

	var chunks []db.Chunk
	emit := func(body string) {
		firstLine := body
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			firstLine = body[:i]
		}
		chunks = append(chunks, db.Chunk{
			Title:       capTitle(firstLine, numberedTitle("section", len(chunks)+1)),
			Content:     body,
			ContentType: contentType,
		})
	}
	var packed []string
	size := 0
	for _, run := range runs {
		if len(run) > chunkMaxBytes {
			if len(packed) > 0 {
				emit(strings.Join(packed, "\n"))
				packed, size = nil, 0
			}
			for _, piece := range windowParagraph(run) {
				emit(piece)
			}
			continue
		}
		if size > 0 && size+len(run)+1 > chunkTargetBytes {
			emit(strings.Join(packed, "\n"))
			packed, size = nil, 0
		}
		packed = append(packed, run)
		size += len(run) + 1
	}
	if len(packed) > 0 {
		emit(strings.Join(packed, "\n"))
	}
	return chunks
}

func timestampAnchor(line string) bool {
	probe := line
	if len(probe) > anchorPrefixBytes+32 {
		probe = probe[:anchorPrefixBytes+32]
	}
	loc := timestampRe.FindStringIndex(probe)
	return loc != nil && loc[0] <= anchorPrefixBytes
}

func init() {
	register(formatLog, logChunks)
}
