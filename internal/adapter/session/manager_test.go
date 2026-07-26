// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
)

const (
	testClient = "claude-code"
	testTTL    = 60
)

func newTestManager(t *testing.T, root, sessionID string, c calm.Client) *Manager {
	t.Helper()
	m, err := New(Config{
		SessionID:  sessionID,
		Client:     testClient,
		CALM:       c,
		Logger:     logging.Nop(),
		TTLMinutes: testTTL,
		RootDir:    root,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// expectRegister allows client registration to land any number of times (it is
// idempotent and re-attempted until it succeeds).
func expectRegister(m *calm.MockClient) {
	m.EXPECT().RegisterClient(mock.Anything, testClient).Return(true, nil).Maybe()
}

func status(code int) error {
	return &calm.StatusError{Op: "ingest", Code: code, Status: "err"}
}

func loadState(t *testing.T, m *Manager) *state {
	t.Helper()
	st, err := m.store.load()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st == nil {
		t.Fatalf("no state persisted")
	}
	return st
}

// Two managers over one conversation race their first capture; the exclusive
// lock plus the disk-authoritative token collapse them into a single CALM
// create — flock conflicts across separate fds even in one process.
func TestEnsureSession_ConcurrentFirstInvocations_SingleCreateViaLock(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()

	root := t.TempDir()
	a := newTestManager(t, root, "conv-1", c)
	b := newTestManager(t, root, "conv-1", c)

	var wg sync.WaitGroup
	results := make([]capture.EnsureResult, 2)
	sigs := make([]*capture.Signal, 2)
	for i, mgr := range []*Manager{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], sigs[i] = mgr.Ensure(context.Background())
		}()
	}
	wg.Wait()

	for i := range results {
		if sigs[i] != nil {
			t.Fatalf("ensure %d degraded: %+v", i, sigs[i])
		}
		if results[i].Token != "tok1" {
			t.Errorf("ensure %d token = %q; want tok1", i, results[i].Token)
		}
	}
	if results[0].Seq == results[1].Seq {
		t.Errorf("sequences not distinct: %d, %d", results[0].Seq, results[1].Seq)
	}
	if got := loadState(t, a).Seq; got != 2 {
		t.Errorf("final seq = %d; want 2", got)
	}
}

// The lock guards only the two phases, never the ingest between them: a second
// capture's Ensure proceeds while the first is mid-ingest, and both later
// record without a lost update.
func TestCapture_ParallelIngestsDoNotSerializeOnLock(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()

	root := t.TempDir()
	a := newTestManager(t, root, "conv-1", c)
	b := newTestManager(t, root, "conv-1", c)
	ctx := context.Background()

	if _, sig := a.Ensure(ctx); sig != nil {
		t.Fatalf("a.Ensure degraded: %+v", sig)
	}
	// If Ensure held the lock across its return, this would deadlock.
	if _, sig := b.Ensure(ctx); sig != nil {
		t.Fatalf("b.Ensure degraded: %+v", sig)
	}
	a.Record(ctx, "tok1", []capture.SourceToken{{Source: "calm:v1:file:read:a.go", Token: "ta"}})
	b.Record(ctx, "tok1", []capture.SourceToken{{Source: "calm:v1:file:read:b.go", Token: "tb"}})

	reg := loadState(t, a).Registry
	if reg["calm:v1:file:read:a.go"] != "ta" || reg["calm:v1:file:read:b.go"] != "tb" {
		t.Errorf("registry lost an update: %v", reg)
	}
}

func TestState_ConcurrentWriters_FinalSeqExact_NoTornRead(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()

	root := t.TempDir()
	if _, sig := newTestManager(t, root, "conv-1", c).Ensure(context.Background()); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}

	const writers = 12
	var wg sync.WaitGroup
	seqs := make(chan int64, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, sig := newTestManager(t, root, "conv-1", c).Ensure(context.Background())
			if sig != nil {
				t.Errorf("writer degraded: %+v", sig)
				return
			}
			seqs <- res.Seq
		}()
	}
	wg.Wait()
	close(seqs)

	seen := map[int64]bool{}
	for s := range seqs {
		if seen[s] {
			t.Errorf("duplicate sequence %d — lost update", s)
		}
		seen[s] = true
	}
	// Establish took seq 1; the writers must have taken exactly 2..writers+1.
	for want := int64(2); want <= writers+1; want++ {
		if !seen[want] {
			t.Errorf("missing sequence %d", want)
		}
	}
	if got := loadState(t, newTestManager(t, root, "conv-1", c)).Seq; got != writers+1 {
		t.Errorf("final seq = %d; want %d", got, writers+1)
	}
}

