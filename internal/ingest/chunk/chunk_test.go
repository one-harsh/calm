// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import "testing"

func TestSplit_TextSplitsOnBlankLines(t *testing.T) {
	content := "para one\nstill one\n\npara two\n\n\npara three"
	got, _ := Split("src", content, formatText, "")
	if len(got) != 3 {
		t.Fatalf("got %d chunks; want 3: %+v", len(got), got)
	}
	if got[0].Content != "para one\nstill one" {
		t.Errorf("chunk[0].Content = %q", got[0].Content)
	}
}

func TestSplit_EmptyContentNoChunks(t *testing.T) {
	if got, _ := Split("src", "   \n  ", "", ""); len(got) != 0 {
		t.Fatalf("got %+v; want zero chunks for whitespace-only content", got)
	}
}

func TestSplit_HonorsContentTypeHint(t *testing.T) {
	got, _ := Split("src", "hello world", formatText, contentTypeCode)
	if got[0].ContentType != contentTypeCode {
		t.Errorf("content_type = %q; want code", got[0].ContentType)
	}
}

func TestSplit_HintWinsOverDetection(t *testing.T) {
	// JSON-shaped content with a text hint stays on the text path.
	got, eff := Split("src", "{\"a\":1}\n{\"b\":2}", formatText, "")
	if eff != formatText || len(got) != 1 {
		t.Fatalf("hinted text = %q/%d chunks; want text path", eff, len(got))
	}
}

// Byte-stability property: the same input yields byte-identical chunks on
// every run, across every fixture and every format (hinted and detected).
// This is what lets the fixtures double as retrieval-level baselines — and
// what catches any map-iteration order leaking into output.
func TestSplit_ByteStable(t *testing.T) {
	fixtures := []string{
		"train-multirank.log", "eval-results.jsonl", "readme-excerpt.md",
		"api-response.json", "api-response-array.json", "scrape.prom",
		"results.csv", "python-traceback.txt", "go-panic.txt",
	}
	formats := []string{
		"", formatLog, formatStacktrace, formatCSV, formatTSV,
		formatMetrics, formatJSON, formatMarkdown, formatText,
	}
	for _, name := range fixtures {
		content := fixture(t, name)
		for _, format := range formats {
			first, effA := Split("src", content, format, "")
			second, effB := Split("src", content, format, "")
			if effA != effB {
				t.Errorf("%s/%q: effective format unstable: %q vs %q", name, format, effA, effB)
			}
			if len(first) != len(second) {
				t.Errorf("%s/%q: chunk count unstable: %d vs %d", name, format, len(first), len(second))
				continue
			}
			for i := range first {
				if first[i] != second[i] {
					t.Errorf("%s/%q: chunk[%d] differs between runs:\n%q\nvs\n%q",
						name, format, i, first[i], second[i])
					break
				}
			}
		}
	}
}
