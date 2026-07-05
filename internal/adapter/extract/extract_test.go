// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func inv(command string, seq int64) Invocation {
	return Invocation{Seq: seq, Command: command, Cwd: "/work", WorkspaceRoot: "/work"}
}

func okResult() ExecResult { return ExecResult{Stdout: "out", ExitCode: 0} }

func TestDerivePlan_GrammarAndMode(t *testing.T) {
	const seq = 7
	cases := []struct {
		name    string
		command string
		mode    CaptureMode
		latest  string
		history string
	}{
		{"file_read", "cat ./foo.py", Replace, "calm:v1:file:read:foo.py", ""},
		{"list", "ls src", Replace, "calm:v1:file:list:src", ""},
		{"list_no_arg", "ls", Replace, "calm:v1:file:list", ""},
		{"git_diff_range", "git diff main..feat", Dual, "calm:v1:vcs:git:diff:main..feat", "calm:v1:vcs:git:diff:main..feat#7"},
		{"git_diff_worktree", "git diff HEAD", Dual, "calm:v1:vcs:git:diff:HEAD", "calm:v1:vcs:git:diff:HEAD#7"},
		{"git_diff_bare", "git diff", Dual, "calm:v1:vcs:git:diff:HEAD", "calm:v1:vcs:git:diff:HEAD#7"},
		{"git_status", "git status", Dual, "calm:v1:vcs:git:status", "calm:v1:vcs:git:status#7"},
		{"go_runner_coexist", "go test ./...", Coexist, "", "calm:v1:shell:go#7"},
		{"grep", "grep TODO src", Replace, "calm:v1:search:grep:TODO:src", ""},
		{"git_show", "git show HEAD:file", Dual, "calm:v1:vcs:git:show:HEAD%3Afile", "calm:v1:vcs:git:show:HEAD%3Afile#7"},
		{"unknown", "frobnicate xyz", Coexist, "", "calm:v1:shell:frobnicate#7"},
		{"pipeline", "cat foo | grep bar", Coexist, "", "calm:v1:shell:sh#7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := DerivePlan(inv(tc.command, seq), okResult())
			if err != nil {
				t.Fatalf("DerivePlan(%q): %v", tc.command, err)
			}
			if p.Mode != tc.mode {
				t.Errorf("mode = %s; want %s", p.Mode, tc.mode)
			}
			if p.LatestSource != tc.latest {
				t.Errorf("latest = %q; want %q", p.LatestSource, tc.latest)
			}
			if p.HistorySource != tc.history {
				t.Errorf("history = %q; want %q", p.HistorySource, tc.history)
			}
		})
	}
}

func TestDerivePlan_NormalizationInvariance(t *testing.T) {
	cases := [][2]string{
		{"cat foo.py", "cat ./foo.py"},
		{"ls src", "ls --color src"},
		{`cat foo.py`, `cat   foo.py`},
	}
	for _, pair := range cases {
		a, err := DerivePlan(inv(pair[0], 1), okResult())
		if err != nil {
			t.Fatalf("DerivePlan(%q): %v", pair[0], err)
		}
		b, err := DerivePlan(inv(pair[1], 1), okResult())
		if err != nil {
			t.Fatalf("DerivePlan(%q): %v", pair[1], err)
		}
		if a.LatestSource != b.LatestSource {
			t.Errorf("%q vs %q: latest %q != %q", pair[0], pair[1], a.LatestSource, b.LatestSource)
		}
	}
}

func TestDerivePlan_ReplaceIgnoresSeq_DualUsesSeq(t *testing.T) {
	r1, _ := DerivePlan(inv("cat foo.py", 1), okResult())
	r2, _ := DerivePlan(inv("cat foo.py", 2), okResult())
	if r1.LatestSource != r2.LatestSource || r1.HistorySource != "" || r2.HistorySource != "" {
		t.Errorf("replace must ignore seq: %+v %+v", r1, r2)
	}

	d1, _ := DerivePlan(inv("git status", 1), okResult())
	d2, _ := DerivePlan(inv("git status", 2), okResult())
	if d1.HistorySource == d2.HistorySource {
		t.Errorf("dual must use seq: both %q", d1.HistorySource)
	}
	if !strings.HasSuffix(d1.HistorySource, "#1") || !strings.HasSuffix(d2.HistorySource, "#2") {
		t.Errorf("dual history suffixes wrong: %q %q", d1.HistorySource, d2.HistorySource)
	}
}

