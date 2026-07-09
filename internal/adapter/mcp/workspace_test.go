// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	logging "github.com/one-harsh/context-logging"
)

func mkDirWS(t *testing.T, parent string, elems ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{parent}, elems...)...)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func gitRepoWS(t *testing.T, parent, name string) string {
	t.Helper()
	dir := mkDirWS(t, parent, name)
	mkDirWS(t, dir, ".git")
	return dir
}

func touchWS(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatalf("touch %s: %v", name, err)
	}
}

func TestAnchorOf_VCSFirst(t *testing.T) {
	base := t.TempDir()
	repo := gitRepoWS(t, base, "repo")
	nested := mkDirWS(t, repo, "pkg", "deep")
	sub := mkDirWS(t, repo, "sub")
	touchWS(t, sub, ".git")
	fixtures := mkDirWS(t, repo, "testdata")
	touchWS(t, fixtures, "package.json")
	store := mkDirWS(t, base, "store")
	touchWS(t, store, "go.mod")
	lib := mkDirWS(t, store, "lib")
	touchWS(t, lib, "go.mod")
	plain := mkDirWS(t, base, "plain")

	cases := []struct {
		name string
		dir  string
		want string
		ok   bool
	}{
		{"repo root", repo, repo, true},
		{"nested dir anchors at the repo", nested, repo, true},
		{"a .git file marks a submodule as its own anchor", sub, sub, true},
		{"in-repo manifest noise loses to the VCS root", fixtures, repo, true},
		{"manifest-only store anchors without VCS", lib, lib, true},
		{"deepest manifest wins", mkDirWS(t, lib, "internal"), lib, true},
		{"unmarked dir has no anchor", plain, "", false},
	}
	for _, c := range cases {
		got, ok := anchorOf(c.dir)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: anchorOf(%q) = %q, %v; want %q, %v", c.name, c.dir, got, ok, c.want, c.ok)
		}
	}
}

func TestAnchorOf_HomeGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	mkDirWS(t, home, ".git")
	proj := gitRepoWS(t, home, "proj")
	plain := mkDirWS(t, home, "plain")

	if got, ok := anchorOf(proj); !ok || got != proj {
		t.Errorf("repo under home = %q, %v; want the repo itself", got, ok)
	}
	if got, ok := anchorOf(plain); ok {
		t.Errorf("home dotfiles marker anchored %q at %q; want none", plain, got)
	}
}

func TestWorkspaceSet_DiscoveryMonotonic(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoWS(t, base, "repoa")
	repob := gitRepoWS(t, base, "repob")
	plain := mkDirWS(t, base, "plain")

	w := newWorkspaceSet(repoa)
	if p := w.primary(); p.ID != "repoa" || p.Root != repoa {
		t.Fatalf("primary = %+v; want repoa at its root", p)
	}

	b := w.byPathOrDiscover(filepath.Join(repob, "foo.py"))
	if b.ID != "repob" || b.Root != repob {
		t.Fatalf("discovery = %+v; want repob", b)
	}
	again := w.byPathOrDiscover(filepath.Join(repob, "other", "bar.py"))
	if again != b {
		t.Errorf("re-touch minted a new binding: %+v vs %+v", again, b)
	}
	if got := w.ids(); strings.Join(got, ",") != "repoa,repob" {
		t.Errorf("ids = %v; want monotonic [repoa repob]", got)
	}

	if esc := w.byPathOrDiscover(filepath.Join(plain, "x.txt")); esc.ID != "repoa" {
		t.Errorf("unanchored path = %+v; want primary (label escape yields coexist)", esc)
	}
	if got := w.ids(); len(got) != 2 {
		t.Errorf("unanchored touch grew the registry: %v", got)
	}
}

