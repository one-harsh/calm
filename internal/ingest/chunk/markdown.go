// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	gtext "github.com/yuin/goldmark/text"

	"github.com/one-harsh/calm/internal/db"
)

var mdParser = goldmark.New()

// markdownChunks walks the goldmark AST and partitions the source into
// exactly-once byte spans: heading boundaries end the previous section, and
// top-level code blocks (fenced or indented) are excised from the
// surrounding prose and emitted as their own content_type=code chunks —
// duplication would double-count vocabulary doc_freq and BM25 mass. Chunk
// content is exact source bytes (content-fidelity), never a render. Titles
// are heading-stack breadcrumbs; preamble before the first heading keeps the
// source label as its title.
func markdownChunks(source, content, contentType string) []db.Chunk {
	src := []byte(content)
	doc := mdParser.Parser().Parse(gtext.NewReader(src))

	type crumb struct {
		level int
		text  string
	}
	var stack []crumb
	title := func() string {
		if len(stack) == 0 {
			return capTitle(source, "section 1")
		}
		parts := make([]string, len(stack))
		for i, c := range stack {
			parts[i] = c.text
		}
		return capTitle(strings.Join(parts, " > "), source)
	}

	var chunks []db.Chunk
	cursor := 0
	emitProse := func(end int) {
		if end <= cursor {
			return
		}
		body := strings.TrimRight(content[cursor:end], "\n")
		if strings.TrimSpace(body) != "" {
			chunks = append(chunks, db.Chunk{Title: title(), Content: body, ContentType: contentType})
		}
	}

	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		switch node := n.(type) {
		case *ast.Heading:
			if node.Lines().Len() == 0 {
				continue
			}
			start := lineStartAt(src, node.Lines().At(0).Start)
			emitProse(start)
			cursor = start
			text := headingTitle(content, node)
			for len(stack) > 0 && stack[len(stack)-1].level >= node.Level {
				stack = stack[:len(stack)-1]
			}
			stack = append(stack, crumb{level: node.Level, text: text})
		case *ast.FencedCodeBlock:
			start, end, ok := fencedSpan(src, node)
			if !ok {
				continue
			}
			emitProse(start)
			chunks = append(chunks, db.Chunk{
				Title:       title(),
				Content:     strings.TrimRight(content[start:end], "\n"),
				ContentType: contentTypeCode,
			})
			cursor = end
		case *ast.CodeBlock:
			if node.Lines().Len() == 0 {
				continue
			}
			start := lineStartAt(src, node.Lines().At(0).Start)
			end := lineEndAfter(src, node.Lines().At(node.Lines().Len()-1).Stop-1)
			emitProse(start)
			chunks = append(chunks, db.Chunk{
				Title:       title(),
				Content:     strings.TrimRight(content[start:end], "\n"),
				ContentType: contentTypeCode,
			})
			cursor = end
		}
	}
	emitProse(len(content))

	if len(chunks) == 0 {
		chunks = append(chunks, db.Chunk{Title: capTitle(source, "section 1"), Content: content, ContentType: contentType})
	}
	return chunks
}

// fencedSpan resolves the full source span of a fenced block including both
// fence-marker lines (the inner Lines() exclude them). ok=false means the
// span can't be resolved (empty info-less block) — the bytes then stay in the
// surrounding prose chunk, preserving exactly-once partitioning.
func fencedSpan(src []byte, node *ast.FencedCodeBlock) (int, int, bool) {
	var innerStart int
	switch {
	case node.Lines().Len() > 0:
		innerStart = lineStartAt(src, node.Lines().At(0).Start)
	case node.Info != nil:
		innerStart = lineEndAfter(src, node.Info.Segment.Start)
	default:
		return 0, 0, false
	}
	if innerStart == 0 {
		return 0, 0, false
	}
	start := lineStartAt(src, innerStart-1)

	end := innerStart
	if node.Lines().Len() > 0 {
		end = node.Lines().At(node.Lines().Len() - 1).Stop
	}
	if end < len(src) {
		closeEnd := lineEndAfter(src, end)
		if fenceMarkerRe.Match(src[end:closeEnd]) {
			end = closeEnd
		}
	}
	return start, end, true
}

func headingTitle(content string, node *ast.Heading) string {
	seg := node.Lines().At(0)
	raw := strings.TrimSpace(content[seg.Start:seg.Stop])
	if h, ok := headingText(raw); ok {
		return h
	}
	return raw
}

// lineStartAt returns the index of the first byte of the line containing pos.
func lineStartAt(src []byte, pos int) int {
	if pos > len(src) {
		pos = len(src)
	}
	for pos > 0 && src[pos-1] != '\n' {
		pos--
	}
	return pos
}

// lineEndAfter returns the index just past the newline of the line containing
// pos (or len(src) on the final unterminated line).
func lineEndAfter(src []byte, pos int) int {
	for pos < len(src) && src[pos] != '\n' {
		pos++
	}
	if pos < len(src) {
		pos++
	}
	return pos
}

func init() {
	register(formatMarkdown, markdownChunks)
}