func TestDerivePlan_IdenticalRetrySameSeqSameHistory(t *testing.T) {
	a, _ := DerivePlan(inv("git status", 5), okResult())
	b, _ := DerivePlan(inv("git status", 5), okResult())
	if a.HistorySource != b.HistorySource {
		t.Errorf("same seq must yield same history: %q != %q", a.HistorySource, b.HistorySource)
	}
}

func TestDerivePlan_Workspace(t *testing.T) {
	t.Run("same_root_same_identity", func(t *testing.T) {
		a, _ := DerivePlan(Invocation{Command: "cat foo.py", Cwd: "/work", WorkspaceRoot: "/work"}, okResult())
		b, _ := DerivePlan(Invocation{Command: "cat foo.py", Cwd: "/work", WorkspaceRoot: "/work"}, okResult())
		if a.LatestSource != b.LatestSource {
			t.Errorf("%q != %q", a.LatestSource, b.LatestSource)
		}
	})

	t.Run("subdir_cwd_differs", func(t *testing.T) {
		root, _ := DerivePlan(Invocation{Command: "cat foo.py", Cwd: "/work", WorkspaceRoot: "/work"}, okResult())
		sub, _ := DerivePlan(Invocation{Command: "cat foo.py", Cwd: "/work/pkg", WorkspaceRoot: "/work"}, okResult())
		if sub.LatestSource != "calm:v1:file:read:pkg/foo.py" {
			t.Errorf("subdir latest = %q", sub.LatestSource)
		}
		if root.LatestSource == sub.LatestSource {
			t.Errorf("subdir must differ from root: %q", sub.LatestSource)
		}
	})

	t.Run("workspace_id_segment", func(t *testing.T) {
		a, _ := DerivePlan(Invocation{Command: "cat foo.py", Cwd: "/work", WorkspaceRoot: "/work", WorkspaceID: "repoA"}, okResult())
		b, _ := DerivePlan(Invocation{Command: "cat foo.py", Cwd: "/work", WorkspaceRoot: "/work", WorkspaceID: "repoB"}, okResult())
		if a.LatestSource != "calm:v1:file:read:repoA:foo.py" {
			t.Errorf("wsid latest = %q", a.LatestSource)
		}
		if a.LatestSource == b.LatestSource {
			t.Errorf("different workspace id must differ: %q", a.LatestSource)
		}
	})

	t.Run("escape_relative_goes_coexist", func(t *testing.T) {
		p, _ := DerivePlan(Invocation{Command: "cat ../secret.py", Cwd: "/work", WorkspaceRoot: "/work", Seq: 3}, okResult())
		if p.Mode != Coexist || p.LatestSource != "" {
			t.Errorf("escape must be coexist with no latest: %+v", p)
		}
		if strings.Contains(p.HistorySource, "secret") || strings.Contains(p.HistorySource, "..") {
			t.Errorf("escape label must not carry the path: %q", p.HistorySource)
		}
	})

	t.Run("absolute_outside_goes_coexist", func(t *testing.T) {
		p, _ := DerivePlan(Invocation{Command: "cat /etc/passwd", Cwd: "/work", WorkspaceRoot: "/work", Seq: 3}, okResult())
		if p.Mode != Coexist {
			t.Errorf("absolute-outside must be coexist: %+v", p)
		}
		if strings.Contains(p.HistorySource, "/etc/passwd") {
			t.Errorf("must not emit raw absolute label: %q", p.HistorySource)
		}
	})

	t.Run("glob_goes_coexist", func(t *testing.T) {
		p, _ := DerivePlan(inv("cat *.go", 4), okResult())
		if p.Mode != Coexist {
			t.Errorf("glob must be coexist: %+v", p)
		}
	})
}

func TestDerivePlan_UntranslatableErrors(t *testing.T) {
	for _, c := range []string{"", "   ", "\t\n"} {
		if _, err := DerivePlan(inv(c, 1), okResult()); err == nil {
			t.Errorf("DerivePlan(%q): want error", c)
		}
	}
}

