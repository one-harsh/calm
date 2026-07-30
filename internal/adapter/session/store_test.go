// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	logging "github.com/one-harsh/context-logging"
)

func saveFixture(t *testing.T, s *store, mutate func(*state)) {
	t.Helper()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	st := newState("conv-1", "claude-code", time.Now())
	if mutate != nil {
		mutate(st)
	}
	if err := s.save(st); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func TestState_PartialWriteLeavesPriorIntact(t *testing.T) {
	s := newStore(t.TempDir(), "conv-1")
	saveFixture(t, s, func(st *state) { st.Seq = 5 })

	orphan := s.statePath() + ".tmp.99999"
	if err := os.WriteFile(orphan, []byte("{ partial write, never renamed"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}

	got, err := s.load()
	if err != nil {
		t.Fatalf("load after partial write: %v", err)
	}
	if got.Seq != 5 {
		t.Errorf("seq = %d; want 5 (prior state intact)", got.Seq)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("orphan temp missing (GC's job to reap, not load's): %v", err)
	}
}

// The state file holds the session token, so the directory and file are
// owner-only.
func TestState_FilePermissionsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	s := newStore(t.TempDir(), "conv-1")
	saveFixture(t, s, nil)

	dirInfo, err := os.Stat(s.dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o; want 700", perm)
	}
	fileInfo, err := os.Stat(s.statePath())
	if err != nil {
		t.Fatalf("stat state: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("state.json perm = %o; want 600", perm)
	}
}

func TestStateDir_ComposesUnderRootAndSanitizesSessionID(t *testing.T) {
	root := t.TempDir()
	m, err := New(Config{SessionID: "abc-123", RootDir: root, Logger: logging.Nop()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := filepath.Join(root, "sessions", "abc-123"); m.store.dir != want {
		t.Errorf("dir = %q; want %q (safe id verbatim under the resolved root)", m.store.dir, want)
	}

	m2, err := New(Config{SessionID: "../../etc/passwd", RootDir: root, Logger: logging.Nop()})
	if err != nil {
		t.Fatalf("New hostile: %v", err)
	}
	if parent := filepath.Dir(m2.store.dir); parent != filepath.Join(root, "sessions") {
		t.Errorf("hostile id escaped sessions root: %q", m2.store.dir)
	}
	if name := filepath.Base(m2.store.dir); strings.ContainsAny(name, `/\.`) || len(name) != 64 {
		t.Errorf("hostile id not hashed to a safe name: %q", name)
	}
}

// A future-format state file is refused, never rewritten — a v1 binary saving
// a v2 record back through this schema would silently erase fields it doesn't
// know.
func TestLoad_FutureVersionRefused(t *testing.T) {
	s := &store{dir: filepath.Join(t.TempDir(), "sessions", "conv-1")}
	saveFixture(t, s, func(st *state) { st.Version = stateVersion + 1 })
	if _, err := s.load(); err == nil {
		t.Fatal("future-version state loaded; want refusal")
	}
}

// A null registry in a hand-edited state file hydrates to an empty map rather
// than a nil-map write panic downstream.
func TestLoad_NilRegistryNormalized(t *testing.T) {
	s := &store{dir: filepath.Join(t.TempDir(), "sessions", "conv-1")}
	saveFixture(t, s, func(st *state) { st.Registry = nil })
	st, err := s.load()
	if err != nil || st == nil {
		t.Fatalf("load: %v, %v", st, err)
	}
	st.Registry["k"] = "v"
}

// A pre-existing session directory with broader modes is tightened to
// owner-only on the next lock acquisition — MkdirAll alone leaves old modes.
func TestState_ExistingDirModeTightened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are advisory on Windows; owner-only rides the profile ACL")
	}
	s := &store{dir: filepath.Join(t.TempDir(), "sessions", "conv-1")}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.lock()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	fi, err := os.Stat(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o; want 0700 re-asserted", perm)
	}
}

// Distinct ids that differ only in case must map to distinct directory names
// even under case-insensitive comparison — NTFS and default APFS would
// otherwise hand two conversations one state directory.
func TestSanitizeSessionID_CaseVariantsStayDistinctCaseFolded(t *testing.T) {
	if sanitizeSessionID("conv-a") != "conv-a" {
		t.Errorf("lowercase safe id should pass verbatim")
	}
	seen := map[string]string{}
	for _, id := range []string{"conv-a", "Conv-A", "CONV-A"} {
		folded := strings.ToLower(sanitizeSessionID(id))
		if prior, ok := seen[folded]; ok {
			t.Fatalf("ids %q and %q collide case-insensitively on %q", prior, id, folded)
		}
		seen[folded] = id
	}
}

// The derived idempotency base is prefixed and bounded even for a pathological
// session id, so it can never overflow the create key.
func TestIdempotencyBase_BoundedAndPrefixed(t *testing.T) {
	base := idempotencyBase(strings.Repeat("x", 8192))
	if !strings.HasPrefix(base, idempotencyPrefix) {
		t.Errorf("base = %q; want %q prefix", base, idempotencyPrefix)
	}
	if len(base) > 256 {
		t.Errorf("base length = %d; want <= 256", len(base))
	}
}

func TestGC_ReapsIdleDirsAndSweepsOrphanTmp(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	old := time.Now().Add(-72 * time.Hour)

	idle := filepath.Join(sessions, "idle")
	if err := os.MkdirAll(idle, 0o700); err != nil {
		t.Fatal(err)
	}
	idleState := filepath.Join(idle, stateFileName)
	if err := os.WriteFile(idleState, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(idleState, old, old); err != nil {
		t.Fatal(err)
	}

	live := filepath.Join(sessions, "live")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, stateFileName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(live, stateFileName+".tmp.123")
	if err := os.WriteFile(orphan, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}

	if err := GC(root, 60*time.Minute); err != nil {
		t.Fatalf("GC: %v", err)
	}

	if _, err := os.Stat(idle); !os.IsNotExist(err) {
		t.Errorf("idle dir not reaped")
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("live dir wrongly reaped: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphan temp not swept")
	}
}
