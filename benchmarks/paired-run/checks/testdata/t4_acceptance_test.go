// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capturecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	logging "github.com/one-harsh/context-logging"

	"github.com/one-harsh/calm/internal/adapter/calm"
	"github.com/one-harsh/calm/internal/adapter/config"
	"github.com/one-harsh/calm/internal/adapter/obs"
)

// Oracle for the `--json` search feature. It drives the real search command
// in-process against a mock CALM client and asserts exactly what the task
// pins: (1) `--json` emits one JSON object with the pinned top-level keys
// `query`/`hits`/`degraded` and nothing else, hits in result order, each hit
// carrying `source`/`snippet`/`offset`/`match_layer`, with absent values null
// rather than omitted; (2) default (human) output is byte-identical to a
// pre-task golden captured from the unfixed tree; (3) a degraded call still
// exits nonzero with the JSON carrying the degradation reason (never-worse).

// t4TopLevelKeys is the pinned top-level key set: exactly these, always
// present, never more.
var t4TopLevelKeys = []string{"degraded", "hits", "query"}

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

// TestT4Search_JSONRankedSchema pins the machine-readable shape, the pinned
// field names, result order, and the null-not-omitted rule for absent values.
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
	t4AssertTopLevelKeys(t, obj)

	if q := t4String(t, obj, "query"); q != "needle" {
		t.Errorf("query = %q; want the query string as given, %q", q, "needle")
	}

	hits := t4Hits(t, obj)
	want := []struct{ source, snippet, layer string }{
		{"calm:v1:x", "found the needle here", "primary"},
		{"calm:v1:y", "another needle", "trigram"},
	}
	if len(hits) != len(want) {
		t.Fatalf("hits length = %d; want %d\nobject=%v", len(hits), len(want), obj)
	}
	for i, h := range hits {
		if src := t4HitString(t, h, i, "source"); src != want[i].source {
			t.Errorf("hits[%d].source = %q; want %q (hits appear in result order)", i, src, want[i].source)
		}
		if snip := t4HitString(t, h, i, "snippet"); snip != want[i].snippet {
			t.Errorf("hits[%d].snippet = %q; want exact indexed text %q", i, snip, want[i].snippet)
		}
		if layer := t4HitString(t, h, i, "match_layer"); layer != want[i].layer {
			t.Errorf("hits[%d].match_layer = %q; want %q", i, layer, want[i].layer)
		}
		// Ranked results carry no continuation hint: null, key still present.
		t4AssertNull(t, h, "offset", fmt.Sprintf("hits[%d].offset", i))
	}

	t4AssertNull(t, obj, "degraded", "degraded")
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
// --json: a failed backend call still exits nonzero, the object keeps the
// pinned key set, and `degraded` carries the reason.
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
	t4AssertTopLevelKeys(t, obj)

	if q := t4String(t, obj, "query"); q != "needle" {
		t.Errorf("degraded JSON (%s) query = %q; want the query string as given, %q", from, q, "needle")
	}
	v, ok := obj["degraded"]
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

// TestT4Search_JSONDocumentOrderOffsetHint pins the per-hit offset hint and the
// null-not-omitted rule for a hit with no match layer.
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
	t4AssertTopLevelKeys(t, obj)

	hits := t4Hits(t, obj)
	if len(hits) != 1 {
		t.Fatalf("document-order hits length = %d; want 1\nobject=%v", len(hits), obj)
	}
	if src := t4HitString(t, hits[0], 0, "source"); src != "calm:v1:x" {
		t.Errorf("hits[0].source = %q; want calm:v1:x", src)
	}
	if snip := t4HitString(t, hits[0], 0, "snippet"); snip != "chunk body text" {
		t.Errorf("hits[0].snippet = %q; want exact chunk text", snip)
	}
	if off := t4HitNumber(t, hits[0], 0, "offset"); off != 3 {
		t.Errorf("hits[0].offset = %d; want the continuation offset hint 3", off)
	}
	// No match layer on this hit: null, key still present.
	t4AssertNull(t, hits[0], "match_layer", "hits[0].match_layer")

	t4AssertNull(t, obj, "degraded", "degraded")
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

// t4AssertTopLevelKeys pins the exact top-level key set: every pinned key is
// present (absent values are null, never omitted) and nothing else is.
func t4AssertTopLevelKeys(t *testing.T, obj map[string]any) {
	t.Helper()
	got := slices.Sorted(maps.Keys(obj))
	if !slices.Equal(got, t4TopLevelKeys) {
		t.Fatalf("top-level keys = %v; want exactly %v (absent values are null, never omitted keys; no extra top-level keys)", got, t4TopLevelKeys)
	}
}

func t4String(t *testing.T, obj map[string]any, key string) string {
	t.Helper()
	v, ok := obj[key]
	if !ok {
		t.Fatalf("json object missing required key %q\nobject=%v", key, obj)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%q is %T; want a string", key, v)
	}
	return s
}

func t4Hits(t *testing.T, obj map[string]any) []map[string]any {
	t.Helper()
	v, ok := obj["hits"]
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

func t4HitString(t *testing.T, h map[string]any, idx int, key string) string {
	t.Helper()
	v, ok := h[key]
	if !ok {
		t.Fatalf("hits[%d] missing required key %q (absent values are null, never omitted keys)\nhit=%v", idx, key, h)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("hits[%d].%s is %T; want string", idx, key, v)
	}
	return s
}

func t4HitNumber(t *testing.T, h map[string]any, idx int, key string) int {
	t.Helper()
	v, ok := h[key]
	if !ok {
		t.Fatalf("hits[%d] missing required key %q (absent values are null, never omitted keys)\nhit=%v", idx, key, h)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("hits[%d].%s is %T; want an integer", idx, key, v)
	}
	return int(f)
}

// t4AssertNull requires the key to be present with a null value — the
// "absent values are null, never omitted keys" rule.
func t4AssertNull(t *testing.T, m map[string]any, key, label string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("%s is omitted; absent values are null, never omitted keys", label)
		return
	}
	if v != nil {
		t.Errorf("%s = %v; want null", label, v)
	}
}