func TestFinalizeEvents_CrossLinksOnlyPersisted(t *testing.T) {
	p, _ := DerivePlan(inv("git status", 9), okResult())

	t.Run("both_persisted", func(t *testing.T) {
		ev := FinalizeEvents(p, []WriteOutcome{
			{Source: p.LatestSource, Persisted: true},
			{Source: p.HistorySource, Persisted: true},
		})
		ti := requireEvent(t, ev, EventToolInvocation)
		if ti.Data[keyLatestSource] != p.LatestSource {
			t.Errorf("latest_source = %v", ti.Data[keyLatestSource])
		}
		if ti.Data[keyHistorySource] != p.HistorySource {
			t.Errorf("history_source = %v", ti.Data[keyHistorySource])
		}
	})

	t.Run("latest_failed_no_dangling_link", func(t *testing.T) {
		ev := FinalizeEvents(p, []WriteOutcome{
			{Source: p.LatestSource, Persisted: false},
			{Source: p.HistorySource, Persisted: true},
		})
		ti := requireEvent(t, ev, EventToolInvocation)
		if _, ok := ti.Data[keyLatestSource]; ok {
			t.Errorf("latest_source must be absent when not persisted")
		}
		if ti.Data[keyHistorySource] != p.HistorySource {
			t.Errorf("history_source should be present")
		}
	})
}

func TestFinalizeEvents_GitOperationAndPriorities(t *testing.T) {
	p, _ := DerivePlan(inv("git status", 9), okResult())
	ev := FinalizeEvents(p, nil)

	ti := requireEvent(t, ev, EventToolInvocation)
	if ti.Priority != priorityToolInvocation {
		t.Errorf("tool_invocation priority = %d; want %d", ti.Priority, priorityToolInvocation)
	}
	if ti.Data[keyToolName] != toolName {
		t.Errorf("tool_name = %v", ti.Data[keyToolName])
	}
	if ti.Data[keyInvocationID] != int64(9) {
		t.Errorf("invocation_id = %v (%T)", ti.Data[keyInvocationID], ti.Data[keyInvocationID])
	}

	gi := requireEvent(t, ev, EventGitOperation)
	if gi.Priority != priorityGitOperation {
		t.Errorf("git_operation priority = %d", gi.Priority)
	}
	if gi.Data[keySubcommand] != "status" {
		t.Errorf("subcommand = %v", gi.Data[keySubcommand])
	}
	if gi.Data[keyCommand] != "git status" {
		t.Errorf("command = %v", gi.Data[keyCommand])
	}
}

func TestFinalizeEvents_NoGitEventForNonGit(t *testing.T) {
	p, _ := DerivePlan(inv("cat foo.py", 1), okResult())
	ev := FinalizeEvents(p, nil)
	for _, e := range ev {
		if e.Type == EventGitOperation {
			t.Errorf("non-git command emitted git_operation")
		}
	}
}

