// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
)

const (
	workloadRequestIDHeader    = "X-Workload-Request-Id"
	maxWorkloadRequestIDLength = 256
)

func WorkloadRequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if workloadReq := r.Header.Get(workloadRequestIDHeader); workloadReq != "" {
				if len(workloadReq) > maxWorkloadRequestIDLength {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"x_workload_request_id_too_long","detail":"X-Workload-Request-Id exceeds 256 characters"}`))
					return
				}
				w.Header().Set(workloadRequestIDHeader, workloadReq)
			}
			next.ServeHTTP(w, r)
		})
	}
}
