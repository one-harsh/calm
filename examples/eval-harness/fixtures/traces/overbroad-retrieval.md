<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Trace cluster: overbroad retrieval and unrelated policy snippets

## Case RET-004

User request: "Is SKU A19-BLACK-8 available in Austin?"

Retrieval calls:

1. `policy_search({"topic":"availability policy"})`
2. `catalog_search({"query":"black size 8"})`
3. no `inventory_lookup`

Retrieved context included return-window policy, restricted-item policy, and a
regional shipping disclaimer. None of those snippets answered the warehouse
availability question.

Judge rationale: overbroad retrieval. The model reached for `policy_search`
because the prompt says "consult policy when availability is uncertain", but
the uncertainty came from skipping `inventory_lookup`. The policy snippets were
unrelated clutter.

## Case RET-015

User request: "Can I buy two of M88-WHITE-11?"

Retrieval calls:

1. `policy_search({"topic":"purchase limits shoes"})`
2. `catalog_search({"query":"M88 white"})`

Judge rationale: overbroad retrieval. Purchase-limit policy is only relevant
after quantity is known. The broad retrieval prompt pulled unrelated policy text
into a direct inventory question.

## Pattern

The v17 prompt increased policy_search use before tool-specific facts were
collected. The fix is to make policy retrieval conditional on an actual policy
question or a tool result that names a policy restriction.