func TestFinalizeEvents_ErrorObserved(t *testing.T) {
	t.Run("non_zero_exit", func(t *testing.T) {
		p, _ := DerivePlan(inv("go test ./...", 1), ExecResult{ExitCode: 2, Stderr: "FAIL\nsome trace"})
		eo := requireEvent(t, FinalizeEvents(p, nil), EventErrorObserved)
		if eo.Priority != priorityErrorObserved {
			t.Errorf("priority = %d", eo.Priority)
		}
		if eo.Data[keyExitCode] != 2 {
			t.Errorf("exit_code = %v", eo.Data[keyExitCode])
		}
		if eo.Data[keySource] != toolName {
			t.Errorf("source = %v", eo.Data[keySource])
		}
		if !strings.Contains(eo.Data[keyMessage].(string), "code 2") {
			t.Errorf("message = %v", eo.Data[keyMessage])
		}
		if eo.Data[keyTraceSnippet] == nil {
			t.Errorf("trace_snippet missing")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		p, _ := DerivePlan(inv("go test ./...", 1), ExecResult{TimedOut: true, ExitCode: -1})
		eo := requireEvent(t, FinalizeEvents(p, nil), EventErrorObserved)
		if !strings.Contains(eo.Data[keyMessage].(string), "timed out") {
			t.Errorf("message = %v", eo.Data[keyMessage])
		}
	})

	t.Run("success_no_error_event", func(t *testing.T) {
		p, _ := DerivePlan(inv("cat foo.py", 1), okResult())
		for _, e := range FinalizeEvents(p, nil) {
			if e.Type == EventErrorObserved {
				t.Errorf("success emitted error_observed")
			}
		}
	})
}

func TestPersistenceSafety_OverLengthIdentityHashed(t *testing.T) {
	long := strings.Repeat("a", 400)
	p, _ := DerivePlan(inv("cat "+long+".py", 1), okResult())
	if len(p.LatestSource) > maxLabelLen {
		t.Errorf("label length %d exceeds cap %d", len(p.LatestSource), maxLabelLen)
	}
	if !strings.HasPrefix(p.LatestSource, "calm:v1:file:read:h") {
		t.Errorf("over-length identity should hash: %q", p.LatestSource)
	}
}

func TestPersistenceSafety_DualLatestStableAcrossSeq(t *testing.T) {
	huge := strings.Repeat("w", 400) // forces the truncation/hash path
	mk := func(seq int64) (latest, history string) {
		p, _ := DerivePlan(Invocation{Command: "git status", Cwd: "/work", WorkspaceRoot: "/work", WorkspaceID: huge, Seq: seq}, okResult())
		return p.LatestSource, p.HistorySource
	}
	l9, h9 := mk(9)    // 1-digit suffix
	l10, h10 := mk(10) // 2-digit suffix
	if l9 != l10 {
		t.Errorf("dual latest must be stable across seq digit growth: %q vs %q", l9, l10)
	}
	if h9 == h10 {
		t.Errorf("history must vary by seq: both %q", h9)
	}
	if len(h9) > maxLabelLen || len(h10) > maxLabelLen {
		t.Errorf("history exceeds cap: %d / %d", len(h9), len(h10))
	}
}

func TestPersistenceSafety_HistorySourceWithinCap(t *testing.T) {
	huge := strings.Repeat("w", 400)

	// Dual: a base capped at maxLabelLen must still leave room for the "#<seq>" suffix.
	dual, _ := DerivePlan(Invocation{Command: "git status", Cwd: "/work", WorkspaceRoot: "/work", WorkspaceID: huge, Seq: 1234567890}, okResult())
	if dual.Mode != Dual {
		t.Fatalf("mode = %s; want dual", dual.Mode)
	}
	if len(dual.LatestSource) > maxLabelLen || len(dual.HistorySource) > maxLabelLen {
		t.Errorf("dual lengths latest=%d history=%d exceed cap %d", len(dual.LatestSource), len(dual.HistorySource), maxLabelLen)
	}

	// Coexist history is also suffix-bearing; 12-digit seq is the widest the reserve covers.
	co, _ := DerivePlan(Invocation{Command: "frobnicate", Cwd: "/work", WorkspaceRoot: "/work", WorkspaceID: huge, Seq: 999999999999}, okResult())
	if len(co.HistorySource) > maxLabelLen {
		t.Errorf("coexist history length %d exceeds cap %d", len(co.HistorySource), maxLabelLen)
	}
}

func TestPersistenceSafety_LongWorkspaceIDKeepsIdentitiesDistinct(t *testing.T) {
	huge := strings.Repeat("w", 400)
	mk := func(file string) string {
		p, _ := DerivePlan(Invocation{Command: "cat " + file, Cwd: "/work", WorkspaceRoot: "/work", WorkspaceID: huge}, okResult())
		return p.LatestSource
	}
	foo, bar := mk("foo.py"), mk("bar.py")
	if len(foo) > maxLabelLen || len(bar) > maxLabelLen {
		t.Fatalf("label exceeds cap: %d / %d", len(foo), len(bar))
	}
	if foo == bar {
		t.Errorf("distinct files collided under a long workspace id: %q", foo)
	}
}

func TestPersistenceSafety_ReservedCharsEncoded(t *testing.T) {
	p, _ := DerivePlan(inv(`cat "a b#c%d.py"`, 1), okResult())
	// The escape char '%' legitimately appears in the encoded form; only the raw
	// reserved chars (space, '#') must be gone.
	if strings.ContainsAny(strings.TrimPrefix(p.LatestSource, "calm:v1:file:read:"), " #") {
		t.Errorf("reserved chars not encoded: %q", p.LatestSource)
	}
	if !strings.Contains(p.LatestSource, "%20") || !strings.Contains(p.LatestSource, "%23") || !strings.Contains(p.LatestSource, "%25") {
		t.Errorf("expected percent-encodings: %q", p.LatestSource)
	}
}

func TestPersistenceSafety_TraceSanitizedRedactedTruncated(t *testing.T) {
	t.Run("control_chars_stripped", func(t *testing.T) {
		got := traceSnippet("err\x00\x07line\x1b[0m")
		if strings.ContainsAny(got, "\x00\x07\x1b") {
			t.Errorf("control chars survived: %q", got)
		}
	})

	t.Run("secrets_redacted", func(t *testing.T) {
		got := traceSnippet("fatal: bad --token=abc123 and Authorization: Bearer xyz.987")
		if strings.Contains(got, "abc123") || strings.Contains(got, "xyz.987") {
			t.Errorf("secret survived redaction: %q", got)
		}
		if !strings.Contains(got, "<redacted>") {
			t.Errorf("expected redaction marker: %q", got)
		}
	})

	t.Run("credential_longer_than_window_redacted", func(t *testing.T) {
		// The marker would fall outside the last maxTraceSnippet bytes; redaction must
		// run before truncation so the credential never survives in the tail.
		token := strings.Repeat("A", maxTraceSnippet+200)
		got := traceSnippet("Authorization: Bearer " + token + " trailing")
		if strings.Contains(got, "AAAA") {
			t.Errorf("over-length credential survived: %q", got)
		}
		if !strings.Contains(got, "<redacted>") {
			t.Errorf("expected redaction marker: %q", got)
		}
		if len(got) > maxTraceSnippet {
			t.Errorf("snippet exceeds cap: %d", len(got))
		}
	})

	t.Run("truncated", func(t *testing.T) {
		got := traceSnippet(strings.Repeat("x", 2000))
		if len(got) > maxTraceSnippet {
			t.Errorf("trace length %d exceeds %d", len(got), maxTraceSnippet)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if traceSnippet("   ") != "" {
			t.Errorf("blank stderr should yield empty snippet")
		}
	})

	t.Run("tail_truncation_keeps_valid_utf8", func(t *testing.T) {
		// A multibyte rune straddling the truncation boundary must not leave a
		// partial leading codepoint in the snippet.
		got := traceSnippet("你" + strings.Repeat("b", maxTraceSnippet-1))
		if !utf8.ValidString(got) {
			t.Errorf("snippet is not valid UTF-8: %q", got)
		}
		if len(got) > maxTraceSnippet {
			t.Errorf("trace length %d exceeds %d", len(got), maxTraceSnippet)
		}
	})
}

func TestDerivePlan_BareListingUsesCwd(t *testing.T) {
	root, _ := DerivePlan(Invocation{Command: "ls", Cwd: "/work", WorkspaceRoot: "/work"}, okResult())
	sub, _ := DerivePlan(Invocation{Command: "ls", Cwd: "/work/pkg", WorkspaceRoot: "/work"}, okResult())
	if root.LatestSource == sub.LatestSource {
		t.Errorf("bare ls in different dirs must not collide: both %q", root.LatestSource)
	}
	if sub.LatestSource != "calm:v1:file:list:pkg" {
		t.Errorf("bare ls in subdir = %q; want calm:v1:file:list:pkg", sub.LatestSource)
	}
}

func TestCommandSummary_NoRawArgs(t *testing.T) {
	p, _ := DerivePlan(inv("git diff --token=secret HEAD", 1), okResult())
	ev := FinalizeEvents(p, nil)
	for _, e := range ev {
		if cmdv, ok := e.Data[keyCommand]; ok {
			if strings.Contains(cmdv.(string), "secret") {
				t.Errorf("command field leaked an arg: %v", cmdv)
			}
		}
	}
}

func TestCaptureMode_String(t *testing.T) {
	cases := map[CaptureMode]string{Replace: "replace", Dual: "dual", Coexist: "coexist", CaptureMode(99): "unknown"}
	for m, want := range cases {
		if m.String() != want {
			t.Errorf("%d.String() = %q; want %q", m, m.String(), want)
		}
	}
}

func TestDerivePlan_RemainingRuleBranches(t *testing.T) {
	cases := []struct {
		name    string
		command string
		mode    CaptureMode
		latest  string
		history string
	}{
		{"find_path", "find src", Replace, "calm:v1:search:find:src", ""},
		{"find_no_arg", "find", Replace, "calm:v1:search:find", ""},
		{"git_log_range", "git log main..feat", Dual, "calm:v1:vcs:git:log:main..feat", "calm:v1:vcs:git:log:main..feat#1"},
		{"git_log_worktree", "git log", Dual, "calm:v1:vcs:git:log:HEAD", "calm:v1:vcs:git:log:HEAD#1"},
		{"git_show_bare", "git show", Dual, "calm:v1:vcs:git:show:HEAD", "calm:v1:vcs:git:show:HEAD#1"},
		{"grep_pattern_only", "grep TODO", Replace, "calm:v1:search:grep:TODO", ""},
		{"head_alias", "head ./a.txt", Replace, "calm:v1:file:read:a.txt", ""},
		{"unknown_git_subcommand", "git blame foo", Coexist, "", "calm:v1:shell:git#1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := DerivePlan(inv(tc.command, 1), okResult())
			if err != nil {
				t.Fatalf("DerivePlan(%q): %v", tc.command, err)
			}
			if p.Mode != tc.mode || p.LatestSource != tc.latest || p.HistorySource != tc.history {
				t.Errorf("got mode=%s latest=%q history=%q; want %s / %q / %q",
					p.Mode, p.LatestSource, p.HistorySource, tc.mode, tc.latest, tc.history)
			}
		})
	}
}

func TestDerivePlan_MultiOperandIdentity(t *testing.T) {
	cases := []struct {
		command string
		latest  string
	}{
		{"cat a b", "calm:v1:file:read:a:b"},
		{"cat a c", "calm:v1:file:read:a:c"},
		{"ls a b", "calm:v1:file:list:a:b"},
		{"grep TODO a b", "calm:v1:search:grep:TODO:a:b"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			p, err := DerivePlan(inv(tc.command, 1), okResult())
			if err != nil {
				t.Fatalf("DerivePlan(%q): %v", tc.command, err)
			}
			if p.LatestSource != tc.latest {
				t.Errorf("latest = %q; want %q", p.LatestSource, tc.latest)
			}
		})
	}

	ab, _ := DerivePlan(inv("cat a b", 1), okResult())
	ac, _ := DerivePlan(inv("cat a c", 1), okResult())
	if ab.LatestSource == ac.LatestSource {
		t.Errorf("multi-operand alias: %q == %q", ab.LatestSource, ac.LatestSource)
	}
}

