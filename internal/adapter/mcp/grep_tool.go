// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const toolNameGrep = "calm_grep"

// probeGrepEngine probes best-first but always lands on an engine that ships
// with the OS: ripgrep when installed (faster, gitignore-aware — captures
// don't wade through .git/ and vendored trees), else grep (POSIX-guaranteed
// on unix; present in Git-for-Windows environments), else findstr on Windows
// (always present; limited regex dialect, named in the tool description).
func probeGrepEngine() string {
	if _, err := osexec.LookPath("rg"); err == nil {
		return "rg"
	}
	if runtime.GOOS == "windows" {
		if _, err := osexec.LookPath("grep"); err == nil {
			return "grep"
		}
		return "findstr"
	}
	return "grep"
}

const grepSchema = `{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Regular expression to search for."},
    "paths": {"type": "array", "items": {"type": "string"}, "description": "Files or directories to search, workspace-relative; defaults to the whole workspace."},
    "case_insensitive": {"type": "boolean", "description": "Case-insensitive matching."},
    "include": {"type": "string", "description": "Only search files matching this glob (e.g. *.go)."},
    "workspace": {"type": "string", "description": "Workspace ID to target; defaults to the primary workspace."}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

type grepArgs struct {
	Pattern         string   `json:"pattern"`
	Paths           []string `json:"paths"`
	CaseInsensitive bool     `json:"case_insensitive"`
	Include         string   `json:"include"`
	Workspace       string   `json:"workspace"`
}

func (s *Server) newGrepTool() Tool {
	desc := "Search workspace files with " + s.grepEngine + " and capture matching lines " +
		"(path:line:text) into CALM. Prefer this over shell grep/rg. Small results come back verbatim " +
		"(still captured and searchable via calm_search). Larger results come back as a compact summary " +
		"plus a source label ending in @<token>; fetch the full match list later with calm_search " +
		"source=<label exactly as returned> rather than re-running. The label refers to the latest " +
		"results for this pattern and scope; case_insensitive/include variants share it. Never append " +
		"#<n> after the @<token>. In multi-workspace sessions, set workspace=<id> to target a " +
		"non-default workspace."
	if s.grepEngine == "findstr" {
		desc += " findstr supports literal and basic regex patterns only — no alternation (|), no + or {} quantifiers."
	}
	return Tool{
		Name:        toolNameGrep,
		Description: desc,
		InputSchema: json.RawMessage(grepSchema),
		Handler:     s.grep,
		Annotations: readOnlyAnnotations,
	}
}

func (s *Server) grep(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var a grepArgs
	if uerr := json.Unmarshal(args, &a); uerr != nil {
		return ToolResult{}, &ArgError{Detail: uerr.Error()}
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return ToolResult{}, &ArgError{Detail: "pattern is required"}
	}
	paths := a.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for i, p := range paths {
		if strings.TrimSpace(p) == "" {
			return ToolResult{}, &ArgError{Detail: "paths entries must be non-blank"}
		}
		paths[i] = p
	}

	wb, werr := s.workspaceForPath(a.Workspace, paths[0])
	if werr != nil {
		return ToolResult{}, werr
	}
	isDir := func(p string) bool {
		fi, statErr := os.Stat(wb.resolve(p))
		return statErr == nil && fi.IsDir()
	}
	ectx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	r, runErr := exec.RunArgv(ectx, grepArgv(s.grepEngine, a.Pattern, paths, a.CaseInsensitive, a.Include, isDir), wb.Root)
	if runErr != nil {
		return TextResult("failed to run "+s.grepEngine+": "+runErr.Error(), true), nil
	}

	// Typed semantics: zero matches is a result, not an error. Both engines
	// exit 1 with empty output on no matches — normalize before planning so
	// the label's replace identity stays current and no error event fires.
	if r.ExitCode == 1 && strings.TrimSpace(r.Stdout) == "" && strings.TrimSpace(r.Stderr) == "" {
		r = exec.Result{Stdout: "(no matches)\n"}
	}

	raw := commandPayload(r)
	return s.capturePipeline(ctx, captureSpec{
		ingest:  raw,
		visible: raw,
		res:     r,
		plan: func() (extract.Plan, error) {
			return extract.PlanGrep(s.invocation(wb, "", wb.Root), execResultOf(r), a.Pattern, paths), nil
		},
	})
}

// grepArgv builds the engine invocation. All engines emit path:line:text;
// `--` guards patterns beginning with `-` (findstr's `/C:` serves the same
// role and additionally forces single-pattern semantics — without it,
// findstr splits the pattern on spaces into an OR of words). isDir is
// consulted by the findstr branch only, to tell directory scopes from
// explicit files.
func grepArgv(engine, pattern string, paths []string, caseInsensitive bool, include string, isDir func(string) bool) []string {
	var argv []string
	switch engine {
	case "rg":
		argv = []string{"rg", "-n", "--no-heading"}
		if caseInsensitive {
			argv = append(argv, "-i")
		}
		if include != "" {
			argv = append(argv, "-g", include)
		}
	case "findstr":
		specs, recurse := findstrFilespecs(paths, include, isDir)
		argv = []string{"findstr"}
		if recurse {
			argv = append(argv, "/S")
		}
		argv = append(argv, "/N", "/R")
		if caseInsensitive {
			argv = append(argv, "/I")
		}
		argv = append(argv, "/C:"+pattern)
		return append(argv, specs...)
	default:
		argv = []string{"grep", "-rn"}
		if caseInsensitive {
			argv = append(argv, "-i")
		}
		if include != "" {
			argv = append(argv, "--include="+include)
		}
	}
	argv = append(argv, "--", pattern)
	return append(argv, paths...)
}

// findstrFilespecs composes scope paths with the include glob: findstr has no
// include flag — the filespec IS the filter (`src\*.go`). Directory scopes get
// `<dir>\<include-or-*>`; explicit files pass through as-is (include filters
// only directory recursion, matching grep --include semantics). /S is emitted
// only when a directory is in scope: a file-only invocation must not recurse,
// because /S re-applies the filespec as a name pattern in every subdirectory.
// Mixed file+dir scopes keep /S and accept that name-shadowing quirk. The `\`
// join is explicit (not filepath.Join) so the pure function is deterministic
// in cross-platform unit tests.
func findstrFilespecs(paths []string, include string, isDir func(string) bool) (specs []string, recurse bool) {
	if include == "" {
		include = "*"
	}
	specs = make([]string, 0, len(paths))
	for _, p := range paths {
		switch {
		case p == ".":
			specs = append(specs, include)
			recurse = true
		case isDir != nil && isDir(p):
			specs = append(specs, strings.TrimSuffix(p, "/")+`\`+include)
			recurse = true
		default:
			specs = append(specs, p)
		}
	}
	return specs, recurse
}
