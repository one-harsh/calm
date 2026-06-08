// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"path/filepath"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

const (
	contentTypeCode  = "code"
	contentTypeProse = "prose"
)

type classification struct {
	id      labelID
	mode    CaptureMode
	content string
	format  calm.Format
}

// build returns false when the command has no stable identity (e.g. a path escaping
// the workspace), so the caller falls back to coexist.
type rule struct {
	match func(c cmd) bool
	build func(c cmd, inv Invocation) (classification, bool)
}

var (
	fileReadProgs = map[string]bool{"cat": true, "head": true, "tail": true, "less": true, "more": true, "bat": true}
	grepProgs     = map[string]bool{"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true}
	gitSubs       = map[string]bool{"diff": true, "status": true, "log": true, "show": true}

	codeExts = map[string]bool{
		"go": true, "py": true, "js": true, "ts": true, "tsx": true, "jsx": true,
		"java": true, "c": true, "h": true, "cpp": true, "hpp": true, "cc": true,
		"rs": true, "rb": true, "php": true, "cs": true, "kt": true, "swift": true,
		"scala": true, "sh": true, "sql": true, "proto": true,
	}
)

// First match wins. Build/test runners (go, pytest, make, …) and anything unrecognized
// fall through to coexist — their output is invocation history, not a content identity
// derivable from the command without per-tool knowledge.
var registry = []rule{
	{match: func(c cmd) bool { return c.program == "git" && gitSubs[c.subcommand] }, build: buildGit},
	{match: progIn(fileReadProgs), build: buildFileRead},
	{match: progIs("ls"), build: buildList},
	{match: progIn(grepProgs), build: buildGrep},
	{match: progIs("find"), build: buildFind},
}

func progIn(set map[string]bool) func(cmd) bool { return func(c cmd) bool { return set[c.program] } }

func progIs(name string) func(cmd) bool { return func(c cmd) bool { return c.program == name } }

func classify(c cmd, inv Invocation) (classification, bool) {
	if c.pipeline || c.hasOutputFlag() {
		return classification{}, false
	}
	for _, r := range registry {
		if r.match(c) {
			return r.build(c, inv)
		}
	}
	return classification{}, false
}

func buildFileRead(c cmd, inv Invocation) (classification, bool) {
	paths := c.pathArgs()
	if len(paths) == 0 {
		return classification{}, false
	}
	ident, ok := relIdents(paths, inv)
	if !ok || len(ident) == 0 {
		return classification{}, false
	}
	id := labelID{domain: domainFile, verb: "read", ident: ident}
	return classification{id: id, mode: Replace, content: contentForPath(paths[0])}, true
}

func buildList(c cmd, inv Invocation) (classification, bool) {
	ident, ok := relIdents(operandsOrCwd(c), inv)
	if !ok {
		return classification{}, false
	}
	id := labelID{domain: domainFile, verb: "list", ident: ident}
	return classification{id: id, mode: Replace, content: contentTypeProse}, true
}

func buildFind(c cmd, inv Invocation) (classification, bool) {
	ident, ok := relIdents(operandsOrCwd(c), inv)
	if !ok {
		return classification{}, false
	}
	id := labelID{domain: domainSearch, verb: "find", ident: ident}
	return classification{id: id, mode: Replace, content: contentTypeProse}, true
}

// operandsOrCwd defaults to "." (the cwd) when a listing command has no path operand —
// bare `ls`/`find` list the cwd, so without this two cwds would collide on one label.
func operandsOrCwd(c cmd) []string {
	if paths := c.pathArgs(); len(paths) > 0 {
		return paths
	}
	return []string{"."}
}

func buildGrep(c cmd, inv Invocation) (classification, bool) {
	paths := c.pathArgs()
	if len(paths) == 0 {
		return classification{}, false
	}
	id := labelID{domain: domainSearch, verb: "grep", context: []string{paths[0]}}
	if len(paths) > 1 {
		ident, ok := relIdents(paths[1:], inv)
		if !ok {
			return classification{}, false
		}
		id.ident = ident
	}
	return classification{id: id, mode: Replace, content: contentTypeProse}, true
}

func buildGit(c cmd, inv Invocation) (classification, bool) {
	id := labelID{domain: domainVCS, verb: "git", context: []string{c.subcommand}}

	// Git output is never replace-safe: refs (HEAD, branches, ranges like main..feat)
	// move, so the same command yields different output over time — always dual. Every
	// operand routes through wsRel, so an absolute or escaping path coexists rather than
	// leaking into the label.
	var operands []string
	for _, a := range c.args {
		if a != "--" { // the rev/pathspec separator, not an operand
			operands = append(operands, a)
		}
	}
	if len(operands) == 0 {
		if c.subcommand != "status" {
			id.ident = []string{"HEAD"}
		}
		return classification{id: id, mode: Dual, content: gitContent(c.subcommand)}, true
	}
	for _, op := range operands {
		rel, ok := wsRel(op, inv)
		if !ok {
			return classification{}, false
		}
		id.ident = append(id.ident, identOf(rel)...)
	}
	return classification{id: id, mode: Dual, content: gitContent(c.subcommand)}, true
}

func coexistID(c cmd) labelID {
	prog := c.program
	if c.pipeline || prog == "" {
		prog = "sh"
	}
	return labelID{domain: domainShell, ident: []string{prog}}
}

// relIdent rejects paths that escape the root (via wsRel) and glob patterns
// (whose expansion makes the identity unstable) — both fall back to coexist.
func relIdent(path string, inv Invocation) ([]string, bool) {
	rel, ok := wsRel(path, inv)
	if !ok {
		return nil, false
	}
	if strings.ContainsAny(rel, "*?[") {
		return nil, false
	}
	return identOf(rel), true
}

// relIdents resolves every path operand into identity segments; any operand that
// escapes or globs collapses to coexist, so multi-operand commands never alias.
func relIdents(paths []string, inv Invocation) ([]string, bool) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		seg, ok := relIdent(p, inv)
		if !ok {
			return nil, false
		}
		out = append(out, seg...)
	}
	return out, true
}

func identOf(rel string) []string {
	if rel == "" || rel == "." {
		return nil
	}
	return []string{rel}
}

func gitContent(sub string) string {
	if sub == "diff" || sub == "show" {
		return contentTypeCode
	}
	return contentTypeProse
}

func contentForPath(path string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if codeExts[ext] {
		return contentTypeCode
	}
	return contentTypeProse
}
