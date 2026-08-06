// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/config"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// Oracle for the `--json` search feature. It drives the real search command
// in-process against a mock CALM client and asserts the three promises the
// task pins: (1) `--json` emits one machine-readable JSON object carrying the
// query, per-hit source/snippet/match-layer, and the continuation offset hint;
// (2) default (human) output is byte-identical to a pre-task golden captured
// from the unfixed tree; (3) a degraded call still exits nonzero with the JSON
// carrying the degradation reason (never-worse). Field names are matched
// leniently where the prompt describes rather than names them, but the shape,
// values, exit codes, and byte-identity are asserted exactly.

func t4NewDeps(t *testing.T, c calm.Client) (Deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv(CaptureActiveEnv, "")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	d := Deps{
		Cfg:    config.Config{Calm: config.CalmConfig{URL: "http://calm.test", Client: "claude-code", SessionTTLMinutes: 60}},
		Logger: logging.Nop(),
		Client: c,
		Root:   t.TempDir(),
		Stdout: stdout,
		Stderr: stderr,
	}
	return d, stdout, stderr
}

func t4ExpectEstablish(c *calm.MockClient, token string) {
	c.EXPECT().RegisterClient(mock.Anything, mock.Anything).Return(true, nil).Maybe()
	c.EXPECT().CreateSession(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(token, nil).Once()
	c.EXPECT().Ingest(mock.Anything, token, mock.Anything).
		Return(calm.IngestSummary{Source: "calm:v1:test", SectionsIndexed: 1, SectionsTotal: 1}, nil).Maybe()
	c.EXPECT().WriteEvents(mock.Anything, token, mock.Anything).Return(nil).Maybe()
}

// t4Establish runs one real capture so the on-disk session exists and search
// resolves a live token — the same way the package's own search tests set up.
func t4Establish(t *testing.T, d Deps, stdout, stderr *bytes.Buffer) {
	t.Helper()
	if code := Dispatch(context.Background(), d, []string{"exec", "--session", "conv", "--", "printf hello"}); code != 0 {
		t.Fatalf("establish exec exited %d; want 0 (stderr=%q)", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
}

func t4RankedResult() calm.SearchResults {
	return calm.SearchResults{
		Queries: []calm.QueryResult{{Query: "needle", Hits: []calm.Hit{
			{Title: "run#1", Snippet: "found the needle here", Source: "calm:v1:x", MatchLayer: "primary"},
			{Title: "run#2", Snippet: "another needle", Source: "calm:v1:y", MatchLayer: "trigram"},
		}}},
		CorrelationID: "corr-7",
	}
}

// TestT4Search_JSONRankedSchema pins the machine-readable shape and values.
func TestT4Search_JSONRankedSchema(t *testing.T) {
	c := calm.NewMockClient(t)
	t4ExpectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(t4RankedResult(), nil).Once()
	d, stdout, stderr := t4NewDeps(t, c)
	t4Establish(t, d, stdout, stderr)

	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "--json", "needle"})
	if code != 0 {
		t.Fatalf("--json ranked search exited %d; want 0 (stderr=%q)", code, stderr.String())
	}

	obj := t4DecodeSingleObject(t, stdout.Bytes())

	if q := t4TopString(t, obj, "query", "queries"); !strings.Contains(q, "needle") {
		t.Errorf("json query = %q; want it to carry the query text 'needle'", q)
	}

	hits := t4Hits(t, obj)
	if len(hits) != 2 {
		t.Fatalf("json hits length = %d; want 2\nobject=%v", len(hits), obj)
	}
	want := map[string][2]string{
		"calm:v1:x": {"found the needle here", "primary"},
		"calm:v1:y": {"another needle", "trigram"},
	}
	for i, h := range hits {
		src := t4HitField(t, h, i, "source", "source_label")
		exp, ok := want[src]
		if !ok {
			t.Errorf("hit[%d] source = %q; not an expected source label", i, src)
			continue
		}
		if snip := t4HitField(t, h, i, "snippet", "text", "snippet_text"); snip != exp[0] {
			t.Errorf("hit[%d] snippet = %q; want exact indexed text %q", i, snip, exp[0])
		}
		if layer := t4HitField(t, h, i, "match_layer", "matchlayer", "layer"); layer != exp[1] {
			t.Errorf("hit[%d] match layer = %q; want %q", i, layer, exp[1])
		}
	}

	t4AssertDegradedNullOrAbsent(t, obj)
}

// TestT4Search_DefaultModeByteIdentical proves the human default output is
// unchanged, byte-for-byte, from the pre-task tree. With T4_UPDATE_GOLDEN set
// it (re)captures the golden from the current tree instead of comparing.
func TestT4Search_DefaultModeByteIdentical(t *testing.T) {
	c := calm.NewMockClient(t)
	t4ExpectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(t4RankedResult(), nil).Once()
	d, stdout, stderr := t4NewDeps(t, c)
	t4Establish(t, d, stdout, stderr)

	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "needle"})
	if code != 0 {
		t.Fatalf("default-mode search exited %d; want 0 (stderr=%q)", code, stderr.String())
	}
	got := stdout.Bytes()

	goldenPath := os.Getenv("T4_GOLDEN")
	if goldenPath == "" {
		t.Fatal("T4_GOLDEN must point at the default-mode golden (set by the acceptance checker)")
	}
	if os.Getenv("T4_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("writing golden %q: %v", goldenPath, err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden %q: %v", goldenPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("default-mode output diverged from the pre-task golden.\n got: %q\nwant: %q", string(got), string(want))
	}
}

