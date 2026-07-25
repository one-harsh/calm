// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
)

// A create rejected 4xx latches auth_failed and the latch persists to disk.
func TestEstablish_CreateRejected_LatchesAuthAndPersists(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("", status(400)).Once()

	root := t.TempDir()
	ctx := context.Background()
	if _, sig := newTestManager(t, root, "conv-1", c).Ensure(ctx); sig == nil || sig.Reason != "auth_failed" {
		t.Fatalf("ensure = %+v; want auth_failed", sig)
	}
	if !loadState(t, newTestManager(t, root, "conv-1", c)).AuthFailed {
		t.Errorf("auth latch not persisted")
	}
}

// A transient create failure degrades to calm_unreachable and persists no token,
// so the next capture retries establishment.
func TestEstablish_CreateTransient_Degrades(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Return("", errors.New("dial tcp: connection refused")).Once()

	_, sig := newTestManager(t, t.TempDir(), "conv-1", c).Ensure(context.Background())
	if sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("ensure = %+v; want calm_unreachable", sig)
	}
}

// A rejected client registration latches auth_failed before any create.
func TestRegister_Rejected_LatchesAuth(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, testClient).Return(false, status(401)).Once()

	_, sig := newTestManager(t, t.TempDir(), "conv-1", c).Ensure(context.Background())
	if sig == nil || sig.Reason != "auth_failed" {
		t.Fatalf("ensure = %+v; want auth_failed", sig)
	}
}

// A transient registration failure defers the create with calm_unreachable.
func TestRegister_Transient_Degrades(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, testClient).Return(false, errors.New("timeout")).Once()

	_, sig := newTestManager(t, t.TempDir(), "conv-1", c).Ensure(context.Background())
	if sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("ensure = %+v; want calm_unreachable", sig)
	}
}

// A 4xx on the replacement create latches auth_failed.
func TestRecover_Rejected_LatchesAuth(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("", status(400)).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "auth_failed" {
		t.Fatalf("recovery = %+v; want auth_failed", sig)
	}
	if !loadState(t, m).AuthFailed {
		t.Errorf("auth latch not persisted after rejected recovery")
	}
}

// A transient replacement create keeps the dead token on disk and does not
// persist the incremented recovery counter, so the next 404 retries with the
// same idempotency key.
func TestRecover_Transient_KeepsDeadTokenAndCounter(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Return("", errors.New("5xx")).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("recovery = %+v; want calm_unreachable", sig)
	}
	st := loadState(t, m)
	if st.SessionToken != "tok1" {
		t.Errorf("token = %q; want dead tok1 retained for retry", st.SessionToken)
	}
	if st.RecoverySeq != 0 {
		t.Errorf("recovery_seq = %d; want 0 (increment not persisted on transient)", st.RecoverySeq)
	}
}

// When another process already replaced the session, a stale failed token is
// classified session_lost without a second create.
func TestRecover_AlreadyReplaced_NoSecondCreate(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	if sig := m.OnCallError(ctx, "stale-token", status(404)); sig == nil || sig.Reason != "session_lost" {
		t.Fatalf("recovery = %+v; want session_lost", sig)
	}
}

// An unwritable state root degrades Ensure to capture_failed (CALM is fine; the
// local store is not) and Record silently no-ops.
func TestStoreUnwritable_EnsureCaptureFailed_RecordNoOp(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := calm.NewMockClient(t)
	ctx := context.Background()
	m := newTestManager(t, file, "conv-1", c)

	if _, sig := m.Ensure(ctx); sig == nil || sig.Reason != "capture_failed" {
		t.Fatalf("ensure = %+v; want capture_failed", sig)
	}
	// Best-effort: Record cannot lock the unwritable store, logs WARN, and never panics.
	m.Record(ctx, []capture.SourceToken{{Source: "s1", Token: "t1"}})
}