func TestDerivePlan_GitOperandSafety(t *testing.T) {
	t.Run("absolute_paths_coexist_no_leak", func(t *testing.T) {
		p, _ := DerivePlan(inv("git diff /etc/passwd /tmp/copy", 3), okResult())
		if p.Mode != Coexist || p.LatestSource != "" {
			t.Errorf("absolute git operands must coexist: %+v", p)
		}
		if strings.Contains(p.HistorySource, "etc") || strings.Contains(p.HistorySource, "tmp") {
			t.Errorf("git label leaked an absolute path: %q", p.HistorySource)
		}
	})

	t.Run("dotdot_path_not_treated_as_range", func(t *testing.T) {
		p, _ := DerivePlan(inv("git diff ../sibling", 3), okResult())
		if p.Mode != Coexist {
			t.Errorf("escaping git path must coexist, not replace-as-range: %+v", p)
		}
		if strings.Contains(p.HistorySource, "..") || strings.Contains(p.HistorySource, "sibling") {
			t.Errorf("escape path leaked into label: %q", p.HistorySource)
		}
	})

	t.Run("ref_range_is_dual", func(t *testing.T) {
		p, _ := DerivePlan(inv("git diff main..feat", 3), okResult())
		if p.Mode != Dual || p.LatestSource != "calm:v1:vcs:git:diff:main..feat" {
			t.Errorf("ref range must be dual (branch refs are mutable): %+v", p)
		}
	})

	t.Run("pathspec_after_dashdash_keeps_separator", func(t *testing.T) {
		p, _ := DerivePlan(inv("git diff HEAD -- src/foo.go", 3), okResult())
		if p.Mode != Dual || p.LatestSource != "calm:v1:vcs:git:diff:HEAD:--:src/foo.go" {
			t.Errorf("pathspec must sit after the literal -- segment: %+v", p)
		}
		// A ref list flattening to the same tokens must NOT alias with it.
		q, _ := DerivePlan(inv("git diff HEAD src/foo.go", 3), okResult())
		if q.LatestSource == p.LatestSource {
			t.Errorf("ref list aliased with ref+pathspec: %q", q.LatestSource)
		}
	})
}

