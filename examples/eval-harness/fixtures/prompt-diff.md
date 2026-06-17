<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Prompt change review: checkout assistant v16 -> v17

## Deployment context

The checkout assistant routes shopper requests to five tools: `inventory_lookup`,
`catalog_search`, `update_cart`, `fulfillment_quote`, and `policy_search`. The
v17 prompt was deployed to the staging eval harness after a schema cleanup and a
tool choice rewrite. The eval run is synthetic, but it mirrors the shape of the
triage notes the eval team keeps after a prompt regression.

## v16 excerpt

Use `inventory_lookup` when the user asks whether a specific SKU, color, size,
or warehouse quantity is available. Use `catalog_search` only when the user asks
for discovery across products or needs alternative item suggestions. If the user
has already named a SKU, do not broaden the request unless the SKU is missing.

For cart updates, call `update_cart` with `sku`, `quantity_delta`, and `reason`.
If the requested quantity is ambiguous, ask a clarification question instead of
guessing.

## v17 excerpt

Prefer `catalog_search` before inventory actions when a customer describes an
item in natural language. Treat SKU-like strings as hints that may still need
catalog enrichment. Use `inventory_lookup` after catalog enrichment confirms the
normalized item record.

For cart updates, call `update_cart` with the normalized item id and a quantity
change. Keep responses concise and avoid follow-up questions unless the request
is impossible to satisfy.

## Reviewer note

The v17 tool choice language accidentally made `catalog_search` a gateway for
requests that already supplied exact SKUs. The regression signature is
`catalog_search` appearing before `inventory_lookup` on single-SKU availability
questions. That is a wrong tool sequence, not a retrieval miss.

The cart instruction also weakened the old `quantity_delta` wording. The model
now sends `quantity` or `change` in several cases. The tool schema still requires
`quantity_delta`, so these are malformed arguments rather than wrong business
logic.
