// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/one-harsh/calm/internal/db"
)

// ---------- WriteEvents ----------

func TestWriteEvents_SingleEventInserted(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	accepted, err := store.Events().Write(context.Background(), "ns-a", sess.ID, []db.EventInput{
		{Type: "tool_invocation", Priority: 2, Data: []byte(`{"cmd":"ls"}`)},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if accepted != 1 {
		t.Errorf("accepted = %d; want 1", accepted)
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, sess.ID)
	if got != 1 {
		t.Errorf("want 1 event row, got %d", got)
	}
}

func TestWriteEvents_BatchAllInserted(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	batch := []db.EventInput{
		{Type: "a", Priority: 1, Data: []byte(`{"i":1}`)},
		{Type: "b", Priority: 2, Data: []byte(`{"i":2}`)},
		{Type: "c", Priority: 3, Data: []byte(`{"i":3}`)},
		{Type: "d", Priority: 4, Data: []byte(`{"i":4}`)},
		{Type: "e", Priority: 1, Data: []byte(`{"i":5}`)},
	}
	accepted, err := store.Events().Write(context.Background(), "ns-a", sess.ID, batch)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if accepted != len(batch) {
		t.Errorf("accepted = %d; want %d", accepted, len(batch))
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, sess.ID)
	if got != len(batch) {
		t.Errorf("want %d event rows, got %d", len(batch), got)
	}
}

func TestWriteEvents_EmptyBatchNoOp(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	accepted, err := store.Events().Write(context.Background(), "ns-a", sess.ID, nil)
	if err != nil {
		t.Fatalf("Write nil batch: %v", err)
	}
	if accepted != 0 {
		t.Errorf("accepted = %d; want 0", accepted)
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, sess.ID)
	if got != 0 {
		t.Errorf("want 0 event rows, got %d", got)
	}
}

func TestWriteEvents_EmptyNamespaceRejects(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, err := store.Events().Write(context.Background(), "", sess.ID, []db.EventInput{
		{Type: "a", Priority: 1, Data: []byte(`{}`)},
	})
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("err = %v; want ErrNamespaceRequired", err)
	}
}

func TestWriteEvents_PriorityZeroRejectsBatch(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	batch := []db.EventInput{
		{Type: "a", Priority: 1, Data: []byte(`{}`)},
		{Type: "b", Priority: 0, Data: []byte(`{}`)},
		{Type: "c", Priority: 2, Data: []byte(`{}`)},
	}
	accepted, err := store.Events().Write(context.Background(), "ns-a", sess.ID, batch)
	if !errors.Is(err, db.ErrInvalidPriority) {
		t.Fatalf("err = %v; want ErrInvalidPriority", err)
	}
	if accepted != 0 {
		t.Errorf("accepted = %d; want 0", accepted)
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, sess.ID)
	if got != 0 {
		t.Errorf("want 0 event rows after rejected batch, got %d", got)
	}
}

func TestWriteEvents_PriorityFiveRejectsBatch(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	accepted, err := store.Events().Write(context.Background(), "ns-a", sess.ID, []db.EventInput{
		{Type: "a", Priority: 5, Data: []byte(`{}`)},
	})
	if !errors.Is(err, db.ErrInvalidPriority) {
		t.Fatalf("err = %v; want ErrInvalidPriority", err)
	}
	if accepted != 0 {
		t.Errorf("accepted = %d; want 0", accepted)
	}
}

func TestWriteEvents_UnknownSessionMapsToNotFound(t *testing.T) {
	store, _, teardown := openConcreteStore(t)
	defer teardown()

	accepted, err := store.Events().Write(context.Background(), "ns-a", 999_999, []db.EventInput{
		{Type: "a", Priority: 1, Data: []byte(`{}`)},
	})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
	if accepted != 0 {
		t.Errorf("accepted = %d; want 0", accepted)
	}
}

// Defense-in-depth: a session_id from another namespace must read as
// ErrSessionNotFound even though session_ids are globally unique. Same
// invisibility-not-denial semantic the rest of the DAL enforces.
func TestWriteEvents_CrossNamespaceSessionMapsToNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, err := store.Events().Write(context.Background(), "ns-b", sessA.ID, []db.EventInput{
		{Type: "a", Priority: 1, Data: []byte(`{}`)},
	})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Fatalf("err = %v; want ErrSessionNotFound", err)
	}
	got := countRows(t, sqlDB, `SELECT COUNT(*) FROM session_events WHERE session_id = $1`, sessA.ID)
	if got != 0 {
		t.Errorf("want 0 event rows after rejected cross-namespace write, got %d", got)
	}
}

