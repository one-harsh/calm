// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build mocks

package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

// serviceHarness wires Service against a fresh mock DAL + ClientRepo. The
// SessionRepo isn't wired because client.Service never touches it.
type serviceHarness struct {
	svc     *Service
	dal     *db.MockDAL
	clients *db.MockClientRepo
}

func newServiceHarness(t *testing.T) *serviceHarness {
	dal := db.NewMockDAL(t)
	clients := db.NewMockClientRepo(t)
	dal.EXPECT().Clients().Return(clients).Maybe()
	return &serviceHarness{svc: New(dal), dal: dal, clients: clients}
}

// ---------- List / CountSessions / Delete (proxies) ----------

func TestList_ProxiesToDAL(t *testing.T) {
	h := newServiceHarness(t)
	want := []db.ClientSummary{
		{Name: "alice", SessionCount: 3},
		{Name: "bob", SessionCount: 1},
	}
	h.clients.EXPECT().List(mock.Anything, "ns-a").Return(want, nil).Once()

	got, err := h.svc.List(context.Background(), "ns-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestList_DALErrorPropagates(t *testing.T) {
	h := newServiceHarness(t)
	storageErr := errors.New("simulated list failure")
	h.clients.EXPECT().List(mock.Anything, "ns-a").Return(nil, storageErr).Once()

	if _, err := h.svc.List(context.Background(), "ns-a"); !errors.Is(err, storageErr) {
		t.Errorf("got %v; want wrapping %v", err, storageErr)
	}
}

func TestCountSessions_ProxiesToDAL(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().CountSessions(mock.Anything, "ns-a", "alice").Return(7, nil).Once()

	got, err := h.svc.CountSessions(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if got != 7 {
		t.Errorf("got %d; want 7", got)
	}
}

func TestDelete_ProxiesToDAL(t *testing.T) {
	h := newServiceHarness(t)
	want := db.DeleteClientResult{Client: "alice", DeletedSessions: 2, Cascaded: db.CascadeCounts{Events: 5}}
	h.clients.EXPECT().Delete(mock.Anything, "ns-a", "alice").Return(want, nil).Once()

	got, err := h.svc.Delete(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got.Client != "alice" || got.DeletedSessions != 2 || got.Cascaded.Events != 5 {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestDelete_DALErrorPropagates(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().Delete(mock.Anything, "ns-a", "alice").Return(db.DeleteClientResult{}, db.ErrClientNotFound).Once()

	if _, err := h.svc.Delete(context.Background(), "ns-a", "alice"); !errors.Is(err, db.ErrClientNotFound) {
		t.Errorf("got %v; want ErrClientNotFound", err)
	}
}

// ---------- SeedDefaults ----------

func TestSeedDefaults_EmptyNamespaceListIsNoop(t *testing.T) {
	h := newServiceHarness(t)
	// No Register expectation — must not be called.
	if err := h.svc.SeedDefaults(context.Background(), nil); err != nil {
		t.Errorf("SeedDefaults(nil): %v", err)
	}
	if err := h.svc.SeedDefaults(context.Background(), []string{}); err != nil {
		t.Errorf("SeedDefaults([]): %v", err)
	}
}

func TestSeedDefaults_RegistersDefaultClientForEveryNamespace(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().Register(mock.Anything, "ns-a", db.DefaultClient).Return(nil).Once()
	h.clients.EXPECT().Register(mock.Anything, "ns-b", db.DefaultClient).Return(nil).Once()
	h.clients.EXPECT().Register(mock.Anything, "ns-c", db.DefaultClient).Return(nil).Once()

	if err := h.svc.SeedDefaults(context.Background(), []string{"ns-a", "ns-b", "ns-c"}); err != nil {
		t.Errorf("SeedDefaults: %v", err)
	}
}

func TestSeedDefaults_StopsAtFirstFailure_AndWrapsWithNamespace(t *testing.T) {
	h := newServiceHarness(t)
	registerErr := errors.New("simulated register failure")
	h.clients.EXPECT().Register(mock.Anything, "ns-a", db.DefaultClient).Return(nil).Once()
	h.clients.EXPECT().Register(mock.Anything, "ns-b", db.DefaultClient).Return(registerErr).Once()
	// ns-c MUST NOT be reached — failure short-circuits the loop.

	err := h.svc.SeedDefaults(context.Background(), []string{"ns-a", "ns-b", "ns-c"})
	if !errors.Is(err, registerErr) {
		t.Fatalf("got %v; want wrapping %v", err, registerErr)
	}
	// Wrap must mention the namespace so operators can identify the offender.
	if !strings.Contains(err.Error(), "ns-b") {
		t.Errorf("error %q must reference the failing namespace ns-b", err.Error())
	}
}
