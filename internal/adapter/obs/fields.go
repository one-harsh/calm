// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package obs defines the adapter's observability surface: context-bound
// per-call identity (see context.go), structured-log field keys, closed-enum
// values for those fields, and open-domain field constructors.
//
// The adapter's output splits into two surfaces per DESIGN.md §7 — visible
// text the agent reads, and OTel emission for the operator. This file owns
// the structured-log/metric field shape used for the OTel emission surface;
// the closed-enum degraded_reason values are reused as the canonical strings
// in visible-text degradation phrasing so both surfaces stay in lockstep.
//
// TODO: when CALM-core adopts an OTel MeterProvider (today CALM emits metrics
// as structured log fields, not OTel instruments), migrate from log-fields to
// real metric instruments alongside CALM.
package obs

import (
	logging "github.com/one-harsh/context-logging"
)

// Per-call categorical / identifier log field keys (flat snake_case per
// project convention for identifier-style fields).
const (
	KeyCaptured          = "captured"
	KeyDegraded          = "degraded"
	KeyDegradedReason    = "degraded_reason"
	KeyCorrelationID     = "correlation_id"
	KeySourceLabel       = "source_label"
	KeyWorkloadRequestID = "workload_request_id"
)

// Closed enum for degraded_reason. Values surface verbatim in visible-text
// degradation phrasing (per DESIGN.md §7) and in OTel structured log fields.
const (
	DegradedReasonCalmUnreachable       = "calm_unreachable"
	DegradedReasonAuthFailed            = "auth_failed"
	DegradedReasonSessionLost           = "session_lost"
	DegradedReasonCaptureFailed         = "capture_failed"
	DegradedReasonCapturePartial        = "capture_partial"
	DegradedReasonFeedbackWindowExpired = "feedback_window_expired"
)

var (
	DegradedReasonFieldCalmUnreachable       = logging.StringField(KeyDegradedReason, DegradedReasonCalmUnreachable)
	DegradedReasonFieldAuthFailed            = logging.StringField(KeyDegradedReason, DegradedReasonAuthFailed)
	DegradedReasonFieldSessionLost           = logging.StringField(KeyDegradedReason, DegradedReasonSessionLost)
	DegradedReasonFieldCaptureFailed         = logging.StringField(KeyDegradedReason, DegradedReasonCaptureFailed)
	DegradedReasonFieldCapturePartial        = logging.StringField(KeyDegradedReason, DegradedReasonCapturePartial)
	DegradedReasonFieldFeedbackWindowExpired = logging.StringField(KeyDegradedReason, DegradedReasonFeedbackWindowExpired)
)

func DegradedPhrase(reason string) string {
	switch reason {
	case DegradedReasonCalmUnreachable:
		return "CALM degraded — calm_unreachable. Capture and search may fail; local result is shown."
	case DegradedReasonAuthFailed:
		return "CALM degraded — auth_failed. CALM credentials rejected; capture and feedback are disabled for this conversation."
	case DegradedReasonSessionLost:
		return "CALM degraded — session_lost. The prior session expired or was replaced; references to prior captures will fail."
	case DegradedReasonCaptureFailed:
		return "CALM degraded — capture_failed. Local action ran; CALM did not index the output."
	case DegradedReasonCapturePartial:
		return "CALM degraded — capture_partial. Some captured sources were indexed; others were not."
	case DegradedReasonFeedbackWindowExpired:
		return "CALM degraded — feedback_window_expired. The feedback window for this reference has closed."
	default:
		return "CALM degraded — " + reason + "."
	}
}

// Per-call measurement field keys (dotted-schema scoped to entity + action).
// The OTel-Prometheus exporter converts . → _ at emission, so PromQL sees the
// underscored form.
const (
	KeyResponseVisibleBytes = "adapter.response.visible_bytes"
	KeyResponseRawBytes     = "adapter.response.raw_bytes"
	KeyCallDurationMs       = "adapter.call.duration_ms"
	KeyPresentationMode     = "adapter.presentation.mode"
)

const (
	PresentationModeInline  = "inline"
	PresentationModeSummary = "summary"
	PresentationModeRanged  = "ranged"
)

var (
	PresentationModeFieldInline  = logging.StringField(KeyPresentationMode, PresentationModeInline)
	PresentationModeFieldSummary = logging.StringField(KeyPresentationMode, PresentationModeSummary)
	PresentationModeFieldRanged  = logging.StringField(KeyPresentationMode, PresentationModeRanged)
)

func ResponseVisibleBytes(n int) logging.LoggingField {
	return logging.IntField(KeyResponseVisibleBytes, n)
}

func ResponseRawBytes(n int) logging.LoggingField {
	return logging.IntField(KeyResponseRawBytes, n)
}

func CallDurationMs(ms int64) logging.LoggingField {
	return logging.Int64Field(KeyCallDurationMs, ms)
}

func CorrelationID(id string) logging.LoggingField {
	return logging.StringField(KeyCorrelationID, id)
}

func SourceLabel(label string) logging.LoggingField {
	return logging.StringField(KeySourceLabel, label)
}

func WorkloadRequestID(id string) logging.LoggingField {
	return logging.StringField(KeyWorkloadRequestID, id)
}
