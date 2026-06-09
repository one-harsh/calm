// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm

import (
	"net/http"
	"time"

	logging "github.com/one-harsh/context-logging"
	"go.opentelemetry.io/otel/propagation"

	"github.com/one-harsh/calm/internal/adapter/obs"
)

const (
	headerAPIKey            = "X-CALM-API-Key"        //nolint:gosec // header name, not a credential
	headerWorkloadRequestID = "X-Workload-Request-Id" //nolint:gosec // header name, not a credential
	headerCorrelationID     = "X-CALM-Correlation-Id" //nolint:gosec // header name, not a credential
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type layer func(http.RoundTripper) http.RoundTripper

func chain(base http.RoundTripper, layers ...layer) http.RoundTripper {
	rt := base
	for i := len(layers) - 1; i >= 0; i-- {
		rt = layers[i](rt)
	}
	return rt
}

// withAuth stamps the namespace API key.
func withAuth(apiKey string) layer {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if apiKey != "" {
				req = req.Clone(req.Context())
				req.Header.Set(headerAPIKey, apiKey)
			}
			return next.RoundTrip(req)
		})
	}
}

func withWorkloadRequestID() layer {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if id := obs.RequestIDFromContext(req.Context()); id != "" {
				req = req.Clone(req.Context())
				req.Header.Set(headerWorkloadRequestID, id)
			}
			return next.RoundTrip(req)
		})
	}
}

// withTracePropagation injects W3C traceparent from the call's ctx. Propagation-only — no
// OTel SDK or spans — so CALM can link its trace to this call at zero infra cost.
func withTracePropagation() layer {
	prop := propagation.TraceContext{}
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			req = req.Clone(req.Context())
			prop.Inject(req.Context(), propagation.HeaderCarrier(req.Header))
			return next.RoundTrip(req)
		})
	}
}

func withLogging(log *logging.Logger) layer {
	return func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			start := time.Now()
			resp, err := next.RoundTrip(req)
			fields := []logging.LoggingField{
				logging.StringField("method", req.Method),
				logging.StringField("path", req.URL.Path),
				logging.IntField("http.duration_ms", int(time.Since(start).Milliseconds())),
			}
			if err != nil {
				log.WithContext(req.Context()).Warn("calm call failed", append(fields, logging.ErrorField(err))...)
				return resp, err
			}
			fields = append(fields, logging.IntField("status", resp.StatusCode))
			if cid := resp.Header.Get(headerCorrelationID); cid != "" {
				fields = append(fields, logging.StringField("correlation_id", cid))
			}
			log.WithContext(req.Context()).Debug("calm call", fields...)
			return resp, err
		})
	}
}
