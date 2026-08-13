// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"fmt"
	"strings"
)

type InventoryEntry struct {
	Label string
	Token string
	Seq   int64
}

const (
	inventoryMaxEntries = 20
	inventoryMaxBytes   = 1024
)

func withDiscoveryCard(visible, recall string) string {
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		visible += "\n"
	}
	return visible + "\n" + discoveryCard(recall)
}

func discoveryCard(recall string) string {
	return recallAffordance(recall) + "\n   (shown once, on this conversation's first capture)"
}

// recallAffordance is the copy-pasteable retrieval block shared by the
// first-capture discovery card and the session-start card.
func recallAffordance(recall string) string {
	var b strings.Builder
	b.WriteString("── CALM keeps captured output searchable — retrieve it instead of re-running:\n")
	fmt.Fprintf(&b, "   • find by query:        %s \"<terms>\"\n", recall)
	fmt.Fprintf(&b, "   • scope to one capture:  %s source=<label> \"<terms>\"\n", recall)
	fmt.Fprintf(&b, "   • reread in order:       %s source=<label>\n", recall)
	b.WriteString("   <label> is the source= value on a capture's trailer; pass it verbatim\n")
	b.WriteString("   (it reports staleness rather than empty, and returns exact text).")
	return b.String()
}

// SessionStartCard is the retrieval card injected at session start over a fresh
// context (startup/clear).
func SessionStartCard(recall string, captures int64, entries []InventoryEntry) string {
	var b strings.Builder
	if captures > 0 {
		fmt.Fprintf(&b, "This context is from the calm-capture session-start hook. Shell command output in this conversation is captured and searchable (%d captured so far) — it can be retrieved instead of re-run.\n", captures)
	} else {
		b.WriteString("This context is from the calm-capture session-start hook. It captures the full output of shell commands run in this conversation and keeps it searchable, so past output can be retrieved instead of re-run.\n")
	}
	b.WriteString(recallAffordance(recall))
	b.WriteString(renderInventory(entries))
	return b.String()
}

// SessionRefresherCard is the shorter card injected over a summarized context
// (compact), where an earlier card likely survives in the summary.
func SessionRefresherCard(recall string, entries []InventoryEntry) string {
	return "This context is from the calm-capture session-start hook: shell output in this conversation stays captured and searchable via " +
		recall + " source=<label>. This replaces any earlier CALM capture inventory above." +
		renderInventory(entries)
}

// renderInventory lists the most-recent captured identities, bounded to
// inventoryMaxEntries and then byte-capped by whole lines. Entries arrive
// ordered most-recent-first, so both bounds keep the most-recent. Returns "" for
// an empty corpus.
func renderInventory(entries []InventoryEntry) string {
	if len(entries) == 0 {
		return ""
	}
	if len(entries) > inventoryMaxEntries {
		entries = entries[:inventoryMaxEntries]
	}
	var b strings.Builder
	b.WriteString("\n── captured so far (retrieve any with source=<label>):")
	for _, e := range entries {
		// Fuse the staleness token so the emitted label validates locally on reuse
		// — the recall affordance promises staleness-not-empty, which a bare base
		// label (no `@token`) would silently bypass.
		b.WriteString("\n   • ")
		b.WriteString(fuseSource(e.Label, e.Token))
	}
	return capLines(b.String(), inventoryMaxBytes)
}

// capLines truncates s to at most maxBytes, cutting only at a line boundary so
// no partial line survives; the earliest lines are the ones kept.
func capLines(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if cut := strings.LastIndexByte(s[:maxBytes], '\n'); cut > 0 {
		return s[:cut]
	}
	return ""
}
