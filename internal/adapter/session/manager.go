// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package session is the capture shell's session-state strategy: the engine's
// per-session state persisted on disk under an exclusive advisory lock, one
// directory per harness conversation (DESIGN.md §10, AD05). It implements
// capture.Session so the capture engine reaches session state through the
// frozen seam, and is consumed by the calm-capture CLI. Establishment and
// recovery hold the lock across their one network call; ingest never does.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/capture"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

const (
	sessionOpTimeout       = 5 * time.Second
	establishRetryInterval = 60 * time.Second
)

var (
	_ capture.Session   = (*Manager)(nil)
	_ capture.EventSink = (*Manager)(nil)
)

type Config struct {
	SessionID  string
	Client     string
	CALM       calm.Client
	Logger     *logging.Logger
	TTLMinutes int
	RootDir    string // override; empty → $CALM_HOME → ~/.calm
}

// Manager persists the engine's session state for one harness conversation and
// implements capture.Session. Every method takes the lock, does the smallest
// load-modify-save, and releases it; a network create runs under the lock only
// on establish and recovery.
type Manager struct {
	store     *store
	calm      calm.Client
	log       *logging.Logger
	sessionID string
	client    string
	ttlMin    int
}

func New(cfg Config) (*Manager, error) {
	root, err := resolveRoot(cfg.RootDir)
	if err != nil {
		return nil, err
	}
	return &Manager{
		store:     newStore(root, cfg.SessionID),
		calm:      cfg.CALM,
		log:       cfg.Logger,
		sessionID: cfg.SessionID,
		client:    cfg.Client,
		ttlMin:    cfg.TTLMinutes,
	}, nil
}