// TestT4Search_JSONDegradedCarriesReason pins the never-worse contract under
// --json: a failed backend call still exits nonzero and the JSON carries the
// reason.
func TestT4Search_JSONDegradedCarriesReason(t *testing.T) {
	c := calm.NewMockClient(t)
	t4ExpectEstablish(c, "tok1")
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).
		Return(calm.SearchResults{}, &calm.StatusError{Op: "search", Code: 503, Status: "503"}).Once()
	d, stdout, stderr := t4NewDeps(t, c)
	t4Establish(t, d, stdout, stderr)

	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "--json", "needle"})
	if code == 0 {
		t.Fatalf("degraded --json search exited 0; never-worse requires a nonzero exit\nstdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	obj, from := t4FirstJSONObject(stdout.Bytes(), stderr.Bytes())
	if obj == nil {
		t.Fatalf("degraded --json produced no JSON object on stdout or stderr\nstdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	v, ok := t4Get(obj, "degraded")
	if !ok || v == nil {
		t.Fatalf("degraded JSON (%s) has no non-null 'degraded' field\nobject=%v", from, obj)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("'degraded' is %T; want the reason string", v)
	}
	if !strings.Contains(s, obs.DegradedReasonCalmUnreachable) {
		t.Errorf("degraded reason = %q; want it to carry %q", s, obs.DegradedReasonCalmUnreachable)
	}
}

// TestT4Search_JSONDocumentOrderOffsetHint pins the offset hint (continuation
// offset) surfacing in the document-order machine output.
func TestT4Search_JSONDocumentOrderOffsetHint(t *testing.T) {
	c := calm.NewMockClient(t)
	t4ExpectEstablish(c, "tok1")
	next := 3
	c.EXPECT().Search(mock.Anything, "tok1", mock.Anything).Return(calm.SearchResults{
		Queries:    []calm.QueryResult{{Hits: []calm.Hit{{Title: "chunk-0", Snippet: "chunk body text", Source: "calm:v1:x"}}}},
		NextOffset: &next,
	}, nil).Once()
	d, stdout, stderr := t4NewDeps(t, c)
	t4Establish(t, d, stdout, stderr)

	code := Dispatch(context.Background(), d, []string{"search", "--session", "conv", "--json", "source=calm:v1:x"})
	if code != 0 {
		t.Fatalf("--json document-order search exited %d; want 0 (stderr=%q)", code, stderr.String())
	}

	obj := t4DecodeSingleObject(t, stdout.Bytes())

	hits := t4Hits(t, obj)
	if len(hits) != 1 {
		t.Fatalf("document-order hits length = %d; want 1\nobject=%v", len(hits), obj)
	}
	if src := t4HitField(t, hits[0], 0, "source", "source_label"); src != "calm:v1:x" {
		t.Errorf("hit source = %q; want calm:v1:x", src)
	}
	if snip := t4HitField(t, hits[0], 0, "snippet", "text", "snippet_text"); snip != "chunk body text" {
		t.Errorf("hit snippet = %q; want exact chunk text", snip)
	}
	if !t4HasOffsetHint(obj, 3) {
		t.Errorf("document-order --json must surface the continuation offset hint (3)\nobject=%v", obj)
	}

	t4AssertDegradedNullOrAbsent(t, obj)
}

// --- JSON shape helpers ----------------------------------------------------

// t4DecodeSingleObject requires the bytes to be exactly one JSON object with
// nothing but whitespace after it — the "one JSON object" machine contract.
func t4DecodeSingleObject(t *testing.T, b []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("--json stdout is not a single JSON object: %v\nstdout=%q", err, string(b))
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Fatalf("--json stdout has content after the JSON object (machine output must be one object); err=%v\nstdout=%q", err, string(b))
	}
	return obj
}

// t4FirstJSONObject returns the first JSON object decodable from stdout, else
// stderr, tolerating trailing bytes (e.g. a human detail line after JSON).
func t4FirstJSONObject(stdout, stderr []byte) (map[string]any, string) {
	if obj := t4TryObject(stdout); obj != nil {
		return obj, "stdout"
	}
	if obj := t4TryObject(stderr); obj != nil {
		return obj, "stderr"
	}
	return nil, ""
}

func t4TryObject(b []byte) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(b))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil
	}
	return obj
}

