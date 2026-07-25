// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/one-harsh/calm/internal/adapter/extract"
)

// WorkspaceBinding is one discovered workspace: ID appears as the label
// context segment on non-primary captures (LABELING.md §2); Root anchors path
// resolution and label normalization — labeling-only, not a sandbox
// (DESIGN.md §5).
type WorkspaceBinding struct {
	ID   string
	Root string
}

// resolve maps a tool path argument into this workspace: absolute paths pass
// through, relative paths join the root.
func (b WorkspaceBinding) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(b.Root, path)
}

// vcsMarkers anchor working repositories; a .git FILE (not just dir) marks
// submodules/worktrees, which are legitimately distinct workspaces.
var vcsMarkers = []string{".git", ".hg", ".svn"}

// manifestMarkers anchor dependency stores (module caches, node_modules,
// registry sources), which are never under VCS. Consulted only when no VCS
// ancestor exists — VCS-first keeps in-repo manifests (test fixtures,
// examples/) from fragmenting a repository's identity, and keeps a manifest
// created mid-session from re-anchoring a subtree. Curated, extensible —
// implementer policy per LABELING.md §2.
var manifestMarkers = []string{
	"go.mod", "package.json", "pyproject.toml", "setup.py",
	"Cargo.toml", "pom.xml", "build.gradle", "Gemfile", "composer.json",
}

// anchorOf resolves a directory's project anchor per DESIGN.md §5: the
// deepest VCS-marker ancestor (ignoring a marker at the user home directory
// or filesystem root — a home-level repository is a dotfiles setup, not a
// project boundary), else the deepest manifest ancestor, else none.
func anchorOf(dir string) (string, bool) {
	home, _ := os.UserHomeDir()
	manifestAnchor := ""
	for d := filepath.Clean(dir); ; {
		if d != home && !isFSRoot(d) && hasAnyMarker(d, vcsMarkers) {
			return d, true
		}
		if manifestAnchor == "" && hasAnyMarker(d, manifestMarkers) {
			manifestAnchor = d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if manifestAnchor != "" {
		return manifestAnchor, true
	}
	return "", false
}

func isFSRoot(d string) bool {
	return filepath.Dir(d) == d
}

func hasAnyMarker(dir string, markers []string) bool {
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// workspaceSet is the session's workspace registry. Discovery is monotonic:
// bindings are appended on first touch and never removed or renamed
// (DESIGN.md §5). Index 0 is the primary — the launch directory's anchor —
// whose captures label bare.
type workspaceSet struct {
	mu         sync.Mutex
	bindings   []WorkspaceBinding
	usedIDs    map[string]bool
	dirAnchors map[string]string // dir → anchor root ("" = no anchor) memo
}

func newWorkspaceSet(launchDir string) *workspaceSet {
	root := launchDir
	if a, ok := anchorOf(launchDir); ok {
		root = a
	}
	if root == "" {
		// Harness parity: an empty launch dir yields one anonymous binding.
		w := &workspaceSet{usedIDs: map[string]bool{}, dirAnchors: map[string]string{}}
		w.bindings = []WorkspaceBinding{{}}
		return w
	}
	root = filepath.Clean(root)
	w := &workspaceSet{usedIDs: map[string]bool{}, dirAnchors: map[string]string{}}
	id := filepath.Base(root)
	w.bindings = []WorkspaceBinding{{ID: id, Root: root}}
	w.usedIDs[id] = true
	return w
}

func (w *workspaceSet) primary() WorkspaceBinding {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bindings[0]
}

func (w *workspaceSet) byID(id string) (WorkspaceBinding, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range w.bindings {
		if b.ID == id {
			return b, true
		}
	}
	return WorkspaceBinding{}, false
}

func (w *workspaceSet) ids() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.bindings))
	for i, b := range w.bindings {
		out[i] = b.ID
	}
	return out
}

// byPathOrDiscover routes an absolute path to the workspace of its own
// project anchor, minting the binding on first touch. Anchor-first, never
// containment-first: a path's workspace is a deterministic function of the
// filesystem, so labels are discovery-order-independent and a nested anchor
// (a submodule inside an already-known repository) is its own workspace.
// Unanchored paths return the primary — either the path lives under an
// unmarked launch directory (the only binding an unanchored path can sit
// inside), or label normalization's escape check yields coexist.
func (w *workspaceSet) byPathOrDiscover(abs string) WorkspaceBinding {
	abs = filepath.Clean(abs)
	w.mu.Lock()
	defer w.mu.Unlock()

	dir := abs
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		dir = filepath.Dir(abs)
	}
	root, memoized := w.dirAnchors[dir]
	if !memoized {
		if a, ok := anchorOf(dir); ok {
			root = a
		}
		w.dirAnchors[dir] = root
	}
	if root == "" {
		return w.bindings[0]
	}
	for _, b := range w.bindings {
		if b.Root == root {
			return b
		}
	}

	id := filepath.Base(root)
	if w.usedIDs[id] {
		sum := sha256.Sum256([]byte(root))
		id = id + "-" + hex.EncodeToString(sum[:4])
	}
	w.usedIDs[id] = true
	b := WorkspaceBinding{ID: id, Root: root}
	w.bindings = append(w.bindings, b)
	return b
}

// selectWorkspace resolves the optional `workspace` tool argument: empty →
// primary; unknown → ArgError naming the session's known IDs.
func (s *Server) selectWorkspace(explicitID string) (WorkspaceBinding, error) {
	if explicitID == "" {
		return s.workspaces.primary(), nil
	}
	b, ok := s.workspaces.byID(explicitID)
	if !ok {
		return WorkspaceBinding{}, &ArgError{
			Detail: "unknown workspace " + explicitID + "; known: " + strings.Join(s.workspaces.ids(), ", "),
		}
	}
	return b, nil
}

// workspaceForPath resolves a structured tool's path argument: explicit
// workspace arg wins; an absolute path routes (and discovers) by containment;
// relative paths anchor to the primary.
func (s *Server) workspaceForPath(explicitID, path string) (WorkspaceBinding, error) {
	if explicitID != "" {
		return s.selectWorkspace(explicitID)
	}
	if filepath.IsAbs(path) {
		return s.workspaces.byPathOrDiscover(path), nil
	}
	return s.workspaces.primary(), nil
}

// workspaceForCwd routes calm_run_command's cwd to a binding plus the
// effective execution dir. Empty → primary at its root; otherwise the dir
// (resolved against the primary root when relative) routes by containment
// with discovery on first touch — outside every anchor the command still
// runs there and label normalization's escape check yields coexist.
func (s *Server) workspaceForCwd(cwd string) (WorkspaceBinding, string) {
	primary := s.workspaces.primary()
	if cwd == "" {
		return primary, primary.Root
	}
	dir := primary.resolve(cwd)
	b := s.workspaces.byPathOrDiscover(dir)
	return b, dir
}

// invocation mints the per-call extract.Invocation for a selected binding.
// WorkspaceID is populated only for non-primary workspaces, so primary labels
// stay bare regardless of how many workspaces the session has discovered
// (LABELING.md §2 — late discovery never mutates existing label meaning).
func (s *Server) invocation(seq int64, b WorkspaceBinding, command, cwd string) extract.Invocation {
	inv := extract.Invocation{Seq: seq, Command: command, Cwd: cwd, WorkspaceRoot: b.Root}
	if b.Root != s.workspaces.primary().Root {
		inv.WorkspaceID = b.ID
	}
	return inv
}
