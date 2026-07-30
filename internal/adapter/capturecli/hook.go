// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/extract"
	"github.com/one-harsh/calm/internal/adapter/session"
)

// hook is the harness-facing adapter: one stdin JSON payload → one stdout response.
// On the rewrite and pass-through paths it is a pure transform — no CALM client,
// no session lock.

const (
	hookPassThrough = 0

	// hookStdinLimit bounds the payload read so a hostile stdin cannot exhaust
	// memory on the hot path.
	hookStdinLimit = 1 << 20

	eventPreToolUse   = "PreToolUse"
	eventSessionStart = "SessionStart"
	toolBash          = "Bash"

	sourceStartup = "startup"
	sourceClear   = "clear"
	sourceCompact = "compact"
)

type hookPayload struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Source        string `json:"source"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

type hookConfig struct {
	stdin   io.Reader
	stdout  io.Writer
	root    string // "" → degraded: static card, no inventory, no reclamation
	ttl     time.Duration
	logger  *logging.Logger
	binPath string // absolute calm-capture path the rewrite invokes
	recall  string
}

func (d Deps) hookCmd(ctx context.Context) int {
	return runHook(ctx, hookConfig{
		stdin:   d.Stdin,
		stdout:  d.Stdout,
		root:    d.Root,
		ttl:     d.sessionTTL(),
		logger:  d.Logger,
		binPath: hookBinPath(),
		recall:  recallCommand,
	})
}

// HookDegraded is the hook's bootstrap-failure floor (no usable config/logger/client),
// the rewrite and pass-through paths still run — they need nothing but stdin and stdout.
func HookDegraded(ctx context.Context, stdin io.Reader, stdout io.Writer) int {
	return runHook(ctx, hookConfig{
		stdin:   stdin,
		stdout:  stdout,
		logger:  logging.Nop(),
		binPath: hookBinPath(),
		recall:  recallCommand,
	})
}

func runHook(ctx context.Context, c hookConfig) int {
	p, err := readHookPayload(c.stdin)
	if err != nil {
		return hookPassThrough
	}
	switch p.HookEventName {
	case eventPreToolUse:
		return c.handlePreToolUse(p)
	case eventSessionStart:
		return c.handleSessionStart(ctx, p)
	default:
		return hookPassThrough
	}
}

func readHookPayload(r io.Reader) (hookPayload, error) {
	var p hookPayload
	if r == nil {
		return p, errors.New("hook: nil stdin")
	}
	data, err := io.ReadAll(io.LimitReader(r, hookStdinLimit))
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, err
	}
	return p, nil
}

type hookUpdatedInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type hookSpecificOutput struct {
	HookEventName string           `json:"hookEventName"`
	UpdatedInput  hookUpdatedInput `json:"updatedInput"`
}

type preToolUseResponse struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

func (c hookConfig) handlePreToolUse(p hookPayload) int {
	command := p.ToolInput.Command
	if p.ToolName != toolBash || p.SessionID == "" || strings.TrimSpace(command) == "" {
		return hookPassThrough
	}
	// AD07: a command that already invokes calm-capture passes through — no reentrancy.
	if extract.Program(command) == binaryName {
		return hookPassThrough
	}

	// The rewrite is reparsed by the harness's shell, so the single-quoted argv
	// resolves to one shell string (exec's single-string path) and captures the
	// compound command whole. The original command rides in description so the
	// approval dialog — which shows the rewritten string — still names what runs.
	rewrite := shellSingleQuote(c.binPath) + " exec --session " + shellSingleQuote(p.SessionID) + " -- " + shellSingleQuote(command)
	data, err := json.Marshal(preToolUseResponse{
		HookSpecificOutput: hookSpecificOutput{
			HookEventName: eventPreToolUse,
			UpdatedInput:  hookUpdatedInput{Command: rewrite, Description: command},
		},
	})
	if err != nil {
		return hookPassThrough
	}
	_, _ = fmt.Fprintln(c.stdout, string(data))
	return 0
}

func (c hookConfig) handleSessionStart(ctx context.Context, p hookPayload) int {
	// a bootstrap-degraded hook (no usable config) can neither capture nor
	// search, so it claims nothing.
	if c.root == "" {
		return 0
	}
	switch p.Source {
	case sourceStartup, sourceClear:
		c.reclaim(ctx)
		c.writeCard(ctx, p.SessionID, false)
	case sourceCompact:
		c.reclaim(ctx)
		c.writeCard(ctx, p.SessionID, true)
	}
	return 0
}

func (c hookConfig) writeCard(ctx context.Context, sessionID string, refresher bool) {
	captures, entries := c.readInventory(ctx, sessionID)
	if refresher {
		_, _ = fmt.Fprintln(c.stdout, capture.SessionRefresherCard(c.recall, entries))
		return
	}
	_, _ = fmt.Fprintln(c.stdout, capture.SessionStartCard(c.recall, captures, entries))
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

// `shellSingleQuote` wraps `s` in POSIX single quotes, escaping embedded single
// quotes as '\” so the harness's shell reparses s to its exact bytes. The
// rewrite is parsed by the shell, not our tokenizer, so this must be POSIX-exact
// (POSIX harness shells only; Windows shells are out of scope for the hook arm).
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