func TestDerivePlan_OutputAffectingFlagsCoexist(t *testing.T) {
	flagFree, _ := DerivePlan(inv("grep TODO src", 1), okResult())

	for _, command := range []string{
		"grep -q TODO src", "ls -la src", "cat -n foo.py", "go test -v ./...", "git diff --stat",
		"head -n 20 foo.py", // value-consuming flag must not label this read:20
	} {
		p, err := DerivePlan(inv(command, 1), okResult())
		if err != nil {
			t.Fatalf("DerivePlan(%q): %v", command, err)
		}
		if p.Mode != Coexist || p.LatestSource != "" {
			t.Errorf("%q: mode=%s latest=%q; want coexist with no latest", command, p.Mode, p.LatestSource)
		}
	}

	// The quiet variant must not share the flag-free replace label — its empty
	// output would otherwise overwrite the prior content.
	quiet, _ := DerivePlan(inv("grep -q TODO src", 1), okResult())
	if quiet.HistorySource == flagFree.LatestSource {
		t.Errorf("quiet grep collides with flag-free label: %q", quiet.HistorySource)
	}
}

func TestDerivePlan_EscapingPathArgsGoCoexist(t *testing.T) {
	for _, command := range []string{"find ../outside", "grep TODO /etc", "ls ../.."} {
		p, err := DerivePlan(inv(command, 1), okResult())
		if err != nil {
			t.Fatalf("DerivePlan(%q): %v", command, err)
		}
		if p.Mode != Coexist {
			t.Errorf("%q: mode = %s; want coexist", command, p.Mode)
		}
	}
}

