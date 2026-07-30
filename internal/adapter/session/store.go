// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const (
	stateVersion      = 1
	stateFileName     = "state.json"
	idempotencyPrefix = "harness-"
)

// safeSessionID matches ids usable verbatim as a directory name: no path
// separators, no dots (traversal), bounded length, and lowercase only — NTFS
// and default APFS compare names case-insensitively, so two ids differing only
// in case must never share a directory; mixed-case ids route to the hash path.
// Anything else is hashed so a hostile id can neither escape the sessions root
// nor collide with another id.
var safeSessionID = regexp.MustCompile(`^[a-z0-9_-]{1,128}$`)

// state is the on-disk session record (state.json v1). session_token is a
// secret — the file is owner-only and the token is never logged.
type state struct {
	Version         int               `json:"version"`
	SessionID       string            `json:"session_id"`
	SessionToken    string            `json:"session_token"`
	Client          string            `json:"client"`
	IdempotencyBase string            `json:"idempotency_base"`
	RecoverySeq     int               `json:"recovery_seq"`
	Seq             int64             `json:"seq"`
	Epoch           int64             `json:"epoch"`
	AuthFailed      bool              `json:"auth_failed"`
	NextAttemptAt   time.Time         `json:"next_attempt_at"`
	CreatedAt       time.Time         `json:"created_at"`
	Registry        map[string]string `json:"registry"`
	// RegistrySeq stamps each registry identity with the sequence at which it
	// was last captured, so the session-start inventory can order identities by
	// recency (a latest label carries no `#seq`). Additive on the versioned
	// schema: a state file predating it loads as nil and refills on next Record.
	RegistrySeq map[string]int64 `json:"registry_seq,omitempty"`
}

// store resolves one conversation's on-disk layout and performs atomic
// load/save. Callers hold the lock (see lock.go) across every load-modify-save.
type store struct {
	dir string
}

func newStore(root, sessionID string) *store {
	return &store{dir: filepath.Join(root, "sessions", sanitizeSessionID(sessionID))}
}

func (s *store) statePath() string { return filepath.Join(s.dir, stateFileName) }

// lockPath is a sibling of the session directory, never inside it: reclamation
// can hold the lock through the directory's removal on every platform, and the
// lock file itself is never deleted, so a held lock can never be unlinked out
// from under its holder.
func (s *store) lockPath() string { return s.dir + ".lock" }

// load returns the persisted state, or nil when none exists yet.
func (s *store) load() (*state, error) {
	data, err := os.ReadFile(s.statePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	// A future-format file is refused, never rewritten: saving it back through
	// this schema would silently erase fields this version doesn't know.
	if st.Version != stateVersion {
		return nil, fmt.Errorf("state version %d unsupported (want %d)", st.Version, stateVersion)
	}
	if st.Registry == nil {
		st.Registry = map[string]string{}
	}
	if st.RegistrySeq == nil {
		st.RegistrySeq = map[string]int64{}
	}
	return &st, nil
}

// save writes state atomically: a fresh per-pid temp file is fsynced then
// renamed over state.json, so a crash mid-write leaves the prior state intact
// and at worst orphans the temp file (reaped by GC).
func (s *store) save(st *state) error {
	//nolint:gosec // AD05: the owner-only state file is authoritative for the session token by design
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.statePath() + ".tmp." + strconv.Itoa(os.Getpid())
	//nolint:gosec // tmp is the adapter's own state file under $CALM_HOME, not attacker-controlled
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// newState seeds a fresh record for sessionID with its derived idempotency base.
func newState(sessionID, client string, now time.Time) *state {
	return &state{
		Version:         stateVersion,
		SessionID:       sessionID,
		Client:          client,
		IdempotencyBase: idempotencyBase(sessionID),
		CreatedAt:       now,
		Registry:        map[string]string{},
		RegistrySeq:     map[string]int64{},
	}
}

// idempotencyBase derives the stable create-idempotency prefix from the session
// id (total well under 256 chars). Recovery creates extend it with the persisted
// recovery counter so a replacement can never collide with the original (AD05).
func idempotencyBase(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return idempotencyPrefix + hex.EncodeToString(sum[:])
}

// sanitizeSessionID maps a session id to a filesystem-safe directory name: safe
// ids pass verbatim, hostile ids hash to a stable collision-resistant name.
func sanitizeSessionID(id string) string {
	if safeSessionID.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
