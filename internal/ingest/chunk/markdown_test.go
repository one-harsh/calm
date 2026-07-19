// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"strings"
	"testing"
)

func TestSplit_MarkdownSplitsAtHeadings(t *testing.T) {
	content := "intro line\n\n# First\nbody one\n\n## Second\nbody two\n"
	got, eff := Split("src", content, "", "")
	if eff != formatMarkdown {
		t.Errorf("effective format = %q; want detected markdown", eff)
	}
	if len(got) != 3 {
		t.Fatalf("got %d chunks; want 3: %+v", len(got), got)
	}
	if got[0].Title != "src" {
		t.Errorf("chunk[0].Title = %q; want preamble titled source", got[0].Title)
	}
	if got[1].Title != "First" || got[2].Title != "First > Second" {
		t.Errorf("heading titles = %q,%q; want First, First > Second breadcrumb", got[1].Title, got[2].Title)
	}
	for _, c := range got {
		if c.ContentType != contentTypeProse {
			t.Errorf("content_type = %q; want prose default", c.ContentType)
		}
	}
}

func TestMarkdownChunks_ReadmeFixture(t *testing.T) {
	got := markdownChunks("readme", fixture(t, "readme-excerpt.md"), contentTypeProse)

	wantTitles := []string{
		"readme",                  // preamble
		"Install",                 // heading + intro
		"Install > macOS",         // prose before the fence
		"Install > macOS",         // fenced sh block (code)
		"Install > macOS",         // prose between fence and indented block
		"Install > macOS",         // indented code block (code)
		"Install > Linux",         // heading + list
		"Install > Configuration", // setext heading + prose
		"Install > Configuration", // fenced yaml block (code)
		"Install > Configuration", // trailing prose + ordered list
	}
	if len(got) != len(wantTitles) {
		for i, c := range got {
			t.Logf("chunk[%d] %s title=%q content=%.60q", i, c.ContentType, c.Title, c.Content)
		}
		t.Fatalf("got %d chunks; want %d", len(got), len(wantTitles))
	}
	for i, w := range wantTitles {
		if got[i].Title != w {
			t.Errorf("chunk[%d].Title = %q; want %q", i, got[i].Title, w)
		}
	}

	codeIdx := map[int]bool{3: true, 5: true, 8: true}
	for i, c := range got {
		want := contentTypeProse
		if codeIdx[i] {
			want = contentTypeCode
		}
		if c.ContentType != want {
			t.Errorf("chunk[%d].ContentType = %q; want %q", i, c.ContentType, want)
		}
	}

	// Exact source-byte spans, fence markers included.
	wantFence := "```sh\n# this hash line must never become a heading\nbrew install go-task\ntask build\n```"
	if got[3].Content != wantFence {
		t.Errorf("fenced chunk content = %q; want exact span %q", got[3].Content, wantFence)
	}
	wantIndented := "    ./bin/calm --version\n    # indented code block, also never a heading"
	if got[5].Content != wantIndented {
		t.Errorf("indented chunk content = %q; want exact span %q", got[5].Content, wantIndented)
	}

	// Exactly-once partitioning: excised code never re-appears in prose, and
	// hash lines inside code never become titles.
	if strings.Contains(got[2].Content, "brew install") || strings.Contains(got[4].Content, "./bin/calm") {
		t.Errorf("code bytes leaked into surrounding prose chunks")
	}
	for _, c := range got {
		if strings.Contains(c.Title, "hash line") || strings.Contains(c.Title, "indented code block") {
			t.Errorf("code-interior line surfaced as a title: %q", c.Title)
		}
	}
}

func TestMarkdownChunks_Edges(t *testing.T) {
	// No headings, no code: one chunk titled by the source.
	got := markdownChunks("src", "plain prose only\n", contentTypeProse)
	if len(got) != 1 || got[0].Title != "src" {
		t.Fatalf("plain doc = %+v; want one source-titled chunk", got)
	}

	// Unclosed fence at EOF still excises cleanly to the end.
	got = markdownChunks("src", "intro\n\n```go\ncode := 1\n", contentTypeProse)
	if len(got) != 2 {
		t.Fatalf("unclosed fence: got %d chunks; want prose + code: %+v", len(got), got)
	}
	if got[1].ContentType != contentTypeCode || !strings.Contains(got[1].Content, "code := 1") {
		t.Errorf("unclosed fence chunk = %+v; want code chunk with body", got[1])
	}

	// Deeper heading after shallower pops correctly: h2 under h1, then new h1.
	md := "# A\n\nbody a\n\n## B\n\nbody b\n\n# C\n\nbody c\n"
	got = markdownChunks("src", md, contentTypeProse)
	titles := make([]string, len(got))
	for i, c := range got {
		titles[i] = c.Title
	}
	want := []string{"A", "A > B", "C"}
	for i, w := range want {
		if titles[i] != w {
			t.Errorf("titles = %v; want %v", titles, want)
			break
		}
	}
}
