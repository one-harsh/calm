// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"strings"
	"testing"
)

func TestCSVChunks_Fixture(t *testing.T) {
	got := csvChunks("results", fixture(t, "results.csv"), contentTypeProse)
	if len(got) != 1 {
		t.Fatalf("small csv should pack into one chunk; got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Title != "rows 1-8" {
		t.Errorf("title = %q; want rows 1-8", c.Title)
	}
	if !strings.HasPrefix(c.Content, "case,verdict,latency_ms,notes\n") {
		t.Errorf("chunk not header-prefixed: %.60q", c.Content)
	}
	// The quoted field's embedded newline stays inside its row.
	if !strings.Contains(c.Content, "\"wrong tool selected:\ncatalog_search instead of inventory_lookup\"") {
		t.Errorf("quoted embedded newline mangled:\n%s", c.Content)
	}
}

func TestCSVChunks_SplitsAndRepeatsHeader(t *testing.T) {
	var b strings.Builder
	b.WriteString("id,payload\n")
	for i := 1; i <= 40; i++ {
		b.WriteString(strings.Repeat("x", 100))
		b.WriteString(",row\n")
	}
	got := csvChunks("big", b.String(), contentTypeProse)
	if len(got) < 2 {
		t.Fatalf("4KB of rows must split at the 2KB target; got %d chunks", len(got))
	}
	for i, c := range got {
		if !strings.HasPrefix(c.Content, "id,payload\n") {
			t.Errorf("chunk[%d] missing repeated header", i)
		}
		if len(c.Content) > chunkMaxBytes {
			t.Errorf("chunk[%d] exceeds max bytes: %d", i, len(c.Content))
		}
	}
	if got[0].Title != "rows 1-19" && !strings.HasPrefix(got[0].Title, "rows 1-") {
		t.Errorf("first chunk title = %q; want a rows 1-N range", got[0].Title)
	}
}

func TestTSVChunks_Delimiter(t *testing.T) {
	got := tsvChunks("t", "a\tb\n1\t2\n3\t4\n", contentTypeProse)
	if len(got) != 1 || !strings.HasPrefix(got[0].Content, "a\tb\n1\t2") {
		t.Fatalf("tsv = %+v; want header-prefixed tab rows", got)
	}
}

func TestCSVChunks_MalformedTailPreserved(t *testing.T) {
	content := "a,b\n1,2\n\"unclosed quote\n"
	got := csvChunks("src", content, contentTypeProse)
	joined := ""
	for _, c := range got {
		joined += c.Content + "\n"
	}
	if !strings.Contains(joined, "unclosed quote") {
		t.Errorf("malformed tail dropped: %+v", got)
	}
}
