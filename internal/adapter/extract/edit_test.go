// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"strings"
	"testing"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

// The load-bearing aliasing pin: an edit's latest label must be byte-identical
// to the read label for the same path — the shared identity is what makes
// read-after-edit dedup work. Includes an over-length path so the
// hash-collapse budget (reserve 0 on both routes) is exercised too.
func TestPlanFileEdit_LatestAliasesWithRead(t *testing.T) {
	inv := structuredInv(3)
	for _, path := range []string{
		"src/app.go",
		"deep/" + strings.Repeat("d", 220) + "/file.go",
	} {
		edit := PlanFileEdit(inv, ExecResult{}, path, "old", "new")
		read := PlanFileRead(inv, ExecResult{}, path)
		if edit.LatestSource != read.LatestSource {
			t.Errorf("path %q: edit latest %q != read latest %q", path, edit.LatestSource, read.LatestSource)
		}
	}
}

func TestPlanFileMutation_LabelsModeAndHints(t *testing.T) {
	p := PlanFileEdit(structuredInv(7), ExecResult{}, "src/app.go", "a", "b")
	if p.Mode != Dual {
		t.Errorf("mode = %v; want dual", p.Mode)
	}
	if p.LatestSource != "calm:v1:file:read:src/app.go" {
		t.Errorf("latest = %q", p.LatestSource)
	}
	if p.HistorySource != "calm:v1:file:edit:src/app.go#7" {
		t.Errorf("history = %q", p.HistorySource)
	}
	if p.ContentType != "code" {
		t.Errorf("content type = %q; want code", p.ContentType)
	}

	p = PlanFileWrite(structuredInv(2), ExecResult{}, "data.json", OperationCreate, "", `{"a":1}`)
	if p.Format != calm.FormatJSON {
		t.Errorf("format = %q; want json", p.Format)
	}
	if p.base.fileTouched.operation != OperationCreate {
		t.Errorf("operation = %q; want create", p.base.fileTouched.operation)
	}
	if p.base.toolName != "calm_write_file" {
		t.Errorf("tool = %q; want calm_write_file", p.base.toolName)
	}
}

// Escape paths keep the mutation event but label under the program-equivalent
// coexist bucket (LABELING.md §4).
func TestPlanFileMutation_EscapeCoexistsWithEvent(t *testing.T) {
	p := PlanFileEdit(structuredInv(4), ExecResult{}, "../outside.txt", "a", "b")
	if p.Mode != Coexist || p.HistorySource != "calm:v1:shell:sed#4" {
		t.Errorf("plan = %+v; want coexist shell:sed#4", p)
	}
	if p.base.fileTouched == nil || p.base.fileTouched.operation != OperationEdit {
		t.Fatalf("fileTouched facts missing on escape fallback: %+v", p.base.fileTouched)
	}

	p = PlanFileWrite(structuredInv(5), ExecResult{}, "/abs/out.txt", OperationWrite, "a", "b")
	if p.HistorySource != "calm:v1:shell:tee#5" {
		t.Errorf("write escape history = %q; want shell:tee#5", p.HistorySource)
	}
}

func TestFinalizeEvents_FileTouchedPayloadAndGating(t *testing.T) {
	p := PlanFileEdit(structuredInv(9), ExecResult{}, "f.txt", "hello\nold\n", "hello\nnew\n")

	// Latest failed, history persisted: the event keeps only the history link.
	evs := FinalizeEvents(p, []WriteOutcome{
		{Source: p.HistorySource, Persisted: true},
		{Source: p.LatestSource, Persisted: false},
	})
	var ft map[string]any
	for _, e := range evs {
		if e.Type == EventFileTouched {
			if e.Priority != 1 {
				t.Errorf("priority = %d; want 1", e.Priority)
			}
			ft = e.Data
		}
	}
	if ft == nil {
		t.Fatalf("no file_touched event in %+v", evs)
	}
	if ft[keyPath] != "f.txt" || ft[keyOperation] != "edit" || ft[keyInvocationID] != int64(9) {
		t.Errorf("payload = %+v", ft)
	}
	diff, _ := ft[keyDiff].(string)
	if !strings.Contains(diff, "-old") || !strings.Contains(diff, "+new") || !strings.Contains(diff, "a/f.txt") {
		t.Errorf("diff = %q; want unified -old/+new with a/ header", diff)
	}
	if _, has := ft[keyLatestSource]; has {
		t.Errorf("latest_source present despite failed persist")
	}
	if ft[keyHistorySource] != p.HistorySource {
		t.Errorf("history_source = %v; want %q", ft[keyHistorySource], p.HistorySource)
	}
}

// An idempotent write carries no diff key; a create diffs from empty content.
func TestFileTouched_DiffEdgeCases(t *testing.T) {
	p := PlanFileWrite(structuredInv(1), ExecResult{}, "same.txt", OperationWrite, "unchanged\n", "unchanged\n")
	evs := FinalizeEvents(p, nil)
	for _, e := range evs {
		if e.Type == EventFileTouched {
			if _, has := e.Data[keyDiff]; has {
				t.Errorf("idempotent write must omit the diff key: %+v", e.Data)
			}
		}
	}

	p = PlanFileWrite(structuredInv(1), ExecResult{}, "new.txt", OperationCreate, "", "line one\nline two\n")
	d := p.base.fileTouched.diff
	if !strings.Contains(d, "+line one") || !strings.Contains(d, "+line two") {
		t.Errorf("create diff = %q; want additions for both lines", d)
	}
	for _, line := range strings.Split(d, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Errorf("create diff has a removal line %q; want all-additions", line)
		}
	}
}

func TestSanitizeDiff(t *testing.T) {
	// Redact-before-truncate: a secret near the head survives redaction even
	// when the diff is longer than the cap.
	long := "+--password=hunter2\n" + strings.Repeat("+padding line\n", 300)
	got, truncated := sanitizeDiff(long)
	if !truncated {
		t.Errorf("expected truncation at %d bytes", maxDiffBytes)
	}
	if len(got) > maxDiffBytes {
		t.Errorf("len = %d; want <= %d", len(got), maxDiffBytes)
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("secret survived sanitization: %q", got[:80])
	}
	if !strings.HasPrefix(got, "+--password=<redacted>") {
		t.Errorf("head not kept; got prefix %q", got[:40])
	}

	// Control bytes stripped; CRLF renders as LF inside the event diff.
	got, _ = sanitizeDiff("+line\r\n")
	if strings.Contains(got, "\r") {
		t.Errorf("carriage return survived: %q", got)
	}
}
