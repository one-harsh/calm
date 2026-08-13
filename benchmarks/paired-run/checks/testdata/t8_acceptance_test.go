// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/db"
	"github.com/one-harsh/calm/internal/ingest/chunk"
)

// Oracle for capture sectioning. Each case runs a representative capture
// through the pipeline the way production composes it — the adapter's deriver
// decides the capture's identity and its content hints, and the sectioning
// stage turns the captured bytes into titled sections — then asserts the three
// promises: a git diff sections per file per hunk with the hunk header naming
// the section, code sections land on declaration boundaries with headers that
// name what they contain, and capture identities are untouched by any of it.
//
// Fixtures live outside the module tree; the acceptance checker exports
// T8_FIXTURES with their directory.

const t8FixtureEnv = "T8_FIXTURES"

func t8Fixture(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv(t8FixtureEnv)
	if dir == "" {
		t.Fatalf("%s must point at the oracle's fixture directory (set by the acceptance checker)", t8FixtureEnv)
	}
	path := filepath.Join(dir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %q: %v", path, err)
	}
	return string(b)
}

// t8Sections sections one capture: the plan carries the source identity and
// the content hints the deriver chose, and the sectioning stage consumes both.
func t8Sections(t *testing.T, plan extract.Plan, content string) []db.Chunk {
	t.Helper()
	source := plan.LatestSource
	if source == "" {
		source = plan.HistorySource
	}
	sections, _ := chunk.Split(source, content, string(plan.Format), plan.ContentType)
	return sections
}

// t8Describe renders the produced sections for a failure message.
func t8Describe(sections []db.Chunk) string {
	var b strings.Builder
	fmt.Fprintf(&b, "produced %d section(s):\n", len(sections))
	for i, s := range sections {
		fmt.Fprintf(&b, "  [%d] title=%q (%d bytes)\n", i, s.Title, len(s.Content))
	}
	return b.String()
}

func t8HunkHeaderCount(content string) int {
	n := 0
	for line := range strings.SplitSeq(content, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			n++
		}
	}
	return n
}

// A multi-file, multi-hunk git diff sections once per hunk, and each section's
// header names the file and the hunk it covers.
func TestT8Oracle_GitDiffSectionsPerFilePerHunk(t *testing.T) {
	t.Parallel()
	content := t8Fixture(t, "t8_multifile.diff")
	plan, err := extract.DerivePlan(
		extract.Invocation{Seq: 7, Command: "git diff", Cwd: "/w", WorkspaceRoot: "/w"},
		extract.ExecResult{Stdout: content})
	if err != nil {
		t.Fatalf("deriving the capture plan for `git diff`: %v", err)
	}
	sections := t8Sections(t, plan, content)

	hunks := []struct{ file, header, marker string }{
		{"a.go", "-1,5 +1,6", "Alpha marker line one."},
		{"a.go", "-18,5 +19,5", "zeta marker line two."},
		{"b.go", "-3,5 +3,6", "beta marker line three."},
		{"b.go", "-30,3 +31,4", "omega marker line four."},
	}

	if len(sections) < len(hunks) {
		t.Fatalf("a diff spanning 2 files and %d hunks produced %d section(s); a git diff sections per file per hunk\n%s",
			len(hunks), len(sections), t8Describe(sections))
	}

	for _, h := range hunks {
		matched := false
		for _, s := range sections {
			if strings.Contains(s.Title, h.file) && strings.Contains(s.Title, h.header) {
				matched = true
				if !strings.Contains(s.Content, h.marker) {
					t.Errorf("section %q does not carry its own hunk's content (%q)", s.Title, h.marker)
				}
				break
			}
		}
		if !matched {
			t.Errorf("no section header names file %q and hunk %q — the hunk header names the section\n%s",
				h.file, h.header, t8Describe(sections))
		}
	}

	for _, s := range sections {
		if n := t8HunkHeaderCount(s.Content); n > 1 {
			t.Errorf("section %q spans %d hunks; a diff sections per hunk", s.Title, n)
		}
	}
}

