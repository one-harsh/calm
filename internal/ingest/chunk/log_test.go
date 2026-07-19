// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"strings"
	"testing"
)

func TestLogChunks_MultiRankFixture(t *testing.T) {
	content := fixture(t, "train-multirank.log")
	got := logChunks("train", content, contentTypeProse)
	if len(got) == 0 {
		t.Fatal("no chunks")
	}
	// The leading unanchored launch block is its own run at the front.
	if !strings.HasPrefix(got[0].Content, "Launching torchrun") {
		t.Errorf("chunk[0] = %.60q; want the unanchored preamble first", got[0].Content)
	}
	// Dense per-line timestamps must pack, not shatter: far fewer chunks than
	// the ~24 anchored lines.
	if len(got) > 4 {
		t.Errorf("got %d chunks; runs are not packing toward the 2KB target", len(got))
	}
	// Continuation lines (NCCL warn details, checkpoint shards) stay inside
	// their anchored run.
	joined := ""
	for _, c := range got {
		joined += c.Content + "\n"
	}
	if !strings.Contains(joined, "falling back to tree algorithm") || !strings.Contains(joined, "shards: 4/4 complete") {
		t.Errorf("continuation lines lost")
	}
	for i, c := range got {
		if len(c.Content) > chunkMaxBytes {
			t.Errorf("chunk[%d] exceeds max: %d bytes", i, len(c.Content))
		}
	}
}

func TestLogChunks_NoAnchorsDelegatesToText(t *testing.T) {
	got := logChunks("src", "plain line\nanother line\n\nsecond para", contentTypeProse)
	if len(got) != 2 {
		t.Fatalf("unanchored log = %d chunks; want text-chunker paragraphs (2): %+v", len(got), got)
	}
}

func TestTimestampAnchor_Grammar(t *testing.T) {
	yes := []string{
		"2026-07-01T09:14:02.118Z step=100",
		"[rank 3] 2026-07-01 09:14:02 loss=2.4",
		"09:14:02 short clock",
		"Jul  1 09:14:02 host daemon[12]: msg",
		"I0701 09:14:02.118211 worker.go:42] msg",
		"[1751360042.118] epoch bracketed",
	}
	for _, l := range yes {
		if !timestampAnchor(l) {
			t.Errorf("timestampAnchor(%q) = false; want true", l)
		}
	}
	no := []string{
		"    ring 0 -> peer 3 latency 812ms",
		"plain prose line",
		"this prose line runs long enough that its embedded clock reading of 09:14:02 sits past the prefix window",
	}
	for _, l := range no {
		if timestampAnchor(l) {
			t.Errorf("timestampAnchor(%q) = true; want false", l)
		}
	}
}