func TestWsRel_NoAnchorAndNoRoot(t *testing.T) {
	t.Run("relative_no_anchor_ok", func(t *testing.T) {
		p, _ := DerivePlan(Invocation{Command: "cat foo.py", Seq: 1}, okResult())
		if p.LatestSource != "calm:v1:file:read:foo.py" {
			t.Errorf("latest = %q", p.LatestSource)
		}
	})
	t.Run("relative_escape_no_anchor_coexist", func(t *testing.T) {
		p, _ := DerivePlan(Invocation{Command: "cat ../foo.py", Seq: 1}, okResult())
		if p.Mode != Coexist {
			t.Errorf("mode = %s; want coexist", p.Mode)
		}
	})
	t.Run("absolute_no_root_coexist", func(t *testing.T) {
		p, _ := DerivePlan(Invocation{Command: "cat /etc/hosts", Seq: 1}, okResult())
		if p.Mode != Coexist {
			t.Errorf("mode = %s; want coexist", p.Mode)
		}
	})
}

func requireEvent(t *testing.T, events []calm.EventInput, typ string) calm.EventInput {
	t.Helper()
	for _, e := range events {
		if e.Type == typ {
			return e
		}
	}
	t.Fatalf("event %q not found in %+v", typ, events)
	return calm.EventInput{}
}

func FuzzDerivePlan(f *testing.F) {
	seeds := []string{
		"", "cat foo.py", "git diff HEAD", "go test ./...", "grep TODO .",
		"cat foo | grep bar", "ls", "weird $(cmd) thing", `cat "unterminated`,
		"git", "git frobnicate", "  ", "../../etc/passwd",
	}
	for _, s := range seeds {
		f.Add(s, int64(1), "/work", "/work")
	}
	f.Fuzz(func(t *testing.T, command string, seq int64, cwd, root string) {
		p, err := DerivePlan(Invocation{Seq: seq, Command: command, Cwd: cwd, WorkspaceRoot: root}, ExecResult{ExitCode: 1, Stderr: command})
		if err != nil {
			return // untranslatable is a valid outcome
		}
		if p.LatestSource == "" && p.HistorySource == "" {
			t.Fatalf("translatable plan with no source: %q", command)
		}
		ev := FinalizeEvents(p, []WriteOutcome{{Source: p.LatestSource, Persisted: true}})
		if len(ev) == 0 {
			t.Fatalf("no events for %q", command)
		}
	})
}
