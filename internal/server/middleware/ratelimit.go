// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import "net/http"

// RateLimit enforces a per-namespace requests-per-second cap (HLD §11 → 429).
// Scaffolding placeholder; concrete impl lands with the auth/session work.
func RateLimit(perSecond int) func(http.Handler) http.Handler {
	_ = perSecond
	return func(next http.Handler) http.Handler {
		return next
	}
}
