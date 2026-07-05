// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	osexec "os/exec"
	"strings"

	"github.com/one-harsh/calm/internal/adapter/exec"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

const toolNameGrep = "calm_grep"

// probeGrepEngine prefers ripgrep when installed — faster and
// gitignore-aware, so captures don't wade through .git/ and vendored trees.
func probeGrepEngine() string {
	if _, err := osexec.LookPath("rg"); err == nil {
		return "rg"
	}
	return "grep"
}

const grepSchema = `{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Regular expression to search for."},
    "paths": {"type": "array", "items": {"type": "string"}, "description": "Files or directories to search, workspace-relative; defaults to the whole workspace."},
    "case_insensitive": {"type": "boolean", "description": "Case-insensitive matching."},
    "include": {"type": "string", "description": "Only search files matching this glob (e.g. *.go)."}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`

type grepArgs struct {
	Pattern         string   `json:"pattern"`
	Paths           []string `json:"paths"`
	CaseInsensitive bool     `json:"case_insensitive"`
	Include         string   `json:"include"`
}

func (s *Server) newGrepTool() Tool {
	return Tool{
		Name: toolNameGrep,
		Description: "Search workspace files with " + s.grepEngine + " and capture matching lines " +
			"(path:line:text) into CALM. Prefer this over shell grep/rg. Small results come back verbatim " +
			"(still captured and searchable via calm_search). Larger results come back as a compact summary " +
			"plus a source label ending in @<token>; fetch the full match list later with calm_search " +
			"source=<label exactly as returned> rather than re-running. The label refers to the latest " +
			"results for this pattern and scope; case_insensitive/include variants share it. Never append " +
			"#<n> after the @<token>.",
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

	ectx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	r, runErr := exec.RunArgv(ectx, grepArgv(s.grepEngine, a.Pattern, paths, a.CaseInsensitive, a.Include), s.workspaceRoot)
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
			inv := extract.Invocation{Seq: s.seq.Add(1), Cwd: s.workspaceRoot, WorkspaceRoot: s.workspaceRoot}
			return extract.PlanGrep(inv, execResultOf(r), a.Pattern, paths), nil
		},
	})
}

// grepArgv builds the engine invocation. Both engines emit path:line:text;
// `--` guards patterns beginning with `-`.
func grepArgv(engine, pattern string, paths []string, caseInsensitive bool, include string) []string {
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
