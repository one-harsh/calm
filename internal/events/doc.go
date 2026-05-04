// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package events implements structured event capture per HLD §4 and §6.
//
// Event type and content are workload-defined and opaque to CALM (HLD DL08).
// Responsibilities: validate priority is in range 1..4, compute the dedup
// hash over (type, data), and persist via the DAL.
package events