// Two managers interleave their post-ingest record; each reloads before
// merging, so neither delta is lost.
func TestRecord_ReloadMergeNoLostUpdate(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()

	root := t.TempDir()
	a := newTestManager(t, root, "conv-1", c)
	b := newTestManager(t, root, "conv-1", c)
	ctx := context.Background()
	if _, sig := a.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}

	a.Record(ctx, "tok1", []capture.SourceToken{{Source: "s1", Token: "t1"}})
	b.Record(ctx, "tok1", []capture.SourceToken{{Source: "s2", Token: "t2"}})

	reg := loadState(t, a).Registry
	if reg["s1"] != "t1" || reg["s2"] != "t2" {
		t.Errorf("registry = %v; want both s1 and s2", reg)
	}
}

func TestAuthLatch_PersistsThenResetClears(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok2", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	a := newTestManager(t, root, "conv-1", c)
	if _, sig := a.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	if sig := a.OnCallError(ctx, "tok1", status(401)); sig == nil || sig.Reason != "auth_failed" {
		t.Fatalf("auth signal = %+v; want auth_failed", sig)
	}

	// Fresh process: latch is on disk.
	b := newTestManager(t, root, "conv-1", c)
	if _, sig := b.Ensure(ctx); sig == nil || sig.Reason != "auth_failed" {
		t.Fatalf("post-latch ensure = %+v; want auth_failed", sig)
	}
	if err := b.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	res, sig := b.Ensure(ctx)
	if sig != nil {
		t.Fatalf("post-reset ensure degraded: %+v", sig)
	}
	if res.Token != "tok2" {
		t.Errorf("post-reset token = %q; want tok2", res.Token)
	}
}

func TestAuthLatch_NewSessionIdStartsClean(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok", nil).Times(2)

	root := t.TempDir()
	ctx := context.Background()
	a := newTestManager(t, root, "conv-A", c)
	if _, sig := a.Ensure(ctx); sig != nil {
		t.Fatalf("establish A degraded: %+v", sig)
	}
	if sig := a.OnCallError(ctx, "tok", status(403)); sig == nil || sig.Reason != "auth_failed" {
		t.Fatalf("auth signal = %+v; want auth_failed", sig)
	}

	b := newTestManager(t, root, "conv-B", c)
	if _, sig := b.Ensure(ctx); sig != nil {
		t.Fatalf("conv-B should start clean; got %+v", sig)
	}
}

func TestSessionManager_NeverCallsDeleteSession(t *testing.T) {
	c := calm.NewMockClient(t) // strict: any DeleteSession call fails the test
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok2", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok3", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	m.Record(ctx, "tok1", []capture.SourceToken{{Source: "s1", Token: "t1"}})
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "session_lost" {
		t.Fatalf("recovery signal = %+v; want session_lost", sig)
	}
	if err := m.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("re-establish degraded: %+v", sig)
	}
}

func TestSeq_MonotonicAcrossInvocationsAndRecovery(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok2", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)

	r1, _ := m.Ensure(ctx) // seq 1, establish
	r2, _ := m.Ensure(ctx) // seq 2, reuse
	if r1.Seq != 1 || r2.Seq != 2 {
		t.Fatalf("seqs = %d, %d; want 1, 2", r1.Seq, r2.Seq)
	}
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "session_lost" {
		t.Fatalf("recovery signal = %+v; want session_lost", sig)
	}
	if ep := loadState(t, m).Epoch; ep != 1 {
		t.Errorf("epoch after recovery = %d; want 1", ep)
	}
	r3, sig := m.Ensure(ctx) // seq 3, new session tok2 — seq continues
	if sig != nil {
		t.Fatalf("post-recovery ensure degraded: %+v", sig)
	}
	if r3.Seq != 3 {
		t.Errorf("post-recovery seq = %d; want 3 (continues, never resets)", r3.Seq)
	}
	if r3.Token != "tok2" {
		t.Errorf("post-recovery token = %q; want tok2", r3.Token)
	}
}

// Recovery's replacement create uses the same client identity the manager was
// constructed with, so a lost session can never leak across the credential
// boundary. The mock's fixed client matcher enforces it on both creates.
func TestRecovery_UsesConstructedClientIdentity(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	// Both the establish and recovery creates must present testClient, or these
	// fixed-argument expectations do not match and the strict mock fails.
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok2", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "session_lost" {
		t.Fatalf("recovery signal = %+v; want session_lost", sig)
	}
	if got := loadState(t, m).Client; got != testClient {
		t.Errorf("persisted client = %q; want %q", got, testClient)
	}
}

// A non-session-level error (e.g. a plain 500) is not classified as a session
// failure — OnCallError returns nil so the engine falls through.
func TestOnCallError_NonSessionError_ReturnsNil(t *testing.T) {
	c := calm.NewMockClient(t)
	m := newTestManager(t, t.TempDir(), "conv-1", c)
	if sig := m.OnCallError(context.Background(), "tok", status(500)); sig != nil {
		t.Errorf("signal = %+v; want nil for non-session error", sig)
	}
}
