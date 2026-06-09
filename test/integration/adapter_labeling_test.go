// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/extract"
)

func adapterSession(t *testing.T) (calm.Client, string) {
	t.Helper()
	idem := fmt.Sprintf("wi37b-%s-%d", t.Name(), time.Now().UnixNano())
	c, err := calm.NewGenapiClient(env.serverURL, testMasterKey, idem, nil)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	// Empty client → server applies its default; the labeling contract is
	// independent of client identity, so this stays decoupled from client seeding.
	token, err := c.CreateSession(context.Background(), "", testDefaultTTLMinutes)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteSession(context.Background(), token) })
	return c, token
}

func ingestUnder(t *testing.T, c calm.Client, token, source, contentType, content string) {
	t.Helper()
	if _, err := c.Ingest(context.Background(), token, calm.IngestInput{
		Source:      source,
		Content:     content,
		ContentType: contentType,
	}); err != nil {
		t.Fatalf("Ingest(%q): %v", source, err)
	}
}

func hitCount(t *testing.T, c calm.Client, token, source, query string) int {
	t.Helper()
	res, err := c.Search(context.Background(), token, calm.SearchInput{
		Queries: []string{query},
		Source:  source,
	})
	if err != nil {
		t.Fatalf("Search(source=%q, q=%q): %v", source, query, err)
	}
	if len(res.Queries) == 0 {
		return 0
	}
	return len(res.Queries[0].Hits)
}

func TestAdapterLabeling_DualPreservesDistinctHistory(t *testing.T) {
	c, token := adapterSession(t)
	root := "/work"

	plan1, err := extract.DerivePlan(
		extract.Invocation{Seq: 1, Command: "git diff HEAD", Cwd: root, WorkspaceRoot: root},
		extract.ExecResult{Stdout: "diff one", ExitCode: 0},
	)
	if err != nil {
		t.Fatalf("DerivePlan v1: %v", err)
	}
	plan2, err := extract.DerivePlan(
		extract.Invocation{Seq: 2, Command: "git diff HEAD", Cwd: root, WorkspaceRoot: root},
		extract.ExecResult{Stdout: "diff two", ExitCode: 0},
	)
	if err != nil {
		t.Fatalf("DerivePlan v2: %v", err)
	}

	if plan1.Mode != extract.Dual {
		t.Fatalf("mode = %s; want dual", plan1.Mode)
	}
	if plan1.HistorySource == plan2.HistorySource {
		t.Fatalf("history sources collided: %q", plan1.HistorySource)
	}
	if plan1.LatestSource != plan2.LatestSource {
		t.Fatalf("latest sources should be the same semantic identity: %q vs %q", plan1.LatestSource, plan2.LatestSource)
	}

	ingestUnder(t, c, token, plan1.HistorySource, plan1.ContentType, "git diff\n\nalphamarker changed in feature one")
	ingestUnder(t, c, token, plan1.LatestSource, plan1.ContentType, "git diff\n\nalphamarker changed in feature one")
	ingestUnder(t, c, token, plan2.HistorySource, plan2.ContentType, "git diff\n\nbetamarker changed in feature two")
	ingestUnder(t, c, token, plan2.LatestSource, plan2.ContentType, "git diff\n\nbetamarker changed in feature two")

	if n := hitCount(t, c, token, plan1.HistorySource, "alphamarker"); n == 0 {
		t.Errorf("invocation-1 history not retrievable under %q", plan1.HistorySource)
	}
	if n := hitCount(t, c, token, plan2.HistorySource, "betamarker"); n == 0 {
		t.Errorf("invocation-2 history not retrievable under %q", plan2.HistorySource)
	}

	if n := hitCount(t, c, token, plan1.LatestSource, "betamarker"); n == 0 {
		t.Errorf("latest source missing newest content under %q", plan1.LatestSource)
	}
	if n := hitCount(t, c, token, plan1.LatestSource, "alphamarker"); n != 0 {
		t.Errorf("latest source still holds stale content (%d hits for alphamarker)", n)
	}

	events := extract.FinalizeEvents(plan2, []extract.WriteOutcome{
		{Source: plan2.LatestSource, Persisted: true},
		{Source: plan2.HistorySource, Persisted: true},
	})
	if len(events) == 0 {
		t.Fatal("FinalizeEvents returned no events")
	}
	if err := c.WriteEvents(context.Background(), token, events); err != nil {
		t.Fatalf("WriteEvents: %v", err)
	}
}

func TestAdapterLabeling_ReplaceDedupsOnReread(t *testing.T) {
	c, token := adapterSession(t)
	root := "/work"

	plan, err := extract.DerivePlan(
		extract.Invocation{Seq: 1, Command: "cat foo.py", Cwd: root, WorkspaceRoot: root},
		extract.ExecResult{ExitCode: 0},
	)
	if err != nil {
		t.Fatalf("DerivePlan: %v", err)
	}
	if plan.Mode != extract.Replace || plan.LatestSource != "calm:v1:file:read:foo.py" {
		t.Fatalf("unexpected plan: mode=%s latest=%q", plan.Mode, plan.LatestSource)
	}
	if plan.HistorySource != "" {
		t.Fatalf("replace mode must not produce a history source: %q", plan.HistorySource)
	}

	ingestUnder(t, c, token, plan.LatestSource, plan.ContentType, "package main\n\nversionone marker")
	ingestUnder(t, c, token, plan.LatestSource, plan.ContentType, "package main\n\nversiontwo marker")

	if n := hitCount(t, c, token, plan.LatestSource, "versionone"); n != 0 {
		t.Errorf("stale content survived replace (%d hits for versionone)", n)
	}
	if n := hitCount(t, c, token, plan.LatestSource, "versiontwo"); n == 0 {
		t.Errorf("latest content not retrievable under %q", plan.LatestSource)
	}
}
