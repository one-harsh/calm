// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

func TestFeedback_ClosedSetEnforced(t *testing.T) {
	c := calm.NewMockClient(t) // strict: no CALM call expected
	d, _, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", "ref-1", "great"})

	if code != 2 {
		t.Fatalf("exit = %d; want 2 (usage error)", code)
	}
	if !strings.Contains(stderr.String(), "success|retry|degraded") {
		t.Errorf("stderr must name the bounded set; got:\n%s", stderr.String())
	}
}

func TestFeedback_Accepted(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Feedback(mock.Anything, "tok1", "ref-1", "success").Return(nil).Once()
	d, _, _ := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello")) // establish
	code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", "ref-1", "success"})

	if code != 0 {
		t.Fatalf("exit = %d; want 0 on acceptance", code)
	}
}

func TestFeedback_WindowExpired(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Feedback(mock.Anything, "tok1", "ref-1", "retry").
		Return(&calm.StatusError{Op: "feedback", Code: 410, Status: "410 Gone"}).Once()
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello")) // establish
	code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", "ref-1", "retry"})

	if code == 0 {
		t.Fatalf("exit = 0; want nonzero on an expired window")
	}
	if !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonFeedbackWindowExpired)) {
		t.Errorf("stderr must carry the feedback_window_expired phrasing; got:\n%s", stderr.String())
	}
}

func TestFeedback_AlreadyRecorded(t *testing.T) {
	c := calm.NewMockClient(t)
	expectEstablish(c, "tok1")
	c.EXPECT().Feedback(mock.Anything, "tok1", "ref-1", "success").
		Return(&calm.StatusError{Op: "feedback", Code: 409, Status: "409 Conflict"}).Once()
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
	code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", "ref-1", "success"})

	if code == 0 || !strings.Contains(stderr.String(), "already recorded") {
		t.Errorf("409: exit=%d stderr=%q; want nonzero + already-recorded phrasing", code, stderr.String())
	}
}

func TestFeedback_Latched_AuthFailed(t *testing.T) {
	c := calm.NewMockClient(t)
	expectLatch(c)
	d, _, stderr := newDeps(t, c)

	Dispatch(context.Background(), d, execArgs("conv", "printf hello")) // latches
	code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", "ref-1", "success"})

	if code == 0 || !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonAuthFailed)) {
		t.Errorf("latched feedback: exit=%d stderr=%q; want nonzero + auth_failed", code, stderr.String())
	}
}

func TestFeedback_NoSession(t *testing.T) {
	c := calm.NewMockClient(t) // strict: no Feedback call
	d, _, stderr := newDeps(t, c)

	code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", "ref-1", "success"})

	if code == 0 || !strings.Contains(stderr.String(), obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)) {
		t.Errorf("no session: exit=%d stderr=%q; want nonzero + unavailable", code, stderr.String())
	}
}

func TestFeedback_StatusMappings(t *testing.T) {
	cases := []struct {
		code   int
		wantIn string
	}{
		{401, obs.DegradedPhrase(obs.DegradedReasonAuthFailed)},
		{404, obs.DegradedPhrase(obs.DegradedReasonSessionLost)},
		{400, "malformed reference"},
		{503, obs.DegradedPhrase(obs.DegradedReasonCalmUnreachable)},
	}
	for _, tc := range cases {
		c := calm.NewMockClient(t)
		expectEstablish(c, "tok1")
		c.EXPECT().Feedback(mock.Anything, "tok1", "ref-1", "degraded").
			Return(&calm.StatusError{Op: "feedback", Code: tc.code, Status: "err"}).Once()
		d, _, stderr := newDeps(t, c)

		Dispatch(context.Background(), d, execArgs("conv", "printf hello"))
		code := Dispatch(context.Background(), d, []string{"feedback", "--session", "conv", "ref-1", "degraded"})

		if code == 0 || !strings.Contains(stderr.String(), tc.wantIn) {
			t.Errorf("feedback %d: exit=%d stderr=%q; want nonzero containing %q", tc.code, code, stderr.String(), tc.wantIn)
		}
	}
}

func TestValidOutcome(t *testing.T) {
	for _, ok := range []string{"success", "retry", "degraded"} {
		if !validOutcome(ok) {
			t.Errorf("%q must be a valid outcome", ok)
		}
	}
	for _, bad := range []string{"", "SUCCESS", "ok", "fail"} {
		if validOutcome(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
