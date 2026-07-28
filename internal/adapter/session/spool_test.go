// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func sampleEvents() []calm.EventInput {
	return []calm.EventInput{{Type: "error_reported", Priority: 1, Data: map[string]any{"detail": "x"}}}
}

func marshalLine(t *testing.T, ln spoolLine) []byte {
	t.Helper()
	b, err := json.Marshal(ln)
	if err != nil {
		t.Fatalf("marshal line: %v", err)
	}
	return append(b, '\n')
}

func writeSpool(t *testing.T, s *store, lines ...spoolLine) {
	t.Helper()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var buf bytes.Buffer
	for _, ln := range lines {
		buf.Write(marshalLine(t, ln))
	}
	if err := os.WriteFile(s.spoolPath(), buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write spool: %v", err)
	}
}

func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	old := time.Now().Add(-age)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func assertNoSpoolFiles(t *testing.T, s *store) {
	t.Helper()
	if _, err := os.Stat(s.spoolPath()); !os.IsNotExist(err) {
		t.Errorf("events.spool must be gone; stat err = %v", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), inflightPrefix) {
			t.Errorf("inflight claim must be gone after a full drain: %s", e.Name())
		}
	}
}

// An inflight claim that outlived its drainer is deleted unread and never
// re-delivered — deletion is the at-most-once guarantee (AD06), so no POST is
// attempted even though the lines are otherwise well-formed and current.
func TestSpool_StaleInflightDroppedNotReplayed(t *testing.T) {
	c := calm.NewMockClient(t) // strict: any WriteEvents proves a forbidden re-delivery
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })

	inflight := m.store.inflightPath(4242)
	if err := os.WriteFile(inflight, marshalLine(t, spoolLine{Epoch: 1, SessionToken: "tok", Events: sampleEvents()}), 0o600); err != nil {
		t.Fatalf("write inflight: %v", err)
	}
	ageFile(t, inflight, 11*time.Minute)

	m.Drain(context.Background())

	if _, err := os.Stat(inflight); !os.IsNotExist(err) {
		t.Errorf("stale inflight must be deleted unread; stat err = %v", err)
	}
}

// A spooled line from a superseded epoch is discarded at drain, never posted —
// output belonging to a replaced session is never delivered as current.
func TestSpool_StaleEpochSkipped(t *testing.T) {
	c := calm.NewMockClient(t) // strict: a POST for a superseded epoch fails the test
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 5 })
	writeSpool(t, m.store, spoolLine{Epoch: 3, SessionToken: "old", Events: sampleEvents()})

	m.Drain(context.Background())

	assertNoSpoolFiles(t, m.store)
}

// A 404 on delivery drops that line's batch and never triggers session recovery
// (AD06): the strict mock has no CreateSession expectation, so any recovery
// attempt fails the test.
func TestSpool_404DropsBatchNoRecovery(t *testing.T) {
	c := calm.NewMockClient(t) // strict: a CreateSession here proves forbidden recovery
	c.EXPECT().WriteEvents(mock.Anything, "tok", mock.Anything).Return(calm.ErrSessionNotFound).Once()
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })
	writeSpool(t, m.store, spoolLine{Epoch: 1, SessionToken: "tok", Events: sampleEvents()})

	m.Drain(context.Background())

	assertNoSpoolFiles(t, m.store)
}

// A 404 drops that line and delivery proceeds (AD06): with two current-epoch
// lines on a dead token, both are attempted and dropped, no session recovery is
// triggered (the strict mock has no CreateSession), and the fully processed
// claim is removed — a 404 does not abandon the batch.
func TestSpool_404ContinuesAcrossLines(t *testing.T) {
	c := calm.NewMockClient(t) // strict: a CreateSession here proves forbidden recovery
	c.EXPECT().WriteEvents(mock.Anything, "tok", mock.Anything).Return(calm.ErrSessionNotFound).Twice()
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })
	writeSpool(
		t, m.store,
		spoolLine{Epoch: 1, SessionToken: "tok", Events: sampleEvents()},
		spoolLine{Epoch: 1, SessionToken: "tok", Events: sampleEvents()},
	)

	m.Drain(context.Background())

	assertNoSpoolFiles(t, m.store)
}

