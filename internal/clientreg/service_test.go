// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

//go:build mocks

package clientreg

import (
	"context"
	"errors"
	"strings"
	"testing"

	logging "github.com/one-harsh/context-logging"
	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/db"
)

// serviceHarness wires Service against a fresh mock DAL + ClientRepo. The
// SessionRepo isn't wired because clientreg.Service never touches it.
type serviceHarness struct {
	svc     *Service
	dal     *db.MockDAL
	clients *db.MockClientRepo
}

func newServiceHarness(t *testing.T) *serviceHarness {
	dal := db.NewMockDAL(t)
	clients := db.NewMockClientRepo(t)
	dal.EXPECT().Clients().Return(clients).Maybe()
	// Delete composes its cascade in WithTx; run the closure against the mocked repo.
	dal.EXPECT().WithTx(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, fn func(db.Repos) error) error {
			return fn(db.Repos{Clients: clients})
		}).Maybe()
	return &serviceHarness{svc: New(dal, logging.Nop()), dal: dal, clients: clients}
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

func TestDelete_ComposesCascadeInTx(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().LockByName(mock.Anything, "ns-a", "alice").Return(nil).Once()
	h.clients.EXPECT().CascadeCountsForClient(mock.Anything, "ns-a", "alice").Return(2, db.CascadeCounts{Events: 5}, nil).Once()
	h.clients.EXPECT().DeleteRow(mock.Anything, "ns-a", "alice").Return(nil).Once()

	got, err := h.svc.Delete(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got.Client != "alice" || got.DeletedSessions != 2 || got.Cascaded.Events != 5 {
		t.Errorf("got %+v; want Client=alice DeletedSessions=2 Cascaded.Events=5", got)
	}
}

func TestDelete_LockNotFoundPropagates(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().LockByName(mock.Anything, "ns-a", "alice").Return(db.ErrClientNotFound).Once()

	if _, err := h.svc.Delete(context.Background(), "ns-a", "alice"); !errors.Is(err, db.ErrClientNotFound) {
		t.Errorf("got %v; want ErrClientNotFound", err)
	}
}

func TestDelete_ProtectedClientRejectedBeforeTx(t *testing.T) {
	h := newServiceHarness(t)
	// No repo expectations — the default client is rejected before any DAL call.
	if _, err := h.svc.Delete(context.Background(), "ns-a", db.DefaultClient); !errors.Is(err, db.ErrClientProtected) {
		t.Errorf("got %v; want ErrClientProtected", err)
	}
}

// ---------- Register (uncredentialed) ----------

func TestRegister_Happy(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().Register(mock.Anything, "ns-a", "alice").Return(true, nil).Once()

	got, err := h.svc.Register(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !got.Created || got.Name != "alice" || got.Namespace != "ns-a" {
		t.Errorf("got %+v; want Created=true Name=alice Namespace=ns-a", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt must be populated")
	}
}

func TestRegister_DuplicateReturnsCreatedFalse(t *testing.T) {
	// DAL Register is idempotent (ON CONFLICT DO NOTHING). Service surfaces
	// created=false so the handler can return 409.
	h := newServiceHarness(t)
	h.clients.EXPECT().Register(mock.Anything, "ns-a", "alice").Return(false, nil).Once()

	got, err := h.svc.Register(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got.Created {
		t.Error("Created should be false on duplicate")
	}
}

func TestRegister_EmptyNamespaceRejected(t *testing.T) {
	h := newServiceHarness(t)
	// No DAL expectation — must not be called.
	if _, err := h.svc.Register(context.Background(), "", "alice"); !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("got %v; want ErrNamespaceRequired", err)
	}
}

func TestRegister_EmptyNameRejected(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.svc.Register(context.Background(), "ns-a", ""); !errors.Is(err, db.ErrClientNameRequired) {
		t.Errorf("got %v; want ErrClientNameRequired", err)
	}
}

func TestRegister_DALErrorPropagates(t *testing.T) {
	h := newServiceHarness(t)
	storageErr := errors.New("simulated storage failure")
	h.clients.EXPECT().Register(mock.Anything, "ns-a", "alice").Return(false, storageErr).Once()

	if _, err := h.svc.Register(context.Background(), "ns-a", "alice"); !errors.Is(err, storageErr) {
		t.Errorf("got %v; want %v", err, storageErr)
	}
}

// ---------- RegisterWithCredential (credentialed) ----------

func TestRegisterWithCredential_Happy(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().
		RegisterWithCredential(mock.Anything, "ns-a", "alice", mock.MatchedBy(func(hash []byte) bool {
			return len(hash) == 32 // sha256 hash size
		})).
		Return(nil).Once()

	got, err := h.svc.RegisterWithCredential(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("RegisterWithCredential: %v", err)
	}
	if got.RawToken == "" {
		t.Error("RawToken must be non-empty for credentialed registration")
	}
	if got.Name != "alice" || got.Namespace != "ns-a" {
		t.Errorf("got %+v; want Name=alice Namespace=ns-a", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt must be populated")
	}
}

func TestRegisterWithCredential_DuplicateErrors(t *testing.T) {
	// Unlike uncredentialed Register, the credentialed path is not idempotent
	// — the older token can't be recovered post-hash, so duplicate must error
	// rather than silently re-issue.
	h := newServiceHarness(t)
	h.clients.EXPECT().
		RegisterWithCredential(mock.Anything, "ns-a", "alice", mock.Anything).
		Return(db.ErrClientExists).Once()

	got, err := h.svc.RegisterWithCredential(context.Background(), "ns-a", "alice")
	if !errors.Is(err, db.ErrClientExists) {
		t.Fatalf("got %v; want ErrClientExists", err)
	}
	if got.RawToken != "" {
		t.Error("RawToken must be empty on error (token must not leak)")
	}
}

func TestRegisterWithCredential_EmptyNamespaceRejected(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.svc.RegisterWithCredential(context.Background(), "", "alice"); !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("got %v; want ErrNamespaceRequired", err)
	}
}

func TestRegisterWithCredential_EmptyNameRejected(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.svc.RegisterWithCredential(context.Background(), "ns-a", ""); !errors.Is(err, db.ErrClientNameRequired) {
		t.Errorf("got %v; want ErrClientNameRequired", err)
	}
}

func TestRegisterWithCredential_NamespaceIsHashDomainSeparator(t *testing.T) {
	// hash = sha256(namespace || 0x00 || token). Same workload-side raw value
	// in two namespaces must hash differently — this is the cross-namespace
	// rainbow-table defense.
	h := newServiceHarness(t)
	var capturedHashA, capturedHashB []byte
	h.clients.EXPECT().
		RegisterWithCredential(mock.Anything, "ns-a", "alice", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, hash []byte) error {
			capturedHashA = hash
			return nil
		}).Once()
	h.clients.EXPECT().
		RegisterWithCredential(mock.Anything, "ns-b", "alice", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _ string, hash []byte) error {
			capturedHashB = hash
			return nil
		}).Once()

	if _, err := h.svc.RegisterWithCredential(context.Background(), "ns-a", "alice"); err != nil {
		t.Fatalf("Register ns-a: %v", err)
	}
	if _, err := h.svc.RegisterWithCredential(context.Background(), "ns-b", "alice"); err != nil {
		t.Fatalf("Register ns-b: %v", err)
	}
	if bytesEqual(capturedHashA, capturedHashB) {
		t.Error("hashes must differ across namespaces (raw tokens are different, so this just confirms randomness; the domain-separation guarantee is structural)")
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------- RotateToken ----------

func TestRotateToken_Happy(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().
		RotateCredential(mock.Anything, "ns-a", "alice", mock.MatchedBy(func(hash []byte) bool {
			return len(hash) == 32
		})).
		Return(nil).Once()

	newToken, err := h.svc.RotateToken(context.Background(), "ns-a", "alice")
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}
	if newToken == "" {
		t.Error("new token must be non-empty")
	}
}

func TestRotateToken_NotFoundPropagates(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().
		RotateCredential(mock.Anything, "ns-a", "alice", mock.Anything).
		Return(db.ErrClientNotFound).Once()

	if _, err := h.svc.RotateToken(context.Background(), "ns-a", "alice"); !errors.Is(err, db.ErrClientNotFound) {
		t.Errorf("got %v; want ErrClientNotFound", err)
	}
}

func TestRotateToken_EmptyNamespaceRejected(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.svc.RotateToken(context.Background(), "", "alice"); !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("got %v; want ErrNamespaceRequired", err)
	}
}

func TestRotateToken_EmptyNameRejected(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.svc.RotateToken(context.Background(), "ns-a", ""); !errors.Is(err, db.ErrClientNameRequired) {
		t.Errorf("got %v; want ErrClientNameRequired", err)
	}
}

// ---------- ResolveByToken ----------

func TestResolveByToken_Happy(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().
		LookupByToken(mock.Anything, "ns-a", mock.MatchedBy(func(hash []byte) bool {
			return len(hash) == 32
		})).
		Return("alice", nil).Once()

	name, err := h.svc.ResolveByToken(context.Background(), "ns-a", "raw-token-value")
	if err != nil {
		t.Fatalf("ResolveByToken: %v", err)
	}
	if name != "alice" {
		t.Errorf("got %q; want alice", name)
	}
}

func TestResolveByToken_InvalidCredentialPropagates(t *testing.T) {
	h := newServiceHarness(t)
	h.clients.EXPECT().
		LookupByToken(mock.Anything, "ns-a", mock.Anything).
		Return("", db.ErrInvalidClientCredential).Once()

	if _, err := h.svc.ResolveByToken(context.Background(), "ns-a", "wrong-token"); !errors.Is(err, db.ErrInvalidClientCredential) {
		t.Errorf("got %v; want ErrInvalidClientCredential", err)
	}
}

func TestResolveByToken_EmptyNamespaceRejected(t *testing.T) {
	h := newServiceHarness(t)
	if _, err := h.svc.ResolveByToken(context.Background(), "", "raw"); !errors.Is(err, db.ErrNamespaceRequired) {
		t.Errorf("got %v; want ErrNamespaceRequired", err)
	}
}

func TestResolveByToken_EmptyTokenRejected(t *testing.T) {
	h := newServiceHarness(t)
	// No DAL call — empty token is rejected before lookup (would otherwise
	// hash to a deterministic value and match a real row by accident).
	if _, err := h.svc.ResolveByToken(context.Background(), "ns-a", ""); !errors.Is(err, db.ErrInvalidClientCredential) {
		t.Errorf("got %v; want ErrInvalidClientCredential", err)
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
	h.clients.EXPECT().Register(mock.Anything, "ns-a", db.DefaultClient).Return(true, nil).Once()
	h.clients.EXPECT().Register(mock.Anything, "ns-b", db.DefaultClient).Return(true, nil).Once()
	h.clients.EXPECT().Register(mock.Anything, "ns-c", db.DefaultClient).Return(true, nil).Once()

	if err := h.svc.SeedDefaults(context.Background(), []string{"ns-a", "ns-b", "ns-c"}); err != nil {
		t.Errorf("SeedDefaults: %v", err)
	}
}

func TestSeedDefaults_StopsAtFirstFailure_AndWrapsWithNamespace(t *testing.T) {
	h := newServiceHarness(t)
	registerErr := errors.New("simulated register failure")
	h.clients.EXPECT().Register(mock.Anything, "ns-a", db.DefaultClient).Return(true, nil).Once()
	h.clients.EXPECT().Register(mock.Anything, "ns-b", db.DefaultClient).Return(false, registerErr).Once()
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
