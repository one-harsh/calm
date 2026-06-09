// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package calm_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

var traceparentRe = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

const ingestOKBody = `{"source":"s","sections_indexed":1,"sections_total":1,"summary":[],"summary_truncated":false,"distinctive_terms":[]}`

func TestThreading_InjectsHeadersAndLogsCall(t *testing.T) {
	var gotReqID, gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReqID = r.Header.Get("X-Workload-Request-Id")
		gotTraceparent = r.Header.Get("traceparent")
		w.Header().Set("X-CALM-Correlation-Id", "corr-xyz")
		jsonResp(w, http.StatusOK, ingestOKBody)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	logger, err := logging.New(logging.Config{Level: "debug", Format: "json", Output: &buf, Service: "test"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	c, err := calm.NewGenapiClient(srv.URL, "k", "idem", logger)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}

	ctx, reqID := obs.WithCallContext(context.Background())
	if _, err := c.Ingest(ctx, "tok", calm.IngestInput{Source: "s", Content: "c"}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	_ = logger.Sync()

	if gotReqID != reqID {
		t.Errorf("X-Workload-Request-Id = %q; want minted id %q", gotReqID, reqID)
	}
	if !traceparentRe.MatchString(gotTraceparent) {
		t.Errorf("traceparent = %q; want a valid W3C traceparent", gotTraceparent)
	}
	logs := buf.String()
	for _, want := range []string{"corr-xyz", "correlation_id", "http.duration_ms", "status"} {
		if !strings.Contains(logs, want) {
			t.Errorf("call log missing %q; logs:\n%s", want, logs)
		}
	}
}

func TestThreading_LogsFailedCall(t *testing.T) {
	var buf bytes.Buffer
	logger, err := logging.New(logging.Config{Level: "debug", Format: "json", Output: &buf, Service: "test"})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	// Port 1 refuses connections → transport error path.
	c, err := calm.NewGenapiClient("http://127.0.0.1:1", "k", "idem", logger)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	ctx, _ := obs.WithCallContext(context.Background())
	if _, err := c.Ingest(ctx, "tok", calm.IngestInput{Source: "s", Content: "c"}); err == nil {
		t.Fatal("Ingest: want error against a dead address")
	}
	_ = logger.Sync()
	if logs := buf.String(); !strings.Contains(logs, "calm call failed") || !strings.Contains(logs, "http.duration_ms") {
		t.Errorf("expected a failed-call log with latency; logs:\n%s", logs)
	}
}

func TestThreading_NoCallContextSendsNoWorkloadHeaders(t *testing.T) {
	var sawReqID, sawTraceparent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawReqID = r.Header["X-Workload-Request-Id"]
		_, sawTraceparent = r.Header["Traceparent"]
		jsonResp(w, http.StatusOK, ingestOKBody)
	}))
	t.Cleanup(srv.Close)

	c, err := calm.NewGenapiClient(srv.URL, "k", "idem", nil)
	if err != nil {
		t.Fatalf("NewGenapiClient: %v", err)
	}
	if _, err := c.Ingest(context.Background(), "tok", calm.IngestInput{Source: "s", Content: "c"}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if sawReqID {
		t.Error("X-Workload-Request-Id set without a call context")
	}
	if sawTraceparent {
		t.Error("traceparent set without a call context")
	}
}
