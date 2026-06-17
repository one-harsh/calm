<!--
Copyright 2026 The CALM Authors
SPDX-License-Identifier: Apache-2.0
-->

# Trace cluster: wrong tool selected before inventory lookup

## Case INV-042

User request: "Do you have SKU A19-BLACK-8 in the Austin warehouse?"

Expected tool path:

1. `inventory_lookup({"sku":"A19-BLACK-8","warehouse":"AUS"})`
2. answer from returned quantity

Actual v17 tool path:

1. `catalog_search({"query":"A19 BLACK 8 Austin warehouse"})`
2. `inventory_lookup({"sku":"A19-BLK-08","warehouse":"AUS"})`

Judge rationale: wrong tool. The user supplied an exact SKU and warehouse. The
prompt v17 instruction made the model call `catalog_search` before
`inventory_lookup`, then it normalized the SKU incorrectly. The failure is not a
missing inventory record; it is a tool choice regression.

## Case INV-077

User request: "Check inventory_lookup for SKU M88-WHITE-11, any store is fine."

Expected tool path:

1. `inventory_lookup({"sku":"M88-WHITE-11","warehouse":"any"})`

Actual v17 tool path:

1. `catalog_search({"query":"M88 WHITE 11"})`
2. answer says "I found the white version" without calling `inventory_lookup`

Judge rationale: wrong tool and unsupported answer. `catalog_search` only proves
catalog presence, not available quantity. The answer should have waited for
`inventory_lookup`.

## Pattern

The shared signature is `catalog_search` appearing before or instead of
`inventory_lookup` when the request already contains an exact SKU. The likely
source is the v17 prompt line "Prefer `catalog_search` before inventory actions
when a customer describes an item in natural language." The model treats SKU-like
strings as natural language descriptions instead of exact identifiers.
