// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package extract

import (
	"crypto/rand"
	"strconv"
	"strings"
)

// tokenAlphabet is RFC 4648 lowercase base32 (32 symbols).
const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// TokenLen is the canonical length of the per-call staleness suffix per
// LABELING.md §2 (`@<token>` = 6 chars). 32^6 = ~1.07B distinct tokens —
// collision-immune at any realistic per-source invocation count.
const TokenLen = 6

// MintToken returns a fresh 6-character staleness token drawn uniformly from
// the lowercase base32 alphabet. crypto/rand backs the entropy so tokens are
// unpredictable across sessions (a cross-session token guessing attack would
// have to guess a specific 30-bit value against an ephemeral session).
func MintToken() string {
	var buf [TokenLen]byte
	_, _ = rand.Read(buf[:])
	// 256 % 32 == 0, so a byte-wise modulo is unbiased over the alphabet.
	for i, b := range buf {
		buf[i] = tokenAlphabet[b%32]
	}
	return string(buf[:])
}

// ParseSource splits a source label of the canonical form
// `<base>[#<seq>][@<token>]` into its parts. seq is -1 when no `#<seq>` is
// present; token is "" when no `@<token>` is present. Malformed suffixes
// (unparseable seq / wrong-length token / non-alphabet token) yield the
// zero-values for those parts and leave the remainder attached to base — the
// caller decides whether to treat that as base-only or as a validation
// failure. Never panics.
func ParseSource(label string) (base string, seq int64, token string) {
	seq = -1

	// The token suffix, if present, is at the tail. Split at the LAST '@' so
	// any earlier '@' in an unencoded caller-supplied label doesn't confuse us
	// (encoded bases percent-encode '@'; only the trailing suffix uses it raw).
	if i := strings.LastIndex(label, "@"); i >= 0 {
		tail := label[i+1:]
		if isValidToken(tail) {
			token = tail
			label = label[:i]
		}
	}

	// The seq suffix, if present, is at the tail of what remains.
	if i := strings.LastIndex(label, "#"); i >= 0 {
		tail := label[i+1:]
		if n, err := strconv.ParseInt(tail, 10, 64); err == nil && n >= 0 {
			seq = n
			label = label[:i]
		}
	}

	base = label
	return
}

func isValidToken(s string) bool {
	if len(s) != TokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			continue
		case c >= '2' && c <= '7':
			continue
		default:
			return false
		}
	}
	return true
}
