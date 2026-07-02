// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
)

// Source-label grammar: calm:v1:<domain>:<verb>:<context…>:<identity>[#<seq>].
// calm:v1 is the product + grammar version — bump when normalization changes.
const (
	labelProduct = "calm"
	labelVersion = "v1"
	maxLabelLen  = 200
)

// maxSeqSuffix reserves room for the "#<seq>" a history source appends. seq is a
// session-local invocation counter, so "#" + 12 digits (~10^12, far beyond any real
// session) is ample — no need for the full int64 width. It's a fixed budget (not the
// actual suffix length) so a dual latest source stays independent of the seq value;
// otherwise the cap, and a truncated/hashed latest, would shift as seq gains a digit.
const maxSeqSuffix = 13

const (
	domainFile   = "file"
	domainVCS    = "vcs"
	domainSearch = "search"
	domainShell  = "shell"
)

type labelID struct {
	domain  string
	verb    string
	context []string
	ident   []string
}

//   - reserve: leaves room for a trailing seqSuffix so a dual/coexist history source stays
//     within `maxLabelLen` after the suffix is appended.
func buildBase(id labelID, inv Invocation, reserve int) string {
	limit := maxLabelLen - reserve

	prefix := []string{labelProduct, labelVersion, enc(id.domain)}
	if id.verb != "" {
		prefix = append(prefix, enc(id.verb))
	}
	if inv.WorkspaceID != "" {
		prefix = append(prefix, enc(inv.WorkspaceID))
	}
	for _, ctx := range id.context {
		prefix = append(prefix, enc(ctx))
	}

	var ident []string
	for _, x := range id.ident {
		if x == "" || x == "." {
			continue
		}
		ident = append(ident, enc(x))
	}

	label := strings.Join(slices.Concat(prefix, ident), ":")
	if len(label) <= limit {
		return label
	}

	// Over length: collapse the identity to a hash, keeping the prefix readable.
	if len(ident) > 0 {
		label = strings.Join(append(slices.Clone(prefix), "h"+shortHash(strings.Join(ident, ":"))), ":")
		if len(label) <= limit {
			return label
		}
	}

	// The prefix alone still overflows (e.g. a very long WorkspaceID). Truncate the head
	// but append a hash of the full identity, so distinct identities never collapse onto
	// the same truncated bytes.
	digest := "h" + shortHash(strings.Join(slices.Concat(prefix, ident), ":"))
	budget := limit - len(digest) - 1
	if budget < 0 {
		budget = 0
	}
	if len(label) > budget {
		label = label[:budget]
	}
	return label + ":" + digest
}

func seqSuffix(seq int64) string {
	return "#" + strconv.FormatInt(seq, 10)
}

// enc percent-encodes the grammar's reserved characters (':' '#' '@' space '%')
// and control bytes per segment, leaving multibyte UTF-8 untouched. Byte-wise
// so it never panics on arbitrary input.
func enc(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ':' || c == '#' || c == '@' || c == ' ' || c == '%' || c < 0x20 || c == 0x7f {
			b.WriteByte('%')
			b.WriteByte(hexDigit(c >> 4))
			b.WriteByte(hexDigit(c & 0x0f))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func hexDigit(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'A' + (n - 10)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}
