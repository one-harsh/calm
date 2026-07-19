// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"regexp"
	"slices"
	"strings"

	"github.com/one-harsh/calm/internal/db"
)

var (
	pythonTracebackRe = regexp.MustCompile(`^Traceback \(most recent call last\):`)
	pythonChainRe     = regexp.MustCompile(`^(During handling of the above exception|The above exception was the direct cause)`)
	pythonFinalRe     = regexp.MustCompile(`^[A-Za-z_][\w.]*(Error|Exception|Warning|Interrupt|Exit)?: .+`)
	goPanicRe         = regexp.MustCompile(`^panic: `)
	goGoroutineRe     = regexp.MustCompile(`^goroutine \d+ \[`)
	jvmThreadRe       = regexp.MustCompile(`^Exception in thread `)
	jvmExceptionRe    = regexp.MustCompile(`^[A-Za-z_][\w.]*(Exception|Error)(: .*)?$`)
	jvmCausedByRe     = regexp.MustCompile(`^Caused by:`)
	jvmFrameRe        = regexp.MustCompile(`^\s+at `)
)

// stacktraceChunks emits one chunk per logical error report — never per
// frame, and never per Traceback block: a chained Python sequence
// (Traceback → ValueError → "During handling…" → Traceback → RuntimeError)
// is ONE report, as is a JVM trace with its Caused-by blocks and a Go panic
// header with its first goroutine block. Only a start marker with no
// preceding chain connector opens a new report.
func stacktraceChunks(source, content, contentType string) []db.Chunk {
	_ = contentType // stacktrace frames are identifier-heavy: always code
	lines := strings.Split(content, "\n")

	var chunks []db.Chunk
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		body := strings.TrimRight(strings.Join(cur, "\n"), "\n")
		cur = nil
		if strings.TrimSpace(body) == "" {
			return
		}
		chunks = append(chunks, db.Chunk{
			Title:       traceTitle(body, len(chunks)+1),
			Content:     body,
			ContentType: contentTypeCode,
		})
	}

	chainPending := false
	sawMarker := false
	for i, line := range lines {
		switch {
		case pythonChainRe.MatchString(line) || jvmCausedByRe.MatchString(line):
			chainPending = true
			cur = append(cur, line)
		// A bare `Type: message` line is a JVM start only when a frame line
		// follows — Python's final exception lines match the same shape and
		// must stay inside their report.
		case pythonTracebackRe.MatchString(line) || jvmThreadRe.MatchString(line) ||
			(jvmExceptionRe.MatchString(line) && nextNonBlankIsFrame(lines, i+1)):
			sawMarker = true
			if !chainPending && hasReportBody(cur) {
				flush()
			}
			chainPending = false
			cur = append(cur, line)
		case goPanicRe.MatchString(line):
			sawMarker = true
			if hasReportBody(cur) {
				flush()
			}
			cur = append(cur, line)
		case goGoroutineRe.MatchString(line):
			sawMarker = true
			// The first goroutine block groups with its panic header; each
			// further goroutine block in a dump is its own report.
			if containsGoroutine(cur) {
				flush()
			}
			cur = append(cur, line)
		default:
			cur = append(cur, line)
		}
	}
	flush()

	if !sawMarker {
		body := strings.TrimRight(content, "\n")
		return []db.Chunk{{
			Title:       traceTitle(body, 1),
			Content:     body,
			ContentType: contentTypeCode,
		}}
	}
	return chunks
}

// hasReportBody distinguishes a report in progress from leading blanks —
// a new start marker after nothing but whitespace begins, not ends, a report.
func hasReportBody(cur []string) bool {
	for _, l := range cur {
		if strings.TrimSpace(l) != "" {
			return true
		}
	}
	return false
}

func containsGoroutine(cur []string) bool {
	return slices.ContainsFunc(cur, goGoroutineRe.MatchString)
}

func nextNonBlankIsFrame(lines []string, from int) bool {
	for _, l := range lines[min(from, len(lines)):] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		return jvmFrameRe.MatchString(l)
	}
	return false
}

// traceTitle names the report by the exception the user actually sees: the
// FINAL `Type: message` line for Python (chained reports surface the
// outermost error last), the panic message or goroutine header for Go, and
// the FIRST exception line for JVM (outermost-first order).
func traceTitle(body string, n int) string {
	fallback := numberedTitle("trace", n)
	lines := strings.Split(body, "\n")

	if first := firstNonBlank(lines); first != "" {
		switch {
		case goPanicRe.MatchString(first):
			return capTitle(first, fallback)
		case goGoroutineRe.MatchString(first):
			return capTitle(first, fallback)
		case jvmThreadRe.MatchString(first) || jvmExceptionRe.MatchString(first):
			if next := secondNonBlank(lines); next != "" && jvmFrameRe.MatchString(next) {
				return capTitle(first, fallback)
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if pythonFinalRe.MatchString(line) {
			return capTitle(line, fallback)
		}
		break
	}
	return fallback
}

func firstNonBlank(lines []string) string {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func secondNonBlank(lines []string) string {
	seen := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if seen {
			return l
		}
		seen = true
	}
	return ""
}

func init() {
	register(formatStacktrace, stacktraceChunks)
}