// A Go source capture sections on declaration boundaries, and each section's
// header names the declaration that section actually contains.
func TestT8Oracle_CodeSectionsOnDeclarationBoundaries(t *testing.T) {
	t.Parallel()
	content := t8Fixture(t, "t8_sample_go.txt")
	plan := extract.PlanFileRead(
		extract.Invocation{Seq: 3, Cwd: "/w", WorkspaceRoot: "/w"},
		extract.ExecResult{Stdout: content}, "/w/pkg/sample.go")
	sections := t8Sections(t, plan, content)

	decls := []struct{ name, marker string }{
		{"Widget", "Size int"},
		{"Describe", "fmt.Sprintf"},
		{"Assemble", "Widget{Name: trimmed"},
	}

	if len(sections) < 2 {
		t.Fatalf("a Go source file with %d top-level declarations produced %d section(s); code sections on declaration boundaries\n%s",
			len(decls), len(sections), t8Describe(sections))
	}

	for _, d := range decls {
		named := false
		for _, s := range sections {
			if strings.Contains(s.Title, d.name) && strings.Contains(s.Content, d.marker) {
				named = true
				break
			}
		}
		if !named {
			t.Errorf("no section both names %q in its header and contains that declaration — the header must name what the section contains\n%s",
				d.name, t8Describe(sections))
		}
	}

	for _, s := range sections {
		carried := make([]string, 0, len(decls))
		for _, d := range decls {
			if strings.Contains(s.Content, d.marker) {
				carried = append(carried, d.name)
			}
		}
		if len(carried) > 1 {
			t.Errorf("section %q carries declarations %v; boundaries land on declarations", s.Title, carried)
		}
	}
}

// Sectioning changes boundaries and titles inside a capture, never the
// capture's identity: the deriver still mints the same source labels, capture
// mode, and content type for the same invocations.
func TestT8Oracle_CaptureIdentitiesUnchanged(t *testing.T) {
	t.Parallel()
	diff := t8Fixture(t, "t8_multifile.diff")
	src := t8Fixture(t, "t8_sample_go.txt")

	shellDiff, err := extract.DerivePlan(
		extract.Invocation{Seq: 7, Command: "git diff", Cwd: "/w", WorkspaceRoot: "/w"},
		extract.ExecResult{Stdout: diff})
	if err != nil {
		t.Fatalf("deriving the capture plan for `git diff`: %v", err)
	}
	typedDiff := extract.PlanGitDiff(
		extract.Invocation{Seq: 7, Cwd: "/w", WorkspaceRoot: "/w"},
		extract.ExecResult{Stdout: diff}, nil, nil, false)
	typedRead := extract.PlanFileRead(
		extract.Invocation{Seq: 3, Cwd: "/w", WorkspaceRoot: "/w"},
		extract.ExecResult{Stdout: src}, "/w/pkg/sample.go")

	cases := []struct {
		name        string
		got         extract.Plan
		mode        extract.CaptureMode
		latest      string
		history     string
		contentType string
	}{
		{
			name: "shell git diff", got: shellDiff, mode: extract.Dual,
			latest: "calm:v1:vcs:git:diff:HEAD", history: "calm:v1:vcs:git:diff:HEAD#7", contentType: "code",
		},
		{
			name: "typed git diff", got: typedDiff, mode: extract.Dual,
			latest: "calm:v1:vcs:git:diff:HEAD", history: "calm:v1:vcs:git:diff:HEAD#7", contentType: "code",
		},
		{
			name: "typed file read", got: typedRead, mode: extract.Replace,
			latest: "calm:v1:file:read:pkg/sample.go", history: "", contentType: "code",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.got.Mode != c.mode {
				t.Errorf("capture mode = %s; want %s", c.got.Mode, c.mode)
			}
			if c.got.LatestSource != c.latest {
				t.Errorf("latest source = %q; want %q", c.got.LatestSource, c.latest)
			}
			if c.got.HistorySource != c.history {
				t.Errorf("history source = %q; want %q", c.got.HistorySource, c.history)
			}
			if c.got.ContentType != c.contentType {
				t.Errorf("content type = %q; want %q", c.got.ContentType, c.contentType)
			}
		})
	}
}
