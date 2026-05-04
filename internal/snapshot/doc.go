// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

// Package snapshot returns a session's events as a generic event stream per
// HLD §6 / §8 and DL08.
//
// Reads events for the session, orders by (priority asc, created_at desc),
// and accumulates into the response until the requested byte budget is
// reached. CALM does not interpret event content or synthesize structured
// state — workloads needing structured shapes build them in their own
// middleware from the returned event stream.
package snapshot