func resolveRoot(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if h := os.Getenv("CALM_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".calm"), nil
}

func (m *Manager) Ensure(ctx context.Context) (capture.EnsureResult, *capture.Signal) {
	unlock, err := m.store.lock()
	if err != nil {
		return capture.EnsureResult{}, m.storeFailure(ctx, "lock state for ensure", err)
	}
	defer unlock()

	st, err := m.load(ctx)
	if err != nil {
		return capture.EnsureResult{}, m.storeFailure(ctx, "load state for ensure", err)
	}
	if st.AuthFailed {
		return capture.EnsureResult{}, &capture.Signal{Reason: obs.DegradedReasonAuthFailed}
	}
	if st.SessionToken != "" {
		st.Seq++
		if err := m.store.save(st); err != nil {
			return capture.EnsureResult{}, m.storeFailure(ctx, "save sequence", err)
		}
		return capture.EnsureResult{Token: st.SessionToken, Seq: st.Seq}, nil
	}

	if throttleActive(st, time.Now()) {
		m.log.WithContext(ctx).Debug("establishment throttled; degrading without a create attempt")
		return capture.EnsureResult{}, &capture.Signal{
			Reason: obs.DegradedReasonCalmUnreachable,
			Detail: "session establishment throttled after a recent failure",
		}
	}

	// Establish uses the current (unincremented) recovery counter so racing
	// first invocations that reach CALM present the same idempotency key.
	key := st.IdempotencyBase + "-r" + strconv.Itoa(st.RecoverySeq)
	if sig := m.establish(ctx, st, key); sig != nil {
		if sig.Reason == obs.DegradedReasonCalmUnreachable {
			st.NextAttemptAt = time.Now().Add(establishRetryInterval)
		}
		if serr := m.store.save(st); serr != nil {
			m.log.WithContext(ctx).Warn("persist establish outcome failed", logging.ErrorField(serr))
		}
		return capture.EnsureResult{}, sig
	}
	st.NextAttemptAt = time.Time{}
	st.Seq++
	if err := m.store.save(st); err != nil {
		return capture.EnsureResult{}, m.storeFailure(ctx, "save established session", err)
	}
	return capture.EnsureResult{Token: st.SessionToken, Seq: st.Seq}, nil
}

// Record is the capture CLI's post-ingest registry merge (DESIGN.md §10, §11):
// one lock acquisition merges the persisted delta into the on-disk registry.
// Best-effort — a failed save logs WARN and never propagates (response-first). A
// delta from a replaced generation is rejected: a dead-generation label must fail
// with staleness, never validate against the new session (honest capture
// continuity).
func (m *Manager) Record(ctx context.Context, token string, delta []capture.SourceToken) {
	if len(delta) == 0 {
		return
	}
	unlock, err := m.store.lock()
	if err != nil {
		m.log.WithContext(ctx).Warn("lock state for record failed; delta not persisted", logging.ErrorField(err))
		return
	}
	defer unlock()
	st, err := m.store.load()
	if err != nil {
		m.log.WithContext(ctx).Warn("load state for record failed; delta not persisted", logging.ErrorField(err))
		return
	}
	if st == nil {
		m.log.WithContext(ctx).Warn("no session state to record into; delta dropped")
		return
	}
	if err := m.ownedState(st); err != nil {
		m.log.WithContext(ctx).Warn("record refused", logging.ErrorField(err))
		return
	}
	if st.SessionToken != token {
		m.log.WithContext(ctx).Debug("registry delta from replaced session discarded")
		return
	}
	for _, d := range delta {
		st.Registry[d.Source] = d.Token
	}
	if err := m.store.save(st); err != nil {
		m.log.WithContext(ctx).Warn("save registry delta failed", logging.ErrorField(err))
	}
}

// Enqueue is the capture CLI's post-ingest event spool: its own lock acquisition
// tags the finalized events with the current epoch and appends them for a later
// invocation to drain. Best-effort — a failed append logs WARN and never
// propagates (response-first). Events from a replaced generation are rejected: an
// enqueued line would carry a lying (current-epoch, dead-token) tag pair, and a
// dead-generation label must fail with staleness, never validate against the new
// session (honest capture continuity).
func (m *Manager) Enqueue(ctx context.Context, token string, events []calm.EventInput) {
	if len(events) == 0 {
		return
	}
	unlock, err := m.store.lock()
	if err != nil {
		m.log.WithContext(ctx).Warn("lock state for enqueue failed; events not spooled", logging.ErrorField(err))
		return
	}
	defer unlock()
	st, err := m.store.load()
	if err != nil {
		m.log.WithContext(ctx).Warn("load state for enqueue failed; events not spooled", logging.ErrorField(err))
		return
	}
	if st == nil {
		m.log.WithContext(ctx).Warn("no session state to enqueue into; events dropped")
		return
	}
	if err := m.ownedState(st); err != nil {
		m.log.WithContext(ctx).Warn("enqueue refused", logging.ErrorField(err))
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

// OnCallError classifies a session-level CALM-call failure: 401/403 latches
// auth_failed; 404 triggers one replacement create (AD03). Anything else is not
// session-level and returns nil.
func (m *Manager) OnCallError(ctx context.Context, failedToken string, err error) *capture.Signal {
	switch {
	case errors.Is(err, calm.ErrAuthRejected):
		return m.latchAuth(ctx, err)
	case errors.Is(err, calm.ErrSessionNotFound):
		return m.recover(ctx, failedToken)
	default:
		return nil
	}
}

// Reset clears the auth latch and session token so the next capture establishes
// a fresh session — the mechanism behind `init --reset`. Reset abandons the
// session, so the recovery counter and epoch advance exactly as a 404 recovery
// does: within CALM's create-idempotency window an unbumped key would silently
// resume the abandoned session. The sequence persists so history labels stay
// monotonic. It never deletes the CALM session: teardown is CALM-side TTL
// reclaim.
func (m *Manager) Reset(_ context.Context) error {
	unlock, err := m.store.lock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := m.store.load()
	if err != nil || st == nil {
		return err
	}
	if err := m.ownedState(st); err != nil {
		return err
	}
	st.AuthFailed = false
	st.SessionToken = ""
	st.NextAttemptAt = time.Time{}
	st.RecoverySeq++
	st.Epoch++
	st.Registry = map[string]string{}
	return m.store.save(st)
}

func (m *Manager) load(ctx context.Context) (*state, error) {
	st, err := m.store.load()
	if err != nil {
		return nil, err
	}
	if err := m.ownedState(st); err != nil {
		return nil, err
	}
	if st == nil {
		st = newState(m.sessionID, m.client, time.Now())
		m.log.WithContext(ctx).Debug("seeded fresh session state",
			logging.StringField("idempotency_base", st.IdempotencyBase))
	}
	return st, nil
}

// ownedState guards against a routing bug or a copied directory handing this
// conversation another conversation's record — cross-writing would send this
// conversation's captures into that conversation's CALM session (the shell-side
// face of session-isolation). Refusal degrades honestly; it never rewrites.
func (m *Manager) ownedState(st *state) error {
	if st != nil && st.SessionID != m.sessionID {
		return fmt.Errorf("state belongs to session %q, not %q", st.SessionID, m.sessionID)
	}
	return nil
}

func (m *Manager) establish(ctx context.Context, st *state, key string) *capture.Signal {
	if sig := m.register(ctx, st); sig != nil {
		return sig
	}
	token, err := m.create(ctx, key)
	switch {
	case err == nil:
		st.SessionToken = token
		st.Client = m.client
		m.log.WithContext(ctx).Info("session created", logging.StringField("client", m.client))
		return nil
	case isStatus4xx(err):
		st.AuthFailed = true
		st.SessionToken = ""
		m.log.WithContext(ctx).Warn("session create rejected; CALM disabled for this conversation",
			obs.DegradedReasonFieldAuthFailed, logging.ErrorField(err))
		return &capture.Signal{Reason: obs.DegradedReasonAuthFailed}
	default:
		m.log.WithContext(ctx).Warn("session create failed; will retry on next capture",
			obs.DegradedReasonFieldCalmUnreachable, logging.ErrorField(err))
		return &capture.Signal{Reason: obs.DegradedReasonCalmUnreachable, Detail: err.Error()}
	}
}

// register re-attempts client registration; an unregistered client's create is
// a guaranteed rejection that must not read as a credential verdict (DESIGN.md
// §4). Success is idempotent and deliberately not persisted.
func (m *Manager) register(ctx context.Context, st *state) *capture.Signal {
	rctx, cancel := context.WithTimeout(ctx, sessionOpTimeout)
	_, err := m.calm.RegisterClient(rctx, m.client)
	cancel()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, calm.ErrAuthRejected):
		st.AuthFailed = true
		m.log.WithContext(ctx).Warn("client registration rejected; CALM disabled for this conversation",
			obs.DegradedReasonFieldAuthFailed, logging.ErrorField(err))
		return &capture.Signal{Reason: obs.DegradedReasonAuthFailed}
	default:
		m.log.WithContext(ctx).Warn("client registration failed; deferring session create",
			obs.DegradedReasonFieldCalmUnreachable, logging.ErrorField(err))
		return &capture.Signal{Reason: obs.DegradedReasonCalmUnreachable, Detail: err.Error()}
	}
}

func (m *Manager) create(ctx context.Context, key string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, sessionOpTimeout)
	defer cancel()
	return m.calm.CreateSession(cctx, m.client, m.ttlMin, key)
}