func TestWorkspaceSet_CollisionSuffix(t *testing.T) {
	base := t.TempDir()
	first := gitRepoWS(t, mkDirWS(t, base, "one"), "dup")
	second := gitRepoWS(t, mkDirWS(t, base, "two"), "dup")

	w := newWorkspaceSet(t.TempDir())
	b1 := w.byPathOrDiscover(filepath.Join(first, "a.go"))
	b2 := w.byPathOrDiscover(filepath.Join(second, "b.go"))
	if b1.ID != "dup" {
		t.Errorf("first discovery = %q; want the bare basename", b1.ID)
	}
	if b2.ID == b1.ID || !strings.HasPrefix(b2.ID, "dup-") {
		t.Errorf("collision = %q vs %q; want a distinct suffixed ID", b2.ID, b1.ID)
	}
	if again := w.byPathOrDiscover(filepath.Join(second, "c.go")); again.ID != b2.ID {
		t.Errorf("suffixed ID not stable: %q vs %q", again.ID, b2.ID)
	}
}

// Anchor resolution is per-path, never containment-first: a nested anchor
// (submodule inside an already-known repo) is its own workspace regardless of
// touch order, and name-prefix siblings never claim each other.
func TestWorkspaceSet_NestedAndSiblingAnchors(t *testing.T) {
	base := t.TempDir()
	ab := gitRepoWS(t, base, "ab")
	abc := gitRepoWS(t, base, "abc")

	w := newWorkspaceSet(t.TempDir())
	if b := w.byPathOrDiscover(filepath.Join(ab, "f.go")); b.ID != "ab" {
		t.Fatalf("ab discovery = %+v", b)
	}
	if b := w.byPathOrDiscover(filepath.Join(abc, "f.go")); b.ID != "abc" {
		t.Errorf("prefix sibling = %q; want its own workspace abc", b.ID)
	}

	// A submodule under the primary is its own workspace even though the
	// primary root contains it.
	outer := gitRepoWS(t, base, "outer")
	sub := mkDirWS(t, outer, "sub")
	touchWS(t, sub, ".git")
	w2 := newWorkspaceSet(outer)
	if b := w2.byPathOrDiscover(filepath.Join(outer, "top.go")); b.ID != "outer" {
		t.Fatalf("primary path = %+v; want primary outer", b)
	}
	if b := w2.byPathOrDiscover(filepath.Join(sub, "inner.go")); b.ID != "sub" || b.Root != sub {
		t.Errorf("submodule path = %+v; want its own workspace sub", b)
	}

	// Launching in the submodule instead: its paths stay on the primary even
	// after the enclosing repo is discovered.
	w3 := newWorkspaceSet(sub)
	if b := w3.byPathOrDiscover(filepath.Join(outer, "top.go")); b.ID != "outer" {
		t.Fatalf("enclosing repo discovery = %+v", b)
	}
	if b := w3.byPathOrDiscover(filepath.Join(sub, "inner.go")); b.ID != "sub" {
		t.Errorf("nested path = %q; want the primary submodule", b.ID)
	}

	// An unmarked launch dir still claims its own unanchored paths.
	plain := mkDirWS(t, base, "plainlaunch")
	w4 := newWorkspaceSet(plain)
	if b := w4.byPathOrDiscover(filepath.Join(plain, "x.txt")); b.Root != plain {
		t.Errorf("unmarked-launch path = %+v; want the primary launch dir", b)
	}
}

func TestSelectWorkspace_DefaultAndUnknown(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoWS(t, base, "repoa")
	repob := gitRepoWS(t, base, "repob")
	s := NewServer(Config{Logger: logging.Nop(), SessionTTLMinutes: 60, LaunchDir: repoa})
	s.workspaces.byPathOrDiscover(filepath.Join(repob, "x.go"))

	b, err := s.selectWorkspace("")
	if err != nil || b.ID != "repoa" {
		t.Errorf("default = %+v, %v; want primary repoa", b, err)
	}
	_, err = s.selectWorkspace("zzz")
	if err == nil || !strings.Contains(err.Error(), "repoa, repob") {
		t.Errorf("unknown-id error = %v; want the discovered IDs listed", err)
	}
}

