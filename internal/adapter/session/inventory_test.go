// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"testing"

	logging "github.com/one-harsh/context-logging"
)

// Inventory orders identities most-recent-first by the sequence at which each
// was last captured and reports the conversation's capture count, taking only
// the read lock.
func TestInventory_OrdersByRecency(t *testing.T) {
	root := t.TempDir()
	s := newStore(root, "conv-1")
	saveFixture(t, s, func(st *state) {
		st.Seq = 9
		st.Registry = map[string]string{"a": "t1", "b": "t2", "c": "t3"}
		st.RegistrySeq = map[string]int64{"a": 3, "b": 9, "c": 6}
	})
	m, err := New(Config{SessionID: "conv-1", Logger: logging.Nop(), RootDir: root})
	if err != nil {
		t.Fatal(err)
	}

	count, entries, err := m.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if count != 9 {
		t.Errorf("count = %d; want 9 (the capture sequence)", count)
	}
	want := []string{"b", "c", "a"}       // seq 9, 6, 3
	wantTok := []string{"t2", "t3", "t1"} // the registry token for each, carried for fused-label validation
	if len(entries) != len(want) {
		t.Fatalf("entries = %d; want %d", len(entries), len(want))
	}
	for i := range want {
		if entries[i].Label != want[i] {
			t.Errorf("entries[%d].Label = %q; want %q", i, entries[i].Label, want[i])
		}
		if entries[i].Token != wantTok[i] {
			t.Errorf("entries[%d].Token = %q; want %q (needed to fuse a staleness-validated label)", i, entries[i].Token, wantTok[i])
		}
	}
}

// A never-established session yields an empty inventory, not an error.
func TestInventory_NeverEstablished(t *testing.T) {
	m, err := New(Config{SessionID: "ghost", Logger: logging.Nop(), RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	count, entries, err := m.Inventory(context.Background())
	if err != nil || count != 0 || len(entries) != 0 {
		t.Errorf("empty inventory expected; got count=%d entries=%d err=%v", count, len(entries), err)
	}
}
