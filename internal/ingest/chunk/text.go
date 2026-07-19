// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/one-harsh/calm/internal/db"
)

var blankLineRe = regexp.MustCompile(`\n[ \t]*\n`)

// textChunks splits on blank-line paragraphs first, then windows any
// paragraph exceeding chunkMaxBytes — semantic boundaries where they exist,
// bounded chunks where they don't (blank-line-sparse shell output would
// otherwise collapse into one mega-chunk).
func textChunks(_, content, contentType string) []db.Chunk {
	var chunks []db.Chunk
	for _, p := range blankLineRe.Split(content, -1) {
		body := strings.TrimSpace(p)
		if body == "" {
			continue
		}
		for _, piece := range windowParagraph(body) {
			chunks = append(chunks, db.Chunk{
				Title:       paragraphTitle(piece, len(chunks)+1),
				Content:     piece,
				ContentType: contentType,
			})
		}
	}
	if len(chunks) == 0 {
		chunks = append(chunks, db.Chunk{Title: "section 1", Content: content, ContentType: contentType})
	}
	return chunks
}

// windowParagraph returns the paragraph whole when it fits chunkMaxBytes,
// else slices windows of at most chunkTargetBytes with edges snapped back to
// the last newline; a newline-free window splits on the nearest rune
// boundary. The final remainder may run up to chunkMaxBytes.
func windowParagraph(p string) []string {
	if len(p) <= chunkMaxBytes {
		return []string{p}
	}
	var pieces []string
	rest := p
	for len(rest) > chunkMaxBytes {
		cut := strings.LastIndexByte(rest[:chunkTargetBytes], '\n')
		if cut <= 0 {
			cut = runeBoundaryBefore(rest, chunkTargetBytes)
		}
		piece := strings.TrimRight(rest[:cut], "\n")
		if strings.TrimSpace(piece) != "" {
			pieces = append(pieces, piece)
		}
		rest = strings.TrimLeft(rest[cut:], "\n")
	}
	if strings.TrimSpace(rest) != "" {
		pieces = append(pieces, rest)
	}
	return pieces
}

// runeBoundaryBefore returns the largest index ≤ limit that starts a rune.
func runeBoundaryBefore(s string, limit int) int {
	i := limit
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

func paragraphTitle(body string, n int) string {
	firstLine := body
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		firstLine = body[:i]
	}
	return capTitle(firstLine, numberedTitle("section", n))
}

func init() {
	register(formatText, textChunks)
}
