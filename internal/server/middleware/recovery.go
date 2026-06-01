// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"
	"runtime/debug"

	logging "github.com/one-harsh/context-logging"
)

// Recovery holds the base logger directly so panics log even when Context middleware never ran.
func Recovery(base *logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					base.WithContext(r.Context()).Error(
						"panic recovered",
						logging.StringField("panic", toString(rec)),
						logging.StringField("stack", string(debug.Stack())),
						logging.HTTPMethod(r.Method),
						logging.StringField("path", r.URL.Path),
					)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"internal server error"}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case error:
		return t.Error()
	default:
		return ""
	}
}
