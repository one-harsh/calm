// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	logging "github.com/one-harsh/context-logging"
	"golang.org/x/time/rate"

	"github.com/one-harsh/calm/internal/api/genapi"
	"github.com/one-harsh/calm/internal/auth"
	"github.com/one-harsh/calm/internal/obs"
)

const (
	// IP-bucket bounded store. Botnet-scale fanout could otherwise grow the
	// map unbounded; the sweep evicts buckets at full burst (effectively
	// idle clients) once the map crosses ipBucketSweepThreshold.
	ipBucketSweepThreshold = 100_000
	ipSweepEvery           = 10_000
)

// ipBucketStore is a bounded per-IP token-bucket store with soft eviction:
// when len(buckets) crosses sweepThreshold, the next sweep (every
// sweepEvery inserts) drops buckets at full burst capacity (effectively
// idle clients). Defends RateLimitIP against unbounded growth under bot-net
// scale IP fanout. Evicted IPs get fresh buckets on next request — slight
// skew, acceptable.
type ipBucketStore struct {
	mu             sync.Mutex
	buckets        map[string]*rate.Limiter
	inserts        uint64
	rateLim        rate.Limit
	burst          int
	sweepThreshold int
	sweepEvery     uint64
}

func newIPBucketStore(perSecond, sweepThreshold int, sweepEvery uint64) *ipBucketStore {
	return &ipBucketStore{
		buckets:        make(map[string]*rate.Limiter),
		rateLim:        rate.Limit(perSecond),
		burst:          perSecond * 2,
		sweepThreshold: sweepThreshold,
		sweepEvery:     sweepEvery,
	}
}

func (s *ipBucketStore) bucketFor(ip string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lim, ok := s.buckets[ip]; ok {
		return lim
	}
	s.inserts++
	if s.inserts%s.sweepEvery == 0 && len(s.buckets) > s.sweepThreshold {
		for k, v := range s.buckets {
			if v.Tokens() >= float64(s.burst) {
				delete(s.buckets, k)
			}
		}
	}
	lim := rate.NewLimiter(s.rateLim, s.burst)
	s.buckets[ip] = lim
	return lim
}

func (s *ipBucketStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buckets)
}

// RateLimitIP is a pre-auth limiter keyed by client IP. Sits before Auth so
// unauthenticated DDoS doesn't burn registry-lookup CPU. Bucket key derived
// from r.RemoteAddr by default, or the first X-Forwarded-For IP when
// trustProxyHeaders is true (required behind a trusted LB).
//
// perSecond <= 0 disables this tier (returns passthrough).
func RateLimitIP(perSecond int, trustProxyHeaders bool, logger *logging.Logger) func(http.Handler) http.Handler {
	if perSecond <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	store := newIPBucketStore(perSecond, ipBucketSweepThreshold, ipSweepEvery)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r, trustProxyHeaders)
			if !store.bucketFor(ip).Allow() {
				logger.WithContext(r.Context()).Warn(
					"rate limit exceeded",
					obs.Endpoint(r.URL.Path),
					obs.RateLimitTierIP,
					logging.StringField("ratelimit.client_ip", ip),
					logging.IntField("ratelimit.threshold_per_second", perSecond),
				)
				writeRateLimited(w, r.URL.Path, "per-ip", perSecond)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitNamespaceAndGlobal enforces per-namespace + global-aggregate tiers
// after Auth. Order: Namespace → Global. Namespace first because the loser
// tier burns a token on every rejected request: with Global checked first, a
// misbehaving namespace would drain the shared global bucket on requests it
// was always going to 429 at the namespace tier — leaking its overload into
// other namespaces' global headroom and breaking the namespace-isolation
// contract in the cost dimension. Either tier at <= 0 disables that tier.
func RateLimitNamespaceAndGlobal(
	registry auth.Registry,
	defaultNamespacePerSecond int,
	globalPerSecond int,
	logger *logging.Logger,
) func(http.Handler) http.Handler {
	var (
		mu        sync.Mutex
		nsBuckets = make(map[string]*rate.Limiter)
	)
	var globalBucket *rate.Limiter
	if globalPerSecond > 0 {
		globalBucket = rate.NewLimiter(rate.Limit(globalPerSecond), globalPerSecond*2)
	}
	nsLimiterFor := func(ns string) (*rate.Limiter, int) {
		perSec, ok := registry.RateFor(ns)
		if !ok {
			perSec = defaultNamespacePerSecond
		}
		if perSec <= 0 {
			return nil, perSec
		}
		mu.Lock()
		defer mu.Unlock()
		if lim, found := nsBuckets[ns]; found {
			return lim, perSec
		}
		lim := rate.NewLimiter(rate.Limit(perSec), perSec*2)
		nsBuckets[ns] = lim
		return lim, perSec
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ns := auth.NamespaceFromContext(r.Context())
			if ns == "" {
				// Unauthenticated paths (health, version) intentionally bypass
				// the auth middleware and have no namespace. They're also
				// rate-limit-exempt — operational probes shouldn't be throttled.
				next.ServeHTTP(w, r)
				return
			}
			if lim, perSec := nsLimiterFor(ns); lim != nil && !lim.Allow() {
				logger.WithContext(r.Context()).Warn(
					"rate limit exceeded",
					obs.Endpoint(r.URL.Path),
					obs.RateLimitTierNamespace,
					logging.IntField("ratelimit.threshold_per_second", perSec),
				)
				writeRateLimited(w, r.URL.Path, "namespace", perSec)
				return
			}
			if globalBucket != nil && !globalBucket.Allow() {
				logger.WithContext(r.Context()).Warn(
					"rate limit exceeded",
					obs.Endpoint(r.URL.Path),
					obs.RateLimitTierGlobal,
					logging.IntField("ratelimit.threshold_per_second", globalPerSecond),
				)
				writeRateLimited(w, r.URL.Path, "global", globalPerSecond)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Left-most non-empty entry is the original client per RFC 7239.
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeRateLimited(w http.ResponseWriter, endpoint, tier string, perSec int) {
	detail := fmt.Sprintf("%s rate limit of %d req/sec exceeded", tier, perSec)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(genapi.Error{
		Error:    "rate_limited",
		Endpoint: &endpoint,
		Detail:   &detail,
	})
}
