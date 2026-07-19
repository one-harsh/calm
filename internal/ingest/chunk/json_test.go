// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"fmt"
	"strings"
	"testing"
)

// Small records pack toward the target size (a JSON-formatted log must not
// explode into per-line chunks); the fixture's five records fit one chunk,
// titled by record range.
func TestJSONChunks_JSONLFixturePacks(t *testing.T) {
	got := jsonChunks("results", fixture(t, "eval-results.jsonl"), contentTypeProse)
	if len(got) != 1 {
		t.Fatalf("got %d chunks; want the five small records packed into 1: %+v", len(got), got)
	}
	if got[0].Title != "records 1-5" {
		t.Errorf("packed title = %q; want records 1-5", got[0].Title)
	}
	// Verbatim record lines, embedded markdown completion intact.
	if !strings.Contains(got[0].Content, "```python") ||
		!strings.Contains(got[0].Content, "{\"id\": \"eval-001\"") {
		t.Errorf("packed content not the verbatim lines: %.80q", got[0].Content)
	}
}

// Records are atomic: one too large for the open chunk moves wholly into the
// next, an oversized record stands alone, and a standalone record keeps its
// discriminating title.
func TestJSONChunks_RecordAtomicity(t *testing.T) {
	small1 := `{"id": "a", "v": 1}`
	big := fmt.Sprintf(`{"id": "big-record", "completion": %q}`, strings.Repeat("x", chunkTargetBytes))
	small2 := `{"id": "b", "v": 2}`
	got := jsonChunks("src", small1+"\n"+big+"\n"+small2, contentTypeProse)
	if len(got) != 3 {
		t.Fatalf("got %d chunks; want small / big / small: %+v", len(got), got)
	}
	if got[0].Title != "id=a" || got[2].Title != "id=b" {
		t.Errorf("standalone titles = %q,%q; want discriminating fields", got[0].Title, got[2].Title)
	}
	if got[1].Title != "id=big-record" {
		t.Errorf("oversized record title = %q; want its own discriminating title", got[1].Title)
	}
	if strings.Count(got[1].Content, "\n") != 0 {
		t.Errorf("oversized record was split or merged")
	}

	// A flood of records yields packed chunks, each record intact on exactly
	// one side of every boundary.
	var sb strings.Builder
	for i := range 400 {
		fmt.Fprintf(&sb, "{\"seq\": %d, \"msg\": \"event number %d with some padding\"}\n", i, i)
	}
	got = jsonChunks("flood", sb.String(), contentTypeProse)
	if len(got) < 5 || len(got) > 40 {
		t.Fatalf("flood packed into %d chunks; want passage-sized packing", len(got))
	}
	for i, c := range got {
		for line := range strings.SplitSeq(c.Content, "\n") {
			if !strings.HasPrefix(line, "{\"seq\":") || !strings.HasSuffix(line, "}") {
				t.Fatalf("chunk[%d] holds a split record: %q", i, line)
			}
		}
	}
}

func TestJSONChunks_TopLevelArrayFixturePacks(t *testing.T) {
	got := jsonChunks("sessions", fixture(t, "api-response-array.json"), contentTypeProse)
	if len(got) != 1 || got[0].Title != "records 1-3" {
		t.Fatalf("array fixture = %+v; want three small records packed", got)
	}
	if !strings.Contains(got[0].Content, "{\"id\": \"sess-9001\"") {
		t.Errorf("element content not verbatim: %.80q", got[0].Content)
	}
}

func TestRecordTitle_Priority(t *testing.T) {
	cases := []struct {
		record string
		want   string
	}{
		{`{"id": "eval-001", "task": "x"}`, "id=eval-001"},
		{`{"score": 0.5, "case": "sku-17"}`, "case=sku-17"},                    // identity beats document order
		{`{"score": 0.77, "prompt": "p"}`, "score=0.77"},                       // guarded fallback: first non-excluded scalar
		{`{"nested": {"id": "inner"}, "task": "t"}`, "record 7"},               // task excluded from priority and fallback
		{`[1, 2]`, "record 7"},                                                 // non-object: positional fallback
		{`{"id": "x", "msg": "connection refused"}`, "msg=connection refused"}, // short descriptive beats id
		{`{"id": "x", "msg": "` + strings.Repeat("y", 70) + `"}`, "id=x"},      // long descriptive skipped, id wins
		{`{"sample_id": "s-9"}`, "sample_id=s-9"},                              // _id suffix convention
		{`{"name": "foo", "id": "x"}`, "name=foo"},                             // naming beats id
		{`{"level": "error", "trace_id": "t1"}`, "trace_id=t1"},                // identity (suffix) beats categorical
		{`{"level": "error", "ts": 1699887766}`, "level=error"},                // categorical beats guarded fallback; ts excluded
		{`{"task": "t", "model": "m"}`, "record 7"},                            // all excluded -> numbered
	}
	for _, c := range cases {
		if got := recordTitle(c.record, 7); got != c.want {
			t.Errorf("recordTitle(%s) = %q; want %q", c.record, got, c.want)
		}
	}
}

func TestJSONChunks_SingleObjectFixture(t *testing.T) {
	got := jsonChunks("health", fixture(t, "api-response.json"), contentTypeProse)
	wantTitles := []string{"status", "checks", "uptime_seconds", "version", "notes"}
	if len(got) != len(wantTitles) {
		t.Fatalf("got %d chunks; want one per top-level member (%d): %+v", len(got), len(wantTitles), got)
	}
	for i, w := range wantTitles {
		if got[i].Title != w {
			t.Errorf("chunk[%d].Title = %q; want %q", i, got[i].Title, w)
		}
	}
	if got[0].Content != "\"status\": \"degraded\"" {
		t.Errorf("member content = %q; want self-describing pair", got[0].Content)
	}
	if !strings.Contains(got[1].Content, "pg_textsearch") {
		t.Errorf("nested member lost its value bytes: %q", got[1].Content)
	}
}

func TestJSONChunks_Fallbacks(t *testing.T) {
	got := jsonChunks("src", "42", contentTypeProse)
	if len(got) != 1 || got[0].Content != "42" {
		t.Fatalf("scalar fallback = %+v; want one text chunk", got)
	}
	corrupt := "{\"a\":1}\n{\"b\":2}\nnot json\n{\"c\":3}"
	got = jsonChunks("src", corrupt, contentTypeProse)
	joined := ""
	for _, c := range got {
		joined += c.Content + "\n"
	}
	if !strings.Contains(joined, "not json") || !strings.Contains(joined, "{\"c\":3}") {
		t.Errorf("fallback dropped content: %+v", got)
	}
}
