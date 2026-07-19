// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/one-harsh/calm/internal/db"
)

// titleKey is one entry in titlePriority: an exact key (matched case-insensitively)
// or a key suffix (e.g. "_id"), optionally length-gated. A shortOnly entry is
// skipped when its key=value rendering would overflow titleCap — a short
// descriptive field (msg="connection refused") titles better than an opaque id,
// but a long one truncates into prose, so identity wins instead.
type titleKey struct {
	match     string
	suffix    string
	shortOnly bool
}

func (t titleKey) matches(key string) bool {
	if t.suffix != "" {
		return strings.HasSuffix(strings.ToLower(key), t.suffix)
	}
	return strings.EqualFold(key, t.match)
}

// titlePriority ranks the field a record title leads with. Order encodes
// identity-vs-semantic; the shortOnly gate arbitrates short-descriptive vs
// identity. Naming and short descriptive fields outrank identity; identity
// (including the "_id" suffix convention) outranks low-cardinality categoricals.
// The title is the attribution surface a searcher sees, so a discriminating
// field beats a positional index.
var titlePriority = []titleKey{
	{match: "name", shortOnly: true},
	{match: "title", shortOnly: true},
	{match: "message", shortOnly: true},
	{match: "msg", shortOnly: true},
	{match: "event", shortOnly: true},
	{match: "summary", shortOnly: true},
	{match: "id"},
	{suffix: "_id"},
	{match: "uuid"},
	{match: "key"},
	{match: "slug"},
	{match: "case"},
	{match: "role"},
	{match: "level"},
	{match: "status"},
	{match: "type"},
}

// titleExclude are grouping/timestamp keys that are constant or opaque across a
// record set; the document-order fallback skips them so a title never leads on a
// value that repeats across every chunk or reads as noise.
var titleExclude = map[string]bool{
	"task": true, "dataset": true, "benchmark": true, "split": true,
	"model": true, "subject": true, "service": true, "logger": true,
	"namespace": true, "version": true, "env": true,
	"timestamp": true, "ts": true, "time": true,
	"created_at": true, "updated_at": true,
}

// jsonChunks chunks record sequences (JSONL lines or a top-level array) by
// record with small records packed toward the target size, a single
// top-level object one chunk per member, and falls through to the text
// chunker for anything else — nothing is dropped.
func jsonChunks(source, content, contentType string) []db.Chunk {
	if records, ok := jsonlRecords(content); ok {
		return recordChunks(records, contentType)
	}

	trimmed := strings.TrimSpace(content)
	if len(trimmed) > 0 && json.Valid([]byte(trimmed)) {
		if trimmed[0] == '[' {
			if records, ok := topLevelArrayElements(trimmed); ok {
				return recordChunks(records, contentType)
			}
		}
		if trimmed[0] == '{' {
			if chunks, ok := objectMemberChunks(trimmed, contentType); ok {
				return chunks
			}
		}
	}
	return textChunks(source, content, contentType)
}

// recordChunks packs consecutive records greedily toward chunkTargetBytes so
// record floods (JSON-formatted logs) don't explode the chunk count into the
// per-source cap and lose the tail. Records are atomic: one that would break
// over the boundary goes wholly into the next chunk, and a record larger
// than the target stands alone. A chunk holding a single record keeps its
// discriminating title; packed chunks title by record range.
func recordChunks(records []string, contentType string) []db.Chunk {
	var chunks []db.Chunk
	var packed []string
	firstIdx := 0
	size := 0
	flush := func(lastIdx int) {
		if len(packed) == 0 {
			return
		}
		title := fmt.Sprintf("records %d-%d", firstIdx+1, lastIdx)
		if len(packed) == 1 {
			title = recordTitle(packed[0], firstIdx+1)
		}
		chunks = append(chunks, db.Chunk{
			Title:       title,
			Content:     strings.Join(packed, "\n"),
			ContentType: contentType,
		})
		packed, size = nil, 0
	}
	for i, rec := range records {
		if len(packed) > 0 && size+len(rec)+1 > chunkTargetBytes {
			flush(i)
		}
		if len(packed) == 0 {
			firstIdx = i
		}
		packed = append(packed, rec)
		size += len(rec) + 1
	}
	flush(len(records))
	return chunks
}

func jsonlRecords(content string) ([]string, bool) {
	var records []string
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if line[0] != '{' && line[0] != '[' {
			return nil, false
		}
		if !json.Valid([]byte(line)) {
			return nil, false
		}
		records = append(records, line)
	}
	return records, len(records) >= 2
}

func topLevelArrayElements(data string) ([]string, bool) {
	dec := json.NewDecoder(strings.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, false
	}
	var records []string
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		records = append(records, string(raw))
	}
	return records, len(records) > 0
}

// objectMemberChunks emits one chunk per top-level member; the content is a
// self-describing `"key": <value>` pair (same spirit as the CSV header rule),
// with the value bytes verbatim from the source.
func objectMemberChunks(data, contentType string) ([]db.Chunk, bool) {
	dec := json.NewDecoder(strings.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var chunks []db.Chunk
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, false
		}
		chunks = append(chunks, db.Chunk{
			Title:       capTitle(key, numberedTitle("member", len(chunks)+1)),
			Content:     string(encodedKey) + ": " + string(raw),
			ContentType: contentType,
		})
	}
	return chunks, len(chunks) > 0
}

// recordTitle picks the discriminating field: the highest-priority matching
// scalar member (titlePriority), else the first non-excluded scalar in document
// order, else a positional fallback. The token walk keeps this deterministic
// (map iteration would not be).
func recordTitle(record string, n int) string {
	fallback := numberedTitle("record", n)
	keys, values := scalarMembers(record)
	if len(keys) == 0 {
		return fallback
	}
	for _, p := range titlePriority {
		for i, k := range keys {
			if !p.matches(k) {
				continue
			}
			cand := k + "=" + values[i]
			if p.shortOnly && len([]rune(cand)) > titleCap {
				continue
			}
			return capTitle(cand, fallback)
		}
	}
	for i, k := range keys {
		if titleExclude[strings.ToLower(k)] {
			continue
		}
		cand := k + "=" + values[i]
		if len([]rune(cand)) > titleCap {
			continue
		}
		return cand
	}
	return fallback
}

// scalarMembers walks a record's top-level members in document order and
// returns the scalar-valued ones. Non-object records return nothing.
func scalarMembers(record string) (keys, values []string) {
	dec := json.NewDecoder(strings.NewReader(record))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, nil
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, nil
		}
		valTok, err := dec.Token()
		if err != nil {
			return nil, nil
		}
		switch v := valTok.(type) {
		case json.Delim:
			// Nested value: consume it wholesale and move on.
			if err := skipNested(dec); err != nil {
				return nil, nil
			}
		case string:
			keys, values = append(keys, key), append(values, v)
		case float64:
			keys, values = append(keys, key), append(values, strconv.FormatFloat(v, 'g', -1, 64))
		case bool:
			keys, values = append(keys, key), append(values, strconv.FormatBool(v))
		case nil:
			keys, values = append(keys, key), append(values, "null")
		}
	}
	return keys, values
}

// skipNested consumes tokens until the just-opened container closes.
func skipNested(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

func init() {
	register(formatJSON, jsonChunks)
}