// latchAuth sets the terminal auth latch under the lock. A direct 401/403 is
// unambiguous — CALM rejects credentials before resolving the session — so no
// recovery is attempted (AD03).
func (m *Manager) latchAuth(ctx context.Context, cause error) *capture.Signal {
	unlock, err := m.store.lock()
	if err != nil {
		return m.storeFailure(ctx, "lock state for auth latch", err)
	}
	defer unlock()
	st, err := m.load(ctx)
	if err != nil {
		return m.storeFailure(ctx, "load state for auth latch", err)
	}
	st.AuthFailed = true
	st.SessionToken = ""
	if err := m.store.save(st); err != nil {
		return m.storeFailure(ctx, "save auth latch", err)
	}
	m.log.WithContext(ctx).Warn("CALM rejected credentials; CALM disabled for this conversation",
		obs.DegradedReasonFieldAuthFailed, logging.ErrorField(cause))
	return &capture.Signal{Reason: obs.DegradedReasonAuthFailed}
}

// recover replaces a lost session (404) with one create under the lock, resets
// the registry, and bumps the epoch; the sequence continues, never resets
// (AD03). A concurrent process may have already replaced it — a token mismatch
// means recovery is done and this call simply failed against the dead token.
func (m *Manager) recover(ctx context.Context, failedToken string) *capture.Signal {
	unlock, err := m.store.lock()
	if err != nil {
		return m.storeFailure(ctx, "lock state for recovery", err)
	}
	defer unlock()
	st, err := m.load(ctx)
	if err != nil {
		return m.storeFailure(ctx, "load state for recovery", err)
	}
	if st.AuthFailed {
		return &capture.Signal{Reason: obs.DegradedReasonAuthFailed}
	}
	if st.SessionToken != failedToken {
		return &capture.Signal{Reason: obs.DegradedReasonSessionLost}
	}

	st.RecoverySeq++
	key := st.IdempotencyBase + "-r" + strconv.Itoa(st.RecoverySeq)
	token, cerr := m.create(ctx, key)
	switch {
	case cerr == nil:
		st.SessionToken = token
		st.Epoch++
		st.Registry = map[string]string{}
		if serr := m.store.save(st); serr != nil {
			return m.storeFailure(ctx, "save replacement session", serr)
		}
		m.log.WithContext(ctx).Warn("session lost; replacement created", obs.DegradedReasonFieldSessionLost)
		return &capture.Signal{Reason: obs.DegradedReasonSessionLost}
	case isStatus4xx(cerr):
		st.AuthFailed = true
		st.SessionToken = ""
		if serr := m.store.save(st); serr != nil {
			return m.storeFailure(ctx, "save auth latch after recovery", serr)
		}
		m.log.WithContext(ctx).Warn("session recovery rejected; CALM disabled for this conversation",
			obs.DegradedReasonFieldAuthFailed, logging.ErrorField(cerr))
		return &capture.Signal{Reason: obs.DegradedReasonAuthFailed}
	default:
		// Transient failure teaches nothing about credentials; the dead token
		// stays on disk and the incremented counter is not persisted, so the
		// next 404 re-attempts with the same idempotency key.
		m.log.WithContext(ctx).Warn("session recovery failed; will retry on next session loss",
			obs.DegradedReasonFieldCalmUnreachable, logging.ErrorField(cerr))
		return &capture.Signal{Reason: obs.DegradedReasonCalmUnreachable, Detail: cerr.Error()}
	}
}

