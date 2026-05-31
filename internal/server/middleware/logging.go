// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"time"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/obs"
)

// Logging emits a DEBUG line on request start and an INFO summary on completion.
func Logging(logger *logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := wrapResponseWriter(w)

			ctx := logging.BindSummary(r.Context(),
				logging.HTTPMethod(r.Method),
				obs.Endpoint(r.URL.Path),
			)
			r = r.WithContext(ctx)

			logger.WithContext(ctx).Debug(
				"request received",
				logging.HTTPMethod(r.Method),
				obs.Endpoint(r.URL.Path),
			)

			next.ServeHTTP(ww, r)

			logger.SummaryWithContext(r.Context()).Info(
				"request completed",
				logging.HTTPStatus(ww.status),
				logging.Duration(time.Since(start)),
				logging.Bytes(ww.bytesWritten),
			)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int
}

func wrapResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += n
	return n, err
}
