// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const (
	spoolFileName    = "events.spool"
	inflightPrefix   = "events.inflight."
	staleInflightAge = 10 * time.Minute
	spoolLineTimeout = 2 * time.Second
)

// `spoolLine` is one capture's events, epoch- and token-tagged.
//
// The epoch tag lets a later drain skip lines a session replacement superseded;
// session_token is the credential the events were captured under — a secret,
// never logged, held only in the owner-only spool file.
type spoolLine struct {
	Epoch        int64             `json:"epoch"`
	SessionToken string            `json:"session_token"`
	Events       []calm.EventInput `json:"events"`
}

func (s *store) spoolPath() string { return filepath.Join(s.dir, spoolFileName) }

func (s *store) inflightPath(pid int) string {
	return filepath.Join(s.dir, inflightPrefix+strconv.Itoa(pid))
}

func (s *store) appendSpool(line spoolLine) error {
	//nolint:gosec // the owner-only event spool carries the session token by design
	data, err := json.Marshal(line)
	if err != nil {
		return err
	}
	//nolint:gosec // owner-only spool under $CALM_HOME; it holds the session token by design
	f, err := os.OpenFile(s.spoolPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Emit is the capture CLI's event-delivery seam (capture.Session): it spools
// this capture's events for a later flush rather than delivering in-call. The
// process dies milliseconds after responding, and synchronous delivery would
// tie loss to capture size, clustering drops on the highest-value calls
// (DESIGN.md §11, AD06). One brief lock acquisition appends a line tagged with
// the passed token and the loaded state's current epoch. Best-effort: an append
// failure is logged and never propagates (response-first). Events from a
// replaced generation are rejected before enqueue — an enqueued line would
// carry a lying (current-epoch, dead-token) tag pair.
func (m *Manager) Emit(ctx context.Context, token string, events []calm.EventInput) {
	if len(events) == 0 {
		return
	}
	unlock, err := m.store.lock()
	if err != nil {
		m.log.WithContext(ctx).Warn("lock state for event spool failed; events dropped", logging.ErrorField(err))
		return
	}
	defer unlock()
	st, err := m.store.load()
	if err != nil {
		m.log.WithContext(ctx).Warn("load state for event spool failed; events dropped", logging.ErrorField(err))
		return
	}
	if st == nil {
		m.log.WithContext(ctx).Warn("no session state to spool events into; events dropped")
		return
	}
	if err := m.ownedState(st); err != nil {
		m.log.WithContext(ctx).Warn("event spool refused", logging.ErrorField(err))
		return
	}
	if st.SessionToken != token {
		m.log.WithContext(ctx).Debug("events from replaced session discarded")
		return
	}
	if err := m.store.appendSpool(spoolLine{Epoch: st.Epoch, SessionToken: token, Events: events}); err != nil {
		m.log.WithContext(ctx).Warn("append event spool failed; events dropped", logging.ErrorField(err))
	}
}

// Drain is the single spool-flush primitive: the exec binary calls it immediately
// after a response (immediate flush) and opportunistically at invocation start
// (leftovers). Delivery is at-most-once — stale claims are reaped unread and
// never re-delivered, a superseded epoch is skipped, a 404 drops that line without
// triggering session recovery, and any other transport failure abandons the rest,
// leaving the claim to age into the stale reap (delete-not-replay). The caller
// bounds overall time via ctx.
func (m *Manager) Drain(ctx context.Context) {
	m.reapStaleInflight(ctx, time.Now())
	inflight, ok := m.claimSpool(ctx)
	if !ok {
		return
	}
	m.deliverInflight(ctx, inflight)
}

// reapStaleInflight deletes inflight claims older than staleInflightAge without
// reading them: a claim that outlived its drainer is abandoned, and re-delivery
// is unacceptable until CALM deduplicates events (AD06) — deletion is the
// at-most-once guarantee. No lock: a claim within the stale window is either
// mid-delivery (recent mtime, skipped) or already gone.
func (m *Manager) reapStaleInflight(ctx context.Context, now time.Time) {
	entries, err := os.ReadDir(m.store.dir)
	if err != nil {
		return
	}
	cutoff := now.Add(-staleInflightAge)
	reaped := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), inflightPrefix) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(m.store.dir, e.Name())) == nil {
			reaped++
		}
	}
	if reaped > 0 {
		m.log.WithContext(ctx).Debug("reaped stale event inflight claims", obs.SpoolReapStale(reaped))
	}
}