// A corrupt state.json degrades Ensure to capture_failed rather than crashing.
func TestEnsure_CorruptState_CaptureFailed(t *testing.T) {
	root := t.TempDir()
	s := newStore(root, "conv-1")
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.statePath(), []byte("{ not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, sig := newTestManager(t, root, "conv-1", calm.NewMockClient(t)).Ensure(context.Background())
	if sig == nil || sig.Reason != "capture_failed" {
		t.Fatalf("ensure = %+v; want capture_failed", sig)
	}
}

// GC tolerates a missing sessions root and reaps a stateless idle directory by
// its own mtime.
func TestGC_MissingRootAndStatelessIdleDir(t *testing.T) {
	if err := GC(t.TempDir(), time.Hour); err != nil {
		t.Fatalf("GC on empty root: %v", err)
	}

	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "stateless")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	if err := GC(root, time.Hour); err != nil {
		t.Fatalf("GC: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("stateless idle dir not reaped")
	}
}

// resolveRoot falls back to ~/.calm when neither an override nor $CALM_HOME is set.
func TestResolveRoot_FallsBackToHome(t *testing.T) {
	t.Setenv("CALM_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	m, err := New(Config{SessionID: "conv-1", Logger: logging.Nop()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := filepath.Join(home, ".calm", "sessions", "conv-1"); m.store.dir != want {
		t.Errorf("dir = %q; want %q", m.store.dir, want)
	}
}

// The idempotency keys presented to CALM track the persisted counter exactly:
// establish presents -r0, recovery -r1, and a transient recovery failure
// re-presents the same -r1 on the next 404 — recovering a created-but-unacked
// replacement instead of orphaning it.
func TestRecovery_IdempotencyKeys_SameOnTransientRetry(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	var keys []string
	record := func(_ context.Context, _ string, _ int, key string) { keys = append(keys, key) }
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Run(record).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Run(record).Return("", errors.New("timeout")).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Run(record).Return("tok2", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("first recovery = %+v; want calm_unreachable", sig)
	}
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "session_lost" {
		t.Fatalf("retry recovery = %+v; want session_lost", sig)
	}

	if len(keys) != 3 {
		t.Fatalf("CreateSession keys = %v; want 3 calls", keys)
	}
	base := strings.TrimSuffix(keys[0], "-r0")
	if base == keys[0] {
		t.Fatalf("establish key = %q; want -r0 suffix", keys[0])
	}
	if want := base + "-r1"; keys[1] != want {
		t.Errorf("first recovery key = %q; want %q", keys[1], want)
	}
	if keys[2] != keys[1] {
		t.Errorf("retry key = %q; want %q re-presented (transient must not advance the key)", keys[2], keys[1])
	}
	if st := loadState(t, m); st.RecoverySeq != 1 {
		t.Errorf("recovery_seq = %d; want 1 persisted after successful replacement", st.RecoverySeq)
	}
}

// A transient establish failure stamps a persisted throttle: further captures
// degrade without a create attempt until the interval elapses (DESIGN.md §4's
// one-attempt-per-interval promise), collapsing a concurrent herd to one
// network attempt per interval.
func TestEstablish_Transient_ThrottlePersistsAcrossInvocations(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Return("", errors.New("dial tcp: connection refused")).Once()

	root := t.TempDir()
	ctx := context.Background()
	if _, sig := newTestManager(t, root, "conv-1", c).Ensure(ctx); sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("first ensure = %+v; want calm_unreachable", sig)
	}
	m2 := newTestManager(t, root, "conv-1", c)
	if st := loadState(t, m2); st.NextAttemptAt.IsZero() {
		t.Fatalf("throttle stamp not persisted")
	}
	// m2 models the next hook invocation; the strict mock holds no second
	// CreateSession expectation, so any network attempt fails the test.
	if _, sig := m2.Ensure(ctx); sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("throttled ensure = %+v; want calm_unreachable without network", sig)
	}
}

// An elapsed throttle stamp permits the next attempt, and success clears it.
func TestEstablish_ThrottleElapsed_RetriesAndClears(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Return("", errors.New("timeout")).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Return("tok1", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("first ensure = %+v; want calm_unreachable", sig)
	}
	st := loadState(t, m)
	st.NextAttemptAt = time.Now().Add(-time.Second)
	if err := m.store.save(st); err != nil {
		t.Fatal(err)
	}
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("post-elapse ensure degraded: %+v", sig)
	}
	if st := loadState(t, m); !st.NextAttemptAt.IsZero() {
		t.Errorf("throttle stamp not cleared on successful establish")
	}
}

