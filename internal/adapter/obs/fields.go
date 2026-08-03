// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package obs keeps agent-visible degradation and operator telemetry on one
// closed vocabulary without putting telemetry in model context.
// TODO: migrate measurement fields to instruments when CALM adopts a MeterProvider.
package obs

import (
	logging "github.com/one-harsh/context-logging"
)

const (
	KeyCaptured          = "captured"
	KeyDegraded          = "degraded"
	KeyDegradedReason    = "degraded_reason"
	KeyCorrelationID     = "correlation_id"
	KeySourceLabel       = "source_label"
	KeyWorkloadRequestID = "workload_request_id"
	// KeyReplaced reports delivery, not merely the engine's presentation choice.
	KeyReplaced = "replaced"
)

// Values surface verbatim in both degradation text and operator telemetry.
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

func DegradedReasonField(reason string) logging.LoggingField {
	switch reason {
	case DegradedReasonCalmUnreachable:
		return DegradedReasonFieldCalmUnreachable
	case DegradedReasonAuthFailed:
		return DegradedReasonFieldAuthFailed
	case DegradedReasonSessionLost:
		return DegradedReasonFieldSessionLost
	case DegradedReasonCaptureFailed:
		return DegradedReasonFieldCaptureFailed
	case DegradedReasonCapturePartial:
		return DegradedReasonFieldCapturePartial
	case DegradedReasonFeedbackWindowExpired:
		return DegradedReasonFieldFeedbackWindowExpired
	default:
		return logging.StringField(KeyDegradedReason, reason)
	}
}

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

// Prometheus export converts dotted measurement keys to underscores.
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

	ReplacedFieldTrue  = logging.BoolField(KeyReplaced, true)
	ReplacedFieldFalse = logging.BoolField(KeyReplaced, false)
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

const (
	KeySpoolDrainDelivered       = "adapter.spool.drain.delivered"
	KeySpoolDrainStaleDropped    = "adapter.spool.drain.stale_dropped"
	KeySpoolDrainNotFoundDropped = "adapter.spool.drain.not_found_dropped"
	KeySpoolReapStale            = "adapter.spool.reap.stale"
)

func SpoolDrainDelivered(n int) logging.LoggingField {
	return logging.IntField(KeySpoolDrainDelivered, n)
}

func SpoolDrainStaleDropped(n int) logging.LoggingField {
	return logging.IntField(KeySpoolDrainStaleDropped, n)
}

func SpoolDrainNotFoundDropped(n int) logging.LoggingField {
	return logging.IntField(KeySpoolDrainNotFoundDropped, n)
}

func SpoolReapStale(n int) logging.LoggingField {
	return logging.IntField(KeySpoolReapStale, n)
}
