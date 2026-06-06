// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"path/filepath"
	"strings"
)

type cmd struct {
	program    string // base name of argv[0]
	subcommand string // git subcommand; "" otherwise
	args       []string
	pipeline   bool // compound/pipeline line — no stable identity
}

// shellMeta presence means output can't be attributed to one program → coexist.
const shellMeta = "|&;<>()$`\n"

// cosmeticFlags don't change output, so they're stripped to let variants share one
// label. Never add an output-affecting flag here — it would overwrite the flag-free
// label; those route to coexist instead.
var cosmeticFlags = map[string]bool{
	"--color":    true,
	"--no-color": true,
	"--no-pager": true,
}

// parse returns ok=false only for a blank command (the untranslatable signal);
// every other input yields a cmd.
func parse(command string) (cmd, bool) {
	s := strings.TrimSpace(command)
	if s == "" {
		return cmd{}, false
	}
	if strings.ContainsAny(s, shellMeta) {
		return cmd{program: "sh", pipeline: true}, true
	}

	toks := tokenize(s)
	if len(toks) == 0 {
		return cmd{}, false
	}

	c := cmd{program: filepath.Base(toks[0])}
	rest := make([]string, 0, len(toks)-1)
	for _, t := range toks[1:] {
		if cosmeticFlags[t] {
			continue
		}
		rest = append(rest, t)
	}

	// Only git keys a labeling rule off its subcommand; other subcommand-style CLIs
	// (go, cargo, …) are coexist runners, so their first arg stays an ordinary operand.
	if c.program == "git" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		c.subcommand = rest[0]
		c.args = rest[1:]
	} else {
		c.args = rest
	}
	return c, true
}

// tokenize tolerates an unterminated quote — a best-effort normalizer, not a shell.
func tokenize(s string) []string {
	var toks []string
	var b strings.Builder
	var quote rune
	inTok := false

	flush := func() {
		if inTok {
			toks = append(toks, b.String())
			b.Reset()
			inTok = false
		}
	}

	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inTok = true
		case r == ' ' || r == '\t':
			flush()
		default:
			b.WriteRune(r)
			inTok = true
		}
	}
	flush()
	return toks
}

func (c cmd) pathArgs() []string {
	out := make([]string, 0, len(c.args))
	for _, a := range c.args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// The bare "--" separator is not treated as a flag.
func (c cmd) hasOutputFlag() bool {
	for _, a := range c.args {
		if a != "--" && strings.HasPrefix(a, "-") {
			return true
		}
	}
	return false
}

// wsRel returns ok=false for a path that escapes the root ("..", absolute-outside) or
// an absolute path with no known root — those must never become a stable label (they
// leak host paths and collide across workspaces), so the caller falls back to coexist.
func wsRel(path string, inv Invocation) (string, bool) {
	if path == "" {
		return "", false
	}

	p := path
	if !filepath.IsAbs(p) {
		anchor := inv.Cwd
		if anchor == "" {
			anchor = inv.WorkspaceRoot
		}
		if anchor == "" {
			cl := filepath.ToSlash(filepath.Clean(path))
			if cl == ".." || strings.HasPrefix(cl, "../") {
				return "", false
			}
			return cl, true
		}
		p = filepath.Join(anchor, p)
	}
	p = filepath.Clean(p)

	root := inv.WorkspaceRoot
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), p)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
