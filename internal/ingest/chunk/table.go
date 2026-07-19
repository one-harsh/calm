// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/one-harsh/calm/internal/db"
)

func csvChunks(source, content, contentType string) []db.Chunk {
	return delimitedChunks(source, content, contentType, ',')
}

func tsvChunks(source, content, contentType string) []db.Chunk {
	return delimitedChunks(source, content, contentType, '\t')
}

// delimitedChunks groups data rows into chunks of at most chunkTargetBytes
// with the header row prepended to every chunk, so each chunk is
// self-describing. The csv reader locates record boundaries (it is the only
// thing that handles quoted embedded newlines correctly); content is always
// raw source-byte slices, never re-serialized. A mid-stream parse error
// demotes the remaining bytes to text chunks — nothing is dropped.
func delimitedChunks(source, content, contentType string, comma rune) []db.Chunk {
	r := csv.NewReader(strings.NewReader(content))
	r.Comma = comma
	r.FieldsPerRecord = -1

	prevOffset := int64(0)
	if _, err := r.Read(); err != nil {
		return textChunks(source, content, contentType)
	}
	headerEnd := r.InputOffset()
	header := strings.TrimRight(content[:headerEnd], "\r\n")
	prevOffset = headerEnd

	type rowSpan struct {
		start, end int64
	}
	var rows []rowSpan
	tailStart := int64(-1)
	for {
		_, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// The reader stalls on malformed input; everything from the last
			// good boundary onward becomes text.
			tailStart = prevOffset
			break
		}
		rows = append(rows, rowSpan{start: prevOffset, end: r.InputOffset()})
		prevOffset = r.InputOffset()
	}

	var chunks []db.Chunk
	emit := func(firstRow, lastRow int) {
		body := header + "\n" + strings.TrimRight(content[rows[firstRow].start:rows[lastRow].end], "\r\n")
		chunks = append(chunks, db.Chunk{
			Title:       fmt.Sprintf("rows %d-%d", firstRow+1, lastRow+1),
			Content:     body,
			ContentType: contentType,
		})
	}
	groupStart := 0
	size := len(header)
	for i := range rows {
		rowLen := int(rows[i].end - rows[i].start)
		if i > groupStart && size+rowLen > chunkTargetBytes {
			emit(groupStart, i-1)
			groupStart = i
			size = len(header)
		}
		size += rowLen
	}
	if len(rows) > 0 {
		emit(groupStart, len(rows)-1)
	}

	if tailStart >= 0 {
		chunks = append(chunks, textChunks(source, content[tailStart:], contentType)...)
	}
	if len(chunks) == 0 {
		return textChunks(source, content, contentType)
	}
	return chunks
}

func init() {
	register(formatCSV, csvChunks)
	register(formatTSV, tsvChunks)
}