func TestWriteEvents_SessionIsolationWithinNamespace(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	if _, err := store.Events().Write(context.Background(), "ns-a", sessA.ID, []db.EventInput{
		{Type: "in-a", Priority: 1, Data: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("Write A: %v", err)
	}
	if _, err := store.Events().Write(context.Background(), "ns-a", sessB.ID, []db.EventInput{
		{Type: "in-b", Priority: 1, Data: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("Write B: %v", err)
	}

	gotA, err := store.Events().Read(context.Background(), "ns-a", sessA.ID, db.EventFilter{})
	if err != nil {
		t.Fatalf("Read A: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Type != "in-a" {
		t.Errorf("Read A returned %+v; want exactly [in-a]", gotA)
	}
	gotB, err := store.Events().Read(context.Background(), "ns-a", sessB.ID, db.EventFilter{})
	if err != nil {
		t.Fatalf("Read B: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Type != "in-b" {
		t.Errorf("Read B returned %+v; want exactly [in-b]", gotB)
	}
}

func TestWriteEvents_CrossNamespaceIsolation(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	sessB := seedSession(t, sqlDB, "ns-b", db.DefaultClient, 60)

	if _, err := store.Events().Write(context.Background(), "ns-a", sessA.ID, []db.EventInput{
		{Type: "ns-a-event", Priority: 1, Data: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("Write A: %v", err)
	}
	if _, err := store.Events().Write(context.Background(), "ns-b", sessB.ID, []db.EventInput{
		{Type: "ns-b-event", Priority: 1, Data: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("Write B: %v", err)
	}

	gotA, err := store.Events().Read(context.Background(), "ns-a", sessA.ID, db.EventFilter{})
	if err != nil {
		t.Fatalf("Read A: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Type != "ns-a-event" {
		t.Errorf("Read A returned %+v; want exactly [ns-a-event]", gotA)
	}
}

// ---------- ReadEvents ----------

func TestReadEvents_EmptySessionReturnsEmptySlice(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	got, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events; want 0", len(got))
	}
}

func TestReadEvents_EmptyNamespaceRejects(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, err := store.Events().Read(context.Background(), "", sess.ID, db.EventFilter{})
	if !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("err = %v; want ErrNamespaceRequired", err)
	}
}

func TestReadEvents_CrossNamespaceSessionMapsToNotFound(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	seedClient(t, sqlDB, "ns-b", db.DefaultClient)
	sessA := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedEvent(t, sqlDB, sessA.ID, "leak", 1, []byte(`{}`))

	_, err := store.Events().Read(context.Background(), "ns-b", sessA.ID, db.EventFilter{})
	if !errors.Is(err, db.ErrSessionNotFound) {
		t.Errorf("err = %v; want ErrSessionNotFound", err)
	}
}

func TestReadEvents_OrderedByPriorityThenCreatedAtDesc(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	t0 := time.Now().UTC().Add(-10 * time.Minute)
	seedEventAt(t, sqlDB, sess.ID, "p3-old", 3, []byte(`{}`), t0)
	seedEventAt(t, sqlDB, sess.ID, "p1-old", 1, []byte(`{}`), t0.Add(1*time.Minute))
	seedEventAt(t, sqlDB, sess.ID, "p2-mid", 2, []byte(`{}`), t0.Add(2*time.Minute))
	seedEventAt(t, sqlDB, sess.ID, "p1-new", 1, []byte(`{}`), t0.Add(3*time.Minute))

	got, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []string{"p1-new", "p1-old", "p2-mid", "p3-old"}
	if len(got) != len(want) {
		t.Fatalf("got %d events; want %d", len(got), len(want))
	}
	for i, ev := range got {
		if ev.Type != want[i] {
			t.Errorf("got[%d].Type = %q; want %q", i, ev.Type, want[i])
		}
	}
}

func TestReadEvents_TypeFilterSingle(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedEvent(t, sqlDB, sess.ID, "a", 2, []byte(`{}`))
	seedEvent(t, sqlDB, sess.ID, "b", 2, []byte(`{}`))
	seedEvent(t, sqlDB, sess.ID, "c", 2, []byte(`{}`))

	got, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{Types: []string{"b"}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 || got[0].Type != "b" {
		t.Errorf("got %+v; want exactly [b]", got)
	}
}

func TestReadEvents_TypeFilterMultiple(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedEvent(t, sqlDB, sess.ID, "a", 2, []byte(`{}`))
	seedEvent(t, sqlDB, sess.ID, "b", 2, []byte(`{}`))
	seedEvent(t, sqlDB, sess.ID, "c", 2, []byte(`{}`))

	got, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{Types: []string{"a", "c"}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events; want 2", len(got))
	}
	types := map[string]bool{got[0].Type: true, got[1].Type: true}
	if !types["a"] || !types["c"] || types["b"] {
		t.Errorf("got types %+v; want {a,c}", types)
	}
}

func TestReadEvents_MinPriorityCeiling(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	seedEvent(t, sqlDB, sess.ID, "p1", 1, []byte(`{}`))
	seedEvent(t, sqlDB, sess.ID, "p2", 2, []byte(`{}`))
	seedEvent(t, sqlDB, sess.ID, "p3", 3, []byte(`{}`))
	seedEvent(t, sqlDB, sess.ID, "p4", 4, []byte(`{}`))

	got, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{MinPriority: 2})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events; want 2", len(got))
	}
	priorities := map[int]bool{got[0].Priority: true, got[1].Priority: true}
	if !priorities[1] || !priorities[2] {
		t.Errorf("got priorities %+v; want {1,2}", priorities)
	}
}

func TestReadEvents_MinPriorityAboveRangeRejects(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{MinPriority: 5})
	if !errors.Is(err, db.ErrInvalidPriority) {
		t.Errorf("err = %v; want ErrInvalidPriority", err)
	}
}

func TestReadEvents_MinPriorityNegativeRejects(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{MinPriority: -1})
	if !errors.Is(err, db.ErrInvalidPriority) {
		t.Errorf("err = %v; want ErrInvalidPriority", err)
	}
}

func TestReadEvents_LimitHonored(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	for i := 0; i < 5; i++ {
		seedEvent(t, sqlDB, sess.ID, "t", 2, []byte(`{}`))
	}

	got, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{Limit: 2})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d events; want 2", len(got))
	}
}

func TestReadEvents_LimitDefaultsTo100(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)
	for i := 0; i < 150; i++ {
		seedEvent(t, sqlDB, sess.ID, "t", 2, []byte(`{}`))
	}

	got, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("got %d events; want 100 (default limit)", len(got))
	}
}

func TestReadEvents_LimitNegativeRejects(t *testing.T) {
	store, sqlDB, teardown := openConcreteStore(t)
	defer teardown()

	seedClient(t, sqlDB, "ns-a", db.DefaultClient)
	sess := seedSession(t, sqlDB, "ns-a", db.DefaultClient, 60)

	_, err := store.Events().Read(context.Background(), "ns-a", sess.ID, db.EventFilter{Limit: -1})
	if !errors.Is(err, db.ErrInvalidLimit) {
		t.Errorf("err = %v; want ErrInvalidLimit", err)
	}
}
