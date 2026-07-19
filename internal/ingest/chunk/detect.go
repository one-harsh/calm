// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	mdHeadingRe   = regexp.MustCompile(`^\s*#{1,6}\s+(.+?)\s*$`)
	mdListItemRe  = regexp.MustCompile(`^\s*([-*+]|\d+\.)\s+\S`)
	fenceMarkerRe = regexp.MustCompile("^\\s*(```|~~~)")
	// Prometheus exposition comments (# HELP/TYPE/UNIT/EOF) are syntactically
	// ATX headings; excluding them keeps an unhinted scrape from classifying
	// as markdown (metrics is a hint-only tier — the scrape lands in text).
	expositionCommentRe = regexp.MustCompile(`^#\s+(HELP|TYPE|UNIT|EOF)\b`)
)

// minListLines: one list-marker line is everyday prose punctuation; two is a
// document structure signal.
const minListLines = 2

// detectFormat classifies unhinted content as json, markdown, or text. The
// hint-only tiers (log/stacktrace/csv/tsv/metrics) are never auto-detected —
// their shapes are too ambiguous to claim without a workload hint.
func detectFormat(content string) string {
	if looksJSON(content) {
		return formatJSON
	}
	if looksMarkdownDoc(content) {
		return formatMarkdown
	}
	return formatText
}

// looksJSON accepts a whole-document JSON container or a JSONL record
// stream. Empty containers are excluded: the chunker has no records or
// members to work with and would fall back to text, and the detected format
// must describe what the chunker will actually do — it reaches logs and
// correlation request_meta.
func looksJSON(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		if json.Valid([]byte(trimmed)) &&
			strings.TrimSpace(trimmed[1:len(trimmed)-1]) != "" {
			return true
		}
	}
	return looksJSONL(content)
}

// looksJSONL requires EVERY non-empty line to be a standalone JSON object or
// array (scalar lines don't count — a file of bare numbers is text), bailing
// on the first line that isn't. Full validation, not a probe: the detected
// format flows to logs and correlation request_meta, so it must match what
// the chunker will do — the chunker's jsonlRecords applies these same
// criteria, making detected-json succeed by construction. One-line payloads
// are covered by the whole-document rule, so at least two non-empty lines
// are required.
func looksJSONL(content string) bool {
	nonEmpty := 0
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line[0] != '{' && line[0] != '[' {
			return false
		}
		if !json.Valid([]byte(line)) {
			return false
		}
		nonEmpty++
	}
	return nonEmpty >= 2
}

// looksMarkdownDoc scans with fence tracking: an ATX heading outside a fence,
// a complete fence pair, or two list-marker lines classify the document as
// markdown. `#` inside a fenced block does not. Setext headings are left to
// the markdown chunker — too ambiguous as a detection signal.
func looksMarkdownDoc(content string) bool {
	inFence := false
	fenceCount := 0
	listLines := 0
	for line := range strings.SplitSeq(content, "\n") {
		if fenceMarkerRe.MatchString(line) {
			inFence = !inFence
			if !inFence {
				fenceCount++
			}
			continue
		}
		if inFence {
			continue
		}
		if mdHeadingRe.MatchString(line) && !expositionCommentRe.MatchString(line) {
			return true
		}
		if mdListItemRe.MatchString(line) {
			listLines++
		}
	}
	return fenceCount > 0 || listLines >= minListLines
}
