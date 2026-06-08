// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package db

import "errors"

// Every error returned by the DAL must wrap one of these sentinels (directly
// for validation/domain errors, via multi-%w with ErrStorageBackend for
// driver/tx failures) so callers can classify via errors.Is without string
// matching.
//
// Sentinel-first wrap form: the sentinel %w goes at the front of fmt.Errorf
// so the final error string starts with "db: ...". 1-glance log/grep scans
// can identify a DAL error from the prefix without parsing.
//
//	fmt.Errorf("%w: register client %q/%q: %w", ErrStorageBackend, ns, name, err)
//	// → "db: storage backend failure: register client \"ns-a\"/\"alice\": <underlying>"
//
// Every sentinel below starts with "db: " for the same reason.
var (
	// Validation — required input was missing or invalid.

	ErrNamespaceRequired        = errors.New("db: namespace is required")
	ErrClientNameRequired       = errors.New("db: client name is required")
	ErrSessionTokenHashRequired = errors.New("db: session token hash is required")
	ErrSourceRequired           = errors.New("db: source label is required")
	ErrChunksRequired           = errors.New("db: index input has no chunks")
	ErrQueryRequired            = errors.New("db: search query is empty")
	ErrInvalidLimit             = errors.New("db: search limit must be positive")
	ErrInvalidPriority          = errors.New("db: event priority must be 1..4")
	ErrInvalidTTL               = errors.New("db: ttl_minutes must be positive")

	// Domain — business-meaningful states.

	ErrSessionNotFound            = errors.New("db: session not found")
	ErrSessionExists              = errors.New("db: session already exists")
	ErrClientNotFound             = errors.New("db: client not found")
	ErrClientExists               = errors.New("db: client already exists")
	ErrClientProtected            = errors.New("db: cannot delete the default client")
	ErrInvalidClientCredential    = errors.New("db: invalid client credential")
	ErrCorrelationNotFound        = errors.New("db: correlation not found")
	ErrFeedbackAlreadySubmitted   = errors.New("db: feedback already submitted for this correlation")
	ErrCorrelationsNotImplemented = errors.New("db: correlations DAL not implemented")

	// Storage — umbrella for driver/tx failures. Always wrapped via
	// fmt.Errorf("...: %w: %w", ErrStorageBackend, underlying) so callers
	// can errors.Is(err, ErrStorageBackend) and errors.As(err, &pgErr).
	ErrStorageBackend = errors.New("db: storage backend failure")
)
