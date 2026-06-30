// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/obs"
)

// DegradedSignal is the sentinel handlers return when execution completed in
// a degraded state per DESIGN.md §7. invokeTool translates it into:
//   - degraded summary log fields (degraded=true + degraded_reason)
//   - canonical visible-text phrasing prefix
//   - optional [stderr] block carrying implementer-supplied detail
//   - preservation of handler-supplied content + IsError (never-worse for
//     action tools: handler returns the local result, invokeTool wraps).
//
// Handlers retain ownership of their own contextual warn-log emission for
// operators; the signal carries only what invokeTool centralizes.
type DegradedSignal struct {
	Reason string // obs.DegradedReason* value
	Detail string // optional; surfaced as [stderr] block after the phrasing
}

func (d *DegradedSignal) Error() string {
	return "degraded: " + d.Reason
}

// ArgError is the sentinel handlers return when tool arguments are invalid
// in a way the adapter can detect locally — too many queries, out-of-range
// limit, blank required field, etc. Not degraded — tool-level user input
// error, no CALM state involved.
type ArgError struct {
	Detail string
}

func (a *ArgError) Error() string {
	return "invalid arguments: " + a.Detail
}

// degradedReasonField maps a DegradedSignal.Reason to its pre-constructed
// LoggingField. New degraded_reason values must update both this switch and
// the obs.DegradedReason* / DegradedReasonField* pairs in lockstep.
func degradedReasonField(reason string) logging.LoggingField {
	switch reason {
	case obs.DegradedReasonCalmUnreachable:
		return obs.DegradedReasonFieldCalmUnreachable
	case obs.DegradedReasonAuthFailed:
		return obs.DegradedReasonFieldAuthFailed
	case obs.DegradedReasonSessionLost:
		return obs.DegradedReasonFieldSessionLost
	case obs.DegradedReasonCaptureFailed:
		return obs.DegradedReasonFieldCaptureFailed
	case obs.DegradedReasonCapturePartial:
		return obs.DegradedReasonFieldCapturePartial
	case obs.DegradedReasonFeedbackWindowExpired:
		return obs.DegradedReasonFieldFeedbackWindowExpired
	default:
		return logging.StringField(obs.KeyDegradedReason, reason)
	}
}
