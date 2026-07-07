// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"github.com/pmezard/go-difflib/difflib"
)

// Typed plan constructors for the structured editing tools (DESIGN.md AD04).
// Dual capture across two bases: the latest label is the READ identity —
// byte-identical to what PlanFileRead derives for the same path, so
// read-after-edit dedup holds — and the history label preserves each
// invocation's post-modification snapshot under the edit verb.

const (
	toolNameEditFile  = "calm_edit_file"
	toolNameWriteFile = "calm_write_file"
)

func PlanFileEdit(inv Invocation, r ExecResult, path, oldContent, newContent string) Plan {
	return planFileMutation(toolNameEditFile, "sed", inv, r, path, OperationEdit, oldContent, newContent)
}

func PlanFileWrite(inv Invocation, r ExecResult, path string, op FileOperation, oldContent, newContent string) Plan {
	return planFileMutation(toolNameWriteFile, "tee", inv, r, path, op, oldContent, newContent)
}

func planFileMutation(tool, program string, inv Invocation, r ExecResult, path string, op FileOperation, oldContent, newContent string) Plan {
	facts := typedFacts(tool, program, "", false, inv, r)

	ident, ok := relIdent(path, inv)
	eventPath := path
	if ok {
		if len(ident) == 1 {
			eventPath = ident[0]
		} else {
			eventPath = "." // cwd-equivalent path resolves to no segment
		}
	}
	facts.fileTouched = fileTouchedFor(eventPath, op, oldContent, newContent, ok)

	if !ok {
		return coexistPlan(labelID{domain: domainShell, ident: []string{program}}, inv, facts)
	}

	// Dual capture across two bases — assemble() is single-base, so the pair
	// is built directly. The latest call MUST stay identical to the Replace
	// arm of assemble for the read identity (reserve 0 included).
	plan := Plan{
		Mode:        Dual,
		ContentType: contentForPath(path),
		Format:      formatForPath(path),
		base:        facts,
	}
	plan.LatestSource = buildBase(labelID{domain: domainFile, verb: "read", ident: ident}, inv, 0)
	plan.HistorySource = buildBase(labelID{domain: domainFile, verb: "edit", ident: ident}, inv, maxSeqSuffix) + seqSuffix(inv.Seq)
	return plan
}

// fileTouchedFor derives the sanitized event payload. On the escape/glob
// fallback (stable=false) the event path is the agent-supplied argument put
// through the text sanitizer — the no-raw-absolute-paths clause binds label
// metadata, not events (LABELING.md §7).
func fileTouchedFor(path string, op FileOperation, oldContent, newContent string, stable bool) *fileTouchedFacts {
	if !stable {
		path = sanitizeText(path)
	}
	diff, truncated := sanitizeDiff(unifiedDiff(path, oldContent, newContent))
	return &fileTouchedFacts{path: path, operation: op, diff: diff, diffTruncated: truncated}
}

// unifiedDiff is uniform across operations: a create diffs from empty content
// (all additions) rather than special-casing /dev/null — the operation field
// disambiguates, per AD04's no-special-case-per-operation stance.
func unifiedDiff(path, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(oldContent),
		B:        difflib.SplitLines(newContent),
		FromFile: "a/" + path,
		ToFile:   "b/" + path,
		Context:  3,
	})
	if err != nil {
		return ""
	}
	return text
}