// Any non-404 delivery failure abandons the remaining lines rather than skipping
// past them (AD06): the first line's transport error stops the drain, the second
// line is never attempted (the mock allows a single call), and the claim is left
// in place to age into the stale reap.
func TestSpool_TransientAbandonsRemainingLines(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().WriteEvents(mock.Anything, "tok", mock.Anything).
		Return(errors.New("dial tcp: connection refused")).Once()
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })
	writeSpool(
		t, m.store,
		spoolLine{Epoch: 1, SessionToken: "tok", Events: sampleEvents()},
		spoolLine{Epoch: 1, SessionToken: "tok", Events: sampleEvents()},
	)

	m.Drain(context.Background())

	if _, err := os.Stat(m.store.inflightPath(os.Getpid())); err != nil {
		t.Errorf("abandoned claim must survive for the stale reap; stat err = %v", err)
	}
}

func TestSpool_AppendThenDrainDelivers(t *testing.T) {
	c := calm.NewMockClient(t)
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok-live"; st.Epoch = 2 })

	events := sampleEvents()
	var gotToken string
	var gotEvents []calm.EventInput
	c.EXPECT().WriteEvents(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, token string, ev []calm.EventInput) error {
			gotToken, gotEvents = token, ev
			return nil
		}).Once()

	m.Enqueue(context.Background(), "tok-live", events)
	m.Drain(context.Background())

	if gotToken != "tok-live" {
		t.Errorf("delivered token = %q; want tok-live", gotToken)
	}
	if len(gotEvents) != len(events) || gotEvents[0].Type != events[0].Type {
		t.Errorf("delivered events = %+v; want %+v", gotEvents, events)
	}
	assertNoSpoolFiles(t, m.store)
}

// Enqueue spools nothing when there is nothing to spool: events with no
// established session are dropped rather than tagged against a nil epoch, and an
// empty batch never touches the spool file.
func TestEnqueue_SpoolNoOpWhenNoEvents(t *testing.T) {
	c := calm.NewMockClient(t) // strict: the spool path never touches CALM
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)

	m.Enqueue(context.Background(), "tok", sampleEvents()) // no session state yet
	if _, err := os.Stat(m.store.spoolPath()); !os.IsNotExist(err) {
		t.Errorf("events with no session state must not spool; stat err = %v", err)
	}

	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })
	m.Enqueue(context.Background(), "tok", nil) // empty batch
	if _, err := os.Stat(m.store.spoolPath()); !os.IsNotExist(err) {
		t.Errorf("empty batch must write no spool file; stat err = %v", err)
	}
}

// The rename is the claim: when a rival drainer has already claimed the spool,
// this drainer finds nothing to deliver and leaves the rival's fresh claim
// untouched — one delivery, not two.
func TestSpool_ClaimByRenameExclusive(t *testing.T) {
	c := calm.NewMockClient(t) // strict: our drainer must post nothing (the rival owns delivery)
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })
	m.Enqueue(context.Background(), "tok", sampleEvents())

	rival := m.store.inflightPath(999999)
	if err := os.Rename(m.store.spoolPath(), rival); err != nil {
		t.Fatalf("simulate rival claim: %v", err)
	}

	m.Drain(context.Background())

	if _, err := os.Stat(rival); err != nil {
		t.Errorf("rival's fresh claim must be untouched; stat err = %v", err)
	}
}

// A transient transport failure leaves the claim intact (delete-not-replay, not
// retry-in-place), and a later drain, once the claim has aged past the stale
// window, deletes it unread rather than re-delivering it.
func TestSpool_TransportFailureLeavesInflightThenStaleReaped(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().WriteEvents(mock.Anything, "tok", mock.Anything).
		Return(errors.New("dial tcp: connection refused")).Once()
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })
	m.Enqueue(context.Background(), "tok", sampleEvents())

	m.Drain(context.Background())

	inflight := m.store.inflightPath(os.Getpid())
	if _, err := os.Stat(inflight); err != nil {
		t.Fatalf("inflight must survive a transient failure (delete-not-replay); stat err = %v", err)
	}

	// The mock allows exactly one WriteEvents: aging the claim past the stale
	// window makes the next drain reap it unread, with no second delivery.
	ageFile(t, inflight, 11*time.Minute)
	m.Drain(context.Background())

	if _, err := os.Stat(inflight); !os.IsNotExist(err) {
		t.Errorf("stale inflight must be deleted unread; stat err = %v", err)
	}
}

