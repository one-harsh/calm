// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/config"
)

func newDeps(t *testing.T, c calm.Client) (Deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	// The test process may itself run under calm-capture (hook-arm dogfood
	// inherits the AD07 sentinel); reset it so capture-path tests exercise
	// capture rather than pass-through.
	t.Setenv(CaptureActiveEnv, "")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	d := Deps{
		Cfg:    config.Config{Calm: config.CalmConfig{URL: "http://calm.test", Client: "claude-code", SessionTTLMinutes: 60}},
		Logger: logging.Nop(),
		Client: c,
		Root:   t.TempDir(),
		Stdout: stdout,
		Stderr: stderr,
	}
	return d, stdout, stderr
}

// expectEstablish lets a capture establish and ingest under token; register,
// ingest, and event delivery may land any number of times.
func expectEstablish(c *calm.MockClient, token string) {
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	c.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(token, nil).Once()
	c.EXPECT().Ingest(mock.Anything, token, mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:test", SectionsIndexed: 1, SectionsTotal: 1}, nil).Maybe()
	c.EXPECT().WriteEvents(mock.Anything, token, mock.Anything).Return(nil).Maybe()
}

func execArgs(sessionID, command string) []string {
	return []string{"exec", "--session", sessionID, "--", command}
}

func TestDispatch_UsageErrors(t *testing.T) {
	c := calm.NewMockClient(t)
	d, _, stderr := newDeps(t, c)

	if Dispatch(context.Background(), d, nil) != 2 {
		t.Error("empty args must be a usage error")
	}
	if Dispatch(context.Background(), d, []string{"bogus"}) != 2 {
		t.Error("unknown command must be a usage error")
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr must name the unknown command; got:\n%s", stderr.String())
	}
}

func TestResolveRoot(t *testing.T) {
	if r, _ := ResolveRoot("/override"); r != "/override" {
		t.Errorf("override root = %q; want /override", r)
	}
	t.Setenv("CALM_HOME", "/from-env")
	if r, _ := ResolveRoot(""); r != "/from-env" {
		t.Errorf("env root = %q; want /from-env", r)
	}
	t.Setenv("CALM_HOME", "")
	if r, err := ResolveRoot(""); err != nil || !strings.HasSuffix(r, ".calm") {
		t.Errorf("home fallback root = %q (err %v); want ~/.calm", r, err)
	}
}

func TestSessionIDOr(t *testing.T) {
	if sessionIDOr("") != defaultSessionID {
		t.Errorf("empty id must map to %q", defaultSessionID)
	}
	if sessionIDOr("conv") != "conv" {
		t.Error("explicit id must pass through")
	}
}

func TestGCSampleRate(t *testing.T) {
	d := Deps{}
	if d.gcSample() {
		t.Error("rate 0 must disable reclamation sampling")
	}
	d.Cfg.Calm.GCSampleRate = 1
	if !d.gcSample() {
		t.Error("rate 1 must sample every invocation")
	}
}
