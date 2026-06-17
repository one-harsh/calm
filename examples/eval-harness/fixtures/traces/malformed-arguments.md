<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Trace cluster: malformed update_cart arguments

## Case CART-018

User request: "Add two more of SKU A19-BLACK-8 to my cart."

Expected tool call:

```json
{"tool":"update_cart","arguments":{"sku":"A19-BLACK-8","quantity_delta":2,"reason":"user_request"}}
```

Actual tool call:

```json
{"tool":"update_cart","arguments":{"sku":"A19-BLACK-8","quantity":2,"reason":"user_request"}}
```

Judge rationale: malformed arguments. The tool contract requires
`quantity_delta`, not `quantity`. The model selected the right tool but produced
an invalid JSON payload after the schema wording changed.

## Case CART-031

User request: "Remove one pair of M88-WHITE-11."

Actual tool call:

```json
{"tool":"update_cart","arguments":{"sku":"M88-WHITE-11","change":-1,"reason":"remove_item"}}
```

Judge rationale: malformed arguments. The payload uses `change` instead of
`quantity_delta`. This is not a natural-language misunderstanding; it is a schema
following failure on the `update_cart` argument names.

## Pattern

The v17 prompt says "a quantity change" but no longer names `quantity_delta`.
The old wording was more mechanical and produced valid JSON. The eval team
should restore the explicit field name in the prompt and keep the friendly prose
for user-facing answers only.