// A stamp further out than one full interval cannot have been legitimately
// written (clock skew); it is ignored rather than honored, so a bogus
// far-future stamp can never disable capture for the conversation.
func TestEstablish_FarFutureThrottleStamp_Ignored(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Return("", errors.New("timeout")).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Return("tok1", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig == nil || sig.Reason != "calm_unreachable" {
		t.Fatalf("seed ensure = %+v; want calm_unreachable", sig)
	}
	st := loadState(t, m)
	st.NextAttemptAt = time.Now().Add(10 * time.Hour)
	if err := m.store.save(st); err != nil {
		t.Fatal(err)
	}
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("far-future stamp honored: %+v", sig)
	}
}

// Reset abandons the session, so the next establish must mint a fresh one: the
// recovery counter and epoch advance — within CALM's create-idempotency window
// an unbumped key would silently resume the abandoned session. The sequence
// continues.
func TestReset_AdvancesRecoveryCounterAndEpoch(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	var keys []string
	record := func(_ context.Context, _ string, _ int, key string) { keys = append(keys, key) }
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Run(record).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).
		Run(record).Return("tok2", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	epochBefore := loadState(t, m).Epoch
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("post-reset ensure degraded: %+v", sig)
	}
	st := loadState(t, m)
	if st.Epoch != epochBefore+1 {
		t.Errorf("epoch = %d; want %d (reset replaces the session)", st.Epoch, epochBefore+1)
	}
	if len(keys) != 2 || keys[1] == keys[0] {
		t.Errorf("post-reset create key %q must differ from establish key %q", keys[1], keys[0])
	}
	if st.Seq != 2 {
		t.Errorf("seq = %d; want 2 (sequence continues across reset)", st.Seq)
	}
}

// GC must not reap a conversation another process is actively using: advisory
// locks do not prevent deletion, so the reap try-locks the directory and skips
// when the lock is held.
func TestGC_SkipsDirHeldByLiveProcess(t *testing.T) {
	root := t.TempDir()
	s := &store{dir: filepath.Join(root, "sessions", "conv-1")}
	saveFixture(t, s, func(st *state) { st.SessionToken = "tok" })
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(s.statePath(), old, old); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := GC(root, time.Hour); err != nil {
		t.Fatalf("GC: %v", err)
	}
	if _, err := os.Stat(s.statePath()); err != nil {
		t.Errorf("live (locked) dir was reaped")
	}
}

// View exposes the token, latch, epoch, and hydrated registry without
// establishing a session or allocating a sequence — retrieval never establishes
// (DESIGN.md §4). The strict mock proves no network call happens.
func TestView_ReadOnly_NoEstablishNoSeqBurn(t *testing.T) {
	c := calm.NewMockClient(t)
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)

	v, err := m.View(context.Background())
	if err != nil || v.Token != "" || v.AuthFailed {
		t.Fatalf("empty view = %+v, %v; want zero view without error", v, err)
	}
	if v.Registry == nil || len(v.Registry.Snapshot()) != 0 {
		t.Fatal("empty view must carry a hydrated, empty registry")
	}

	saveFixture(t, m.store, func(st *state) {
		st.SessionToken = "tok9"
		st.Epoch = 3
		st.Registry = map[string]string{"calm:v1:shell:echo": "abcd1234"}
	})
	v, err = m.View(context.Background())
	if err != nil || v.Token != "tok9" || v.Epoch != 3 || v.AuthFailed {
		t.Fatalf("view = %+v, %v; want persisted token and epoch", v, err)
	}
	if v.Registry.Snapshot()["calm:v1:shell:echo"] != "abcd1234" {
		t.Errorf("registry not hydrated from disk: %v", v.Registry.Snapshot())
	}
	if st := loadState(t, m); st.Seq != 0 {
		t.Errorf("seq = %d; want 0 — View must not allocate", st.Seq)
	}
}
