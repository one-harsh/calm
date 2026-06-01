// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
)

// BodySizeLimit enforces the configured ingest cap.
func BodySizeLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				writePayloadTooLarge(w, maxBytes)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			defer func() {
				var maxErr *http.MaxBytesError
				if r := recover(); r != nil {
					if err, ok := r.(error); ok && errors.As(err, &maxErr) {
						writePayloadTooLarge(w, maxBytes)
						return
					}
					panic(r)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func writePayloadTooLarge(w http.ResponseWriter, maxBytes int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":     "payload too large",
		"max_bytes": maxBytes,
	})
}