// claimSpool renames events.spool → events.inflight.<pid> under the session lock
// (serializing against Emit's append). The rename is the claim: a racing drainer
// that loses it simply finds no spool file. ok=false means nothing to claim.
func (m *Manager) claimSpool(ctx context.Context) (string, bool) {
	unlock, err := m.store.lock()
	if err != nil {
		m.log.WithContext(ctx).Warn("lock state for spool claim failed", logging.ErrorField(err))
		return "", false
	}
	defer unlock()
	dst := m.store.inflightPath(os.Getpid())
	if err := os.Rename(m.store.spoolPath(), dst); err != nil {
		if !os.IsNotExist(err) {
			m.log.WithContext(ctx).Warn("claim event spool by rename failed", logging.ErrorField(err))
		}
		return "", false
	}
	// Rename preserves the append-time mtime, but the stale-claim clock must
	// measure time since CLAIM — a long-idle spool must not be born stale, or a
	// concurrent reaper deletes the claim mid-delivery.
	now := time.Now()
	_ = os.Chtimes(dst, now, now)
	return dst, true
}

// deliverInflight delivers a claimed file line by line, outside the lock. A
// superseded epoch is skipped; a 404 drops its line and never triggers recovery
// (AD06); any other transport failure abandons the rest, leaving the file to age
// into the stale reap. The file is removed only once every line was delivered,
// skipped, or dropped.
func (m *Manager) deliverInflight(ctx context.Context, path string) {
	//nolint:gosec // path is a claim this drainer renamed under $CALM_HOME, not attacker-controlled
	data, err := os.ReadFile(path)
	if err != nil {
		m.log.WithContext(ctx).Warn("read claimed event spool failed", logging.ErrorField(err))
		return
	}
	st, err := m.store.load()
	if err != nil {
		m.log.WithContext(ctx).Warn("load state for spool delivery failed; abandoning claim", logging.ErrorField(err))
		return
	}
	if st == nil {
		// The session is gone; its spooled lines can no longer be validated.
		m.discardInflight(ctx, path)
		return
	}

	var delivered, staleDropped, notFoundDropped int
	for _, b := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(b)) == 0 {
			continue
		}
		var ln spoolLine
		if err := json.Unmarshal(b, &ln); err != nil {
			staleDropped++ // an unparseable line is discarded, never retried
			continue
		}
		if ln.Epoch != st.Epoch {
			staleDropped++
			continue
		}
		switch err := m.deliverLine(ctx, ln); {
		case err == nil:
			delivered++
		case errors.Is(err, calm.ErrSessionNotFound):
			notFoundDropped++ // AD06: 404 drops the line, never recovers the session
		default:
			m.log.WithContext(ctx).Warn("event delivery failed; abandoning remaining spooled lines",
				logging.ErrorField(err), obs.SpoolDrainDelivered(delivered),
				obs.SpoolDrainStaleDropped(staleDropped), obs.SpoolDrainNotFoundDropped(notFoundDropped))
			return
		}
	}
	m.log.WithContext(ctx).Debug("event spool drained",
		obs.SpoolDrainDelivered(delivered), obs.SpoolDrainStaleDropped(staleDropped),
		obs.SpoolDrainNotFoundDropped(notFoundDropped))
	m.discardInflight(ctx, path)
}

func (m *Manager) deliverLine(ctx context.Context, ln spoolLine) error {
	wctx, cancel := context.WithTimeout(ctx, spoolLineTimeout)
	defer cancel()
	return m.calm.WriteEvents(wctx, ln.SessionToken, ln.Events)
}

func (m *Manager) discardInflight(ctx context.Context, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		m.log.WithContext(ctx).Warn("remove drained event spool failed", logging.ErrorField(err))
	}
}
