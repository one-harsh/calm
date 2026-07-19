// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"strings"
	"testing"
)

// The unit is one logical error report: the fixture's chained sequence
// (ValueError → During handling → RuntimeError) is ONE chunk, and the
// separate psycopg report is the second.
func TestStacktraceChunks_PythonChainedFixture(t *testing.T) {
	got := stacktraceChunks("trace", fixture(t, "python-traceback.txt"), contentTypeProse)
	if len(got) != 2 {
		for i, c := range got {
			t.Logf("chunk[%d] title=%q content=%.60q", i, c.Title, c.Content)
		}
		t.Fatalf("got %d chunks; want 2 logical reports", len(got))
	}
	if !strings.Contains(got[0].Content, "ValueError: bad input") ||
		!strings.Contains(got[0].Content, "During handling of the above exception") ||
		!strings.Contains(got[0].Content, "RuntimeError: failed request") {
		t.Errorf("chained report split apart:\n%s", got[0].Content)
	}
	if got[0].Title != "RuntimeError: failed request" {
		t.Errorf("chained title = %q; want the FINAL exception", got[0].Title)
	}
	if got[1].Title != "psycopg.OperationalError: connection closed unexpectedly" {
		t.Errorf("second report title = %q", got[1].Title)
	}
	for i, c := range got {
		if c.ContentType != contentTypeCode {
			t.Errorf("chunk[%d].ContentType = %q; want code always", i, c.ContentType)
		}
	}
}

func TestStacktraceChunks_GoPanicFixture(t *testing.T) {
	got := stacktraceChunks("trace", fixture(t, "go-panic.txt"), contentTypeProse)
	if len(got) != 2 {
		for i, c := range got {
			t.Logf("chunk[%d] title=%q content=%.60q", i, c.Title, c.Content)
		}
		t.Fatalf("got %d chunks; want panic+first-goroutine, then second goroutine", len(got))
	}
	if !strings.HasPrefix(got[0].Title, "panic: runtime error") {
		t.Errorf("first title = %q; want the panic message", got[0].Title)
	}
	if !strings.Contains(got[0].Content, "goroutine 1 [running]") {
		t.Errorf("panic header separated from its goroutine block")
	}
	if !strings.HasPrefix(got[1].Title, "goroutine 18 [chan receive]") {
		t.Errorf("second title = %q; want the goroutine header", got[1].Title)
	}
}

func TestStacktraceChunks_JVMAndFallback(t *testing.T) {
	jvm := "Exception in thread \"main\" java.lang.IllegalStateException: boom\n" +
		"\tat com.example.Api.call(Api.java:41)\n" +
		"Caused by: java.io.IOException: socket closed\n" +
		"\tat com.example.Net.read(Net.java:9)\n"
	got := stacktraceChunks("trace", jvm, contentTypeProse)
	if len(got) != 1 {
		t.Fatalf("Caused-by must stay in its report; got %d chunks", len(got))
	}
	if !strings.HasPrefix(got[0].Title, "Exception in thread") {
		t.Errorf("jvm title = %q; want the outermost (first) exception line", got[0].Title)
	}

	// Hinted content with no recognizable marker: one code chunk.
	got = stacktraceChunks("trace", "some diagnostic text\nwithout any trace", contentTypeProse)
	if len(got) != 1 || got[0].ContentType != contentTypeCode {
		t.Fatalf("markerless fallback = %+v; want one code chunk", got)
	}
}
