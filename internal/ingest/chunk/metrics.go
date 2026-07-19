// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package chunk

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/one-harsh/calm/internal/db"
)

// The metrics format is Prometheus classic exposition text, narrowly: HELP /
// TYPE / UNIT comment metadata plus `name{labels} value [ts]` samples.
// OpenMetrics is tolerated only as harmless comments (# EOF, exemplars land
// in unparsed). The goal is chunking scrape output into self-contained
// metric evidence, not building a metrics database.
var (
	promCommentRe = regexp.MustCompile(`^#\s+(HELP|TYPE|UNIT)\s+([a-zA-Z_:][a-zA-Z0-9_:]*)(\s|$)`)
	promSampleRe  = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{.*\})?\s+([+-]?(\d+\.?\d*([eE][+-]?\d+)?|\.\d+|Inf|NaN))(\s+\d+)?\s*$`)
)

const metricTitlePrefix = "metric:"

type metricFamily struct {
	name    string
	meta    []string // HELP/TYPE/UNIT lines in input order
	samples []string // sample lines in input order
	kind    string   // TYPE value when declared
}

// metricsChunks chunks a Prometheus exposition payload per metric family —
// histogram families absorb their _bucket/_sum/_count series and summary
// families their quantile/_sum/_count series, because one bucket without its
// _sum and _count is useless retrieval evidence. Oversized families split by
// stable label group with the metadata repeated. Zero parsed families sends
// the whole payload to the text chunker; in mixed content, every unassigned
// line lands in metric:unparsed — nothing is silently dropped.
func metricsChunks(source, content, contentType string) []db.Chunk {
	_ = contentType // metric names and labels are identifier-heavy: always code
	families, unparsed := parseExposition(content)
	if len(families) == 0 {
		return textChunks(source, content, contentType)
	}

	var chunks []db.Chunk
	for _, fam := range families {
		chunks = append(chunks, familyChunks(fam)...)
	}
	if len(unparsed) > 0 {
		body := strings.Join(unparsed, "\n")
		for _, piece := range windowParagraph(body) {
			chunks = append(chunks, db.Chunk{
				Title:       metricTitlePrefix + "unparsed",
				Content:     piece,
				ContentType: contentTypeCode,
			})
		}
	}
	return chunks
}

func parseExposition(content string) ([]*metricFamily, []string) {
	var order []*metricFamily
	byName := map[string]*metricFamily{}
	family := func(name string) *metricFamily {
		if f, ok := byName[name]; ok {
			return f
		}
		f := &metricFamily{name: name}
		byName[name] = f
		order = append(order, f)
		return f
	}

	var unparsed []string
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		if m := promCommentRe.FindStringSubmatch(trimmed); m != nil {
			f := family(m[2])
			f.meta = append(f.meta, trimmed)
			if m[1] == "TYPE" {
				fields := strings.Fields(trimmed)
				if len(fields) >= 4 {
					f.kind = fields[3]
				}
			}
			continue
		}
		if m := promSampleRe.FindStringSubmatch(trimmed); m != nil {
			f := family(sampleFamilyName(m[1], byName))
			f.samples = append(f.samples, trimmed)
			continue
		}
		unparsed = append(unparsed, trimmed)
	}

	// Metadata-only entries (a HELP for a metric that never sampled) are
	// still families; entries that got neither samples nor meta can't exist.
	var families []*metricFamily
	for _, f := range order {
		if len(f.samples) > 0 || len(f.meta) > 0 {
			families = append(families, f)
		}
	}
	return families, unparsed
}

// sampleFamilyName folds histogram/summary series into their declared
// family: _bucket/_sum/_count belong to a histogram base, _sum/_count to a
// summary base. Without a TYPE declaration the raw sample name is its own
// family.
func sampleFamilyName(name string, byName map[string]*metricFamily) string {
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		base, found := strings.CutSuffix(name, suffix)
		if !found {
			continue
		}
		f, ok := byName[base]
		if !ok {
			continue
		}
		if f.kind == "histogram" || (f.kind == "summary" && suffix != "_bucket") {
			return base
		}
	}
	return name
}

func familyChunks(fam *metricFamily) []db.Chunk {
	all := append(append([]string{}, fam.meta...), fam.samples...)
	body := strings.Join(all, "\n")
	if len(body) <= chunkMaxBytes {
		return []db.Chunk{{
			Title:       metricTitlePrefix + fam.name,
			Content:     body,
			ContentType: contentTypeCode,
		}}
	}
	if groups, keys := labelGroups(fam); len(groups) > 1 {
		chunks := make([]db.Chunk, 0, len(keys))
		for _, key := range keys {
			lines := append(append([]string{}, fam.meta...), groups[key]...)
			chunks = append(chunks, db.Chunk{
				Title:       groupTitle(fam.name, key),
				Content:     strings.Join(lines, "\n"),
				ContentType: contentTypeCode,
			})
		}
		return chunks
	}
	// A single-group histogram/summary stays whole even oversized: byte
	// splitting would separate buckets or quantiles from their _sum/_count,
	// and one bucket without them is useless evidence — the semantic unit
	// outranks the size bound, same rule as records and traces.
	if fam.kind == "histogram" || fam.kind == "summary" {
		return []db.Chunk{{
			Title:       metricTitlePrefix + fam.name,
			Content:     body,
			ContentType: contentTypeCode,
		}}
	}
	// Independent-sample kinds part-split by whole sample lines, metadata
	// repeated per part (the CSV header rule).
	var chunks []db.Chunk
	metaLen := len(strings.Join(fam.meta, "\n"))
	var part []string
	size := metaLen
	flush := func() {
		if len(part) == 0 {
			return
		}
		lines := append(append([]string{}, fam.meta...), part...)
		chunks = append(chunks, db.Chunk{
			Title:       metricTitlePrefix + fam.name + " part:" + strconv.Itoa(len(chunks)+1),
			Content:     strings.Join(lines, "\n"),
			ContentType: contentTypeCode,
		})
		part, size = nil, metaLen
	}
	for _, sample := range fam.samples {
		if len(part) > 0 && size+len(sample)+1 > chunkTargetBytes {
			flush()
		}
		part = append(part, sample)
		size += len(sample) + 1
	}
	flush()
	return chunks
}

// labelGroups groups samples by their labelset minus the histogram/summary
// partition labels (le, quantile), in first-appearance order.
func labelGroups(fam *metricFamily) (map[string][]string, []string) {
	groups := map[string][]string{}
	var keys []string
	for _, sample := range fam.samples {
		key := labelGroupKey(sample)
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], sample)
	}
	return groups, keys
}

// labelGroupKey serializes a sample's labels minus le/quantile: sorted keys,
// k=v comma-joined — deterministic across runs by construction.
func labelGroupKey(sample string) string {
	open := strings.IndexByte(sample, '{')
	if open < 0 {
		return ""
	}
	end := strings.LastIndexByte(sample, '}')
	if end < open {
		return ""
	}
	labels := parseLabels(sample[open+1 : end])
	pairs := make([]string, 0, len(labels))
	for _, l := range labels {
		if l.key == "le" || l.key == "quantile" {
			continue
		}
		pairs = append(pairs, l.key+"="+l.value)
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

type label struct{ key, value string }

// parseLabels scans `k="v",...` with escape-aware string tracking — label
// values may contain commas, braces, and escaped quotes.
func parseLabels(s string) []label {
	var labels []label
	i := 0
	for i < len(s) {
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(s[i : i+eq])
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			break
		}
		i++
		var val strings.Builder
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				val.WriteByte(s[i+1])
				i += 2
				continue
			}
			if c == '"' {
				i++
				break
			}
			val.WriteByte(c)
			i++
		}
		labels = append(labels, label{key: key, value: val.String()})
		for i < len(s) && (s[i] == ',' || s[i] == ' ') {
			i++
		}
	}
	return labels
}

// groupTitle truncates the full serialization last, suffixing a short hash of
// the untruncated form so two distinct label groups never collide into one
// title.
func groupTitle(family, serialized string) string {
	title := metricTitlePrefix + family + " labels:" + serialized
	r := []rune(title)
	if len(r) <= titleCap {
		return title
	}
	sum := sha256.Sum256([]byte(serialized))
	suffix := "-" + hex.EncodeToString(sum[:4])
	return string(r[:titleCap-len(suffix)]) + suffix
}

func init() {
	register(formatMetrics, metricsChunks)
}
