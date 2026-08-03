// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"github.com/one-harsh/calm/internal/adapter/capture"
)

// DegradedSignal separates centralized agent signaling from handler-owned diagnostics.
type DegradedSignal struct {
	Reason string // obs.DegradedReason* value
	Detail string // optional; surfaced as [stderr] block after the phrasing
}

func (d *DegradedSignal) Error() string {
	return "degraded: " + d.Reason
}

func (d *DegradedSignal) toCapture() *capture.Signal {
	if d == nil {
		return nil
	}
	return &capture.Signal{Reason: d.Reason, Detail: d.Detail}
}

// ArgError keeps caller mistakes out of degradation telemetry.
type ArgError struct {
	Detail string
}

func (a *ArgError) Error() string {
	return "invalid arguments: " + a.Detail
}
