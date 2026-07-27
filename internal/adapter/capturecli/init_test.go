// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
)

func TestInitReset_ClearsAuthLatch(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	c.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return("", &calm.StatusError{Op: "create", Code: 401, Status: "401 Unauthorized"}).Once()
	d, _, _ := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello")) // establish attempt latches auth_failed

	mgr, err := d.manager("conv")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := mgr.View(context.Background()); !v.AuthFailed {
		t.Fatalf("precondition: auth latch must be set after a rejected create")
	}

	code := Dispatch(context.Background(), d, []string{"init", "--session", "conv", "--reset"})
	if code != 0 {
		t.Fatalf("init --reset exit = %d; want 0", code)
	}
	if v, _ := mgr.View(context.Background()); v.AuthFailed {
		t.Errorf("auth latch must be cleared after --reset")
	}
}

func TestInit_ProbeSuccess(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Once()
	d, _, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"init"})

	if code != 0 || !strings.Contains(stderr.String(), "credentials accepted") {
		t.Errorf("probe ok: exit=%d stderr=%q; want 0 + accepted", code, stderr.String())
	}
}

// A 401 on the probe is §9's credential-pairing failure surfacing at install.
func TestInit_CredentialFailure(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).
		Return(false, &calm.StatusError{Op: "register", Code: 401, Status: "401"}).Once()
	d, _, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"init"})

	if code == 0 || !strings.Contains(stderr.String(), "credential failure") {
		t.Errorf("401 probe: exit=%d stderr=%q; want nonzero + credential failure", code, stderr.String())
	}
}

// A transient probe failure is connectivity, not credentials.
func TestInit_ConnectivityFailure(t *testing.T) {
	c := calm.NewMockClient(t)
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).
		Return(false, errors.New("dial tcp: refused")).Once()
	d, _, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"init"})

	if code == 0 || !strings.Contains(stderr.String(), "connectivity failure") {
		t.Errorf("transient probe: exit=%d stderr=%q; want nonzero + connectivity failure", code, stderr.String())
	}
}
