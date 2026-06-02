// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/one-harsh/calm/internal/db"
)

var (
	mdHeadingRe = regexp.MustCompile(`^\s*#{1,6}\s+(.+?)\s*$`)
	blankLineRe = regexp.MustCompile(`\n[ \t]*\n`)
)

// HLD-DEVIATION: the HLD ingest section specifies auto-detection across the
// full format enum (log/stacktrace/csv/tsv/metrics/json/markdown/text) with
// format-aware chunkers. Smoke handles only markdown (split at ATX headings)
// vs. everything-else (split on blank-line paragraphs) and applies one
// content_type to every chunk.
func chunk(source, content, format, contentType string) []db.Chunk {
	if contentType == "" {
		contentType = "prose"
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if format == "markdown" || (format == "" && looksMarkdown(content)) {
		return markdownChunks(source, content, contentType)
	}
	return paragraphChunks(content, contentType)
}

func looksMarkdown(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if _, ok := headingText(line); ok {
			return true
		}
	}
	return false
}

func headingText(line string) (string, bool) {
	m := mdHeadingRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// TODO: replace this line-based heading split with a goldmark AST walk — the
// regex misclassifies `#` inside fenced code blocks and ignores setext headings.
func markdownChunks(source, content, contentType string) []db.Chunk {
	var chunks []db.Chunk
	title := source
	var buf []string
	flush := func() {
		body := strings.TrimRight(strings.Join(buf, "\n"), "\n")
		buf = nil
		if strings.TrimSpace(body) == "" {
			return
		}
		chunks = append(chunks, db.Chunk{Title: title, Content: body, ContentType: contentType})
	}
	for _, line := range strings.Split(content, "\n") {
		if h, ok := headingText(line); ok {
			flush()
			title = h
		}
		buf = append(buf, line)
	}
	flush()
	if len(chunks) == 0 {
		chunks = append(chunks, db.Chunk{Title: source, Content: content, ContentType: contentType})
	}
	return chunks
}

func paragraphChunks(content, contentType string) []db.Chunk {
	var chunks []db.Chunk
	for _, p := range blankLineRe.Split(content, -1) {
		body := strings.TrimSpace(p)
		if body == "" {
			continue
		}
		chunks = append(chunks, db.Chunk{
			Title:       paragraphTitle(body, len(chunks)+1),
			Content:     body,
			ContentType: contentType,
		})
	}
	if len(chunks) == 0 {
		chunks = append(chunks, db.Chunk{Title: "section 1", Content: content, ContentType: contentType})
	}
	return chunks
}

func paragraphTitle(body string, n int) string {
	firstLine := body
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		firstLine = body[:i]
	}
	firstLine = strings.TrimSpace(firstLine)
	r := []rune(firstLine)
	if len(r) == 0 {
		return fmt.Sprintf("section %d", n)
	}
	if len(r) > 60 {
		return string(r[:60])
	}
	return firstLine
}

func preview(content string) string {
	r := []rune(content)
	if len(r) <= previewMax {
		return content
	}
	return string(r[:previewMax])
}
