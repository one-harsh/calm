// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"path/filepath"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

// Typed plan constructors for the structured inspection tools (DESIGN.md §3).
// Each routes explicit arguments into the same classification cores as the
// shell rules, so a typed call and its shell equivalent cannot diverge on
// labels. All are infallible: handlers validate arguments before executing,
// and operands without a stable identity (escapes, globs) fall back to the
// program-equivalent coexist bucket per LABELING.md §4.

const (
	toolNameReadFile  = "calm_read_file"
	toolNameListDir   = "calm_list_dir"
	toolNameGrep      = "calm_grep"
	toolNameGitStatus = "calm_git_status"
	toolNameGitDiff   = "calm_git_diff"
)

func PlanFileRead(inv Invocation, r ExecResult, path string) Plan {
	facts := typedFacts(toolNameReadFile, "cat", "", false, inv, r)
	cl, ok := classifyFileRead([]string{path}, inv)
	if !ok {
		return coexistPlan(labelID{domain: domainShell, ident: []string{"cat"}}, inv, facts)
	}
	cl.format = formatForPath(path)
	return assemble(cl, inv, facts)
}

func PlanListDir(inv Invocation, r ExecResult, path string) Plan {
	facts := typedFacts(toolNameListDir, "ls", "", false, inv, r)
	if path == "" {
		path = "."
	}
	cl, ok := classifyList([]string{path}, inv)
	if !ok {
		return coexistPlan(labelID{domain: domainShell, ident: []string{"ls"}}, inv, facts)
	}
	return assemble(cl, inv, facts)
}

// TODO: revisit flag-excluded grep identity only on dogfood signal — if
// agents observably alternate flag variants (case_insensitive / include) on
// one pattern+scope within a session and get confused by the latest-wins
// overwrite (LABELING.md §4). The escape hatch of choice is dual mode (keeps
// one identity, makes overwrites non-destructive history) rather than flags
// as context segments (fragments the identity). Until that signal exists,
// this is settled behavior, not a gap.
func PlanGrep(inv Invocation, r ExecResult, pattern string, scopes []string) Plan {
	facts := typedFacts(toolNameGrep, "grep", "", false, inv, r)
	if len(scopes) == 0 {
		scopes = []string{"."}
	}
	cl, ok := classifyGrep(pattern, scopes, inv)
	if !ok {
		return coexistPlan(labelID{domain: domainShell, ident: []string{"grep"}}, inv, facts)
	}
	return assemble(cl, inv, facts)
}

func PlanGitStatus(inv Invocation, r ExecResult) Plan {
	facts := typedFacts(toolNameGitStatus, "git status", "status", true, inv, r)
	cl := classification{
		id:      labelID{domain: domainVCS, verb: "git", context: []string{"status"}},
		mode:    Dual,
		content: gitContent("status"),
	}
	return assemble(cl, inv, facts)
}

// PlanGitDiff keeps refs and pathspecs distinct: refs are revisions, not
// workspace paths, so they enter the identity verbatim (percent-encoded by
// the grammar); pathspecs sit after a literal `--` separator segment (so a
// ref list and a ref+pathspec split can never alias) and resolve
// workspace-relative — an escaping or globbing pathspec drops the whole
// invocation to coexist.
func PlanGitDiff(inv Invocation, r ExecResult, refs, paths []string, staged bool) Plan {
	facts := typedFacts(toolNameGitDiff, "git diff", "diff", true, inv, r)
	id := labelID{domain: domainVCS, verb: "git", context: []string{"diff"}}
	// --staged gets its own identity segment so an index diff never aliases a
	// worktree diff of the same refs; collision-safe since refs can't start with '-'.
	if staged {
		id.ident = append(id.ident, "--staged")
	}
	id.ident = append(id.ident, refs...)
	if len(paths) > 0 {
		ident, ok := relIdents(paths, inv)
		if !ok {
			return coexistPlan(labelID{domain: domainShell, ident: []string{"git"}}, inv, facts)
		}
		id.ident = append(id.ident, "--")
		id.ident = append(id.ident, ident...)
	}
	if len(id.ident) == 0 {
		id.ident = []string{"HEAD"}
	}
	cl := classification{id: id, mode: Dual, content: gitContent("diff")}
	return assemble(cl, inv, facts)
}

// typedFacts mirrors DerivePlan's fact derivation for a typed invocation: the
// summary is arg-free by construction (LABELING.md §7 secret hygiene).
func typedFacts(tool, summary, subcommand string, isGit bool, inv Invocation, r ExecResult) eventFacts {
	f := eventFacts{
		toolName:       tool,
		commandSummary: summary,
		subcommand:     subcommand,
		exitCode:       r.ExitCode,
		timedOut:       r.TimedOut,
		isGit:          isGit,
		invocationID:   inv.Seq,
	}
	fillErrorFacts(&f, r)
	return f
}

// formatForPath is a conservative extension-keyed Format hint — set only where
// the mapping is unambiguous; everything else stays unhinted.
func formatForPath(path string) calm.Format {
	switch strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".") {
	case "json":
		return calm.FormatJSON
	case "md", "markdown":
		return calm.FormatMarkdown
	case "csv":
		return calm.FormatCSV
	case "tsv":
		return calm.FormatTSV
	default:
		return ""
	}
}
