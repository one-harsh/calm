// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func structuredInv(seq int64) Invocation {
	return Invocation{Seq: seq, Cwd: "/ws", WorkspaceRoot: "/ws"}
}

func TestPlanFileRead_LabelAndFormat(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		wantLatest string
		wantFormat calm.Format
	}{
		{"plain file", "notes.txt", "calm:v1:file:read:notes.txt", ""},
		{"json format hint", "data/config.json", "calm:v1:file:read:data/config.json", calm.FormatJSON},
		{"markdown format hint", "README.md", "calm:v1:file:read:README.md", calm.FormatMarkdown},
		{"dot-relative collapses", "./notes.txt", "calm:v1:file:read:notes.txt", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := PlanFileRead(structuredInv(1), ExecResult{}, c.path)
			if p.Mode != Replace || p.LatestSource != c.wantLatest || p.HistorySource != "" {
				t.Errorf("plan = %+v; want replace latest %q", p, c.wantLatest)
			}
			if p.Format != c.wantFormat {
				t.Errorf("format = %q; want %q", p.Format, c.wantFormat)
			}
		})
	}
}

// Escaping and globbing paths have no stable identity: the typed route falls
// back to the same program-equivalent shell bucket as the shell route, per
// LABELING.md §4.
func TestPlanFileRead_EscapeAndGlobCoexist(t *testing.T) {
	for _, path := range []string{"../outside.txt", "/etc/passwd", "src/*.go"} {
		t.Run(path, func(t *testing.T) {
			p := PlanFileRead(structuredInv(7), ExecResult{}, path)
			if p.Mode != Coexist || p.LatestSource != "" {
				t.Fatalf("plan = %+v; want coexist with no latest", p)
			}
			if p.HistorySource != "calm:v1:shell:cat#7" {
				t.Errorf("history = %q; want calm:v1:shell:cat#7", p.HistorySource)
			}
		})
	}
}

func TestPlanListDir_DefaultsAndLabel(t *testing.T) {
	p := PlanListDir(structuredInv(1), ExecResult{}, "src")
	if p.LatestSource != "calm:v1:file:list:src" || p.Mode != Replace {
		t.Errorf("plan = %+v; want replace latest calm:v1:file:list:src", p)
	}
	// Empty path lists the workspace root — the cwd contributes no segment,
	// matching bare `ls` on the shell route.
	p = PlanListDir(structuredInv(1), ExecResult{}, "")
	if p.LatestSource != "calm:v1:file:list" {
		t.Errorf("default-path latest = %q; want calm:v1:file:list", p.LatestSource)
	}
}

func TestPlanGrep_LabelEncodingAndFlaglessIdentity(t *testing.T) {
	p := PlanGrep(structuredInv(1), ExecResult{}, "TODO", []string{"src"})
	if p.LatestSource != "calm:v1:search:grep:TODO:src" || p.Mode != Replace {
		t.Errorf("plan = %+v; want replace latest calm:v1:search:grep:TODO:src", p)
	}

	// Reserved characters in the pattern are percent-encoded per LABELING.md §2.
	p = PlanGrep(structuredInv(1), ExecResult{}, "a:b c#d", nil)
	if p.LatestSource != "calm:v1:search:grep:a%3Ab%20c%23d" {
		t.Errorf("encoded latest = %q", p.LatestSource)
	}

	// Default scope "." contributes no segment — aliases with pattern-only shell grep.
	p = PlanGrep(structuredInv(1), ExecResult{}, "TODO", nil)
	if p.LatestSource != "calm:v1:search:grep:TODO" {
		t.Errorf("default-scope latest = %q; want calm:v1:search:grep:TODO", p.LatestSource)
	}

	// Glob scope → program-equivalent coexist bucket.
	p = PlanGrep(structuredInv(9), ExecResult{}, "TODO", []string{"*.go"})
	if p.Mode != Coexist || p.HistorySource != "calm:v1:shell:grep#9" {
		t.Errorf("glob-scope plan = %+v; want coexist shell:grep#9", p)
	}
}

func TestPlanGitStatus_DualLabels(t *testing.T) {
	p := PlanGitStatus(structuredInv(4), ExecResult{})
	if p.Mode != Dual || p.LatestSource != "calm:v1:vcs:git:status" || p.HistorySource != "calm:v1:vcs:git:status#4" {
		t.Errorf("plan = %+v; want dual status labels with #4 history", p)
	}
}

