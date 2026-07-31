// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/capturecli/harness"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/session"
)

const (
	hookPassThrough = 0

	// hookStdinLimit bounds the payload read so a hostile stdin cannot exhaust
	// memory on the hot path.
	hookStdinLimit = 1 << 20
)

type hookConfig struct {
	stdin   io.Reader
	stdout  io.Writer
	root    string // "" → degraded: static card, no inventory, no reclamation
	ttl     time.Duration
	logger  *logging.Logger
	binPath string
	// deps carries the capture stack the observation path needs; zero-valued on
	// the degraded floor, where the observation path passes through.
	deps Deps
}

func (d Deps) hookCmd(ctx context.Context) int {
	return runHook(ctx, hookConfig{
		stdin:   d.Stdin,
		stdout:  d.Stdout,
		root:    d.Root,
		ttl:     d.sessionTTL(),
		logger:  d.Logger,
		binPath: hookBinPath(),
		deps:    d,
	})
}

// HookDegraded is the hook's bootstrap-failure floor: the rewrite and pass-through
// paths still run — they need nothing but stdin and stdout.
func HookDegraded(ctx context.Context, stdin io.Reader, stdout io.Writer) int {
	return runHook(ctx, hookConfig{
		stdin:   stdin,
		stdout:  stdout,
		logger:  logging.Nop(),
		binPath: hookBinPath(),
	})
}

func runHook(ctx context.Context, c hookConfig) int {
	stdin, err := readStdin(c.stdin)
	if err != nil {
		return hookPassThrough
	}
	ev := harness.Claude.Parse(stdin)
	switch ev.Kind {
	case harness.KindRewrite:
		return c.handleRewrite(ev.Rewrite)
	case harness.KindObserve:
		return c.handleObserve(ctx, ev.Observe)
	case harness.KindSessionStart:
		return c.handleSessionStart(ctx, ev.SessionStart)
	default:
		return hookPassThrough
	}
}

func readStdin(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, errors.New("hook: nil stdin")
	}
	return io.ReadAll(io.LimitReader(r, hookStdinLimit))
}

func (c hookConfig) handleRewrite(ev harness.RewriteEvent) int {
	if ev.SessionID == "" || strings.TrimSpace(ev.Command) == "" {
		return hookPassThrough
	}
	// AD07: a command that invokes calm-capture in any pipeline segment passes
	// through — plumbing must not re-wrap capture's own output.
	if extract.InvokesProgram(ev.Command, binaryName) {
		return hookPassThrough
	}
	// The rewrite is reparsed by the harness's shell, so the single-quoted argv
	// resolves to one shell string (exec's single-string path) and captures the
	// compound command whole. The original rides in description so the approval
	// dialog — which shows the rewritten string — still names what runs.
	rewrite := shellSingleQuote(c.binPath) + " exec --session " + shellSingleQuote(ev.SessionID) + " -- " + shellSingleQuote(ev.Command)
	wire := harness.Claude.RenderRewrite(harness.RewriteResponse{Command: rewrite, Description: ev.Command})
	if wire == nil {
		return hookPassThrough
	}
	_, _ = fmt.Fprintln(c.stdout, string(wire))
	return 0
}

func (c hookConfig) handleSessionStart(ctx context.Context, ev harness.SessionStartEvent) int {
	if c.root == "" {
		return 0
	}
	switch ev.Disposition {
	case harness.DispositionFreshCard:
		c.reclaim(ctx)
		c.writeCard(ctx, ev.SessionID, false)
		c.warnOtherLayers()
	case harness.DispositionRefresherCard:
		c.reclaim(ctx)
		c.writeCard(ctx, ev.SessionID, true)
		c.warnOtherLayers()
	}
	return 0
}

// warnOtherLayers re-checks for a stacked capture layer at runtime (AD07): a
// layer can appear after install, so the card names it and the project-scope one wins.
func (c hookConfig) warnOtherLayers() {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	for _, p := range harness.Claude.OtherHookLayers(home, cwd) {
		_, _ = fmt.Fprintf(c.stdout, "warning: %s already references %s — stacked capture layers corrupt capture identity; the project-scope layer wins, so remove the user-scope one\n", p, binaryName)
	}
}

func (c hookConfig) writeCard(ctx context.Context, sessionID string, refresher bool) {
	captures, entries := c.readInventory(ctx, sessionID)
	recall := recallFor(c.binPath, sessionID)
	if refresher {
		_, _ = fmt.Fprintln(c.stdout, capture.SessionRefresherCard(recall, entries))
		return
	}
	_, _ = fmt.Fprintln(c.stdout, capture.SessionStartCard(recall, captures, entries))
}

func (c hookConfig) readInventory(ctx context.Context, sessionID string) (int64, []capture.InventoryEntry) {
	if c.root == "" || sessionID == "" {
		return 0, nil
	}
	mgr, err := session.New(session.Config{SessionID: sessionID, Logger: c.logger, RootDir: c.root})
	if err != nil {
		return 0, nil
	}
	captures, entries, err := mgr.Inventory(ctx)
	if err != nil {
		return 0, nil
	}
	return captures, entries
}

func (c hookConfig) reclaim(ctx context.Context) {
	if c.root == "" {
		return
	}
	if err := session.GC(c.root, c.ttl); err != nil {
		c.logger.WithContext(ctx).Warn("reclamation sweep failed", logging.ErrorField(err))
	}
}

func hookBinPath() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return binaryName
}

// shellSingleQuote wraps s in POSIX single quotes so the harness's shell reparses
// s to its exact bytes. Parsed by the shell, not our tokenizer, so it must be
// POSIX-exact (POSIX harness shells only; Windows shells are out of scope).
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