// Enqueue holds the session lock while it appends, so concurrent Ensure calls
// contending for the same lock never interleave a write: every spooled line
// parses as one complete JSON object.
func TestEnqueue_AppendUnderLockNoTornLineWithParallelEnsure(t *testing.T) {
	c := calm.NewMockClient(t) // established session → Ensure makes no network call; the spool append makes none
	root := t.TempDir()
	m := newTestManager(t, root, "conv-1", c)
	saveFixture(t, m.store, func(st *state) { st.SessionToken = "tok"; st.Epoch = 1 })

	const writers = 8
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				m.Enqueue(context.Background(), "tok", sampleEvents())
				return
			}
			if _, sig := m.Ensure(context.Background()); sig != nil {
				t.Errorf("ensure degraded: %+v", sig)
			}
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(m.store.spoolPath())
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	lines := 0
	for _, b := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		var ln spoolLine
		if err := json.Unmarshal(b, &ln); err != nil {
			t.Fatalf("torn spool line %q: %v", b, err)
		}
		lines++
	}
	if lines != writers/2 {
		t.Errorf("spooled lines = %d; want %d", lines, writers/2)
	}
}

// Events from a replaced generation are rejected before the spool append: an
// appended line would carry a lying (current-epoch, dead-token) tag pair, and the
// 404 delivery attempt it provokes is avoidable.
func TestEnqueue_RejectsReplacedGenerationBeforeAppend(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok2", nil).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	if sig := m.OnCallError(ctx, "tok1", status(404)); sig == nil || sig.Reason != "session_lost" {
		t.Fatalf("recovery = %+v; want session_lost", sig)
	}

	m.Enqueue(ctx, "tok1", sampleEvents())
	if _, err := os.Stat(m.store.spoolPath()); !os.IsNotExist(err) {
		t.Fatalf("dead-generation events were enqueued")
	}
	m.Enqueue(ctx, "tok2", sampleEvents())
	if _, err := os.Stat(m.store.spoolPath()); err != nil {
		t.Errorf("current-generation events not enqueued: %v", err)
	}
}

// A claim's stale clock measures time since claim, not since append: a
// long-idle spool must not be born stale, or a concurrent reaper would delete
// the claim mid-delivery.
func TestSpool_ClaimNotBornStale(t *testing.T) {
	c := calm.NewMockClient(t)
	expectRegister(c)
	c.EXPECT().CreateSession(mock.Anything, testClient, testTTL, mock.Anything).Return("tok1", nil).Once()
	c.EXPECT().WriteEvents(mock.Anything, "tok1", mock.Anything).Return(errors.New("timeout")).Once()

	root := t.TempDir()
	ctx := context.Background()
	m := newTestManager(t, root, "conv-1", c)
	if _, sig := m.Ensure(ctx); sig != nil {
		t.Fatalf("establish degraded: %+v", sig)
	}
	m.Enqueue(ctx, "tok1", sampleEvents())
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(m.store.spoolPath(), old, old); err != nil {
		t.Fatal(err)
	}

	m.Drain(ctx) // claims the aged spool; delivery fails transiently
	inflight := m.store.inflightPath(os.Getpid())
	fi, err := os.Stat(inflight)
	if err != nil {
		t.Fatalf("inflight claim missing after transient failure: %v", err)
	}
	if time.Since(fi.ModTime()) > time.Minute {
		t.Errorf("claim born stale: mtime %v", fi.ModTime())
	}
	// A second drain's reaper must keep the fresh claim: strict mock permits no
	// further WriteEvents, and the file must survive.
	m.Drain(ctx)
	if _, err := os.Stat(inflight); err != nil {
		t.Errorf("fresh claim reaped: %v", err)
	}
}
