// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package obs

import (
	"testing"
)

func TestLogFieldKeys_StableNames(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{KeyCaptured, "captured"},
		{KeyDegraded, "degraded"},
		{KeyDegradedReason, "degraded_reason"},
		{KeyCorrelationID, "correlation_id"},
		{KeySourceLabel, "source_label"},
		{KeyWorkloadRequestID, "workload_request_id"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("key drift: got %q, want %q", c.got, c.want)
		}
	}
}

func TestDegradedReasonValues_StableEnum(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{DegradedReasonCalmUnreachable, "calm_unreachable"},
		{DegradedReasonAuthFailed, "auth_failed"},
		{DegradedReasonSessionLost, "session_lost"},
		{DegradedReasonCaptureFailed, "capture_failed"},
		{DegradedReasonCapturePartial, "capture_partial"},
		{DegradedReasonFeedbackWindowExpired, "feedback_window_expired"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("enum value drift: got %q, want %q", c.got, c.want)
		}
	}
}

func TestMetricKeys_StableNames(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{KeyResponseVisibleBytes, "adapter.response.visible_bytes"},
		{KeyResponseRawBytes, "adapter.response.raw_bytes"},
		{KeyCallDurationMs, "adapter.call.duration_ms"},
		{KeyPresentationMode, "adapter.presentation.mode"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("metric key drift: got %q, want %q", c.got, c.want)
		}
	}
}

func TestPresentationModeValues_StableEnum(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{PresentationModeInline, "inline"},
		{PresentationModeSummary, "summary"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("presentation mode drift: got %q, want %q", c.got, c.want)
		}
	}
}

func TestDegradedPhrase_CanonicalStrings(t *testing.T) {
	cases := []struct {
		reason, want string
	}{
		{DegradedReasonCalmUnreachable, "CALM degraded — calm_unreachable. Capture and search may fail; local result is shown."},
		{DegradedReasonAuthFailed, "CALM degraded — auth_failed. CALM credentials rejected; capture and feedback are disabled for this conversation."},
		{DegradedReasonSessionLost, "CALM degraded — session_lost. The prior session expired or was replaced; references to prior captures will fail."},
		{DegradedReasonCaptureFailed, "CALM degraded — capture_failed. Local action ran; CALM did not index the output."},
		{DegradedReasonCapturePartial, "CALM degraded — capture_partial. Some captured sources were indexed; others were not."},
		{DegradedReasonFeedbackWindowExpired, "CALM degraded — feedback_window_expired. The feedback window for this reference has closed."},
	}
	for _, c := range cases {
		if got := DegradedPhrase(c.reason); got != c.want {
			t.Errorf("DegradedPhrase(%q) = %q, want %q", c.reason, got, c.want)
		}
	}
}

func TestDegradedPhrase_UnknownFallback(t *testing.T) {
	// A new enum value the helper doesn't yet know about must still produce a
	// structured stub — never-worse, never panic.
	got := DegradedPhrase("hypothetical_future_reason")
	want := "CALM degraded — hypothetical_future_reason."
	if got != want {
		t.Errorf("unknown-reason fallback: got %q, want %q", got, want)
	}
}
