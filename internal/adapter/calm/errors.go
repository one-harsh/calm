// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm

import "errors"

var (
	ErrSessionNotFound = errors.New("calm: session not found")
	ErrAuthRejected    = errors.New("calm: credentials rejected")
)

// StatusError is a non-2xx CALM response. Error text preserves the
// "<op>: <status>" shape so log lines and degradation details read the same
// as a plain formatted error.
type StatusError struct {
	Op     string
	Code   int
	Status string
}

func (e *StatusError) Error() string {
	return e.Op + ": " + e.Status
}

func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrSessionNotFound:
		return e.Code == 404
	case ErrAuthRejected:
		// CALM emits only 401; 403 accepted defensively for edge-gated deployments.
		return e.Code == 401 || e.Code == 403
	}
	return false
}

func statusErr(op string, code int, status string) error {
	return &StatusError{Op: op, Code: code, Status: status}
}
