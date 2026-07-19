// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"fmt"
	"strings"
	"testing"
)

func TestMetricsChunks_ScrapeFixture(t *testing.T) {
	got := metricsChunks("scrape", fixture(t, "scrape.prom"), contentTypeProse)

	byTitle := map[string][]int{}
	for i, c := range got {
		byTitle[c.Title] = append(byTitle[c.Title], i)
		if c.ContentType != contentTypeCode {
			t.Errorf("chunk[%d] %q content_type = %q; want code always", i, c.Title, c.ContentType)
		}
	}

	// Small families: one chunk each, metadata riding with samples.
	counter := got[byTitle["metric:http_requests_total"][0]]
	if !strings.Contains(counter.Content, "# HELP http_requests_total") ||
		!strings.Contains(counter.Content, "# TYPE http_requests_total counter") ||
		!strings.Contains(counter.Content, `http_requests_total{method="get",code="200"} 1027`) {
		t.Errorf("counter family incomplete:\n%s", counter.Content)
	}
	gauge := got[byTitle["metric:process_resident_memory_bytes"][0]]
	if !strings.Contains(gauge.Content, "# UNIT process_resident_memory_bytes bytes") {
		t.Errorf("UNIT line did not ride with its family:\n%s", gauge.Content)
	}

	// Summary keeps quantiles + _sum + _count together.
	summary := got[byTitle["metric:rpc_duration_seconds"][0]]
	for _, want := range []string{`quantile="0.99"`, "rpc_duration_seconds_sum", "rpc_duration_seconds_count"} {
		if !strings.Contains(summary.Content, want) {
			t.Errorf("summary family missing %q:\n%s", want, summary.Content)
		}
	}

	// The oversized histogram splits by label group (le removed): four
	// route/method groups, each repeating HELP/TYPE and keeping
	// _bucket+_sum+_count together.
	var histTitles []string
	for title := range byTitle {
		if strings.HasPrefix(title, "metric:http_request_duration_seconds labels:") {
			histTitles = append(histTitles, title)
		}
	}
	if len(histTitles) != 4 {
		t.Fatalf("histogram groups = %d (%v); want 4 route/method groups", len(histTitles), histTitles)
	}
	for _, title := range histTitles {
		c := got[byTitle[title][0]]
		if !strings.Contains(c.Content, "# TYPE http_request_duration_seconds histogram") {
			t.Errorf("%q missing repeated metadata", title)
		}
		if !strings.Contains(c.Content, "_sum{") || !strings.Contains(c.Content, "_count{") {
			t.Errorf("%q split _sum/_count away from its buckets", title)
		}
		if strings.Contains(title, "le=") {
			t.Errorf("%q leaked the le partition label into the group key", title)
		}
	}

	// TYPE-less sample groups by raw name; garbage + # EOF land in unparsed.
	if _, ok := byTitle["metric:queue_depth"]; !ok {
		t.Errorf("TYPE-less sample lost; titles: %v", keysOf(byTitle))
	}
	unparsed := got[byTitle["metric:unparsed"][0]]
	if !strings.Contains(unparsed.Content, "this is not a metric line") || !strings.Contains(unparsed.Content, "# EOF") {
		t.Errorf("unparsed bucket incomplete:\n%s", unparsed.Content)
	}
}

func TestMetricsChunks_ZeroFamiliesFallsBackToText(t *testing.T) {
	got := metricsChunks("src", "just prose\n\nno metrics here", contentTypeProse)
	if len(got) != 2 || got[0].ContentType != contentTypeProse {
		t.Fatalf("non-exposition payload = %+v; want text-chunker paragraphs", got)
	}
}

// A single-label-group histogram beyond the size bound must NOT byte-split —
// separating buckets from _sum/_count breaks the complete-evidence contract.
// The semantic unit outranks the size bound.
func TestMetricsChunks_SingleGroupOversizedHistogramStaysWhole(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# HELP big_latency_seconds One route, many buckets.\n")
	sb.WriteString("# TYPE big_latency_seconds histogram\n")
	for i := range 90 {
		fmt.Fprintf(&sb, "big_latency_seconds_bucket{route=\"/only/deeply/nested/path\",le=\"%d.5\"} %d\n", i, i*7)
	}
	sb.WriteString("big_latency_seconds_sum{route=\"/only/deeply/nested/path\"} 812.5\n")
	sb.WriteString("big_latency_seconds_count{route=\"/only/deeply/nested/path\"} 413\n")
	if len(sb.String()) <= chunkMaxBytes {
		t.Fatalf("fixture must exceed chunkMaxBytes; got %d", len(sb.String()))
	}

	got := metricsChunks("scrape", sb.String(), contentTypeProse)
	if len(got) != 1 {
		t.Fatalf("got %d chunks; want the whole family in one: %+v", len(got), got)
	}
	for _, want := range []string{"_bucket{", "_sum{", "_count{", "# TYPE big_latency_seconds histogram"} {
		if !strings.Contains(got[0].Content, want) {
			t.Errorf("family chunk missing %q", want)
		}
	}
}

// Independent-sample kinds with a single label group (a label-free
// timestamped series) part-split by whole sample lines with the metadata
// repeated in every part.
func TestMetricsChunks_OversizedCounterPartsRepeatMeta(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("# HELP api_calls_total Total calls, archived per scrape.\n")
	sb.WriteString("# TYPE api_calls_total counter\n")
	for i := range 160 {
		fmt.Fprintf(&sb, "api_calls_total %d 17513600%05d\n", i*3, i*15)
	}
	if len(sb.String()) <= chunkMaxBytes {
		t.Fatalf("fixture must exceed chunkMaxBytes; got %d", len(sb.String()))
	}
	got := metricsChunks("scrape", sb.String(), contentTypeProse)
	if len(got) < 2 {
		t.Fatalf("oversized single-group counter did not part-split: %d chunks", len(got))
	}
	for i, c := range got {
		if !strings.HasPrefix(c.Title, "metric:api_calls_total part:") {
			t.Errorf("chunk[%d].Title = %q; want part-numbered", i, c.Title)
		}
		if !strings.Contains(c.Content, "# TYPE api_calls_total counter") {
			t.Errorf("chunk[%d] missing repeated metadata", i)
		}
		for line := range strings.SplitSeq(c.Content, "\n") {
			if !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "api_calls_total ") {
				t.Errorf("chunk[%d] holds a split sample line: %q", i, line)
			}
		}
	}
}

func TestGroupTitle_TruncationKeepsDistinct(t *testing.T) {
	long1 := strings.Repeat("route=/very/long/path/segment,", 4) + "method=GET"
	long2 := strings.Repeat("route=/very/long/path/segment,", 4) + "method=PUT"
	t1, t2 := groupTitle("m", long1), groupTitle("m", long2)
	if t1 == t2 {
		t.Fatalf("distinct label groups collided after truncation: %q", t1)
	}
	if len([]rune(t1)) > titleCap {
		t.Errorf("truncated title exceeds cap: %d runes", len([]rune(t1)))
	}
}

func keysOf(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
