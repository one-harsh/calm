// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestDetectFormat_Fixtures(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"readme-excerpt.md", formatMarkdown},
		{"eval-results.jsonl", formatJSON},
		{"api-response.json", formatJSON},
		{"api-response-array.json", formatJSON},
		// The hint-only tiers must never auto-detect: these all land in text.
		{"train-multirank.log", formatText},
		{"python-traceback.txt", formatText},
		{"go-panic.txt", formatText},
		{"results.csv", formatText},
		{"scrape.prom", formatText},
	}
	for _, c := range cases {
		if got := detectFormat(fixture(t, c.name)); got != c.want {
			t.Errorf("detectFormat(%s) = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestDetectFormat_Edges(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"heading needs a space", "#comment\nplain", formatText},
		{"heading outside fence", "intro\n# Title\nbody", formatMarkdown},
		{"hash inside unclosed fence stays text", "```\n# not a heading", formatText},
		{"complete fence pair alone", "```\ncode\n```", formatMarkdown},
		{"one list line is prose", "- lonely bullet\nprose", formatText},
		{"two list lines are structure", "- one\n- two", formatMarkdown},
		{"scalar lines are not jsonl", "42\ntrue\nnull", formatText},
		{"two-record jsonl", "{\"a\":1}\n{\"b\":2}", formatJSON},
		{"json then garbage is text", "{\"a\":1}\ngarbage", formatText},
		// Full-stream validation: a bad line past any probe window must not
		// classify json — the detected format reaches logs and correlation
		// meta, so it must match what the chunker will actually do.
		{"late garbage line is text", "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\ngarbage", formatText},
		{"empty object is text", "{}", formatText},
		{"empty array is text", "[ ]", formatText},
		{"whole-doc object", "{\n  \"a\": 1\n}", formatJSON},
		{"whole-doc array", "[1, 2, 3]", formatJSON},
		{"exposition comments are not headings", "# HELP up 1 if up.\n# TYPE up gauge\nup 1", formatText},
	}
	for _, c := range cases {
		if got := detectFormat(c.content); got != c.want {
			t.Errorf("%s: detectFormat = %q; want %q", c.name, got, c.want)
		}
	}
}
