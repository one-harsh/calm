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

// parse returns ok=false for a blank command or one that is only assignment
// prefixes (both untranslatable signals); every other input yields a cmd.
func parse(command string) (cmd, bool) {
	s := strings.TrimSpace(command)
	if s == "" {
		return cmd{}, false
	}
	if strings.ContainsAny(s, shellMeta) {
		return cmd{program: "sh", pipeline: true}, true
	}

	toks := tokenize(s)
	toks = stripAssignmentPrefixes(toks)
	if len(toks) == 0 {
		return cmd{}, false
	}
	// A program token bearing whitespace means quoting glued a phrase the shell
	// would not have — attribution can't be trusted, same verdict as shellMeta.
	if strings.ContainsAny(toks[0], " \t") {
		return cmd{program: "sh", pipeline: true}, true
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

// Program is the base program a command invokes, sharing parse with the labeler
// so a re-entrancy guard and the source label agree on identity: argv[0]'s base
// for a simple command, "sh" for a compound/pipeline (no single owner), and ""
// for a blank or assignment-only line (no program at all). Substring matching a
// program name against the whole command is the bug this replaces — it drops any
// command that merely mentions the name.
func Program(command string) string {
	c, ok := parse(command)
	if !ok {
		return ""
	}
	return c.program
}

// InvokesProgram reports whether any pipeline/list segment of command runs program
// as its argv[0] basename. Program collapses a compound line to "sh", so a
// re-entrancy guard keyed on it is evaded by plumbing (`… | head`, `a && b`);
// checking each segment recognizes calm-capture even when piped or chained.
func InvokesProgram(command, program string) bool {
	for _, seg := range splitSegments(command) {
		toks := stripAssignmentPrefixes(tokenize(seg))
		if len(toks) == 0 {
			continue
		}
		base := filepath.Base(toks[0])
		// Unconditional .exe alias + case-fold: a false match from odd casing on
		// unix only passes capture through — lost capture, never corruption.
		if strings.EqualFold(base, program) || strings.EqualFold(base, program+".exe") {
			return true
		}
	}
	return false
}

// splitSegments cuts command at unquoted command separators (|, ||, |&, &&, ;,
// newline) and returns each segment's raw text for re-tokenizing. A lone & is
// left in place so a redirection like 2>&1 stays intact and never becomes a
// boundary; quotes and backslash escapes suppress separators inside them,
// mirroring tokenize's quoting rules.
func splitSegments(command string) []string {
	var segs []string
	var b strings.Builder
	var quote rune
	pendingEsc := false
	dqEsc := false
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case pendingEsc:
			b.WriteByte('\\')
			b.WriteRune(r)
			pendingEsc = false
		case dqEsc:
			b.WriteRune(r)
			dqEsc = false
		case quote != 0:
			switch {
			case quote == '"' && r == '\\':
				b.WriteRune(r)
				dqEsc = true
			case r == quote:
				b.WriteRune(r)
				quote = 0
			default:
				b.WriteRune(r)
			}
		case r == '\\':
			pendingEsc = true
		case r == '\'' || r == '"':
			b.WriteRune(r)
			quote = r
		case r == ';' || r == '\n':
			segs = append(segs, b.String())
			b.Reset()
		case r == '|':
			segs = append(segs, b.String())
			b.Reset()
			if i+1 < len(rs) && (rs[i+1] == '|' || rs[i+1] == '&') {
				i++ // consume the second byte of || or |&
			}
		case r == '&' && i+1 < len(rs) && rs[i+1] == '&':
			segs = append(segs, b.String())
			b.Reset()
			i++ // consume the second & of &&
		default:
			b.WriteRune(r)
		}
	}
	if pendingEsc {
		b.WriteByte('\\')
	}
	segs = append(segs, b.String())
	return segs
}

// tokenize is a best-effort normalizer, not a shell: it tolerates an
// unterminated quote; escaped whitespace glues and a double-quoted \" or \\
// escapes, so an assignment value never splits a fragment into the program
// slot; every other backslash is literal so Windows paths survive.
func tokenize(s string) []string {
	var toks []string
	var b strings.Builder
	var quote rune
	inTok := false
	pendingEsc := false
	dqEsc := false

	flush := func() {
		if inTok {
			toks = append(toks, b.String())
			b.Reset()
			inTok = false
		}
	}

	for _, r := range s {
		switch {
		case pendingEsc:
			if r == ' ' || r == '\t' {
				b.WriteRune(r) // glue: drop the backslash, keep the whitespace
			} else {
				b.WriteByte('\\') // literal passthrough of both runes
				b.WriteRune(r)
			}
			inTok = true
			pendingEsc = false
		case dqEsc:
			if r != '"' && r != '\\' {
				b.WriteByte('\\') // backslash stays literal before other runes, as in sh
			}
			b.WriteRune(r)
			dqEsc = false
		case quote != 0:
			switch {
			case quote == '"' && r == '\\':
				dqEsc = true
			case r == quote:
				quote = 0
			default:
				b.WriteRune(r)
			}
		case r == '\\':
			pendingEsc = true
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
	if pendingEsc || dqEsc {
		b.WriteByte('\\') // a trailing backslash escapes nothing; keep it literal
		inTok = true
	}
	flush()
	return toks
}

func IsAssignmentPrefix(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}

	// The value after "=" is never inspected: it can be a literal secret
	// and must reach no label component.
	for i := 0; i < eq; i++ {
		ch := tok[i]
		switch {
		case ch == '_', ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z':
		case i > 0 && ch >= '0' && ch <= '9':
		default:
			return false
		}
	}
	return true
}

// stripAssignmentPrefixes drops leading assignment prefixes and a plain env
// wrapper so identity derives from the real program, never from an assignment
// (whose value can be a secret) or the env shim. An env carrying its own flags
// (env -i, env -u NAME) is left intact: attribution isn't worth modeling, so
// the program stays env (a generic coexist identity) rather than guessing past
// env's flag grammar to the wrapped command.
func stripAssignmentPrefixes(toks []string) []string {
	i := 0
	for i < len(toks) {
		if IsAssignmentPrefix(toks[i]) {
			i++
			continue
		}
		if toks[i] == "env" && i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "-") {
			i++
			continue
		}
		break
	}
	return toks[i:]
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