// t4Get finds the value under the first key that case-insensitively equals any
// candidate (candidates must be lowercase).
func t4Get(m map[string]any, candidates ...string) (any, bool) {
	for k, v := range m {
		lk := strings.ToLower(k)
		for _, cand := range candidates {
			if lk == cand {
				return v, true
			}
		}
	}
	return nil, false
}

func t4TopString(t *testing.T, obj map[string]any, candidates ...string) string {
	t.Helper()
	v, ok := t4Get(obj, candidates...)
	if !ok {
		t.Fatalf("json object missing required field %v\nobject=%v", candidates, obj)
	}
	switch tv := v.(type) {
	case string:
		return tv
	case []any:
		parts := make([]string, 0, len(tv))
		for _, e := range tv {
			if s, ok := e.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	default:
		t.Fatalf("field %v is %T; want a string or array of strings", candidates, v)
		return ""
	}
}

func t4Hits(t *testing.T, obj map[string]any) []map[string]any {
	t.Helper()
	v, ok := t4Get(obj, "hits")
	if !ok {
		t.Fatalf("json object missing required 'hits' array\nobject=%v", obj)
	}
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("'hits' is %T; want a JSON array", v)
	}
	out := make([]map[string]any, 0, len(arr))
	for i, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("hits[%d] is %T; want a JSON object", i, e)
		}
		out = append(out, m)
	}
	return out
}

func t4HitField(t *testing.T, h map[string]any, idx int, candidates ...string) string {
	t.Helper()
	v, ok := t4Get(h, candidates...)
	if !ok {
		t.Fatalf("hit[%d] missing required field %v\nhit=%v", idx, candidates, h)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("hit[%d] field %v is %T; want string", idx, candidates, v)
	}
	return s
}

func t4AssertDegradedNullOrAbsent(t *testing.T, obj map[string]any) {
	t.Helper()
	if v, ok := t4Get(obj, "degraded"); ok && v != nil {
		t.Errorf("degraded = %v; want null (or absent) on a successful search", v)
	}
}

// t4HasOffsetHint reports whether the offset value surfaces under any
// offset-named key, at the top level or within a hit.
func t4HasOffsetHint(obj map[string]any, want int) bool {
	if t4OffsetIn(obj, want) {
		return true
	}
	if v, ok := t4Get(obj, "hits"); ok {
		if arr, ok := v.([]any); ok {
			for _, e := range arr {
				if m, ok := e.(map[string]any); ok && t4OffsetIn(m, want) {
					return true
				}
			}
		}
	}
	return false
}

func t4OffsetIn(m map[string]any, want int) bool {
	for k, v := range m {
		if !strings.Contains(strings.ToLower(k), "offset") {
			continue
		}
		if f, ok := v.(float64); ok && int(f) == want {
			return true
		}
	}
	return false
}