// storeFailure classifies a local state-store error (lock/load/save). CALM is
// reachable — the local capture machinery failed — so it maps to capture_failed.
func (m *Manager) storeFailure(ctx context.Context, what string, err error) *capture.Signal {
	m.log.WithContext(ctx).Warn("session state store error: "+what,
		obs.DegradedReasonFieldCaptureFailed, logging.ErrorField(err))
	return &capture.Signal{Reason: obs.DegradedReasonCaptureFailed, Detail: err.Error()}
}

// View is the retrieval path's read-only slice of session state: the current
// token, the auth latch, the epoch, and a hydrated staleness registry. It never
// establishes a session nor allocates a sequence — retrieval fails with its
// unavailability signal before the first capture (DESIGN.md §4).
type View struct {
	Token      string
	AuthFailed bool
	Epoch      int64
	Registry   *capture.Registry
}

func (m *Manager) View(ctx context.Context) (View, error) {
	unlock, err := m.store.lock()
	if err != nil {
		return View{}, err
	}
	defer unlock()
	st, err := m.store.load()
	if err != nil {
		return View{}, err
	}
	if err := m.ownedState(st); err != nil {
		return View{}, err
	}
	reg := capture.NewRegistry()
	if st == nil {
		m.log.WithContext(ctx).Debug("view of never-established session")
		return View{Registry: reg}, nil
	}
	reg.Load(st.Registry)
	return View{Token: st.SessionToken, AuthFailed: st.AuthFailed, Epoch: st.Epoch, Registry: reg}, nil
}

// `throttleActive` reports whether the persisted establishment throttle defers
// this attempt. A stamp further out than one full interval cannot have been
// legitimately written (clock skew); it is ignored rather than honored so a
// bogus far-future stamp can never disable capture for the conversation.
func throttleActive(st *state, now time.Time) bool {
	return now.Before(st.NextAttemptAt) && !st.NextAttemptAt.After(now.Add(establishRetryInterval))
}

func isStatus4xx(err error) bool {
	var se *calm.StatusError
	return errors.As(err, &se) && se.Code >= 400 && se.Code < 500
}