func TestWorkspaceForPath(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoWS(t, base, "repoa")
	repob := gitRepoWS(t, base, "repob")
	s := NewServer(Config{Logger: logging.Nop(), SessionTTLMinutes: 60, LaunchDir: repoa})

	if b, err := s.workspaceForPath("", "rel/file.go"); err != nil || b.ID != "repoa" {
		t.Errorf("relative = %+v, %v; want primary", b, err)
	}
	if b, err := s.workspaceForPath("", filepath.Join(repob, "f.go")); err != nil || b.ID != "repob" {
		t.Errorf("absolute foreign = %+v, %v; want discovered repob", b, err)
	}
	if b, err := s.workspaceForPath("repob", "rel/file.go"); err != nil || b.ID != "repob" {
		t.Errorf("explicit arg = %+v, %v; want repob", b, err)
	}
}

func TestWorkspaceForCwd(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoWS(t, base, "repoa")
	repob := gitRepoWS(t, base, "repob")
	plain := mkDirWS(t, base, "plain")
	s := NewServer(Config{Logger: logging.Nop(), SessionTTLMinutes: 60, LaunchDir: repoa})

	if b, dir := s.workspaceForCwd(""); b.ID != "repoa" || dir != repoa {
		t.Errorf("empty cwd = %q,%q; want primary at its root", b.ID, dir)
	}
	nested := filepath.Join(repob, "nested")
	if b, dir := s.workspaceForCwd(nested); b.ID != "repob" || dir != nested {
		t.Errorf("foreign cwd = %q,%q; want discovered repob", b.ID, dir)
	}
	// Relative cwd resolves against the primary root, then routes — ../repob
	// lands in workspace repob.
	if b, dir := s.workspaceForCwd(filepath.Join("..", "repob")); b.ID != "repob" || dir != repob {
		t.Errorf("relative cwd = %q,%q; want routed to repob", b.ID, dir)
	}
	// Outside every anchor: runs there, primary binding (labels will coexist).
	if b, dir := s.workspaceForCwd(plain); b.ID != "repoa" || dir != plain {
		t.Errorf("escape cwd = %q,%q; want primary binding at the given dir", b.ID, dir)
	}
	// A submodule cwd inside the primary routes to its own workspace — its
	// captures must not reuse the outer repo's bare labels.
	sub := mkDirWS(t, repoa, "sub")
	touchWS(t, sub, ".git")
	if b, dir := s.workspaceForCwd(sub); b.ID != "sub" || dir != sub {
		t.Errorf("submodule cwd = %q,%q; want its own workspace", b.ID, dir)
	}
}

// The single-place population rule: primary labels bare forever; only
// non-primary bindings stamp WorkspaceID (late discovery never mutates
// existing label meaning).
func TestInvocation_PrimaryBare(t *testing.T) {
	base := t.TempDir()
	repoa := gitRepoWS(t, base, "repoa")
	repob := gitRepoWS(t, base, "repob")
	s := NewServer(Config{Logger: logging.Nop(), SessionTTLMinutes: 60, LaunchDir: repoa})

	if inv := s.invocation(s.workspaces.primary(), "", repoa); inv.WorkspaceID != "" {
		t.Errorf("primary inv WorkspaceID = %q; want empty before discovery", inv.WorkspaceID)
	}
	b := s.workspaces.byPathOrDiscover(filepath.Join(repob, "f.go"))
	if inv := s.invocation(b, "", repob); inv.WorkspaceID != "repob" {
		t.Errorf("discovered inv WorkspaceID = %q; want repob", inv.WorkspaceID)
	}
	if inv := s.invocation(s.workspaces.primary(), "", repoa); inv.WorkspaceID != "" {
		t.Errorf("primary inv WorkspaceID = %q; want bare even after discovery", inv.WorkspaceID)
	}
}