func TestPlanGitDiff_RefsPathsAndDefault(t *testing.T) {
	p := PlanGitDiff(structuredInv(2), ExecResult{}, nil, nil)
	if p.LatestSource != "calm:v1:vcs:git:diff:HEAD" || p.HistorySource != "calm:v1:vcs:git:diff:HEAD#2" {
		t.Errorf("default plan = %+v; want HEAD dual labels", p)
	}

	p = PlanGitDiff(structuredInv(2), ExecResult{}, []string{"main..feat"}, nil)
	if p.LatestSource != "calm:v1:vcs:git:diff:main..feat" {
		t.Errorf("refs latest = %q", p.LatestSource)
	}

	p = PlanGitDiff(structuredInv(2), ExecResult{}, []string{"HEAD~1"}, []string{"src/app.go"})
	if p.LatestSource != "calm:v1:vcs:git:diff:HEAD~1:--:src/app.go" {
		t.Errorf("refs+paths latest = %q", p.LatestSource)
	}

	// The separator prevents ref/pathspec aliasing: refs=[main,src] and
	// refs=[main]+paths=[src] are distinct identities.
	refList := PlanGitDiff(structuredInv(2), ExecResult{}, []string{"main", "src"}, nil)
	refPlusPath := PlanGitDiff(structuredInv(2), ExecResult{}, []string{"main"}, []string{"src"})
	if refList.LatestSource == refPlusPath.LatestSource {
		t.Errorf("ref list aliased with ref+pathspec: %q", refList.LatestSource)
	}
	if refPlusPath.LatestSource != "calm:v1:vcs:git:diff:main:--:src" {
		t.Errorf("ref+pathspec latest = %q; want calm:v1:vcs:git:diff:main:--:src", refPlusPath.LatestSource)
	}

	// Escaping pathspec → coexist under the git program bucket.
	p = PlanGitDiff(structuredInv(5), ExecResult{}, []string{"HEAD"}, []string{"../other/repo.go"})
	if p.Mode != Coexist || p.HistorySource != "calm:v1:shell:git#5" {
		t.Errorf("escape plan = %+v; want coexist shell:git#5", p)
	}
}

// Events carry the typed tool's own name — never calm_run_command — and the
// git constructors fire git_operation exactly like the shell route.
func TestTypedPlans_EventToolNames(t *testing.T) {
	p := PlanGrep(structuredInv(3), ExecResult{}, "x", nil)
	evs := FinalizeEvents(p, []WriteOutcome{{Source: p.LatestSource, Persisted: true}})
	if len(evs) != 1 || evs[0].Data[keyToolName] != "calm_grep" {
		t.Fatalf("grep events = %+v; want one tool_invocation from calm_grep", evs)
	}

	p = PlanGitDiff(structuredInv(3), ExecResult{ExitCode: 128, Stderr: "fatal: bad ref"}, []string{"nope"}, nil)
	evs = FinalizeEvents(p, nil)
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if len(evs) != 3 {
		t.Fatalf("git diff error events = %v; want tool_invocation+error_observed+git_operation", types)
	}
	if evs[1].Data[keySource] != "calm_git_diff" {
		t.Errorf("error_observed source = %v; want calm_git_diff", evs[1].Data[keySource])
	}
	if evs[2].Data[keySubcommand] != "diff" {
		t.Errorf("git_operation subcommand = %v; want diff", evs[2].Data[keySubcommand])
	}
}

// The shell route is untouched: DerivePlan still stamps calm_run_command.
func TestDerivePlan_ToolNameRegression(t *testing.T) {
	p, err := DerivePlan(Invocation{Seq: 1, Command: "echo hi", Cwd: "/ws", WorkspaceRoot: "/ws"}, ExecResult{})
	if err != nil {
		t.Fatalf("DerivePlan: %v", err)
	}
	evs := FinalizeEvents(p, nil)
	if evs[0].Data[keyToolName] != "calm_run_command" {
		t.Errorf("tool_name = %v; want calm_run_command", evs[0].Data[keyToolName])
	}
}
