// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package capture

import (
	"fmt"
	"strings"
)

// withDiscoveryCard appends the one-time retrieval capability card to a capture
// presentation (search-retrieval-discovery). recall is the shell's retrieval
// command, so the copy-pasteable lines point at the shell's own affordance.
func withDiscoveryCard(visible, recall string) string {
	if visible != "" && !strings.HasSuffix(visible, "\n") {
		visible += "\n"
	}
	return visible + "\n" + discoveryCard(recall)
}

func discoveryCard(recall string) string {
	var b strings.Builder
	b.WriteString("── CALM keeps captured output searchable — retrieve it instead of re-running:\n")
	fmt.Fprintf(&b, "   • find by query:        %s \"<terms>\"\n", recall)
	fmt.Fprintf(&b, "   • scope to one capture:  %s source=<label> \"<terms>\"\n", recall)
	fmt.Fprintf(&b, "   • reread in order:       %s source=<label>\n", recall)
	b.WriteString("   <label> is the source= value on each capture's trailer line; pass it\n")
	b.WriteString("   verbatim — it validates locally and reports staleness rather than empty.\n")
	b.WriteString("   Captures store and search exact text, never a paraphrase.\n")
	b.WriteString("   (shown once, on this conversation's first capture)")
	return b.String()
}
